"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence } from "motion/react"
import {
  Bot,
  Wifi,
  WifiOff,
  Users,
} from "lucide-react"
import { toast } from "sonner"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"

import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
  ConversationEmptyState,
} from "@/components/ai-elements/conversation"
import { renderAskTemplate } from "@/lib/ask-template"
import { useChat, type HistoryPart } from "@/hooks/use-chat"
import { useSession } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useDrawerStore } from "@/stores/drawer-store"

import { Shimmer } from "@/components/ai-elements/shimmer"
import { TurnRenderer } from "./turn-renderer"
import { PinToTopSpacer } from "./pin-to-top-spacer"
import { RightPanel } from "./right-panel"
import { RightRail } from "./right-rail"
import { RightDrawer } from "./right-drawer"
import { SlashPalette } from "./composer/slash-palette"
import { SlashActionModal } from "./composer/slash-action-modal"
import type { SlashActionSchema as ServerSlashCommand } from "@/hooks/use-slash-commands"
import { type CrewMember } from "./composer/mention-autocomplete"
import { ChatComposer } from "./composer/chat-composer"
import { VirtualConversation, virtualChatEnabled } from "./virtual-conversation"
import { ArtifactPane } from "./artifact/artifact-pane"
import { FollowUps } from "./suggestions/follow-ups"
import { AskRail } from "./asks/ask-rail"
import { useAskForms } from "./asks/use-ask-forms"
import { lookupAskProvenance } from "./asks/ask-provenance"
import { askFormsFromColumn, type AskForm } from "./asks/types"
import { ConversationSearch } from "./search/conversation-search"
import { ExportDialog } from "./export/export-dialog"
import { ReconnectBanner } from "./messages/reconnect-banner"
import type { FileEntry } from "./chat-tree-row"
import { getSuggestions } from "@/lib/agent-suggestions"
import { apiFetch } from "@/lib/api-fetch"
import { resolveWsBase } from "@/lib/server-base"

function getWsUrl(): string {
  const base = resolveWsBase()
  return base === "" ? "" : `${base}/ws`
}

interface ChatPanelProps {
  agentId: string
  sessionId: string
  agentName?: string
  /** Canonical agent slug used to build URLs (`/chat/[agentSlug]`).
   *  Required because SlashPalette commands like /new-session navigate
   *  back to the agent route — passing the display name there breaks
   *  for agents whose name has spaces or non-URL-safe characters. No
   *  fallback to agentName: the display label is the source of the bug
   *  the previous review flagged. */
  agentSlug: string
  /** Agent role / role_title. Used to pick role-aware suggestion packs. */
  agentRole?: string | null
  /** The agent's own chat suggestions — the raw `agents.suggested_prompts`
   *  column, one per line. When set it replaces the role pack's chips; when
   *  null/empty (every agent nobody has configured) the role pack answers
   *  exactly as before. */
  suggestedPrompts?: string | null
  /** The agent's questionnaire forms — the raw `agents.ask_forms` column, a
   *  JSON array as TEXT, from the record the caller already has.
   *
   *  The three states are distinct and all three are used:
   *    · a string → these are the forms;
   *    · `null`   → this agent has no forms (the answer for almost every
   *      agent), and no request is made;
   *    · omitted  → the caller has no agent record, and `useAskForms` falls
   *      back to fetching the detail endpoint for itself.
   *
   *  The chat page passes it because it resolved this agent out of the roster
   *  it fetched for the tree, exactly as it does for `suggestedPrompts`. */
  askForms?: string | null
  /** How this session was created — UI / CLI / WEBHOOK / CRON / AGENT.
   *  Rendered as a chip in the connection bar so the user knows where
   *  they are at a glance. Undefined = unknown (pre-migration). */
  sessionOrigin?: string | null
  /** Pre-populate the chat input with this text on first render. */
  initialInput?: string
  /** When true, `initialInput` is auto-sent once the socket is connected
   *  (used by the describe-first routine authoring handoff: navigate into
   *  a Lead's chat and fire the authoring prompt without a manual click).
   *  Fires at most once per mount. */
  autoSendInitial?: boolean
  /** Mobile-only: which panel to show full-screen. Undefined = desktop mode. */
  mobilePanel?: "chat" | "files" | "files-only" | "more"
  /** Fired when the user sends a message — lets the parent optimistically
   *  title a freshly-created session in the sidebar (matching the server's
   *  auto-title) so the new entry shows its name without a manual refresh. */
  onSend?: (sessionId: string, text: string) => void
  /** Fired when a streamed reply settles (isStreaming true→false) for the
   *  session being viewed — lets the parent re-fire mark-read so the reply
   *  the user just watched doesn't linger as a server-side unread. */
  onReplySettled?: (sessionId: string) => void
}

const noopFileClick = () => {}

