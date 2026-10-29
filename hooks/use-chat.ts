"use client"

import { activitySchema, checkpointSchema, outputSchema, parseChatEvent, safeThinking, terminalTurn, type CanonicalPartFields, type ChatEvent, type TurnCheckpoint, type TurnState } from "@/lib/chat-event-contract"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { toast } from "@/lib/toast"
import { useWebSocket, type WSReason, type WSStatus, type WSMessage } from "@/hooks/use-websocket"
import { checkChatMessageSize } from "@/components/features/chat/hooks/use-message-submit"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { apiFetch, AUTH_EVENT } from "@/lib/api-fetch"
import { projectInputVersionIds } from "@/components/features/chat/files/project-input-metadata"
import { AskSubmissionError, askSubmissionError } from "@/lib/ask-submission-error"
import { ASK_SUBMISSION_METADATA_KEY } from "@/components/features/chat/asks/ask-envelope"
import { randomUUIDv4 } from "@/lib/random-id"

/** Upper bound on out-of-order events held during reassembly. Past this, a gap
 *  is assumed permanently lost and the stream skips ahead so it never freezes.
 *  A run is turn-capped so a healthy stream never approaches this. */
const MAX_PENDING_EVENTS = 1000

/** Copy shown when a run ends without the agent producing any reply (issue
 *  #545). The backend streams the same copy as an explicit error event; this
 *  constant is the client-side fallback for a `done` that arrives with no
 *  reply turn at all (older server / lost error frame), so the transcript is
 *  never left silently empty. */
export const NO_OUTPUT_ERROR = "The agent returned no output — try again"

/** crypto.randomUUID is unavailable in non-secure (HTTP) contexts, which is
 *  how the dev clones are reached. lib/random-id falls back to
 *  crypto.getRandomValues, which has no such gate. */
const uuid = randomUUIDv4

// --- Turn-based model types ---

/** Discriminator for the content type of a turn part (text, tool call, thinking, etc.). */
export type TurnPartType =
  | "text"
  | "thinking"
  | "activity"
  | "output"
  | "tool_call"
  | "tool_result"
  | "status"
  | "error"
  | "result"
  | "system_init"
  | "image"
  | "crew_provisioning"

/** A single content block within a chat turn (e.g. a text fragment, tool call, or thinking block). */
export interface TurnPart extends CanonicalPartFields {
  id: string
  type: TurnPartType
  content: string
  isStreaming?: boolean
  metadata?: Record<string, unknown>
  timestamp: Date
}

interface PendingControls {
  turnId: string
  parts: Map<"system_init" | "result", Record<string, unknown>>
}
function foldControls(parts: TurnPart[], controls: PendingControls | undefined, runId: string, timestamp: Date): TurnPart[] {
  if (!controls) return parts
  const merged = [...parts]
  for (const [type, metadata] of controls.parts) {
    const part: TurnPart = { id: `control:${runId}:${type}`, type, content: "", run_id: runId, turn_id: controls.turnId, metadata, timestamp }
    const at = merged.findIndex(value => value.id === part.id)
    if (at < 0) merged.push(part)
    else merged[at] = part
  }
  return merged
}

/** A complete turn in the conversation (user message or multi-part assistant response). */
export interface ChatTurn {
  /** Original persisted user message identity, never an optimistic turn ID. */
  originalMessageId?: string
  originalAuthorUserId?: string
  turnState?: TurnState
  cancelRequested?: boolean
  /** A missing canonical event or truncated replay has not been recovered. */
  streamIncomplete?: boolean
  runId?: string
  id: string
  role: "user" | "assistant" | "system"
  parts: TurnPart[]
  isStreaming: boolean
  timestamp: Date
  /** For user turns in a group chat: which human authored it. Lets the UI
   *  attribute a teammate's message (avatar/name) and distinguish it from the
   *  local user's own turns. Undefined for the local user / private chats. */
  authorUserId?: string
  /** Per-turn metadata — whatever rode WITH the message rather than inside it.
   *
   *  Two readers today. `trace_id` is stamped by the `done` event and consumed
   *  by the feedback store to link signals back to the OTel trace that produced
   *  the message. The ask-form submission envelope
   *  (components/features/chat/asks/ask-envelope.ts) arrives under its own key
   *  on a user turn — set optimistically by sendMessage, and recovered from
   *  `conversation.Message.Metadata` by messagesToTurns on reload, which is the
   *  path that makes a badge survive a refresh.
   *
   *  Open-ended by design: this map is the message's metadata, and narrowing it
   *  to the keys that happen to have readers is what silently dropped the
   *  envelope. */
  metadata?: { trace_id?: string } & Record<string, unknown>
}

// --- Legacy types (kept for history loading compatibility) ---

/** @deprecated Legacy message role; use ChatTurn for new code. */
export type MessageRole = "user" | "assistant" | "system" | "tool"

/** @deprecated Legacy stream event type; use TurnPartType for new code. */
export type StreamEventType =
  | "text"
  | "tool_call"
  | "tool_result"
  | "thinking"
  | "status"
  | "done"
  | "error"
  | "system"
  | "result"
  | "image"
  | "crew_provisioning"
  | "user_message"
  | "agent_busy"

/** WebSocket event types for agent-to-agent task assignment lifecycle. */
export type AssignmentEventType = "assignment_created" | "assignment_running" | "assignment_completed" | "assignment_failed"

/** Safely render an assignment-event payload field as a string. The
 *  backend has historically sent both `target: "viktor"` and
 *  `target: { slug: "viktor" }`, and naive template-literal interpolation
 *  of the latter renders "[object Object]" in the chat. Prefer a
 *  human-shaped field, fall back to empty.
 *
 *  We intentionally do NOT serialize the whole object: the result lands in
 *  user-visible chat messages, and a backend payload may carry tokens,
 *  emails, or other PII. If none of slug/name/id is present the safer
 *  move is to render nothing rather than dump the object.
 *  Exported for unit tests.
 */
export function assignmentField(v: unknown): string {
  if (v == null) return ""
  if (typeof v === "string") return v
  if (typeof v === "number" || typeof v === "boolean") return String(v)
  if (typeof v === "object") {
    const obj = v as Record<string, unknown>
    if (typeof obj.slug === "string") return obj.slug
    if (typeof obj.name === "string") return obj.name
    if (typeof obj.id === "string") return obj.id
    return ""
  }
  return String(v)
}

/** One structured segment of a persisted assistant turn, as returned by the
 *  history API (`conversation.Part` on the Go side). The same canonical schema
 *  the live WebSocket stream carries, so reloaded turns render identically to
 *  streamed ones. */
export interface HistoryPart extends CanonicalPartFields {
  type: TurnPartType | string
  content: string
  tool_name?: string
  tool_id?: string
  metadata?: Record<string, unknown>
}

/** @deprecated Legacy flat chat message; use ChatTurn/TurnPart for new code. Kept for history loading compatibility. */
export interface ChatMessage {
  /** Set only by the authenticated stored-history response. */
  originalMessageId?: string
  originalAuthorUserId?: string
  checkpoint?: TurnCheckpoint
  runId?: string
  id: string
  role: MessageRole
  content: string
  toolName?: string
  eventType?: StreamEventType
  /** Structured parts from the history API. When present, the assistant turn
   *  is rebuilt from these (faithful reload of thinking + tools + text). When
   *  absent (legacy messages), a single text part is synthesized from content. */
  parts?: HistoryPart[]
  /** Which human authored a user message (group-chat attribution). */
  authorUserId?: string
  timestamp: Date
  isStreaming?: boolean
  metadata?: Record<string, unknown>
}

/** Drop transient status parts and (optionally) finalize any still-streaming
 *  parts. Every event that closes or reshapes a turn (new text run, new
 *  thinking pass, done, error, local stop) applies this same policy — one
 *  helper so call sites can't drift. */
function stripStatusParts(parts: TurnPart[], finalizeStreaming = true): TurnPart[] {
  return parts
    .filter((p) => p.type !== "status")
    .map((p) => (finalizeStreaming && p.isStreaming ? { ...p, isStreaming: false } : p))
}

/** TurnPartTypes that the renderer knows how to display. Unknown/transport
 *  types coming from history are coerced to "text" so a stray value never
 *  renders as a raw label row. */
const RENDERABLE_PART_TYPES: ReadonlySet<string> = new Set<TurnPartType>([
  "text",
  "thinking",
  "activity",
  "output",
  "tool_call",
  "tool_result",
  "error",
  "result",
  "image",
])

interface UseChatOptions {
  canSend?: boolean
  /** False for a session with no realtime at all (a restricted account: the
   *  allowlist refuses /ws-token). The socket is then never opened and the
   *  connection reads "unavailable" — except on a restricted chat, whose
   *  replies stream over the restricted-run response instead. */
  realtimeEnabled?: boolean
  executionProfile?: "trusted" | "restricted" | "pending"
  getExecutionProfile?: () => "trusted" | "restricted" | "pending"
  workspaceId?: string | null
  wsUrl: string
  /** Async callback that fetches the current WS ticket. Replaces the
   *  previous `token: string | null` pre-fetched once at mount; the
   *  hook now re-fetches on every (re)connect so a stale ticket from
   *  before a backend restart can't trap the connection in an infinite
   *  retry loop. */
  getToken: () => Promise<string | null>
  sessionId: string
  /** The local user's id. Used to drop the echo of our OWN broadcast
   *  user_message (the server fans every message out to all session
   *  subscribers including the sender) so the message we already rendered
   *  optimistically doesn't appear twice. */
  currentUserId?: string
  /** Called when the server can't replay an in-flight run (the replay buffer
   *  overflowed): the chat surface should reload history rather than render a
   *  partial stream. Optional — tests and simple callers omit it. */
  onStreamReset?: () => void
  /** A new, successfully persisted assistant answer observed on this connection. */
  onReplyCompleted?: (reply: { sessionId: string; repliedAt: string }) => void
  /** The server echoed our persisted user message; a rejected send has no echo. */
  onOwnMessageSaved?: (content: string, metadata?: Record<string, unknown>) => void
  /** The server refused this chat's in-flight send (busy agent or denied send). */
  onSendRefused?: (reason: string) => void
}

/** Map a structured history part to a renderable TurnPart, coercing unknown
 *  types to "text" and folding tool_name/tool_id into metadata so the tool
 *  cards can read them the same way they do for live events. */
function historyPartToTurnPart(part: HistoryPart, id: string, timestamp: Date): TurnPart {
  const type: TurnPartType = RENDERABLE_PART_TYPES.has(part.type) ? (part.type as TurnPartType) : "text"
  const metadata: Record<string, unknown> = { ...(part.metadata ?? {}) }
  if (part.tool_name !== undefined) metadata.tool_name = part.tool_name
  if (part.tool_id !== undefined) metadata.tool_id = part.tool_id
  const activity = activitySchema.safeParse(part.activity)
  const output = outputSchema.safeParse(part.output)
  return {
    ...part,
    activity: activity.success ? activity.data : undefined,
    output: output.success ? output.data : undefined,
    id: part.id || id,
    type,
    content: type === "thinking" ? (safeThinking(part.thinking).visibility === "summary" ? safeThinking(part.thinking).summary ?? "" : "") : part.content,
    thinking: type === "thinking" ? safeThinking(part.thinking) : undefined,
    metadata: Object.keys(metadata).length > 0 ? metadata : undefined,
    timestamp,
  }
}

