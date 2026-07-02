"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useWebSocket, type WSStatus, type WSMessage } from "@/hooks/use-websocket"

/** Upper bound on out-of-order events held during reassembly. Past this, a gap
 *  is assumed permanently lost and the stream skips ahead so it never freezes.
 *  A run is turn-capped so a healthy stream never approaches this. */
const MAX_PENDING_EVENTS = 1000

/** uuid() is unavailable in non-secure (HTTP) contexts.
 *  Fall back to a simple Math.random-based UUID when needed. */
function uuid(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16)
  })
}

// --- Turn-based model types ---

/** Discriminator for the content type of a turn part (text, tool call, thinking, etc.). */
export type TurnPartType =
  | "text"
  | "thinking"
  | "tool_call"
  | "tool_result"
  | "status"
  | "error"
  | "result"
  | "system_init"
  | "image"
  | "crew_provisioning"

/** A single content block within a chat turn (e.g. a text fragment, tool call, or thinking block). */
export interface TurnPart {
  id: string
  type: TurnPartType
  content: string
  isStreaming?: boolean
  metadata?: Record<string, unknown>
  timestamp: Date
}

/** A complete turn in the conversation (user message or multi-part assistant response). */
export interface ChatTurn {
  id: string
  role: "user" | "assistant" | "system"
  parts: TurnPart[]
  isStreaming: boolean
  timestamp: Date
  /** For user turns in a group chat: which human authored it. Lets the UI
   *  attribute a teammate's message (avatar/name) and distinguish it from the
   *  local user's own turns. Undefined for the local user / private chats. */
  authorUserId?: string
  /** Per-turn metadata. Currently only `trace_id` is consumed (by the
   *  feedback store to link signals back to the OTel trace that
   *  produced the message). Backend wiring is not yet shipped — see
   *  the open follow-up in the feedback guide — so this field is
   *  populated only when the WebSocket event carries it, and is
   *  always optional for downstream readers. */
  metadata?: { trace_id?: string }
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
export interface HistoryPart {
  type: TurnPartType | string
  content: string
  tool_name?: string
  tool_id?: string
  metadata?: Record<string, unknown>
}

/** @deprecated Legacy flat chat message; use ChatTurn/TurnPart for new code. Kept for history loading compatibility. */
export interface ChatMessage {
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
  "tool_call",
  "tool_result",
  "error",
  "result",
  "image",
])

interface UseChatOptions {
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
}

/** Map a structured history part to a renderable TurnPart, coercing unknown
 *  types to "text" and folding tool_name/tool_id into metadata so the tool
 *  cards can read them the same way they do for live events. */
function historyPartToTurnPart(part: HistoryPart, id: string, timestamp: Date): TurnPart {
  const type: TurnPartType = RENDERABLE_PART_TYPES.has(part.type) ? (part.type as TurnPartType) : "text"
  const metadata: Record<string, unknown> = { ...(part.metadata ?? {}) }
  if (part.tool_name !== undefined) metadata.tool_name = part.tool_name
  if (part.tool_id !== undefined) metadata.tool_id = part.tool_id
  return {
    id,
    type,
    content: part.content,
    metadata: Object.keys(metadata).length > 0 ? metadata : undefined,
    timestamp,
  }
}