/** Cold-start rail cap: two rows at 1280px (PRD §5.1). The rest collapses
 *  into `+N`. Follow-ups keep their own cap of 3, inside FollowUps. */
const EMPTY_STATE_CHIP_LIMIT = 6

/** Chat panel with split view: conversation on the left, tabbed panel on the right. */
export function ChatPanel({ agentId, sessionId, agentName, agentSlug, agentRole, suggestedPrompts, askForms, sessionOrigin, initialInput, autoSendInitial, mobilePanel, onSend, onReplySettled }: ChatPanelProps) {
  const suggestionPack = getSuggestions(agentRole, suggestedPrompts)
  const defaultSuggestions = suggestionPack.empty
  const followUpPrompts = suggestionPack.followUps
  const { workspaceId } = useWorkspace()

  // Sessions whose `chats` row this panel has CONFIRMED exists. Confirmed
  // means one of exactly two things happened: we created the row ourselves
  // (the POST below came back ok), or the server handed us real messages for
  // it. It is never INFERRED.
  //
  // It used to be inferred, and that was the bug: the history GET treats
  // anything that is not a 404 as proof of existence, but
  // GET /chats/{id}/messages answers 200 with an empty message list for a chat
  // that does not exist at all (internal/api/proxy.go, ChatMessages — the
  // shape the CLI's history/export/recap commands read too, so it is not
  // moving). A draft session therefore looked "ready", the create POST was
  // skipped, and the first message went out against a chat with no row: no
  // conversation persisted, an auto-title PATCH into the void, and a WS
  // channel the authorizer could not authorise (internal/ws/channel_auth.go,
  // isSessionOwner → send_message denied).
  //
  // A ref, not state: nothing renders from it, and the create path must read
  // the value as it is at click time rather than as it was when the callback
  // was memoised. Keyed by session id so switching back and forth inside one
  // mount doesn't re-ask.
  const confirmedRowsRef = useRef<Set<string>>(new Set())
  // In-flight create per session id, so two sends in the same tick (a
  // suggestion chip plus a fast Enter) share one POST — and one re-subscribe.
  const createInFlightRef = useRef<Map<string, Promise<boolean>>>(new Map())

  // Cutoff: turns whose timestamp is BEFORE this number skip the arrival
  // animation. Bumped on every session swap so loaded-from-history turns
  // appear instantly (no slide-up flash) while genuinely-new turns sent
  // or streamed AFTER the swap still animate.
  const [animateAfter, setAnimateAfter] = useState(() => Date.now())
  const [historyLoading, setHistoryLoading] = useState(true)
  const sessionLoadedFor = useRef<string | null>(null)

  useEffect(() => {
    setHistoryLoading(true)
    setAnimateAfter(Date.now() + 250)
    sessionLoadedFor.current = sessionId
  }, [sessionId])

  const [files, setFiles] = useState<FileEntry[]>([])
  // Narrow selectors — the panel only reads these three fields; a
  // whole-store subscription re-rendered the entire chat (message list
  // included) on every drawer width drag or unrelated store write.
  const drawerOpen = useDrawerStore((s) => s.open)
  const drawerActiveTab = useDrawerStore((s) => s.activeTab)
  const drawerMode = useDrawerStore((s) => s.mode)

  // Per-(re)connect ticket fetch. apiFetch promotes the 401 path —
  // either via silent refresh or the global session-expired event —
  // so this hook no longer needs its own authError state.
  //
  // Two distinct failure modes here, deliberately treated differently:
  //   - 401/403: real auth death. Return null; useWebSocket terminates.
  //   - 5xx / network throw / malformed JSON: transient. Throw; the
  //     WS hook's catch path treats it as a transport error and
  //     schedules the next backoff retry instead of evicting the user.
  // Conflating these two used to bounce users to /login on any
  // ws-token 5xx during a backend hiccup.
  const getWsToken = useCallback(async (): Promise<string | null> => {
    const res = await apiFetch("/api/v1/ws-token")
    if (res.status === 401 || res.status === 403) return null
    if (!res.ok) throw new Error(`ws-token fetch failed: ${res.status}`)
    const data = await res.json() // throws on malformed JSON — also transient
    if (typeof data?.token !== "string") {
      throw new Error("ws-token response missing token field")
    }
    return data.token
  }, [])

  const session = useSession()
  const currentUserId = session.data?.user?.id ?? null

  // Bumped to force a history refetch when the server can't replay an in-flight
  // run (resume_reset — the replay buffer overflowed). Rare; a safety net.
  const [historyReloadNonce, setHistoryReloadNonce] = useState(0)
  const requestHistoryReload = useCallback(() => setHistoryReloadNonce((n) => n + 1), [])

  const { turns, sendMessage, stopGeneration, regenerateLastTurn, editAndResend, loadHistory, markHistoryUnavailable, resubscribeSession, isStreaming, connectionStatus } = useChat({
    wsUrl: getWsUrl(),
    getToken: getWsToken,
    sessionId,
    currentUserId: currentUserId ?? undefined,
    onStreamReset: requestHistoryReload,
  })

  // Reply-settled hook: when a stream the user watched in THIS session
  // finishes (isStreaming true→false), tell the parent so it can re-fire
  // mark-read — the server counted the just-persisted reply as unread,
  // and without this the session grows a phantom badge the moment the
  // user switches away. The ref pins the session the stream belonged to,
  // so swapping sessions mid-stream never fires for the wrong one.
  const streamingSessionRef = useRef<string | null>(null)
  useEffect(() => {
    if (isStreaming) {
      streamingSessionRef.current = sessionId
    } else if (streamingSessionRef.current === sessionId) {
      streamingSessionRef.current = null
      onReplySettled?.(sessionId)
    }
  }, [isStreaming, sessionId, onReplySettled])

  useEffect(() => {
    // workspaceId is REQUIRED by GET /chats/{id}/messages — without it the
    // endpoint 400s ("workspace_id is required") and history silently stays
    // empty. useWorkspace() resolves asynchronously, so wait for it (the effect
    // re-runs when workspaceId arrives) rather than firing a doomed request.
    if (!sessionId || !workspaceId) return
    let cancelled = false

    type HistoryMessage = {
      id: string
      role: string
      content: string
      parts?: HistoryPart[]
      ts: string
    }

    // Fetch history with a couple of retries on transient failures. The old
    // code called loadHistory([]) on ANY error, which blanked a conversation
    // that actually had messages whenever a single fetch hiccupped. We now
    // retry with backoff and, if it still fails, LEAVE the existing turns in
    // place rather than wiping them — a network blip must never look like an
    // empty chat. A genuine 404 (brand-new session) is not an error.
    //
    // A 404 and a 200 carrying an empty list are the SAME answer here — "no
    // history" — and this panel deliberately draws no other conclusion from
    // either. The server returns the second for a chat that does not exist
    // (proxy.go), so "not a 404" says nothing about whether the row is there;
    // reading it as existence is what skipped the create and lost the
    // conversation. Only messages that actually came back are proof, and that
    // is recorded below.
    const fetchOnce = async (): Promise<{ messages: HistoryMessage[] } | "retry"> => {
      try {
        const r = await apiFetch(`/api/v1/chats/${sessionId}/messages?workspace_id=${encodeURIComponent(workspaceId)}`)
        if (r.status === 404) return { messages: [] }
        if (!r.ok) return "retry"
        const data = await r.json()
        return { messages: (data?.messages ?? []) as HistoryMessage[] }
      } catch {
        return "retry"
      }
    }

    const run = async () => {
      let result: { messages: HistoryMessage[] } | "retry" = "retry"
      for (let attempt = 0; attempt < 3 && !cancelled; attempt++) {
        if (attempt > 0) await new Promise((res) => setTimeout(res, 300 * attempt))
        result = await fetchOnce()
        if (result !== "retry") break
      }
      if (cancelled) return

      if (result === "retry") {
        // All attempts failed — do NOT wipe existing turns; only stop the
        // loading spinner. Still mark history "settled" so the streaming gate
        // opens: otherwise a transient history 5xx would freeze the live stream
        // (every seq'd event would buffer unseen behind the closed gate).
        markHistoryUnavailable()
        setHistoryLoading(false)
        return
      }

      const { messages } = result
      // Messages exist ⇒ the chat they belong to exists. This is the one
      // reading of a history response that is safe, and it is what lets a
      // conversation the user is coming back to skip the create POST.
      if (messages.length > 0) confirmedRowsRef.current.add(sessionId)
      // Replace atomically — including with [] for an empty (newly created)
      // session — so visible turns swap cleanly between sessions.
      loadHistory(messages.map((m) => ({
        id: m.id,
        role: m.role as "user" | "assistant" | "system" | "tool",
        content: m.content,
        parts: m.parts,
        timestamp: new Date(m.ts),
      })))
      setHistoryLoading(false)
    }

    void run()
    return () => { cancelled = true }
  }, [sessionId, workspaceId, loadHistory, markHistoryUnavailable, historyReloadNonce])

  // Group-chat participants → display-name map for author attribution. Empty
  // for a private 1:1 chat (the endpoint returns no participants), so the
  // resolver yields null and messages render exactly as before.
  const [participantNames, setParticipantNames] = useState<Record<string, string>>({})
  useEffect(() => {
    if (!sessionId || !workspaceId) return
    let cancelled = false
    apiFetch(`/api/v1/chats/${sessionId}/participants?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : { participants: [] }))
      .then((data: { participants?: { user_id: string; email?: string; full_name?: string }[] }) => {
        if (cancelled) return
        const map: Record<string, string> = {}
        for (const p of data?.participants ?? []) {
          map[p.user_id] = p.full_name || p.email || "Teammate"
        }
        setParticipantNames(map)
      })
      .catch(() => { /* private chat / transient — no attribution, no error */ })
    return () => { cancelled = true }
  }, [sessionId, workspaceId])

  const resolveAuthorName = useCallback(
    (userId: string): string | null => {
      if (!userId || userId === currentUserId) return null
      return participantNames[userId] ?? "Teammate"
    },
    [currentUserId, participantNames],
  )

  const isGroupChat = Object.keys(participantNames).length > 1

  // @mention autocomplete members. The chat's agent is always offered
  // (mentioning it is what makes it respond in a group chat); teammates are
  // offered too as a courtesy. The picker itself lives in ChatComposer.
  const mentionMembers = useMemo<CrewMember[]>(() => {
    const list: CrewMember[] = [{ id: agentId, slug: agentSlug, name: agentName ?? agentSlug, role_title: agentRole ?? undefined }]
    for (const [uid, name] of Object.entries(participantNames)) {
      if (uid !== currentUserId) {
        list.push({ id: uid, slug: name.replace(/\s+/g, "").toLowerCase(), name })
      }
    }
    return list
  }, [agentId, agentSlug, agentName, agentRole, participantNames, currentUserId])

  /**
   * Make sure this session's `chats` row exists before anything is sent into
   * it (PRD Step 3: arriving is not sending, so the row is created by the
   * first message).
   *
   * Returns whether the row is there. **Callers must not send when it is
   * false** — the WS channel authorizer resolves a session by looking the chat
   * up (internal/ws/channel_auth.go, isSessionOwner), so a `send_message` for
   * a row that does not exist is refused server-side and the reply, the
   * persisted turn and the auto-title all quietly fail to happen.
   *
   * The rule is "confirm, don't infer": on the first send for a session we
   * POST unless we have already confirmed the row. The POST is an upsert
   * (`INSERT OR IGNORE`, internal/api/agent_chats.go CreateChat), so the
   * redundant one — for a session created by /chats up front, or one whose
   * history came back legitimately empty — costs a single round trip that
   * writes nothing, once per session, on a surface that is about to open a
   * WebSocket anyway. Probing first to avoid it would cost the same round trip
   * and could not answer the question (see `confirmedRowsRef`).
   *
   * A failure is not latched: nothing is marked confirmed, so the next send
   * tries again.
   */
  const ensureSession = useCallback(async (): Promise<boolean> => {
    if (!workspaceId || !sessionId) return false
    if (confirmedRowsRef.current.has(sessionId)) return true

    // Two sends racing into the same fresh session share one POST — and
    // therefore one resubscribeSession.
    const pending = createInFlightRef.current.get(sessionId)
    if (pending) return pending

    const sid = sessionId
    const attempt = (async (): Promise<boolean> => {
      try {
        const res = await apiFetch(
          `/api/v1/agents/${agentId}/chats?workspace_id=${encodeURIComponent(workspaceId)}`,
          { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session_id: sid, origin: "UI" }) },
        )
        if (!res.ok) return false
        confirmedRowsRef.current.add(sid)
        // The mount-time `subscribe` for this session was refused — the
        // channel authorizer needs the row, and until this POST there was no
        // row (see the comment on resubscribeSession in hooks/use-chat.ts).
        // Now there is, so take the channel before the message goes out.
        // resubscribeSession is per-session idempotent and reuses the socket
        // this panel already has; it never opens a second one.
        resubscribeSession()
        return true
      } catch {
        return false
      } finally {
        createInFlightRef.current.delete(sid)
      }
    })()
    createInFlightRef.current.set(sid, attempt)
    return attempt
  }, [agentId, workspaceId, sessionId, resubscribeSession])

  /** ensureSession, plus the one thing the user has to be told about.
   *
   *  A create that fails means the message cannot be sent, and a message that
   *  cannot be sent must not look sent — the composer keeps the draft (onSent
   *  never runs), the sidebar gets no phantom row (onSend never runs), and
   *  this says why. It is deliberately the ONLY toast on this path: it fires
   *  when a send is actually refused, not on a background probe or a retry, so
   *  it cannot become the noise nobody reads. */
  const ensureSessionForSend = useCallback(async (): Promise<boolean> => {
    const ok = await ensureSession()
    if (!ok) {
      toast.error("Couldn't start this conversation — your message wasn't sent. Check your connection and try again.")
    }
    return ok
  }, [ensureSession])

  // Fetch files only when the Files tab might be visible (drawer open + active)
  const filesVisible = drawerOpen && drawerActiveTab === "files"
  useEffect(() => {
    if (!workspaceId || !filesVisible || !sessionId) return
    apiFetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data: FileEntry[] | null) => setFiles(data ?? []))
      .catch(() => {})
  }, [agentId, workspaceId, filesVisible, sessionId])

  // Bumped on every locally-sent message; arms the pin-to-top spacer so the
  // just-sent question anchors at the viewport top while the reply streams
  // in below it (the pin-to-top scroll pattern).
  const [pinNonce, setPinNonce] = useState(0)
  const lastUserTurnId = useMemo(() => {
    for (let i = turns.length - 1; i >= 0; i--) {
      // Skip teammate messages (authorUserId set): in a group chat an
      // incoming user_message must not retarget an active pin mid-stream.
      if (turns[i].role === "user" && !turns[i].authorUserId) return turns[i].id
    }
    return null
  }, [turns])

  // Fires from ChatComposer when a message actually went out (the size
  // guard passed) — re-arms the pin-to-top spacer. Input/draft clearing
  // lives inside the composer, next to the state it clears.
  const handleSent = useCallback(() => {
    setPinNonce((n) => n + 1)
  }, [])

  // Auto-send the initial prompt once, after the socket is connected.
  // The WS `send` silently drops while not OPEN, so we gate on
  // connectionStatus rather than firing on mount. Guarded by a ref so a
  // re-render (or a transient reconnect) can't double-send. When
  // auto-sending, the composer is NOT prefilled (the prefill used to be
  // cleared right after the send anyway — see composerInitialInput below).
  const autoSentRef = useRef(false)
  useEffect(() => {
    if (!autoSendInitial || autoSentRef.current) return
    const text = (initialInput ?? "").trim()
    if (!text) return
    if (connectionStatus !== "connected" || isStreaming) return
    autoSentRef.current = true
    void (async () => {
      // No row, no send — the server would refuse it anyway, and the handoff
      // silently dropping the goal it was sent here with is exactly the shape
      // of failure this whole change is about. The toast tells the user; the
      // ref stays set so a failed handoff does not retry itself in a loop.
      if (!(await ensureSessionForSend())) return
      sendMessage(text)
      onSend?.(sessionId, text)
    })()
  }, [autoSendInitial, initialInput, connectionStatus, isStreaming, ensureSessionForSend, sendMessage, onSend, sessionId])

  const composerInitialInput = autoSendInitial ? undefined : initialInput

  const handleSuggestionClick = useCallback(async (suggestion: string) => {
    if (isStreaming) return
    if (!(await ensureSessionForSend())) return
    sendMessage(suggestion)
    setPinNonce((n) => n + 1)
    onSend?.(sessionId, suggestion)
  }, [isStreaming, sendMessage, ensureSessionForSend, sessionId, onSend])

  // This agent's questionnaire forms. Empty for every agent nobody has
  // configured — which is to say for almost all of them — and an empty list
  // is what makes the rail below render exactly the chips it rendered before
  // this feature existed.
  //
  // Parsed here, from the column the caller handed down, so the chat costs no
  // request for it. `undefined` (no record at all) is passed through as
  // undefined, which is what leaves the hook's own fetch in charge. Memoised
  // because the hook takes the parsed list as a dependency: a fresh array
  // every render would re-run its effect on every render.
  const providedAskForms = useMemo(
    () => (askForms === undefined ? undefined : askFormsFromColumn(askForms)),
    [askForms],
  )
  const askFormList = useAskForms(agentId, providedAskForms)

  // The form whose sheet is open, if any. Owned here rather than in the
  // composer because the chips that open it live here; the SHEET is mounted
  // by the composer, which is the only place it can both sit above the input
  // and reuse the composer's own submit path.
  const [activeAskForm, setActiveAskForm] = useState<AskForm | null>(null)
  // A sheet is about one conversation. Swapping sessions with one open would
  // leave it hovering over a chat it was never opened from, holding answers
  // meant for the previous one.
  useEffect(() => { setActiveAskForm(null) }, [sessionId])

  const handleFormClick = useCallback((form: AskForm) => {
    // Deliberately no send, no ensureSession, no pin. Opening a form is not
    // an interaction with the agent yet — that is the whole distinction the
    // chip's glyph and ellipsis are promising.
    setActiveAskForm(form)
  }, [])

  const closeAskForm = useCallback(() => setActiveAskForm(null), [])

  /** "via Add a receipt" over a user bubble that came out of a form. */
  const resolveAskProvenance = useCallback(
    (content: string) => lookupAskProvenance(sessionId, content),
    [sessionId],
  )

  const handleCopy = useCallback((content: string) => {
    navigator.clipboard.writeText(content).catch(() => {})
  }, [])

  // Regenerate/edit also stream a fresh reply, so they re-arm the pin just
  // like a plain send — otherwise the scroll behavior is inconsistent.
  const regenerateWithPin = useCallback(() => {
    regenerateLastTurn()
    setPinNonce((n) => n + 1)
  }, [regenerateLastTurn])

  const editAndResendWithPin = useCallback((turnId: string, newContent: string) => {
    editAndResend(turnId, newContent)
    setPinNonce((n) => n + 1)
  }, [editAndResend])

  // ── The slash palette's delegated commands ────────────────────────────────
  //
  // Every id the palette hands over must DO something here. It used to hand
  // over branch / search / export / run-task as well, and this handler covered
  // regenerate and clear: the other four closed the palette and did nothing
  // (audit P0.8). Search and export are now real — the two surfaces that own
  // those UIs already existed, they were just unreachable from anywhere but
  // their own hotkey — and the rest are classified in the palette itself.
  //
  // The list the palette delegates is PANEL_HANDLED_COMMAND_IDS, which
  // chat-panel-slash-actions.test.tsx walks: a new delegated command is red
  // until it is handled here.
  const [searchOpen, setSearchOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)

  const handleSlashCommand = useCallback((id: string) => {
    if (id === "regenerate") regenerateWithPin()
    else if (id === "clear") loadHistory([])
    else if (id === "search") setSearchOpen(true)
    else if (id === "export") setExportOpen(true)
  }, [regenerateWithPin, loadHistory])

  // Rows that would be no-ops on THIS conversation right now. Static
  // classification (does the capability exist at all?) lives in the palette;
  // this is the part only the host can know.
  const slashDisabledCommands = useMemo<Record<string, string>>(() => {
    const out: Record<string, string> = {}
    if (turns.length === 0) {
      out.clear = "Nothing to clear yet"
      out.regenerate = "Nothing to regenerate yet"
      out.search = "Nothing to search yet"
      out.export = "Nothing to export yet"
    } else if (isStreaming) {
      out.regenerate = "Wait for the reply to finish"
    }
    return out
  }, [turns.length, isStreaming])

  // The server-driven action the user picked, if any. The panel owns the
  // modal (not the palette) because the form pre-fills from the conversation.
  const [slashAction, setSlashAction] = useState<ServerSlashCommand | null>(null)
  useEffect(() => { setSlashAction(null) }, [sessionId])

  /** What "…from this conversation" means for an action's form: the transcript,
   *  trimmed, as the raw material for the field that asks for one. Only fields
   *  the catalogue actually declares are used (SlashActionModal ignores keys
   *  that no field is named after). */
  const slashActionPreFill = useMemo<Record<string, string>>(() => {
    const transcript = turns
      .filter((t) => t.role === "user" || t.role === "assistant")
      .slice(-6)
      .map((t) => {
        const text = t.parts.filter((p) => p.type === "text").map((p) => p.content).join("\n").trim()
        if (!text) return ""
        return `${t.role === "user" ? "You" : agentName ?? "Assistant"}: ${text}`
      })
      .filter(Boolean)
      .join("\n\n")
      .slice(0, 4000)
    const preFill: Record<string, string> = {}
    if (transcript) {
      preFill.prompt = transcript
      preFill.description = transcript
    }
    return preFill
  }, [turns, agentName])

  // Opt-in virtualized list (localStorage crewship.virtualChat=1) — mounts
  // only the viewport instead of every turn. Initialized false and flipped
  // post-mount: reading localStorage during the hydration render made the
  // client tree diverge from the prerendered HTML (recoverable React 19
  // hydration error for flag users). One post-mount swap is the price of
  // an experimental gate; flipping the flag still requires a reload.
  const [useVirtualChat, setUseVirtualChat] = useState(false)
  useEffect(() => {
    if (virtualChatEnabled()) setUseVirtualChat(true)
  }, [])

  // One conversation surface, rendered by both the mobile-chat and desktop
  // branches — the two copies had already drifted once; don't re-fork them.
  const conversationEl = useVirtualChat ? (
    <VirtualConversation
      turns={turns}
      sessionId={sessionId}
      agentId={agentId}
      agentName={agentName}
      historyLoading={historyLoading}
      isStreaming={isStreaming}
      animateAfter={animateAfter}
      onCopy={handleCopy}
      onFileClick={noopFileClick}
      onRegenerate={!isStreaming ? regenerateWithPin : undefined}
      onEditUserMessage={!isStreaming ? editAndResendWithPin : undefined}
      resolveAuthorName={resolveAuthorName}
      resolveAskProvenance={resolveAskProvenance}
      footer={<StreamingIndicator isStreaming={isStreaming} turns={turns} agentName={agentName} />}
    />
  ) : (
    <Conversation>
      <ConversationContent className="mx-auto w-full max-w-3xl">
        {turns.length === 0 && !historyLoading && (
          <ConversationEmptyState
            icon={<Bot className="h-12 w-12" />}
            title="Start a conversation"
            description={agentName ? `Send a message to ${agentName}` : "Send a message or pick a suggestion below"}
          />
        )}
        <AnimatePresence key={sessionId} initial={false} mode="popLayout">
          {turns.map((turn, idx) => (
            <TurnRenderer
              key={turn.id}
              turn={turn}
              onCopy={handleCopy}
              onFileClick={noopFileClick}
              isLastAssistant={turn.role === "assistant" && idx === turns.length - 1}
              onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? regenerateWithPin : undefined}
              onEditUserMessage={!isStreaming ? editAndResendWithPin : undefined}
              animateAfter={animateAfter}
              agentId={agentId}
              chatId={sessionId}
              resolveAuthorName={resolveAuthorName}
              resolveAskProvenance={resolveAskProvenance}
            />
          ))}
        </AnimatePresence>
        <StreamingIndicator isStreaming={isStreaming} turns={turns} agentName={agentName} />
        <PinToTopSpacer pinNonce={pinNonce} pinTurnId={lastUserTurnId} sessionId={sessionId} />
      </ConversationContent>
      <ConversationScrollButton />
    </Conversation>
  )

  // Mobile: files-only mode -- just the file tree, no tabs
  if (mobilePanel === "files-only") {
    return (
      <RightPanel
        agentId={agentId}
        workspaceId={workspaceId}
        files={files}
        initialTab="files"
        hideTabs
        style={{ width: "100%" }}
      />
    )
  }

  // Mobile: show full RightPanel with all tabs (files + triggers + team + context)
  if (mobilePanel === "files") {
    return (
      <RightPanel
        agentId={agentId}
        workspaceId={workspaceId}
        files={files}
        initialTab="files"
        style={{ width: "100%" }}
      />
    )
  }

  if (mobilePanel === "more") {
    return (
      <RightPanel
        agentId={agentId}
        workspaceId={workspaceId}
        files={files}
        initialTab="triggers"
        style={{ width: "100%" }}
      />
    )
  }

  if (mobilePanel === "chat") {
    return (
      <div className="flex flex-col h-full">
        <div className="flex items-center gap-2 px-4 py-1.5 shrink-0">
          <ConnectionBadge status={connectionStatus} />
          {isGroupChat && (
            <span className="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2 py-0.5 text-micro font-medium text-primary">
              <Users className="h-3 w-3" />
              Group · {Object.keys(participantNames).length}
            </span>
          )}
          <span className="text-micro text-muted-foreground ml-auto font-mono">
            {sessionId.slice(0, 8)}
          </span>
        </div>
        <div className="flex-1 flex flex-col overflow-hidden min-h-0">
          {conversationEl}
        </div>
        {turns.length === 0 && !historyLoading && (
          <div className="px-4 pb-2 shrink-0">
            <AskRail
              questions={defaultSuggestions}
              forms={askFormList}
              limit={EMPTY_STATE_CHIP_LIMIT}
              disabled={isStreaming}
              onPickQuestion={handleSuggestionClick}
              onPickForm={handleFormClick}
            />
          </div>
        )}
        <ChatComposer
          agentId={agentId}
          sessionId={sessionId}
          agentName={agentName}
          variant="mobile"
          isStreaming={isStreaming}
          connectionStatus={connectionStatus}
          stopGeneration={stopGeneration}
          ensureSession={ensureSessionForSend}
          sendMessage={sendMessage}
          onSend={onSend}
          onSent={handleSent}
          initialInput={composerInitialInput}
          askForm={activeAskForm}
          onCloseAskForm={closeAskForm}
          renderAskTemplate={renderAskTemplate}
        />
      </div>
    )
  }

  // Desktop: chat + icon rail; drawer overlays (or pushes) when open
  const pushOpen = drawerOpen && drawerMode === "push"
  return (
    <div className="relative flex h-full">
      <div className="flex flex-col overflow-hidden flex-1 min-w-0">
        <div className="flex items-center gap-2 px-4 md:px-6 h-[41px] border-b shrink-0">
          <ConnectionBadge status={connectionStatus} />
          <OriginChip origin={sessionOrigin} />
          <span className="text-micro text-muted-foreground ml-auto font-mono">
            {sessionId.slice(0, 8)}
          </span>
        </div>
        <div className="flex-1 flex flex-col overflow-hidden min-h-0">
          {conversationEl}
        </div>
        {turns.length === 0 && !historyLoading && (
          <div className="mx-auto w-full max-w-3xl px-4 md:px-6 pb-2 shrink-0">
            <AskRail
              questions={defaultSuggestions}
              forms={askFormList}
              limit={EMPTY_STATE_CHIP_LIMIT}
              disabled={isStreaming}
              onPickQuestion={handleSuggestionClick}
              onPickForm={handleFormClick}
            />
          </div>
        )}
        <div className="mx-auto w-full max-w-3xl">
        <FollowUps
          prompts={followUpPrompts}
          onPick={handleSuggestionClick}
          forms={askFormList}
          onPickForm={handleFormClick}
          show={!isStreaming && turns.length > 0 && turns[turns.length - 1].role === "assistant"}
        />
        </div>
        <ChatComposer
          agentId={agentId}
          sessionId={sessionId}
          agentName={agentName}
          variant="desktop"
          isStreaming={isStreaming}
          connectionStatus={connectionStatus}
          stopGeneration={stopGeneration}
          ensureSession={ensureSessionForSend}
          sendMessage={sendMessage}
          onSend={onSend}
          onSent={handleSent}
          initialInput={composerInitialInput}
          mentionMembers={mentionMembers}
          askForm={activeAskForm}
          onCloseAskForm={closeAskForm}
          renderAskTemplate={renderAskTemplate}
        />
      </div>

      <RightDrawer>
        <RightPanel
          key={drawerActiveTab}
          agentId={agentId}
          workspaceId={workspaceId}
          files={files}
          initialTab={drawerActiveTab}
          hideTabs
          style={{ width: "100%", height: "100%" }}
        />
      </RightDrawer>

      <RightRail className={cn(pushOpen && "border-l-0")} />
      {/* workspaceId is what makes the server-driven Actions group exist at
          all: useSlashCommands(undefined) never runs its query, so the palette
          rendered without it could only ever show the client rows. */}
      <SlashPalette
        agentSlug={agentSlug}
        workspaceId={workspaceId ?? undefined}
        onCommand={handleSlashCommand}
        onAction={setSlashAction}
        disabledCommands={slashDisabledCommands}
      />
      {workspaceId && (
        <SlashActionModal
          command={slashAction}
          workspaceId={workspaceId}
          contextPreFill={slashActionPreFill}
          onClose={() => setSlashAction(null)}
        />
      )}
      <ArtifactPane agentId={agentId} />
      <ConversationSearch turns={turns} open={searchOpen} onOpenChange={setSearchOpen} />
      <ExportDialog turns={turns} agentName={agentName} open={exportOpen} onOpenChange={setExportOpen} />
      <ReconnectBanner status={connectionStatus} />
    </div>
  )
}

/* ---- Small shared sub-components extracted to reduce duplication ---- */

function ConnectionBadge({ status }: { status: string }) {
  return (
    <div className={cn(
      "flex items-center gap-1.5 px-2 py-0.5 rounded-full text-micro font-medium",
      status === "connected"
        ? "bg-success/10 text-success dark:bg-success/30 dark:text-success"
        : status === "connecting"
          ? "bg-warn/10 text-warn dark:bg-warn/30 dark:text-warn"
          : "bg-destructive/10 text-destructive dark:bg-destructive/30 dark:text-destructive"
    )}>
      {status === "connected" ? (
        <Wifi className="h-3 w-3" />
      ) : status === "connecting" ? (
        <Spinner className="h-3 w-3" />
      ) : (
        <WifiOff className="h-3 w-3" />
      )}
      <span className="capitalize">{status}</span>
    </div>
  )
}

/** Origin chip in the chat header strip — tells the user at a glance
 *  whether they're looking at a session started from the UI, the CLI,
 *  a webhook, a cron, or an agent-to-agent assignment. Hidden when
 *  origin is unknown (pre-migration sessions or legacy backends). */
function OriginChip({ origin }: { origin?: string | null }) {
  if (!origin) return null
  const map: Record<string, { label: string; className: string }> = {
    UI:      { label: "UI",      className: "bg-info/15 text-info" },
    CLI:     { label: "CLI",     className: "bg-purple/15 text-purple" },
    WEBHOOK: { label: "Hook",    className: "bg-warn/15 text-warn" },
    CRON:    { label: "Cron",    className: "bg-warn/15 text-warn" },
    AGENT:   { label: "Agent",   className: "bg-purple/15 text-purple" },
  }
  const tag = map[origin]
  if (!tag) return null
  return (
    <span className={cn("text-[10px] px-1.5 py-0.5 rounded font-medium", tag.className)}>
      {tag.label}
    </span>
  )
}

interface StreamingIndicatorProps {
  isStreaming: boolean
  turns: { role: string }[]
  agentName?: string
}

/** Pre-first-token indicator: a shimmering "<name> is thinking…" label (the
 *  reasoning-shimmer pattern) instead of generic bouncing dots. Shows only in the gap
 *  between sending and the first streamed event. */
function StreamingIndicator({ isStreaming, turns, agentName }: StreamingIndicatorProps) {
  if (!isStreaming || turns.length === 0 || turns[turns.length - 1]?.role !== "user") return null
  return (
    <div className="flex items-center gap-2 px-4 py-3 text-sm animate-in fade-in">
      <Shimmer duration={1.6}>{`${agentName ?? "Agent"} is thinking…`}</Shimmer>
    </div>
  )
}