/** Convert flat ChatMessage history into turns for display.
 *
 *  A persisted message's own `metadata` is carried onto the turn it becomes.
 *  It used to be dropped on every 1:1 message→turn branch, and that drop is
 *  why a reloaded conversation could not show which form a turn came out of
 *  even when the server had the answer sitting on the row: the renderer reads
 *  `turn.metadata` (asks/ask-provenance.ts, via `askProvenanceForTurn`) and
 *  there was never anything there.
 *
 *  The legacy assistant-grouping branch at the bottom is left alone — it
 *  already forwards `msg.metadata` onto the PART, which the same reader also
 *  looks at, and several such messages can share one turn, so there is no
 *  single message whose metadata the turn could honestly claim. */
export function messagesToTurns(messages: ChatMessage[]): ChatTurn[] {
  const turns: ChatTurn[] = []
  for (const msg of messages) {
    if (msg.role === "user") {
      turns.push({
        id: msg.id,
        originalMessageId: msg.originalMessageId,
        originalAuthorUserId: msg.originalAuthorUserId,
        role: "user",
        parts: [{ id: msg.id, type: "text", content: msg.content, timestamp: msg.timestamp }],
        isStreaming: false,
        timestamp: msg.timestamp,
        authorUserId: msg.authorUserId,
        metadata: msg.metadata,
        runId: msg.runId,
      })
    } else if (msg.role === "system") {
      // Persisted system turns may carry structured parts (e.g. the error
      // turn the backend writes for a zero-output run or a reply interrupted
      // by a server restart). Honor them so reloads render the same error
      // bubble the live stream showed, instead of downgrading to plain text.
      const parts: TurnPart[] = msg.parts && msg.parts.length > 0
        ? msg.parts.map((p, i) => historyPartToTurnPart(p, `${msg.id}-${i}`, msg.timestamp))
        : [historyPartToTurnPart({ type: msg.eventType === "thinking" ? "thinking" : msg.eventType === "error" ? "error" : "text", content: msg.content }, msg.id, msg.timestamp)]
      turns.push({
        id: msg.id,
        role: "system",
        parts,
        isStreaming: false,
        timestamp: msg.timestamp,
        metadata: msg.metadata,
        runId: msg.runId,
      })
    } else if (msg.parts && msg.parts.length > 0) {
      // Modern message: rebuild the turn from its structured parts so the
      // reload renders thinking + tools + interleaved text exactly as streamed.
      // Each persisted assistant message already carries its full ordered
      // parts, so it is one complete turn (no consecutive-message grouping).
      turns.push({
        id: msg.id,
        role: "assistant",
        parts: msg.parts.map((p, i) => historyPartToTurnPart(p, `${msg.id}-${i}`, msg.timestamp)),
        turnState: msg.checkpoint?.turn_state,
        isStreaming: msg.checkpoint ? !terminalTurn(msg.checkpoint.turn_state) : false,
        timestamp: msg.timestamp,
        metadata: msg.metadata,
        runId: msg.runId,
      })
    } else {
      // assistant/tool messages: group consecutive ones into a single turn
      const lastTurn = turns[turns.length - 1]
      const partType: TurnPartType = (msg.eventType === "tool_call" || msg.eventType === "tool_result")
        ? msg.eventType
        : msg.eventType === "thinking" ? "thinking" : "text"

      if (lastTurn?.role === "assistant" && !lastTurn.isStreaming && lastTurn.runId === msg.runId) {
        lastTurn.parts.push({
          id: msg.id,
          type: partType,
          content: partType === "thinking" ? "" : msg.content,
          metadata: msg.metadata,
          timestamp: msg.timestamp,
        })
      } else {
        turns.push({
          id: msg.id,
          role: "assistant",
          runId: msg.runId,
          parts: [{
            id: msg.id,
            type: partType,
            content: partType === "thinking" ? "" : msg.content,
            metadata: msg.metadata,
            timestamp: msg.timestamp,
          }],
          isStreaming: false,
          timestamp: msg.timestamp,
        })
      }
    }
  }
  return turns
}

/**
 * Full-featured chat hook that manages a WebSocket-based conversation with an agent.
 * Handles streaming text/thinking/tool events, turn grouping, history loading,
 * message editing, regeneration, and stop/cancel.
 */