/** Convert flat ChatMessage history into turns for display */
export function messagesToTurns(messages: ChatMessage[]): ChatTurn[] {
  const turns: ChatTurn[] = []
  for (const msg of messages) {
    if (msg.role === "user") {
      turns.push({
        id: msg.id,
        role: "user",
        parts: [{ id: msg.id, type: "text", content: msg.content, timestamp: msg.timestamp }],
        isStreaming: false,
        timestamp: msg.timestamp,
        authorUserId: msg.authorUserId,
      })
    } else if (msg.role === "system") {
      turns.push({
        id: msg.id,
        role: "system",
        parts: [{ id: msg.id, type: msg.eventType === "error" ? "error" : "text", content: msg.content, timestamp: msg.timestamp }],
        isStreaming: false,
        timestamp: msg.timestamp,
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
        isStreaming: false,
        timestamp: msg.timestamp,
      })
    } else {
      // assistant/tool messages: group consecutive ones into a single turn
      const lastTurn = turns[turns.length - 1]
      const partType: TurnPartType = (msg.eventType === "tool_call" || msg.eventType === "tool_result")
        ? msg.eventType
        : msg.eventType === "thinking" ? "thinking" : "text"

      if (lastTurn?.role === "assistant" && !lastTurn.isStreaming) {
        lastTurn.parts.push({
          id: msg.id,
          type: partType,
          content: msg.content,
          metadata: msg.metadata,
          timestamp: msg.timestamp,
        })
      } else {
        turns.push({
          id: msg.id,
          role: "assistant",
          parts: [{
            id: msg.id,
            type: partType,
            content: msg.content,
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
export function useChat({ wsUrl, getToken, sessionId, currentUserId, onStreamReset }: UseChatOptions) {
  const [turns, setTurns] = useState<ChatTurn[]>([])
  const [isStreaming, setIsStreaming] = useState(false)
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

  // --- Resumable-stream reassembly ---------------------------------------
  // Streamed chat events carry a per-session monotonic seq (see internal/ws).
  // On reconnect the client replays the gap, so the same event can arrive twice
  // and out of order (replay interleaved with live). lastSeqRef is the highest
  // contiguous seq applied; pendingRef holds out-of-order events keyed by seq
  // until their predecessors arrive. This guarantees every event is applied
  // exactly once, in order — the core "never lose (or double) text" invariant.
  const lastSeqRef = useRef(0)
  const pendingRef = useRef<Map<number, { eventType?: StreamEventType; content: string; metadata?: Record<string, unknown> }>>(new Map())
  // Live/replayed events are held until the session's history has loaded, so a
  // late-resolving history fetch can't clobber an already-rendered in-flight
  // run (the mount race). loadHistory flips this true and drains.
  const historyLoadedRef = useRef(false)
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

  // Reset stream-side state when session changes. We deliberately do NOT
  // reset turns here — that would cause a blank-canvas flash between the
  // old session and the new one's history fetch. The chat-panel calls
  // loadHistory() once the new session's messages arrive, which performs
  // an atomic replace (including the empty-array case for fresh sessions).
  useEffect(() => {
    setIsStreaming(false)
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    cancelledRef.current = false
    // Drop any text buffered for the previous session and cancel its
    // pending frame so it can't commit into the new session's turns.
    pendingTextRef.current = ""
    if (rafIdRef.current !== null) {
      cancelAnimationFrame(rafIdRef.current)
      rafIdRef.current = null
    }
    // Reset resumable-stream reassembly for the new session.
    lastSeqRef.current = 0
    pendingRef.current.clear()
    historyLoadedRef.current = false
    adoptNextSeqRef.current = false
  }, [sessionId])

  // Cancel any in-flight frame on unmount so the scheduled callback never
  // runs against a torn-down component.
  useEffect(() => {
    return () => {
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
    const traceID = metadata && typeof metadata.trace_id === "string" ? (metadata.trace_id as string) : undefined
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
        const finalTurn: ChatTurn = {
          ...last,
          parts: finalParts,
          isStreaming: false,
        }
        if (traceID) {
          finalTurn.metadata = { ...(last.metadata ?? {}), trace_id: traceID }
        }
        return [...cleaned.slice(0, -1), finalTurn]
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
      return [
        ...prev,
        {
          id: uuid(),
          role: "system",
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
      if (eventType === "text") {
        pendingTextRef.current += content
        scheduleTextFlush()
        return
      }
      flushPendingText()
      switch (eventType) {
        case "status": handleStatusEvent(content); break
        case "thinking": handleThinkingEvent(content); break
        case "tool_call": handleToolCallEvent(content, metadata); break
        case "tool_result": handleToolResultEvent(content, metadata); break
        case "image": handleImageEvent(content, metadata); break
        case "result": handleResultEvent(content, metadata); break
        case "system": handleSystemEvent(content, metadata); break
        case "done": handleDoneEvent(metadata); break
        case "crew_provisioning": handleCrewProvisioningEvent(content, metadata); break
        case "user_message": handleUserMessageEvent(content, metadata); break
        case "error": handleErrorEvent(content); break
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
      applyChatEvent(next.eventType, next.content, next.metadata)
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

  const handleMessage = useCallback(
    (msg: { type: string; payload?: string | Record<string, unknown>; channel?: string; [key: string]: unknown }) => {
      const channelSessionId = msg.channel?.startsWith("session:") ? msg.channel.slice(8) : undefined
      const envSeq = typeof msg.seq === "number" ? (msg.seq as number) : 0

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
        lastSeqRef.current = 0
        pendingRef.current.clear()
        adoptNextSeqRef.current = true
        historyLoadedRef.current = false
        onStreamResetRef.current?.()
        return
      }

      if (msg.type !== "chat_event") return
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
      handleAssignmentEvent,
      flushPendingText,
      ingestChatEvent,
      drainPending,
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
    if (hasConnectedRef.current) {
      lastSeqRef.current = 0
      pendingRef.current.clear()
      adoptNextSeqRef.current = false
      historyLoadedRef.current = false
      onStreamResetRef.current?.()
    }
    hasConnectedRef.current = true
    subscribeAndResume()
  }, [subscribeAndResume])

  const { status, send } = useWebSocket({
    url: wsUrl,
    getToken,
    onMessage: handleMessage,
    onConnect: handleConnect,
  })
  sendRef.current = send

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

  const sendMessage = useCallback(
    (content: string) => {
      if (!content.trim() || isStreaming) return

      const userTurn: ChatTurn = {
        id: uuid(),
        role: "user",
        parts: [{ id: uuid(), type: "text", content: content.trim(), timestamp: new Date() }],
        isStreaming: false,
        timestamp: new Date(),
      }

      setTurns((prev) => [...prev, userTurn])
      setIsStreaming(true)
      textBufferRef.current = ""
      thinkingBufferRef.current = ""
      cancelledRef.current = false

      send({
        type: "send_message",
        payload: JSON.stringify({
          session_id: sessionId,
          content: content.trim(),
        }),
      })
    },
    [sessionId, send, isStreaming],
  )

  const stopGeneration = useCallback(() => {
    send({
      type: "cancel_message",
      payload: JSON.stringify({ session_id: sessionId }),
    })
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
  }, [send, sessionId])

  // Regenerate the last assistant response by re-sending the last user message.
  const regenerateLastTurn = useCallback(() => {
    if (isStreaming) return
    // Find the last user turn
    const lastUserIdx = turns.map((t) => t.role).lastIndexOf("user")
    if (lastUserIdx === -1) return
    const lastUserContent = turns[lastUserIdx].parts.find((p) => p.type === "text")?.content
    if (!lastUserContent) return

    // Remove all turns after (and including) the last assistant turn
    setTurns((prev) => prev.slice(0, lastUserIdx + 1))
    setIsStreaming(true)
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    cancelledRef.current = false

    send({
      type: "send_message",
      payload: JSON.stringify({
        session_id: sessionId,
        content: lastUserContent,
      }),
    })
  }, [turns, sessionId, send, isStreaming])

  // Edit a user message and resend — removes all subsequent turns.
  const editAndResend = useCallback(
    (turnId: string, newContent: string) => {
      if (isStreaming || !newContent.trim()) return
      const turnIdx = turns.findIndex((t) => t.id === turnId)
      if (turnIdx === -1 || turns[turnIdx].role !== "user") return

      // Replace the user turn content and remove everything after
      const editedTurn: ChatTurn = {
        ...turns[turnIdx],
        parts: [{ id: uuid(), type: "text", content: newContent.trim(), timestamp: new Date() }],
      }
      setTurns(turns.slice(0, turnIdx).concat(editedTurn))
      setIsStreaming(true)
      textBufferRef.current = ""
      thinkingBufferRef.current = ""
      cancelledRef.current = false

      send({
        type: "send_message",
        payload: JSON.stringify({
          session_id: sessionId,
          content: newContent.trim(),
        }),
      })
    },
    [turns, sessionId, send, isStreaming],
  )

  const loadHistory = useCallback((history: ChatMessage[]) => {
    setTurns(messagesToTurns(history))
    setIsStreaming(false)
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    // History is now the base. Release any in-flight run events that arrived via
    // resume/live before the history fetch resolved, applying them ON TOP so a
    // late history load can't wipe an already-streaming reply (the mount race).
    historyLoadedRef.current = true
    drainPending()
  }, [drainPending])

  // markHistoryUnavailable opens the streaming gate WITHOUT replacing turns, for
  // when the history fetch fails outright. Without this a transient history 5xx
  // would leave the gate shut forever and every subsequent streamed event would
  // buffer unseen — a frozen chat. The existing turns are left intact.
  const markHistoryUnavailable = useCallback(() => {
    historyLoadedRef.current = true
    drainPending()
  }, [drainPending])

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
    regenerateLastTurn,
    editAndResend,
    loadHistory,
    markHistoryUnavailable,
    isStreaming,
    connectionStatus: status as WSStatus,
  }
}