export function useChat({ canSend = true, realtimeEnabled = true, wsUrl, getToken, sessionId, currentUserId, onStreamReset, onReplyCompleted, onOwnMessageSaved, onSendRefused, executionProfile, getExecutionProfile, workspaceId }: UseChatOptions) {
  const completedReplyRef = useRef(onReplyCompleted)
  completedReplyRef.current = onReplyCompleted
  const ownMessageSavedRef = useRef(onOwnMessageSaved)
  ownMessageSavedRef.current = onOwnMessageSaved
  const sendRefusedRef = useRef(onSendRefused)
  sendRefusedRef.current = onSendRefused
  const replySoundSinceRef = useRef(Date.now())
  const replyHasTextRef = useRef(false)
  const restrictedAbortRef = useRef<AbortController | null>(null)
  useEffect(() => () => {restrictedAbortRef.current?.abort()}, [sessionId])
  const principalKey = JSON.stringify([currentUserId ?? null, workspaceId ?? null])
  const authorityKey = JSON.stringify([principalKey, sessionId ?? null])
  const authorityRef = useRef(authorityKey)
  const knownHumanRef = useRef(!!currentUserId)
  const knownWorkspaceRef = useRef(!!workspaceId)
  if (currentUserId) knownHumanRef.current = true
  if (workspaceId) knownWorkspaceRef.current = true
  const [deniedConversation, setDeniedConversation] = useState<string | null>(null)
  const deniedConversationRef = useRef(deniedConversation)
  deniedConversationRef.current = deniedConversation
  const [deniedScope, setDeniedScope] = useState<string | null>(null)
  const deniedScopeRef = useRef(deniedScope)
  deniedScopeRef.current = deniedScope
  const historyDenied = deniedScope === principalKey || deniedConversation === authorityKey
  const authorityReadable = authorityRef.current === authorityKey && !historyDenied
    && (!knownHumanRef.current || !!currentUserId) && (!knownWorkspaceRef.current || !!workspaceId)
  const isCurrentAuthority = useCallback(() => authorityRef.current === authorityKey
    && deniedScopeRef.current !== principalKey && deniedConversationRef.current !== authorityKey && authorityReadable, [authorityKey, principalKey, authorityReadable])
  const [storedTurns, setTurns] = useState<ChatTurn[]>([])
  // Mask before passive effects: a new/unknown human cannot paint old parts.
  const turns = useMemo(() => authorityReadable ? storedTurns : [], [authorityReadable, storedTurns])
  const [streaming, setIsStreaming] = useState(false)
  const isStreaming = authorityReadable && streaming
  // Mirror of isStreaming for the (deps: []) event handlers — lets
  // handleDoneEvent tell "a local send is pending" apart from an
  // unsolicited done (another tab's run) without being re-created.
  const pendingAsksRef = useRef(new Map<string, {
    turnId: string
    resolve: (sent: boolean) => void
    reject: (error: AskSubmissionError) => void
    timer: ReturnType<typeof setTimeout>
  }>())
  const settleAsk = useCallback((id: string, error?: AskSubmissionError) => {
    const pending = pendingAsksRef.current.get(id)
    if (!pending) return
    pendingAsksRef.current.delete(id)
    clearTimeout(pending.timer)
    if (error) {
      setTurns((turns) => turns.filter((turn) => turn.id !== pending.turnId))
      setIsStreaming(false)
      pending.reject(error)
    } else pending.resolve(true)
  }, [])
  const rejectPendingAsks = useCallback((message: string) => {
    for (const id of pendingAsksRef.current.keys()) settleAsk(id, new AskSubmissionError(message))
  }, [settleAsk])
  useEffect(() => () => {
    rejectPendingAsks("The conversation changed before this form was confirmed. Your answers are still here.")
  }, [authorityKey, rejectPendingAsks])
  const isStreamingRef = useRef(false)
  isStreamingRef.current = isStreaming
  const textBufferRef = useRef("")
  const thinkingBufferRef = useRef("")
  // Tracked in a ref so the (deps: []) WS handlers see the latest value without
  // being re-created on every render.
  const currentUserIdRef = useRef(currentUserId)
  currentUserIdRef.current = currentUserId
  // Streaming text arrives token-by-token. pendingTextRef accumulates the
  // tokens seen since the last commit; rafIdRef holds the scheduled frame
  // so a whole burst commits with a single setTurns instead of one per
  // token (each commit re-renders the streaming turn and re-parses its
  // markdown). See flushPendingText / scheduleTextFlush below.
  const pendingTextRef = useRef("")
  const rafIdRef = useRef<number | null>(null)
  // True between stopGeneration() and the next sendMessage/regenerate/edit.
  // Used to drop chat_event deltas that arrive after a local cancel — the
  // server's cancel ack races against in-flight packets, and without this
  // gate the late deltas re-create the cancelled assistant turn and the
  // typing indicator reappears. Only blocks AFTER an explicit cancel so
  // unsolicited stream events (multi-tab observation, history replay)
  // still flow through.
  const cancelledRef = useRef(false)
  // Stop sent on the canonical stream and no terminal turn state confirmed
  // yet. Kept apart from the turns: a stop can precede the first event of its
  // turn, and a history reload replaces the turns while the stop is pending.
  const [stopPending, setStopPending] = useState(false)
  const stopPendingRef = useRef(false)

  // --- Resumable-stream reassembly ---------------------------------------
  // Streamed chat events carry a per-session monotonic seq (see internal/ws).
  // On reconnect the client replays the gap, so the same event can arrive twice
  // and out of order (replay interleaved with live). lastSeqRef is the highest
  // contiguous seq applied; pendingRef holds out-of-order events keyed by seq
  // until their predecessors arrive. This guarantees every event is applied
  // exactly once, in order — the core "never lose (or double) text" invariant.
  const lastSeqRef = useRef(0)
  const pendingRef = useRef<Map<number, { eventType?: StreamEventType; content: string; metadata?: Record<string, unknown>; canonical?: boolean }>>(new Map())
  // Live/replayed events are held until the session's history has loaded, so a
  // late-resolving history fetch can't clobber an already-rendered in-flight
  // run (the mount race). loadHistory flips this true and drains.
  const historyLoadedRef = useRef(false)
  const activeRunIdRef = useRef<string | undefined>(undefined)
  const canonicalRef = useRef(new Map<string, { turnId: string; through: number; pending: Map<number, ChatEvent>; seen: Set<string>; refreshes: number; resetThrough?: number; state?: TurnState }>())
  const pendingControlsRef = useRef(new Map<string, PendingControls>())
  const canonicalActiveRef = useRef(false)
  const requestCanonicalHistory = useCallback((runId: string) => {
    const cursor = canonicalRef.current.get(runId)
    if (cursor && cursor.refreshes < 2) {
      cursor.refreshes++
      onStreamResetRef.current?.()
    }
  }, [])

  // Refs so the stable onConnect callback (passed to useWebSocket before `send`
  // exists) can resubscribe/resume without being re-created.
  const sendRef = useRef<((msg: WSMessage) => void) | null>(null)
  const sessionIdRef = useRef(sessionId)
  sessionIdRef.current = sessionId
  // Set by the caller; invoked when history must be reloaded — either the server
  // said the replay buffer overflowed (resume_reset) or we reconnected and a run
  // may have finished/advanced while we were away.
  const onStreamResetRef = useRef<(() => void) | null>(null)
  onStreamResetRef.current = onStreamReset ?? null
  // False until the socket's first open, so we can tell a fresh mount (history is
  // already loading) from a reconnect (must reset + reload to recover a run that
  // finished or advanced while disconnected).
  const hasConnectedRef = useRef(false)
  // After a truncated-buffer resume_reset there is no run_begin to re-anchor the
  // cursor, so the next seq'd frame seen becomes the new baseline (the truncated
  // middle is unrecoverable; we resync to the live tail).
  const adoptNextSeqRef = useRef(false)

  const clearStreamState = useCallback(() => {
    restrictedAbortRef.current?.abort()
    restrictedAbortRef.current = null
    activeRunIdRef.current = undefined
    canonicalRef.current.clear()
    pendingControlsRef.current.clear()
    canonicalActiveRef.current = false
    replySoundSinceRef.current = Date.now()
    replyHasTextRef.current = false
    setIsStreaming(false)
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    cancelledRef.current = false
    stopPendingRef.current = false
    setStopPending(false)
    pendingTextRef.current = ""
    if (rafIdRef.current !== null) {
      cancelAnimationFrame(rafIdRef.current)
      rafIdRef.current = null
    }
    lastSeqRef.current = 0
    pendingRef.current.clear()
    historyLoadedRef.current = false
    adoptNextSeqRef.current = false
  }, [])

  useEffect(() => {
    const changedScope = authorityRef.current !== authorityKey
    authorityRef.current = authorityKey
    clearStreamState()
    // A conversation change invalidates the visible transcript even for the same human.
    if (changedScope) {
      setTurns([])
      setDeniedConversation(null)
    }
  }, [sessionId, authorityKey, clearStreamState])

  // Cancel any in-flight frame on unmount so the scheduled callback never
  // runs against a torn-down component.
  useEffect(() => {
    return () => {
      restrictedAbortRef.current?.abort()
      if (rafIdRef.current !== null) {
        cancelAnimationFrame(rafIdRef.current)
        rafIdRef.current = null
      }
    }
  }, [])

  const handleAssignmentEvent = useCallback(
    (type: AssignmentEventType, payload: Record<string, unknown>) => {
      let content = ""
      const target = assignmentField(payload.target)
      const task = assignmentField(payload.task)
      const result = assignmentField(payload.result)
      const errMsg = assignmentField(payload.error)
      switch (type) {
        case "assignment_created":
          content = `[Assignment] Assigning task to @${target}: ${task}`
          break
        case "assignment_running":
          content = `[Assignment] @${target} is working on the task...`
          break
        case "assignment_completed":
          content = `[Assignment] @${target} completed the task.`
          if (result) content += `\nResult: ${result}`
          break
        case "assignment_failed":
          content = `[Assignment] @${target} failed: ${errMsg}`
          break
      }
      setTurns((prev) => [
        ...prev,
        {
          id: uuid(),
          role: "system",
          parts: [{ id: uuid(), type: "text", content, timestamp: new Date() }],
          isStreaming: false,
          timestamp: new Date(),
        },
      ])
    },
    [],
  )

  const handleStatusEvent = useCallback((content: string) => {
    setTurns((prev) => {
      const last = prev[prev.length - 1]
      if (last?.role === "assistant" && last.isStreaming) {
        // A turn shows at most ONE status line — a quiet, in-place "current
        // step" indicator. Bursts of internal progress chatter (thinking_tokens,
        // task_started, task_progress, …) must update that single line, never
        // stack into a column of rows. Replace an existing status part if there
        // is one; otherwise append a single status part at the end.
        const statusIdx = last.parts.findLastIndex((p) => p.type === "status")
        if (statusIdx >= 0) {
          const updatedParts = [...last.parts]
          updatedParts[statusIdx] = { ...updatedParts[statusIdx], content }
          return [...prev.slice(0, -1), { ...last, parts: updatedParts }]
        }
        return [
          ...prev.slice(0, -1),
          {
            ...last,
            parts: [
              ...last.parts,
              { id: uuid(), type: "status" as TurnPartType, content, timestamp: new Date() },
            ],
          },
        ]
      }
      // Create new assistant turn with status part
      return [
        ...prev,
        {
          id: uuid(),
          role: "assistant",
          parts: [{ id: uuid(), type: "status" as TurnPartType, content, timestamp: new Date() }],
          isStreaming: true,
          timestamp: new Date(),
        },
      ]
    })
  }, [])

  const handleThinkingEvent = useCallback(
    (content: string) => {
      // A single reasoning pass is chunked into many `thinking` events by the
      // adapter (thinking_delta) and the stdout scrubber (buffered-tail flushes
      // that drop the streaming flag). We must NOT relabel each chunk a separate
      // "Thought" card — the boundaries are arbitrary and split sentences. So we
      // accumulate thinking chunks into one part regardless of the streaming
      // flag, looking PAST transient status parts (progress lines / non-init
      // system logs) when locating the open block: the backend PartAccumulator
      // ignores those when persisting, so if they split the live block, a
      // streamed turn shows N "Thought for 1s" stubs where the reload shows one.
      // A real content event (text/tool) still closes the block, so genuinely
      // separate reasoning passes (e.g. think → tool → think) stay distinct.
      setTurns((prev) => {
        const last = prev[prev.length - 1]
        if (last?.role === "assistant" && last.isStreaming) {
          let anchorIdx = last.parts.length - 1
          while (anchorIdx >= 0 && last.parts[anchorIdx].type === "status") anchorIdx--
          const anchor = anchorIdx >= 0 ? last.parts[anchorIdx] : undefined
          if (anchor?.type === "thinking" && anchor.isStreaming) {
            // Same reasoning pass — append into the open block, leaving any
            // trailing status line in place below it.
            thinkingBufferRef.current += content
            const updatedParts = [...last.parts]
            updatedParts[anchorIdx] = {
              ...anchor,
              content: thinkingBufferRef.current,
              isStreaming: true,
            }
            return [...prev.slice(0, -1), { ...last, parts: updatedParts }]
          }
          // First thinking chunk after text/tool — finalize any open
          // streaming text part, drop status parts, open a fresh thinking block.
          thinkingBufferRef.current = content
          const cleanedParts = stripStatusParts(last.parts)
          return [
            ...prev.slice(0, -1),
            {
              ...last,
              parts: [
                ...cleanedParts,
                { id: uuid(), type: "thinking" as TurnPartType, content, isStreaming: true, timestamp: new Date() },
              ],
            },
          ]
        }
        // Create new assistant turn — remove any orphaned status-only turns
        thinkingBufferRef.current = content
        const cleaned = prev.filter((t) => {
          if (t.role === "assistant" && t.isStreaming && t.parts.every((p) => p.type === "status")) {
            return false
          }
          return true
        })
        return [
          ...cleaned,
          {
            id: uuid(),
            role: "assistant",
            parts: [{ id: uuid(), type: "thinking" as TurnPartType, content, isStreaming: true, timestamp: new Date() }],
            isStreaming: true,
            timestamp: new Date(),
          },
        ]
      })
    },
    [],
  )

  // A message from ANOTHER human in a shared group chat (the backend broadcasts
  // it to other session subscribers). Render it as a distinct user turn,
  // attributed to its author, without touching the local streaming state.
  const handleUserMessageEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      const authorUserId = typeof metadata?.author_user_id === "string"
        ? (metadata.author_user_id as string)
        : undefined
      // Drop the echo of our OWN message — the server broadcasts every user
      // message to all session subscribers (incl. the sender), but we already
      // rendered it optimistically in sendMessage. Without this guard the
      // sender sees their message twice.
      if (authorUserId && currentUserIdRef.current && authorUserId === currentUserIdRef.current) {
        ownMessageSavedRef.current?.(content, metadata)
        return
      }
      const userTurn: ChatTurn = {
        id: uuid(),
        role: "user",
        parts: [{ id: uuid(), type: "text", content, timestamp: new Date() }],
        isStreaming: false,
        timestamp: new Date(),
        authorUserId,
      }
      setTurns((prev) => {
        // If the assistant is mid-turn, insert the teammate's message BEFORE the
        // streaming turn so it stays the tail — otherwise the next text/tool
        // delta would see a non-assistant last turn and spawn a second one.
        const last = prev[prev.length - 1]
        if (last?.role === "assistant" && last.isStreaming) {
          return [...prev.slice(0, -1), userTurn, last]
        }
        return [...prev, userTurn]
      })
    },
    [],
  )

  const handleTextEvent = useCallback((content: string) => {
    setTurns((prev) => {
      const last = prev[prev.length - 1]
      if (last?.role === "assistant" && last.isStreaming) {
        // Only accumulate into the IMMEDIATELY-preceding text part. Matching any
        // earlier streaming text part (the old findLastIndex) merged text that
        // arrived AFTER a tool_result back into the pre-tool bubble — so the
        // final answer landed above the tool call and nothing rendered after it.
        // A tool/thinking event between two text runs finalizes the first, so
        // this check starts a new part in the correct position.
        const lastPart = last.parts[last.parts.length - 1]
        if (lastPart?.type === "text" && lastPart.isStreaming) {
          textBufferRef.current += content
          const updatedParts = [...last.parts]
          updatedParts[updatedParts.length - 1] = {
            ...lastPart,
            content: textBufferRef.current,
          }
          return [...prev.slice(0, -1), { ...last, parts: updatedParts }]
        }
        // First text of a new run: remove status parts + close streaming thinking
        const cleanedParts = stripStatusParts(last.parts)
        // New text part
        textBufferRef.current = content
        return [
          ...prev.slice(0, -1),
          {
            ...last,
            parts: [
              ...cleanedParts,
              { id: uuid(), type: "text" as TurnPartType, content, isStreaming: true, timestamp: new Date() },
            ],
          },
        ]
      }
      // Create new assistant turn — remove any orphaned status-only turns
      const cleaned = prev.filter((t) => {
        if (t.role === "assistant" && t.isStreaming && t.parts.every((p) => p.type === "status")) {
          return false
        }
        return true
      })
      textBufferRef.current = content
      return [
        ...cleaned,
        {
          id: uuid(),
          role: "assistant",
          runId: activeRunIdRef.current,
          parts: [{ id: uuid(), type: "text" as TurnPartType, content, isStreaming: true, timestamp: new Date() }],
          isStreaming: true,
          timestamp: new Date(),
        },
      ]
    })
  }, [])

  // flushPendingText commits the buffered tokens (if any) with a single
  // handleTextEvent call and clears the scheduled frame. Called from the
  // animation frame and synchronously before any non-text event so the
  // streamed text lands in order ahead of tool calls, status, done, etc.
  const flushPendingText = useCallback(() => {
    if (rafIdRef.current !== null) {
      cancelAnimationFrame(rafIdRef.current)
      rafIdRef.current = null
    }
    const pending = pendingTextRef.current
    if (!pending) return
    pendingTextRef.current = ""
    handleTextEvent(pending)
  }, [handleTextEvent])

  // scheduleTextFlush coalesces a burst of tokens into one commit per
  // animation frame. Falls back to a synchronous flush where rAF is
  // unavailable (non-browser / tests without the global stubbed).
  const scheduleTextFlush = useCallback(() => {
    if (typeof requestAnimationFrame === "undefined") {
      flushPendingText()
      return
    }
    if (rafIdRef.current !== null) return
    rafIdRef.current = requestAnimationFrame(() => {
      rafIdRef.current = null
      flushPendingText()
    })
  }, [flushPendingText])

  const handleToolCallEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      // A tool ends the current text/thinking run: reset the accumulation
      // buffers so any text/thinking that follows opens a fresh part in its
      // correct position instead of appending to the pre-tool bubble.
      textBufferRef.current = ""
      thinkingBufferRef.current = ""
      setTurns((prev) => {
        const last = prev[prev.length - 1]
        const part: TurnPart = {
          id: uuid(),
          type: "tool_call",
          content,
          metadata,
          timestamp: new Date(),
        }
        if (last?.role === "assistant" && last.isStreaming) {
          const finalizedParts = last.parts.map((p) =>
            (p.type === "text" || p.type === "thinking") && p.isStreaming
              ? { ...p, isStreaming: false }
              : p
          )
          return [
            ...prev.slice(0, -1),
            { ...last, parts: [...finalizedParts, part] },
          ]
        }
        return [
          ...prev,
          {
            id: uuid(),
            role: "assistant",
            parts: [part],
            isStreaming: true,
            timestamp: new Date(),
          },
        ]
      })
    },
    [],
  )

  const handleToolResultEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      // Same as tool_call: the result closes the current text/thinking run so
      // the model's follow-up answer opens a fresh part AFTER the result.
      textBufferRef.current = ""
      thinkingBufferRef.current = ""
      setTurns((prev) => {
        const last = prev[prev.length - 1]
        const part: TurnPart = {
          id: uuid(),
          type: "tool_result",
          content,
          metadata,
          timestamp: new Date(),
        }
        if (last?.role === "assistant" && last.isStreaming) {
          // Try to mark matching tool_call as completed via tool_use_id, and
          // finalize any still-open streaming text/thinking part.
          const toolUseId = metadata?.tool_use_id as string | undefined
          const updatedParts = last.parts.map((p) => {
            if (toolUseId && p.type === "tool_call" && p.metadata?.tool_id === toolUseId) {
              return { ...p, metadata: { ...p.metadata, completed: true } }
            }
            if ((p.type === "text" || p.type === "thinking") && p.isStreaming) {
              return { ...p, isStreaming: false }
            }
            return p
          })
          return [
            ...prev.slice(0, -1),
            { ...last, parts: [...updatedParts, part] },
          ]
        }
        return [
          ...prev,
          {
            id: uuid(),
            role: "assistant",
            parts: [part],
            isStreaming: true,
            timestamp: new Date(),
          },
        ]
      })
    },
    [],
  )

  const handleImageEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      setTurns((prev) => {
        const last = prev[prev.length - 1]
        const part: TurnPart = {
          id: uuid(),
          type: "image",
          content,
          metadata,
          timestamp: new Date(),
        }
        if (last?.role === "assistant" && last.isStreaming) {
          return [
            ...prev.slice(0, -1),
            { ...last, parts: [...last.parts, part] },
          ]
        }
        return [
          ...prev,
          {
            id: uuid(),
            role: "assistant",
            parts: [part],
            isStreaming: true,
            timestamp: new Date(),
          },
        ]
      })
    },
    [],
  )

  const handleResultEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      // Run result with cost/usage/duration metadata — add as result part
      setTurns((prev) => {
        const last = prev[prev.length - 1]
        const part: TurnPart = {
          id: uuid(),
          type: "result",
          content: content || "",
          metadata,
          timestamp: new Date(),
        }
        if (last?.role === "assistant" && last.isStreaming) {
          return [
            ...prev.slice(0, -1),
            { ...last, parts: [...last.parts, part] },
          ]
        }
        return [
          ...prev,
          {
            id: uuid(),
            role: "assistant",
            parts: [part],
            isStreaming: true,
            timestamp: new Date(),
          },
        ]
      })
    },
    [],
  )

  const handleSystemEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      // Claude Code system events: init (model, tools, cwd) or compact_boundary
      const subtype = metadata?.subtype as string | undefined
      if (subtype === "init") {
        setTurns((prev) => {
          // Only show session init once per session — skip if already shown
          const alreadyHasInit = prev.some((t) =>
            t.role === "system" && t.parts.some((p) => p.type === "system_init")
          )
          if (alreadyHasInit) return prev

          // Remove preceding status-only assistant turn (Starting container..., etc.)
          let cleaned = prev
          const last = prev[prev.length - 1]
          if (last?.role === "assistant" && last.isStreaming && last.parts.every((p) => p.type === "status")) {
            cleaned = prev.slice(0, -1)
          }

          return [
            ...cleaned,
            {
              id: uuid(),
              role: "system",
              parts: [{
                id: uuid(),
                type: "system_init" as TurnPartType,
                content: content || "init",
                metadata,
                timestamp: new Date(),
              }],
              isStreaming: false,
              timestamp: new Date(),
            },
          ]
        })
      } else if (subtype === "thinking_tokens") {
        // Heartbeat chatter: Claude Code emits a `system`/thinking_tokens
        // message per reasoning progress tick and the adapter forwards it
        // with content = subtype. Rendering these stacked a wall of
        // "thinking_tokens" rows under the reply; the Thinking header's live
        // timer already conveys the same signal. Drop them entirely.
        return
      } else {
        // Other system events (sidecar security logs, api_retry, …) render
        // as the single quiet current-step line — same replace-in-place
        // policy as status events, never a stack of rows. Unlike status
        // events they never open a new turn: trailing chatter after `done`
        // must not resurrect a ghost status-only turn.
        setTurns((prev) => {
          const last = prev[prev.length - 1]
          if (last?.role !== "assistant" || !last.isStreaming) return prev
          const statusIdx = last.parts.findLastIndex((p) => p.type === "status")
          const part = { id: uuid(), type: "status" as TurnPartType, content, timestamp: new Date() }
          if (statusIdx >= 0) {
            const updatedParts = [...last.parts]
            updatedParts[statusIdx] = { ...updatedParts[statusIdx], content }
            return [...prev.slice(0, -1), { ...last, parts: updatedParts }]
          }
          return [...prev.slice(0, -1), { ...last, parts: [...last.parts, part] }]
        })
      }
    },
    [],
  )

  const handleDoneEvent = useCallback((metadata?: Record<string, unknown>) => {
    // The "done" WS event may carry { trace_id } in metadata — the
    // backend stamps the active OTel trace id there so the assistant
    // turn ties back to the routine run that produced it. Lifted onto
    // ChatTurn.metadata.trace_id so feedback POSTs from this turn can
    // include the trace id for eval-mining correlation.
    // A done marked no_reply legitimately carries no assistant turn (group
    // chat where the agent wasn't @mentioned) — never synthesize an error
    // for it.
    const noReply = metadata?.no_reply === true
    const repliedAt = typeof metadata?.replied_at === "string" ? metadata.replied_at : ""
    const stamp = Date.parse(repliedAt)
    const now = Date.now()
    if (!noReply && !cancelledRef.current && replyHasTextRef.current
      && typeof metadata?.message_id === "string" && metadata.message_id.trim()
      && Number.isFinite(stamp) && stamp > replySoundSinceRef.current && stamp <= now + 5000 && now - stamp < 60000) {
      completedReplyRef.current?.({ sessionId: sessionIdRef.current, repliedAt })
    }
    replyHasTextRef.current = false

    // Whether OUR run was pending when this done arrived. Captured before
    // the setTurns updater (which React may invoke more than once).
    const localRunPending = isStreamingRef.current
    setTurns((prev) => {
      // Remove any orphaned status-only assistant turns and finalize the streaming turn
      const cleaned = prev.filter((t) => {
        if (t.role === "assistant" && t.isStreaming && t.parts.every((p) => p.type === "status")) {
          return false
        }
        return true
      })
      const last = cleaned[cleaned.length - 1]
      if (last?.role === "assistant" && last.isStreaming) {
        const finalParts = stripStatusParts(last.parts)
        const persistedMessageId = typeof metadata?.message_id === "string" && metadata.message_id.trim()
          ? metadata.message_id
          : last.id
        const finalTurn: ChatTurn = {
          ...last,
          id: persistedMessageId,
          parts: finalParts,
          isStreaming: false,
        }
        // `done` is the durable boundary for metadata that describes the
        // completed assistant turn. Trace correlation was the first reader,
        // but onboarding proposal suggestions use the same transport. Keep
        // the map open-ended as ChatTurn.metadata's type promises instead of
        // silently retaining only trace_id.
        if (metadata && Object.keys(metadata).length > 0) {
          finalTurn.metadata = { ...(last.metadata ?? {}), ...metadata }
        }
        return [...cleaned.slice(0, -1), finalTurn]
      }
      // done arrived while OUR send was pending, but nothing replied — no
      // assistant turn, no error event (zero-output run, #545, on a server
      // that couldn't say more / a lost error frame). Leaving the transcript
      // as just the user's message is indistinguishable from a broken app,
      // so surface an explicit error turn. Assistant role so the Regenerate
      // retry affordance renders under it. Skipped for no_reply dones and
      // for runs that already got a reply/error.
      //
      // The tail turn can't be trusted alone: in a group chat, a teammate's
      // user_message broadcast can land after our send and become the last
      // turn (it carries authorUserId), which would otherwise mask a real
      // zero-output run. Walk back from the tail past any such teammate
      // turns to find our own pending user turn; stop at anything else
      // (an assistant/system turn means our run already got a reply).
      let ownTurnStillPending = false
      for (let i = cleaned.length - 1; i >= 0; i--) {
        const t = cleaned[i]
        if (t.role === "user" && t.authorUserId) continue
        ownTurnStillPending = t.role === "user" && !t.authorUserId
        break
      }
      if (!noReply && localRunPending && ownTurnStillPending) {
        return [
          ...cleaned,
          {
            id: uuid(),
            role: "assistant",
            parts: [{ id: uuid(), type: "error" as TurnPartType, content: NO_OUTPUT_ERROR, timestamp: new Date() }],
            isStreaming: false,
            timestamp: new Date(),
          },
        ]
      }
      return cleaned
    })
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    setIsStreaming(false)
  }, [])

  const handleCrewProvisioningEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      // Auto-provision kicked off by chatbridge — render a system turn
      // carrying the crew_id so the chat surface can render the same
      // build progress card the toolbar popover shows. Replaces the
      // legacy red "Run `crewship crew provision …` first" error.
      setTurns((prev) => [
        ...prev,
        {
          id: uuid(),
          role: "system",
          parts: [
            {
              id: uuid(),
              type: "crew_provisioning",
              content: content || "Building crew image…",
              metadata,
              timestamp: new Date(),
            },
          ],
          isStreaming: false,
          timestamp: new Date(),
        },
      ])
      setIsStreaming(false)
    },
    [],
  )

  const handleErrorEvent = useCallback((content: string) => {
    replyHasTextRef.current = false
    setTurns((prev) => {
      const last = prev[prev.length - 1]
      const errorPart: TurnPart = {
        id: uuid(),
        type: "error",
        content: content || "An error occurred",
        timestamp: new Date(),
      }
      if (last?.role === "assistant" && last.isStreaming) {
        // Finalize any open streaming parts (e.g. a thinking block) so they stop
        // rendering a "thinking…" spinner on a turn that has actually errored,
        // and drop the transient status row like handleDoneEvent does.
        const finalizedParts = stripStatusParts(last.parts)
        return [
          ...prev.slice(0, -1),
          { ...last, parts: [...finalizedParts, errorPart], isStreaming: false },
        ]
      }
      // No open assistant turn (e.g. a zero-output run that streamed nothing
      // before failing, #545). Render the error as an ASSISTANT turn so the
      // chat's existing retry affordance — the Regenerate button under the
      // last assistant turn — appears with the error bubble.
      return [
        ...prev,
        {
          id: uuid(),
          role: "assistant",
          parts: [errorPart],
          isStreaming: false,
          timestamp: new Date(),
        },
      ]
    })
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    setIsStreaming(false)
  }, [])

  // applyChatEvent dispatches a single (already in-order) chat event to the
  // per-type handlers. Text is coalesced per animation frame; every other event
  // first flushes buffered text so nothing renders ahead of the text it
  // followed on the wire.
  const applyChatEvent = useCallback(
    (eventType: StreamEventType | undefined, content: string, metadata?: Record<string, unknown>) => {
      const controlRun = typeof metadata?.run_id === "string" ? metadata.run_id : undefined
      if (((eventType === "result" || eventType === "done") && (canonicalActiveRef.current || metadata?.event_contract_version === 1))
        || (eventType === "system" && (metadata?.event_contract_version === 1 || (controlRun && canonicalRef.current.has(controlRun))))) {
        // Controls carry provenance/accounting, never sequence or completion.
        const turnId = typeof metadata?.turn_id === "string" ? metadata.turn_id : controlRun ? canonicalRef.current.get(controlRun)?.turnId : undefined
        if (eventType !== "done" && controlRun && controlRun.length <= 256 && turnId && turnId.length <= 256 && !content
          && (eventType !== "system" || metadata?.subtype === "init")) {
          canonicalActiveRef.current = true
          let controls = pendingControlsRef.current.get(controlRun)
          if (!controls && pendingControlsRef.current.size < MAX_PENDING_EVENTS) {
            controls = { turnId, parts: new Map() }
            pendingControlsRef.current.set(controlRun, controls)
          }
          if (controls?.turnId !== turnId) return
          const type = eventType === "system" ? "system_init" : "result"
          controls.parts.set(type, metadata ?? {})
          const captured = controls
          setTurns(previous => previous.map(turn => turn.role === "assistant" && turn.runId === controlRun && turn.id === turnId
            ? { ...turn, parts: foldControls(turn.parts, captured, controlRun, turn.timestamp) } : turn))
        }
        return
      }
      if (controlRun) activeRunIdRef.current = controlRun
      if (eventType === "text" && content.trim()) replyHasTextRef.current = true
      if (eventType === "error" || eventType === "agent_busy") replyHasTextRef.current = false
      if (eventType === "text") {
        pendingTextRef.current += content
        scheduleTextFlush()
        return
      }
      flushPendingText()
      switch (eventType) {
        case "status": handleStatusEvent(content); break
        case "thinking": handleThinkingEvent(""); break
        case "tool_call": handleToolCallEvent(content, metadata); break
        case "tool_result": handleToolResultEvent(content, metadata); break
        case "image": handleImageEvent(content, metadata); break
        case "result": handleResultEvent(content, metadata); break
        case "system": handleSystemEvent(content, metadata); break
        case "done": handleDoneEvent(metadata); break
        case "crew_provisioning": handleCrewProvisioningEvent(content, metadata); break
        case "user_message": handleUserMessageEvent(content, metadata); break
        case "error": handleErrorEvent(content); break
        // Cross-user run exclusivity (backend: chatbridge.HandleChatMessage):
        // another sender's message is already being processed for this chat.
        // Reuses the error rendering shape — additive case only, no new UI.
        case "agent_busy": handleErrorEvent(content); sendRefusedRef.current?.("busy"); break
      }
      if (activeRunIdRef.current) {
        const runId = activeRunIdRef.current
        setTurns((prev) => {
          const last = prev.at(-1)
          return last?.role === "assistant" ? [...prev.slice(0, -1), { ...last, runId }] : prev
        })
      }
    },
    [
      scheduleTextFlush, flushPendingText, handleStatusEvent, handleThinkingEvent,
      handleToolCallEvent, handleToolResultEvent, handleImageEvent, handleResultEvent,
      handleSystemEvent, handleDoneEvent, handleCrewProvisioningEvent, handleUserMessageEvent,
      handleErrorEvent,
    ],
  )

  // drainPending applies buffered events in strict seq order, as far as the
  // contiguous run allows. Gated on history having loaded so a late history
  // fetch can't wipe an already-rendered in-flight run.
  const drainPending = useCallback(() => {
    if (!historyLoadedRef.current) return
    const p = pendingRef.current
    for (;;) {
      const next = p.get(lastSeqRef.current + 1)
      if (next === undefined) break
      p.delete(lastSeqRef.current + 1)
      lastSeqRef.current += 1
      if (!next.canonical) applyChatEvent(next.eventType, next.content, next.metadata)
    }
  }, [applyChatEvent])

  // ingestChatEvent reassembles the resumable stream: unseq'd events (legacy /
  // non-run broadcasts) apply immediately; seq'd events are deduped and ordered.
  const ingestChatEvent = useCallback(
    (seq: number, eventType: StreamEventType | undefined, content: string, metadata?: Record<string, unknown>) => {
      if (!seq) {
        // No sequence number — not part of a resumable run stream (legacy
        // server, or a non-run broadcast). Apply immediately, as before.
        applyChatEvent(eventType, content, metadata)
        return
      }
      // After a truncated resume_reset, adopt the first seq seen as the baseline
      // so the live tail can drain (the dropped middle is unrecoverable).
      if (adoptNextSeqRef.current) {
        adoptNextSeqRef.current = false
        if (seq - 1 > lastSeqRef.current) lastSeqRef.current = seq - 1
      }
      if (seq <= lastSeqRef.current) return // already applied — duplicate from replay∩live
      pendingRef.current.set(seq, { eventType, content, metadata })
      // Safety valve: if a gap never fills (a truly lost frame) the buffer would
      // grow forever. Past the cap, skip to the smallest pending seq so the
      // stream keeps moving rather than freezing.
      if (pendingRef.current.size > MAX_PENDING_EVENTS) {
        let min = Infinity
        for (const k of pendingRef.current.keys()) if (k < min) min = k
        if (min !== Infinity && min - 1 > lastSeqRef.current) lastSeqRef.current = min - 1
      }
      drainPending()
    },
    [applyChatEvent, drainPending],
  )

  const ingestCanonicalEvent = useCallback((event: ChatEvent) => {
    canonicalActiveRef.current = true
    let cursor = canonicalRef.current.get(event.run_id)
    if (!cursor) {
      if (canonicalRef.current.size >= MAX_PENDING_EVENTS) { requestCanonicalHistory(event.run_id); return false }
      cursor = { turnId: event.turn_id, through: 0, pending: new Map(), seen: new Set(), refreshes: 0 }
      canonicalRef.current.set(event.run_id, cursor)
    }
    if (cursor.turnId !== event.turn_id) return false
    if (event.seq <= cursor.through || cursor.seen.has(event.event_id)) return true
    const existing = cursor.pending.get(event.seq)
    if (existing && existing.event_id !== event.event_id) { requestCanonicalHistory(event.run_id); return false }
    const queued = [...canonicalRef.current.values()].reduce((count, value) => count + value.pending.size, 0)
    if (!existing && queued >= MAX_PENDING_EVENTS) { requestCanonicalHistory(event.run_id); return false }
    cursor.pending.set(event.seq, event)
    if (!historyLoadedRef.current) return true
    for (;;) {
      const next = cursor.pending.get(cursor.through + 1)
      if (!next) break
      cursor.pending.delete(next.seq)
      cursor.through = next.seq
      if (cursor.seen.has(next.event_id)) continue
      cursor.seen.add(next.event_id)
      // Seq remains the durable replay boundary; event IDs only need a bounded
      // recent window once earlier seqs have been folded into the turn.
      if (cursor.seen.size > MAX_PENDING_EVENTS) cursor.seen.delete(cursor.seen.values().next().value!)
      activeRunIdRef.current = next.run_id
      if (next.type === "turn") cursor.state = next.payload.turn.state
      else cursor.state ??= "running"
      const controls = pendingControlsRef.current.get(next.run_id)
      if (controls?.turnId === next.turn_id) pendingControlsRef.current.delete(next.run_id)
      const stopRequested = stopPendingRef.current
      setTurns(previous => {
        const at = previous.findIndex(turn => turn.role === "assistant" && (turn.id === next.turn_id || (turn.runId === next.run_id && turn.parts.some(part => part.turn_id === next.turn_id))))
        const prior = at >= 0 ? previous[at] : undefined
        const state = next.type === "turn" ? next.payload.turn.state : prior?.turnState ?? "running"
        const terminal = terminalTurn(state)
        let parts = foldControls(prior?.parts ?? [], controls?.turnId === next.turn_id ? controls : undefined, next.run_id, prior?.timestamp ?? new Date(next.occurred_at))
        if (next.type !== "turn") {
          const old = parts.find(part => part.id === next.part_id)
          const common = { id: next.part_id!, run_id: next.run_id, turn_id: next.turn_id, first_seq: old?.first_seq ?? next.seq, last_seq: next.seq, tool_call_id: next.tool_call_id, parent_tool_call_id: next.parent_tool_call_id, timestamp: old?.timestamp ?? new Date(next.occurred_at), isStreaming: !terminal }
          let part: TurnPart
          if (next.type === "text") part = { ...common, type: "text", content: next.payload.text.operation === "delta" ? (old?.content ?? "") + next.payload.text.content : next.payload.text.content }
          else if (next.type === "thinking") part = { ...common, type: "thinking", content: next.payload.thinking.visibility === "summary" ? next.payload.thinking.summary ?? "" : "", thinking: next.payload.thinking, isStreaming: next.payload.thinking.state === "running" }
          else if (next.type === "activity") part = { ...common, type: "activity", content: next.payload.activity.name ?? next.payload.activity.kind, activity: next.payload.activity, metadata: { tool_name: next.payload.activity.name ?? next.payload.activity.kind, tool_id: next.tool_call_id } }
          else part = { ...common, type: "output", content: next.payload.output.label ?? next.payload.output.state, output: next.payload.output }
          parts = old ? parts.map(value => value.id === part.id ? part : value) : [...parts, part]
        }
        if (terminal) parts = parts.map(part => ({ ...part, isStreaming: false }))
        const turn: ChatTurn = { ...prior, id: prior?.id ?? next.turn_id, runId: next.run_id, role: "assistant", timestamp: prior?.timestamp ?? new Date(next.occurred_at), parts, turnState: state, isStreaming: !terminal, ...(stopRequested && !terminal ? { cancelRequested: true } : {}) }
        return at < 0 ? [...previous, turn] : previous.map((value, index) => index === at ? turn : value)
      })
      setIsStreaming(!terminalTurn(cursor.state))
      if (terminalTurn(cursor.state)) { stopPendingRef.current = false; setStopPending(false) }
    }
    if (cursor.resetThrough !== undefined && cursor.through >= cursor.resetThrough) cursor.resetThrough = undefined
    const incomplete = cursor.pending.size > 0 || cursor.resetThrough !== undefined
    setTurns(previous => previous.map(turn => turn.runId === event.run_id ? { ...turn, streamIncomplete: incomplete } : turn))
    if (cursor.pending.size) requestCanonicalHistory(event.run_id)
    else cursor.refreshes = 0
    return true
  }, [requestCanonicalHistory])

  const handleMessage = useCallback(
    (msg: { type: string; payload?: string | Record<string, unknown>; channel?: string; [key: string]: unknown }) => {
      if (!isCurrentAuthority()) return
      const channelSessionId = msg.channel?.startsWith("session:") ? msg.channel.slice(8) : undefined
      const envSeq = typeof msg.seq === "number" ? (msg.seq as number) : 0

      // Submission receipts are addressed to this session and do not wait for
      // history replay or stream sequencing. Only the matching author/id can
      // acknowledge the durable append; socket send alone is not acceptance.
      if (msg.type === "chat_event" && channelSessionId === sessionId) {
        const event = msg.payload && typeof msg.payload === "object" ? msg.payload as Record<string, unknown> : {}
        const metadata = event.metadata && typeof event.metadata === "object" ? event.metadata as Record<string, unknown> : {}
        if (event.type === "ask_submission_error" && typeof metadata.submission_id === "string") {
          settleAsk(metadata.submission_id, askSubmissionError({ error: event.content, ask_errors: metadata.ask_errors }))
          return
        }
        if (event.type === "user_message" && typeof metadata.ask_submission_id === "string" &&
          typeof metadata.author_user_id === "string" && metadata.author_user_id === currentUserIdRef.current) {
          settleAsk(metadata.ask_submission_id)
        }
      }

      // Handle assignment lifecycle events
      const assignmentTypes: AssignmentEventType[] = ["assignment_created", "assignment_running", "assignment_completed", "assignment_failed"]
      if (assignmentTypes.includes(msg.type as AssignmentEventType)) {
        if (channelSessionId && channelSessionId !== sessionId) return
        const payload = (typeof msg.payload === "object" && msg.payload !== null)
          ? msg.payload as Record<string, unknown>
          : {}
        // Commit any buffered stream text before the assignment turn so
        // the two render in arrival order.
        flushPendingText()
        handleAssignmentEvent(msg.type as AssignmentEventType, payload)
        return
      }

      // run_begin anchors reassembly at the start of a run. Its own seq consumes
      // the slot (no renderable content), so jump the baseline to it — this also
      // rebases a fresh client that joined a channel whose counter already
      // advanced on earlier runs. Never regress the baseline.
      if (msg.type === "run_begin") {
        if (channelSessionId && channelSessionId !== sessionId) return
        // A fresh baseline supersedes any pending adopt-next resync.
        adoptNextSeqRef.current = false
        if (envSeq > lastSeqRef.current) {
          lastSeqRef.current = envSeq
          for (const k of pendingRef.current.keys()) {
            if (k <= lastSeqRef.current) pendingRef.current.delete(k)
          }
          drainPending()
        }
        return
      }

      // resume_reset: the server couldn't replay the in-flight run (buffer
      // overflow). Drop the stale cursor, resync to the live tail via adopt-next,
      // and reload history so we don't render a partial/garbled stream.
      if (msg.type === "resume_reset") {
        if (channelSessionId && channelSessionId !== sessionId) return
        replySoundSinceRef.current = Date.now()
        replyHasTextRef.current = false
        if (canonicalActiveRef.current) {
          // No transport watermark is supplied: retain only the proven cursor.
          // A history checkpoint must cover the known per-run gap before the
          // UI can stop describing this turn as partial.
          for (const cursor of canonicalRef.current.values()) {
            if (cursor.state === undefined || !terminalTurn(cursor.state)) cursor.resetThrough = Math.max(cursor.through + 1, ...cursor.pending.keys())
          }
          setTurns(previous => previous.map(turn => turn.runId && canonicalRef.current.get(turn.runId)?.resetThrough !== undefined ? { ...turn, streamIncomplete: true } : turn))
        } else {
          lastSeqRef.current = 0
          pendingRef.current.clear()
          adoptNextSeqRef.current = true
        }
        historyLoadedRef.current = false
        onStreamResetRef.current?.()
        return
      }

      // The server uses a top-level error frame for failures that happen
      // before a chat run exists: denied subscriptions/sends, malformed
      // payloads, and an unavailable chat handler. These are not chat_event
      // payloads, but they still terminate an optimistic local send. Ignoring
      // one leaves the user turn showing "thinking" forever.
      //
      // Which of them the USER sees is a narrower question, and the answer is:
      // only the ones that terminate something this chat has in flight.
      //
      //  - No channel → the frame belongs to no conversation. The server sends
      //    those for connection-level faults (an unparseable frame, an unrouted
      //    verb) that say nothing about this chat, and every chat open on this
      //    socket would otherwise adopt them. Our own sends name their channel
      //    (see sendMessage), so a refused send still comes back addressed.
      //  - Another session's channel → not ours.
      //  - Our channel, nothing in flight → an UNSOLICITED deny. The one that
      //    actually happens is a draft session's subscribe: an unsent
      //    conversation has no `chats` row (PRD step 3), isSessionOwner refuses
      //    the channel until it exists, and ChatPanel re-subscribes the moment
      //    ensureSession() creates it. Expected, self-correcting, and no reason
      //    to put "access denied" — as an assistant turn under a Regenerate
      //    button — into a chat the user has not typed into. A deny that is NOT
      //    self-correcting (access genuinely revoked) shows up on the next send,
      //    which is the point at which it stops the user and is surfaced.
      if (msg.type === "error") {
        if (!channelSessionId || channelSessionId !== sessionId) return
        // An uncorrelated error cannot prove this submission was refused.
        // Its correlated receipt, disconnect, or timeout will settle it.
        if (pendingAsksRef.current.size) return
        if (!isStreamingRef.current) return
        const payload = msg.payload
        let reason = ""
        if (typeof payload === "string") {
          reason = payload
        } else if (payload && typeof payload === "object") {
          const details = payload as Record<string, unknown>
          if (typeof details.message === "string") reason = details.message
          else if (typeof details.error === "string") reason = details.error
        }
        flushPendingText()
        handleErrorEvent(reason)
        sendRefusedRef.current?.(/already being processed/i.test(reason) ? "busy" : "refused")
        return
      }

      if (msg.type !== "chat_event") return
      const data = typeof msg.payload === "object" && msg.payload !== null ? msg.payload as Record<string, unknown> : {}
      if ("event" in data) {
        if (channelSessionId !== sessionId) return
        const event = parseChatEvent(data.event)
        if (event) {
          // Session receipt bookkeeping is independent of GoEvent.seq. Never
          // acknowledge a refused frame or skip a missing transport sequence.
          const validSeq = Number.isSafeInteger(envSeq) && envSeq > 0
          if (validSeq && envSeq > lastSeqRef.current && !pendingRef.current.has(envSeq) && pendingRef.current.size >= MAX_PENDING_EVENTS) {
            requestCanonicalHistory(event.run_id)
            return
          }
          if (ingestCanonicalEvent(event) && validSeq && envSeq > lastSeqRef.current) {
            pendingRef.current.set(envSeq, { content: "", canonical: true })
            drainPending()
          }
        }
        return // Never fall back to the legacy/raw body for an invalid v1 frame.
      }
      // Drop deltas arriving after a local cancel so the cancelled stream
      // can't resurrect itself. The server's cancel ack races against
      // in-flight packets — without this gate, those late deltas re-open
      // the just-closed assistant turn.
      if (cancelledRef.current) return

      const payload = (typeof msg.payload === "object" && msg.payload !== null)
        ? msg.payload as Record<string, unknown>
        : {}

      const eventType = (payload.type as StreamEventType) ?? undefined
      const content = (payload.content as string) ?? ""
      const metadata = (payload.metadata as Record<string, unknown>) ?? undefined

      // Filter by session
      if (channelSessionId && channelSessionId !== sessionId) return

      // Reassemble in seq order (dedup + reorder) so replay-after-reconnect and
      // the live stream never lose or double an event.
      ingestChatEvent(envSeq, eventType, content, metadata)
    },
    [
      sessionId,
      isCurrentAuthority,
      settleAsk,
      handleAssignmentEvent,
      flushPendingText,
      ingestChatEvent,
      ingestCanonicalEvent,
      requestCanonicalHistory,
      drainPending,
      handleErrorEvent,
    ],
  )

  // subscribeAndResume (re)subscribes to the session channel and asks the server
  // to replay any in-flight run from the last seq we applied. Stable (reads refs)
  // so it can run before `send` exists.
  const subscribeAndResume = useCallback(() => {
    const sid = sessionIdRef.current
    const s = sendRef.current
    if (!sid || !s) return
    s({ type: "subscribe", channel: "session:" + sid })
    s({ type: "resume", payload: JSON.stringify({ session_id: sid, last_seq: lastSeqRef.current }) })
  }, [])

  // handleConnect fires on every socket (re)open. On a RECONNECT (not the first
  // open) a run may have finished or advanced while we were disconnected, so we
  // reset reassembly and reload history — that picks up the persisted reply
  // (clearing a stale spinner) or lets resume replay a still-active run fresh.
  const handleConnect = useCallback(() => {
    replySoundSinceRef.current = Date.now()
    replyHasTextRef.current = false
    if (hasConnectedRef.current) {
      if (!canonicalActiveRef.current) {
        lastSeqRef.current = 0
        pendingRef.current.clear()
        adoptNextSeqRef.current = false
      }
      historyLoadedRef.current = false
      onStreamResetRef.current?.()
    }
    hasConnectedRef.current = true
    subscribeAndResume()
  }, [subscribeAndResume])

  const { status, reason, send, retry } = useWebSocket({
    enabled: authorityReadable && realtimeEnabled && executionProfile !== "restricted" && executionProfile !== "pending",
    url: wsUrl,
    getToken,
    onMessage: handleMessage,
    onConnect: handleConnect,
  })
  sendRef.current = send
  useEffect(() => {
    if (status !== "connected") rejectPendingAsks("The connection was lost before this form was confirmed. Check the conversation before sending again.")
  }, [status, rejectPendingAsks])
  useEffect(() => {
    if (!authorityReadable || !canSend) rejectPendingAsks("This conversation is no longer available. Your form was not confirmed.")
  }, [authorityReadable, canSend, rejectPendingAsks])

  // The socket is app-scoped (URL has no session id), so switching chats does
  // NOT reopen it and handleConnect won't refire. Subscribe + resume whenever
  // the active session changes while connected. Cleanup unsubscribes the old
  // channel so we stop receiving its fan-out.
  useEffect(() => {
    if (status !== "connected") return
    const channel = "session:" + sessionId
    subscribeAndResume()
    return () => { sendRef.current?.({ type: "unsubscribe", channel }) }
  }, [sessionId, status, subscribeAndResume])

  // …but that subscribe is DENIED for a draft session.
  //
  // A conversation the user has opened and not yet sent into has no `chats`
  // row (the row appears on first send — PRD Step 3), and the channel
  // authorizer resolves ownership by looking the chat up: isSessionOwner in
  // internal/ws/channel_auth.go refuses a channel whose session does not
  // exist. The server DOES answer that refusal — an "access denied" error
  // frame on this very channel (internal/ws/client.go's subscribe) — but
  // handleMessage deliberately does not surface an unsolicited deny, so
  // nothing about it is visible in an empty chat. The first reply still lands,
  // which is what hid this — a run started by this socket is written straight
  // back to the sending client (internal/ws/client.go:470-474) and never
  // touches the channel fan-out. Everything from another origin in the same
  // session (a teammate in a group chat, a CLI run, a webhook) is lost until
  // the socket reconnects or the user leaves the session and comes back.
  //
  // So the caller tells us when the row came into existence — ChatPanel calls
  // this right after its ensureSession() POST succeeds — and we take the
  // channel again. Once per session: `resume` asks the server to replay an
  // in-flight run from our last seq, and firing it twice would replay it
  // twice. The ref holds the session id it already re-subscribed for, so a
  // later session gets its own single retry without needing a reset.
  const resubscribedForRef = useRef<string | null>(null)
  const resubscribeSession = useCallback(() => {
    const sid = sessionIdRef.current
    if (!sid || resubscribedForRef.current === sid) return
    resubscribedForRef.current = sid
    subscribeAndResume()
  }, [subscribeAndResume])

  /** Send a message.
   *
   *  `metadata` rides WITH the message, never inside it: `content` is
   *  byte-identical to what a plain send of the same text produces, and the
   *  payload omits the key entirely when there is nothing to carry, so a plain
   *  composer message, a suggestion chip and `?prompt=` all serialize to the
   *  exact bytes they did before any of this existed. Today the only occupant
   *  is the ask-form submission envelope, forwarded by useMessageSubmit.
   *
   *  It is also stamped on the optimistic turn, so the "via <form>" badge is
   *  on screen the moment the message is — the reload path recovers the same
   *  thing from the persisted message (messagesToTurns above). */
  const sendMessage = useCallback(
    (content: string, metadata?: Record<string, unknown>) => {
      if (!canSend || !isCurrentAuthority()) return false
      const profile = getExecutionProfile?.() ?? executionProfile
      if (!content.trim() || isStreaming || profile === "pending") return false
      if (profile === "restricted") {
        if (metadata && ASK_SUBMISSION_METADATA_KEY in metadata) {
          return Promise.reject(new AskSubmissionError("Ask forms are not supported in restricted chat. Your answers have not been sent."))
        }
        const projectFileVersions = projectInputVersionIds(metadata)
        if (!workspaceId || projectFileVersions === null) {toast.error("Restricted chat supports text and explicitly selected project files only"); return false}
        const controller = new AbortController()
        restrictedAbortRef.current = controller
        setTurns(prev => [...prev, {id:uuid(),role:"user",parts:[{id:uuid(),type:"text",content:content.trim(),timestamp:new Date()}],isStreaming:false,timestamp:new Date()}])
        setIsStreaming(true)
        isStreamingRef.current = true
        textBufferRef.current = ""
        cancelledRef.current = false
        stopPendingRef.current = false
        setStopPending(false)
        replyHasTextRef.current = false
        void (async () => {
          let done = false
          try {
            const response = await apiFetch(`/api/v1/chats/${requireApiPathSegment(sessionId)}/restricted-run?workspace_id=${encodeURIComponent(workspaceId)}`, {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({content:content.trim(), ...(projectFileVersions.length ? {project_file_versions:projectFileVersions} : {})}),signal:controller.signal})
            if (controller.signal.aborted || !isCurrentAuthority() || sessionIdRef.current !== sessionId) return
            if (!response.ok || !response.body) throw new Error("Restricted text run denied or unavailable")
            const reader = response.body.getReader()
            const decoder = new TextDecoder()
            let pending = ""
            while (true) {
              const next = await reader.read()
              if (controller.signal.aborted || !isCurrentAuthority() || sessionIdRef.current !== sessionId) return
              if (next.done) break
              pending += decoder.decode(next.value,{stream:true})
              if (pending.length > 256*1024) throw new Error("Restricted output exceeds limit")
              let boundary: number
              while ((boundary = pending.indexOf("\n\n")) >= 0) {
                if (controller.signal.aborted || cancelledRef.current) throw new Error("Restricted response cancelled")
                const frame = pending.slice(0,boundary); pending = pending.slice(boundary+2)
                if (!frame.startsWith("data: ")) throw new Error("Invalid restricted output")
                const event = JSON.parse(frame.slice(6)) as {type:string;text:string}
                if (event.type === "text" && !done && typeof event.text === "string") applyChatEvent("text",event.text)
                else if (event.type === "done" && !done) {done = true;applyChatEvent("done","")}
                else throw new Error("Invalid restricted output")
              }
            }
            if (!done) throw new Error("Restricted response incomplete or revoked")
          } catch (error) {
            if (!controller.signal.aborted && isCurrentAuthority() && sessionIdRef.current === sessionId) applyChatEvent("error",error instanceof Error ? error.message : "Restricted text run failed")
          } finally {
            controller.abort()
            if (restrictedAbortRef.current === controller) restrictedAbortRef.current = null
          }
        })()
        return true
      }

      const ask = metadata?.[ASK_SUBMISSION_METADATA_KEY]
      const submissionId = ask && typeof ask === "object" && "submission_id" in ask && typeof ask.submission_id === "string" ? ask.submission_id : undefined
      if (pendingAsksRef.current.size) return false
      const optimisticId = uuid()
      let confirmation: Promise<boolean> | undefined
      if (submissionId) {
        confirmation = new Promise<boolean>((resolve, reject) => {
          const timer = setTimeout(() => settleAsk(submissionId, new AskSubmissionError("Delivery could not be confirmed. Check the conversation before sending again.")), 30000)
          pendingAsksRef.current.set(submissionId, { turnId: optimisticId, resolve, reject, timer })
        })
      }
      const sent = send({
        type: "send_message",
        // Name the channel this send is about. The server echoes it on any
        // frame-level refusal, so errors can be attributed to this chat.
        channel: "session:" + sessionId,
        payload: JSON.stringify({
          session_id: sessionId,
          content: content.trim(),
          ...(metadata ? { metadata } : {}),
        }),
      })
      if (sent === false) {
        if (submissionId) settleAsk(submissionId, new AskSubmissionError("Not connected — your answers are still here."))
        return confirmation ?? false
      }

      const userTurn: ChatTurn = {
        id: optimisticId,
        role: "user",
        parts: [{ id: uuid(), type: "text", content: content.trim(), timestamp: new Date() }],
        isStreaming: false,
        timestamp: new Date(),
        metadata,
      }

      setTurns((prev) => [...prev, userTurn])
      setIsStreaming(true)
      textBufferRef.current = ""
      thinkingBufferRef.current = ""
      cancelledRef.current = false
      stopPendingRef.current = false
      setStopPending(false)
    replyHasTextRef.current = false

      return confirmation ?? true
    },
    [canSend, sessionId, send, isStreaming, executionProfile, getExecutionProfile, workspaceId, applyChatEvent, isCurrentAuthority, settleAsk],
  )

  const stopGeneration = useCallback(() => {
    if (!isCurrentAuthority()) return
    // One cancel per stop on the canonical stream: a press while one is pending sends nothing.
    if (canonicalActiveRef.current && stopPendingRef.current) return
    restrictedAbortRef.current?.abort()
    restrictedAbortRef.current = null
    send({
      type: "cancel_message",
      payload: JSON.stringify({ session_id: sessionId }),
    })
    if (canonicalActiveRef.current) {
      stopPendingRef.current = true
      setStopPending(true)
      setTurns(previous => previous.map(turn => turn.isStreaming ? { ...turn, cancelRequested: true } : turn))
      return // A Stop request is not a server terminal acknowledgement.
    }
    // Flip local streaming state immediately so the UI returns to the
    // input-ready state even if the WS is dropped before the server's
    // cancel ack arrives. Closing assistant turns mark them no longer
    // streaming so the typing indicator stops. The cancelled flag blocks
    // any deltas already in flight from re-opening the cancelled turn
    // or appending parts to it; cleared by the next sendMessage.
    setIsStreaming(false)
    cancelledRef.current = true
    // Drop any buffered-but-uncommitted tokens and cancel their frame so
    // a late flush can't re-open the turn we're closing here.
    pendingTextRef.current = ""
    if (rafIdRef.current !== null) {
      cancelAnimationFrame(rafIdRef.current)
      rafIdRef.current = null
    }
    setTurns((prev) =>
      prev.map((t) =>
        t.role === "assistant" && t.isStreaming
          ? {
              ...t,
              isStreaming: false,
              // Drop transient status rows (mirrors handleDoneEvent) — a
              // stopped turn must not keep a stale animated progress line.
              parts: stripStatusParts(t.parts),
            }
          : t,
      )
      // A turn that held only status parts is now empty — drop it entirely
      // (same as handleDoneEvent's orphaned status-only turn cleanup).
      .filter((t) => !(t.role === "assistant" && t.parts.length === 0)),
    )
  }, [send, sessionId, isCurrentAuthority])

  // Regenerate the last assistant response by re-sending the last user message.
  const regenerateLastTurn = useCallback(() => {
    if (!isCurrentAuthority()) return
    if (!canSend || isStreaming) return
    // Find the last user turn
    const lastUserIdx = turns.map((t) => t.role).lastIndexOf("user")
    if (lastUserIdx === -1) return
    const lastUserContent = turns[lastUserIdx].parts.find((p) => p.type === "text")?.content
    if (!lastUserContent) return
    if ((getExecutionProfile?.() ?? executionProfile) === "restricted") {sendMessage(lastUserContent); return}

    // An ask submission id belongs to the original send. A Page slug can be
    // retried: the server checks access again and captures a fresh snapshot.
    const page = turns[lastUserIdx].metadata?.page_context
    const pageSlug = page && typeof page === "object" && "slug" in page ? page.slug : undefined
    const retryMetadata = typeof pageSlug === "string" && pageSlug ? { page_context: { slug: pageSlug } } : undefined
    // History contains the server-appended Page snapshot in the message text.
    // Remove only a matching, server-attributed suffix before asking the server
    // to attach a fresh snapshot; otherwise each retry would duplicate it.
    const pageMetadata = page && typeof page === "object" ? page as Record<string, unknown> : undefined
    const suffixStart = lastUserContent.lastIndexOf("\n\n[Page context — untrusted reference; snapshot ")
    const suffix = suffixStart < 0 ? "" : lastUserContent.slice(suffixStart)
    const pageSuffix = /^\n\n\[Page context — untrusted reference; snapshot ([^\]\n]+)\]\nPage: [^\n]*\nSlug: ([^\n]*)\nPage ID: ([^\n]*)\n\[\/Page context\]$/.exec(suffix)
    const retryContent = pageSuffix && pageMetadata
      && pageSuffix[1] === pageMetadata.snapshot_at
      && pageSuffix[2] === pageMetadata.slug
      && pageSuffix[3] === pageMetadata.page_id
      ? lastUserContent.slice(0, suffixStart)
      : lastUserContent

    // Same pre-send guard the composer runs (checkChatMessageSize) — checked
    // BEFORE any turn truncation or isStreaming flip. Without this, resending
    // an oversize turn truncates the transcript locally, then the server
    // kills the whole socket on the oversize frame: message gone, transcript
    // tail already removed, panel stuck streaming with no error.
    const sizeCheck = checkChatMessageSize(sessionId, retryContent, retryMetadata)
    if (!sizeCheck.ok) {
      toast.error(sizeCheck.message)
      return
    }

    const sent = send({
      type: "send_message",
      channel: "session:" + sessionId,
      payload: JSON.stringify({
        session_id: sessionId,
        content: retryContent,
        ...(retryMetadata ? { metadata: retryMetadata } : {}),
      }),
    })
    if (sent === false) {
      toast.error("Not connected — try again when chat reconnects.")
      return
    }

    // Remove all turns after the last user turn only after the socket accepts
    // the retry. The new Page snapshot will be supplied by the server.
    setTurns((prev) => prev.slice(0, lastUserIdx).concat({
      ...prev[lastUserIdx],
      parts: prev[lastUserIdx].parts.map((part) => part.type === "text" ? { ...part, content: retryContent } : part),
      metadata: retryMetadata,
    }))
    setIsStreaming(true)
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    cancelledRef.current = false
    stopPendingRef.current = false
    setStopPending(false)
    replyHasTextRef.current = false

  }, [canSend, turns, sessionId, send, isStreaming, executionProfile, getExecutionProfile, sendMessage, isCurrentAuthority])

  // Edit a user message and resend — removes all subsequent turns.
  const editAndResend = useCallback(
    (turnId: string, newContent: string) => {
      if (!canSend || !isCurrentAuthority() || isStreaming || !newContent.trim()) return
      const turnIdx = turns.findIndex((t) => t.id === turnId)
      if (turnIdx === -1 || turns[turnIdx].role !== "user") return

      const trimmed = newContent.trim()
      if ((getExecutionProfile?.() ?? executionProfile) === "restricted") {
        toast.error("Restricted history is immutable; send your revision as a new message")
        return
      }

      // Same pre-send guard the composer runs (checkChatMessageSize) — checked
      // BEFORE any turn mutation or isStreaming flip. Without this, an oversize
      // paste into an edit replaces the turn and truncates everything after it
      // locally, then the server kills the whole socket on the oversize frame:
      // message gone, transcript tail already removed, panel stuck streaming
      // with no error. Leave the transcript and edit draft intact instead.
      const sizeCheck = checkChatMessageSize(sessionId, trimmed)
      if (!sizeCheck.ok) {
        toast.error(sizeCheck.message)
        return
      }

      // Replace the user turn content and remove everything after.
      //
      // The metadata goes with it. An ask envelope claims "the user answered
      // this form this way, and this is the text it rendered" — once the text
      // has been edited by hand that is no longer true, so the badge must not
      // survive the edit. Nothing is sent in its place either (the payload
      // below carries no metadata), so the local turn and the persisted one
      // agree: this is a typed message now.
      const { metadata: _replacedEnvelope, ...priorTurn } = turns[turnIdx]
      const editedTurn: ChatTurn = {
        ...priorTurn,
        parts: [{ id: uuid(), type: "text", content: trimmed, timestamp: new Date() }],
      }
      setTurns(turns.slice(0, turnIdx).concat(editedTurn))
      setIsStreaming(true)
      textBufferRef.current = ""
      thinkingBufferRef.current = ""
      cancelledRef.current = false
      stopPendingRef.current = false
      setStopPending(false)
    replyHasTextRef.current = false

      send({
        type: "send_message",
        channel: "session:" + sessionId,
        payload: JSON.stringify({
          session_id: sessionId,
          content: trimmed,
        }),
      })
    },
    [canSend, turns, sessionId, send, isStreaming, executionProfile, getExecutionProfile, isCurrentAuthority],
  )

  const loadHistory = useCallback((history: ChatMessage[], options: { reset?: boolean } = {}) => {
    if (!isCurrentAuthority() || sessionIdRef.current !== sessionId) return
    if (options.reset) { clearStreamState(); setTurns([]) }
    const loaded = messagesToTurns(history).map(turn => {
      const controls = turn.runId ? pendingControlsRef.current.get(turn.runId) : undefined
      if (!turn.runId || controls?.turnId !== turn.id || turn.role !== "assistant") return turn
      pendingControlsRef.current.delete(turn.runId)
      return { ...turn, parts: foldControls(turn.parts, controls, turn.runId, turn.timestamp) }
    })
    const stale = new Set<string>()
    for (const message of history) {
      const parsed = checkpointSchema.safeParse(message.checkpoint)
      if (!parsed.success) continue
      const cp = parsed.data
      canonicalActiveRef.current = true
      let cursor = canonicalRef.current.get(cp.run_id)
      if (!cursor) {
        cursor = { turnId: cp.turn_id, through: cp.through_seq, pending: new Map(), seen: new Set(), refreshes: 0 }
        canonicalRef.current.set(cp.run_id, cursor)
      }
      if (cursor.turnId !== cp.turn_id) continue
      if (cp.through_seq >= cursor.through) cursor.state = cp.turn_state
      if (cursor.through > cp.through_seq) stale.add(cp.run_id)
      cursor.through = Math.max(cursor.through, cp.through_seq)
      if (cursor.resetThrough !== undefined && cp.through_seq >= cursor.resetThrough) cursor.resetThrough = undefined
      for (const n of cursor.pending.keys()) if (n <= cursor.through) cursor.pending.delete(n)
    }
    setTurns(previous => {
      const merged = loaded.map(turn => {
        if (!turn.runId || !stale.has(turn.runId)) return turn
        return previous.find(old => old.runId === turn.runId && old.role === "assistant") ?? turn
      })
      if (!options.reset) for (const turn of previous) {
        if (!turn.runId || !canonicalRef.current.has(turn.runId) || merged.some(value => value.runId === turn.runId && value.role !== "user")) continue
        if (turn.isStreaming) merged.push(turn)
        // A stopped or failed run the server did not persist (a Stop before any
        // output writes no assistant message) exists only here; keep it after
        // the turn it answered (canonical timestamps are server time). The
        // user message carrying the same run_id does not count as the reply.
        // Completed runs are always persisted, so they are never kept.
        else if (turn.role === "assistant" && turn.turnState && turn.turnState !== "completed" && terminalTurn(turn.turnState)) {
          const at = merged.findIndex(value => value.timestamp > turn.timestamp)
          merged.splice(at < 0 ? merged.length : at, 0, turn)
        }
      }
      return merged.map(turn => {
        const cursor = turn.runId ? canonicalRef.current.get(turn.runId) : undefined
        return cursor ? { ...turn, streamIncomplete: cursor.pending.size > 0 || cursor.resetThrough !== undefined } : turn
      })
    })
    const live = loaded.some(turn => turn.isStreaming) || [...canonicalRef.current.values()].some(cursor => cursor.state !== undefined && !terminalTurn(cursor.state))
    setIsStreaming(live)
    // History can be what confirms the stop: nothing left running ends it.
    if (!live) { stopPendingRef.current = false; setStopPending(false) }
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    // History is now the base. Release any in-flight run events that arrived via
    // resume/live before the history fetch resolved, applying them ON TOP so a
    // late history load can't wipe an already-streaming reply (the mount race).
    historyLoadedRef.current = true
    for (const cursor of canonicalRef.current.values()) {
      const next = cursor.pending.get(cursor.through + 1)
      if (next) { cursor.pending.delete(next.seq); ingestCanonicalEvent(next) }
    }
    drainPending()
  }, [drainPending, ingestCanonicalEvent, clearStreamState, isCurrentAuthority, sessionId])

  // markHistoryUnavailable opens the streaming gate WITHOUT replacing turns, for
  // when the history fetch fails outright. Without this a transient history 5xx
  // would leave the gate shut forever and every subsequent streamed event would
  // buffer unseen — a frozen chat. The existing turns are left intact.
  const markHistoryUnavailable = useCallback(() => {
    if (!isCurrentAuthority() || sessionIdRef.current !== sessionId) return
    historyLoadedRef.current = true
    for (const cursor of canonicalRef.current.values()) {
      const next = cursor.pending.get(cursor.through + 1)
      if (next) { cursor.pending.delete(next.seq); ingestCanonicalEvent(next) }
    }
    drainPending()
  }, [drainPending, ingestCanonicalEvent, isCurrentAuthority, sessionId])

  /** A definitive source-read refusal clears protected state until the source is reopened. */
  const markHistoryDenied = useCallback(() => {
    if (!isCurrentAuthority() || sessionIdRef.current !== sessionId) return
    deniedConversationRef.current = authorityKey
    setDeniedConversation(authorityKey)
    clearStreamState()
    setTurns([])
  }, [isCurrentAuthority, sessionId, authorityKey, clearStreamState])

  useEffect(() => {
    const revoke = () => {
      deniedScopeRef.current = principalKey
      setDeniedScope(principalKey)
      clearStreamState()
      setTurns([])
    }
    window.addEventListener(AUTH_EVENT, revoke)
    return () => window.removeEventListener(AUTH_EVENT, revoke)
  }, [clearStreamState, principalKey])

  // Derive flat messages for backwards compat (used by history loading)
  const messages: ChatMessage[] = turns.flatMap((turn) =>
    turn.parts.map((part) => ({
      id: part.id,
      role: turn.role === "system" ? "system" as MessageRole : turn.role === "user" ? "user" as MessageRole : (part.type === "tool_call" || part.type === "tool_result" ? "tool" as MessageRole : "assistant" as MessageRole),
      content: part.content,
      eventType: part.type as StreamEventType,
      timestamp: part.timestamp,
      isStreaming: part.isStreaming,
      metadata: part.metadata,
    }))
  )

  return {
    turns,
    messages,
    sendMessage,
    stopGeneration,
    /** Stop was requested and the server has not confirmed a terminal state yet. */
    stopping: isStreaming && stopPending,
    regenerateLastTurn,
    editAndResend,
    loadHistory,
    markHistoryUnavailable,
    markHistoryDenied,
    historyDenied,
    resubscribeSession,
    isStreaming,
    connectionStatus: (executionProfile === "restricted" ? "connected" : realtimeEnabled ? status : "unavailable") as WSStatus,
    /** Why the socket is not up (WSReason), for the reconnect banner. */
    connectionReason: (executionProfile !== "restricted" && realtimeEnabled ? reason : null) as WSReason | null,
    /** "Retry now": every socket in the tab tries at once. */
    retryConnection: retry,
  }
}
