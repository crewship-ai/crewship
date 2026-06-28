"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence } from "motion/react"
import {
  Bot,
  Wifi,
  WifiOff,
  Loader2,
  Users,
} from "lucide-react"
import { cn } from "@/lib/utils"

import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
  ConversationEmptyState,
} from "@/components/ai-elements/conversation"
import {
  PromptInput,
  PromptInputTextarea,
  PromptInputFooter,
  PromptInputSubmit,
  type PromptInputMessage,
} from "@/components/ai-elements/prompt-input"
import { Suggestion, Suggestions } from "@/components/ai-elements/suggestion"
import { useChat, type HistoryPart } from "@/hooks/use-chat"
import { useSession } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useDrawerStore } from "@/stores/drawer-store"

import { TurnRenderer } from "./turn-renderer"
import { RightPanel } from "./right-panel"
import { RightRail } from "./right-rail"
import { RightDrawer } from "./right-drawer"
import { SlashPalette } from "./composer/slash-palette"
import { MentionAutocomplete, type CrewMember } from "./composer/mention-autocomplete"
import { AttachmentZone, AttachmentButton } from "./composer/attachment-zone"
import { ArtifactPane } from "./artifact/artifact-pane"
import { FollowUps } from "./suggestions/follow-ups"
import { ConversationSearch } from "./search/conversation-search"
import { ExportDialog } from "./export/export-dialog"
import { ReconnectBanner } from "./messages/reconnect-banner"
import type { FileEntry } from "./chat-tree-row"
import { useComposerStore } from "@/stores/composer-store"
import { getSuggestions } from "@/lib/agent-suggestions"
import { apiFetch } from "@/lib/api-fetch"

function getWsUrl(): string {
  if (typeof window === "undefined") return ""
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${window.location.host}/ws`
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
  /** How this session was created — UI / CLI / WEBHOOK / CRON / AGENT.
   *  Rendered as a chip in the connection bar so the user knows where
   *  they are at a glance. Undefined = unknown (pre-migration). */
  sessionOrigin?: string | null
  /** Pre-populate the chat input with this text on first render. */
  initialInput?: string
  /** Mobile-only: which panel to show full-screen. Undefined = desktop mode. */
  mobilePanel?: "chat" | "files" | "files-only" | "more"
  /** Fired when the user sends a message — lets the parent optimistically
   *  title a freshly-created session in the sidebar (matching the server's
   *  auto-title) so the new entry shows its name without a manual refresh. */
  onSend?: (sessionId: string, text: string) => void
}

const noopFileClick = () => {}

/** Chat panel with split view: conversation on the left, tabbed panel on the right. */
export function ChatPanel({ agentId, sessionId, agentName, agentSlug, agentRole, sessionOrigin, initialInput, mobilePanel, onSend }: ChatPanelProps) {
  const suggestionPack = getSuggestions(agentRole)
  const defaultSuggestions = suggestionPack.empty
  const followUpPrompts = suggestionPack.followUps
  const { workspaceId } = useWorkspace()
  const [input, setInput] = useState(initialInput ?? "")
  const [sessionReady, setSessionReady] = useState(false)

  // Cutoff: turns whose timestamp is BEFORE this number skip the arrival
  // animation. Bumped on every session swap so loaded-from-history turns
  // appear instantly (no slide-up flash) while genuinely-new turns sent
  // or streamed AFTER the swap still animate.
  const [animateAfter, setAnimateAfter] = useState(() => Date.now())
  const [historyLoading, setHistoryLoading] = useState(true)
  const sessionLoadedFor = useRef<string | null>(null)

  useEffect(() => {
    setSessionReady(false)
    setHistoryLoading(true)
    setAnimateAfter(Date.now() + 250)
    sessionLoadedFor.current = sessionId
  }, [sessionId])

  // Pre-populate input when a new session is started with a prefill value
  useEffect(() => {
    if (initialInput) setInput(initialInput)
  }, [initialInput])

  const [files, setFiles] = useState<FileEntry[]>([])
  const drawer = useDrawerStore()

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

  const { turns, sendMessage, stopGeneration, regenerateLastTurn, editAndResend, loadHistory, isStreaming, connectionStatus } = useChat({
    wsUrl: getWsUrl(),
    getToken: getWsToken,
    sessionId,
    currentUserId: currentUserId ?? undefined,
  })

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
    const fetchOnce = async (): Promise<{ exists: boolean; messages: HistoryMessage[] } | "retry"> => {
      try {
        const r = await apiFetch(`/api/v1/chats/${sessionId}/messages?workspace_id=${encodeURIComponent(workspaceId)}`)
        if (r.status === 404) return { exists: false, messages: [] }
        if (!r.ok) return "retry"
        const data = await r.json()
        return { exists: true, messages: (data?.messages ?? []) as HistoryMessage[] }
      } catch {
        return "retry"
      }
    }

    const run = async () => {
      let result: { exists: boolean; messages: HistoryMessage[] } | "retry" = "retry"
      for (let attempt = 0; attempt < 3 && !cancelled; attempt++) {
        if (attempt > 0) await new Promise((res) => setTimeout(res, 300 * attempt))
        result = await fetchOnce()
        if (result !== "retry") break
      }
      if (cancelled) return

      if (result === "retry") {
        // All attempts failed — do NOT wipe; only stop the loading spinner.
        setHistoryLoading(false)
        return
      }

      const { exists, messages } = result
      setSessionReady(exists)
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
  }, [sessionId, workspaceId, loadHistory])

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

  // @mention autocomplete. The chat's agent is always offered (mentioning it is
  // what makes it respond in a group chat); teammates are offered too as a
  // courtesy. The textarea ref lets the popover anchor to the caret.
  const mentionTextareaRef = useRef<HTMLTextAreaElement>(null)
  const mentionMembers = useMemo<CrewMember[]>(() => {
    const list: CrewMember[] = [{ id: agentId, slug: agentSlug, name: agentName ?? agentSlug, role_title: agentRole ?? undefined }]
    for (const [uid, name] of Object.entries(participantNames)) {
      if (uid !== currentUserId) {
        list.push({ id: uid, slug: name.replace(/\s+/g, "").toLowerCase(), name })
      }
    }
    return list
  }, [agentId, agentSlug, agentName, agentRole, participantNames, currentUserId])
  const handleMentionPick = useCallback((member: CrewMember, atIndex: number) => {
    setInput((prev) => {
      const after = prev.slice(atIndex)
      const ws = after.search(/\s/)
      const end = ws === -1 ? prev.length : atIndex + ws
      return prev.slice(0, atIndex) + "@" + member.slug + " " + prev.slice(end)
    })
  }, [])

  const ensureSession = useCallback(async () => {
    if (sessionReady || !workspaceId || !sessionId) return
    try {
      const res = await apiFetch(
        `/api/v1/agents/${agentId}/chats?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session_id: sessionId, origin: "UI" }) },
      )
      if (res.ok) setSessionReady(true)
    } catch { /* ignore */ }
  }, [agentId, workspaceId, sessionId, sessionReady])

  // Fetch files only when the Files tab might be visible (drawer open + active)
  const filesVisible = drawer.open && drawer.activeTab === "files"
  useEffect(() => {
    if (!workspaceId || !filesVisible || !sessionId) return
    apiFetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data: FileEntry[] | null) => setFiles(data ?? []))
      .catch(() => {})
  }, [agentId, workspaceId, filesVisible, sessionId])

  const composer = useComposerStore()

  const handleSubmit = useCallback(async (message: PromptInputMessage) => {
    const text = message.text?.trim()
    if (!text || isStreaming) return
    await ensureSession()
    sendMessage(text)
    onSend?.(sessionId, text)
    setInput("")
    composer.clearDraft(sessionId)
    composer.clearAttachments(sessionId)
  }, [isStreaming, sendMessage, ensureSession, composer, sessionId, onSend])

  const handleSuggestionClick = useCallback(async (suggestion: string) => {
    if (isStreaming) return
    await ensureSession()
    sendMessage(suggestion)
    onSend?.(sessionId, suggestion)
  }, [isStreaming, sendMessage, ensureSession, sessionId, onSend])

  const handleCopy = useCallback((content: string) => {
    navigator.clipboard.writeText(content).catch(() => {})
  }, [])

  const handleSlashCommand = useCallback((id: string) => {
    if (id === "regenerate") regenerateLastTurn()
    else if (id === "clear") loadHistory([])
  }, [regenerateLastTurn, loadHistory])

  const chatStatus = isStreaming ? "streaming" as const : "ready" as const

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
                    onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? regenerateLastTurn : undefined}
                    onEditUserMessage={!isStreaming ? editAndResend : undefined}
                    animateAfter={animateAfter}
                    agentId={agentId}
                    chatId={sessionId}
                    resolveAuthorName={resolveAuthorName}
                  />
                ))}
              </AnimatePresence>
              <StreamingIndicator isStreaming={isStreaming} turns={turns} />
            </ConversationContent>
            <ConversationScrollButton />
          </Conversation>
        </div>
        {turns.length === 0 && !historyLoading && (
          <div className="px-4 pb-2 shrink-0">
            <Suggestions>
              {defaultSuggestions.map((s) => (
                <Suggestion key={s} suggestion={s} onClick={() => handleSuggestionClick(s)}>{s}</Suggestion>
              ))}
            </Suggestions>
          </div>
        )}
        <div className="p-3 shrink-0">
          <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
            <PromptInputTextarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={agentName ? `Message ${agentName}...` : "Send a message..."}
              className="min-h-[44px]"
            />
            <PromptInputFooter className="justify-end p-2">
              <PromptInputSubmit
                disabled={!isStreaming && (!input.trim() || connectionStatus !== "connected")}
                status={chatStatus}
                onStop={stopGeneration}
              />
            </PromptInputFooter>
          </PromptInput>
        </div>
      </div>
    )
  }

  // Desktop: chat + icon rail; drawer overlays (or pushes) when open
  const pushOpen = drawer.open && drawer.mode === "push"
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
                    onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? regenerateLastTurn : undefined}
                    onEditUserMessage={!isStreaming ? editAndResend : undefined}
                    animateAfter={animateAfter}
                    agentId={agentId}
                    chatId={sessionId}
                    resolveAuthorName={resolveAuthorName}
                  />
                ))}
              </AnimatePresence>
              <StreamingIndicator isStreaming={isStreaming} turns={turns} />
            </ConversationContent>
            <ConversationScrollButton />
          </Conversation>
        </div>
        {turns.length === 0 && !historyLoading && (
          <div className="mx-auto w-full max-w-3xl px-4 md:px-6 pb-2 shrink-0">
            <Suggestions>
              {defaultSuggestions.map((s) => (
                <Suggestion key={s} suggestion={s} onClick={() => handleSuggestionClick(s)}>{s}</Suggestion>
              ))}
            </Suggestions>
          </div>
        )}
        <div className="mx-auto w-full max-w-3xl">
        <FollowUps
          prompts={followUpPrompts}
          onPick={handleSuggestionClick}
          show={!isStreaming && turns.length > 0 && turns[turns.length - 1].role === "assistant"}
        />
        </div>
        <div className="mx-auto w-full max-w-3xl p-3 md:px-6 shrink-0">
          <AttachmentZone agentId={agentId} sessionId={sessionId}>
            <MentionAutocomplete
              text={input}
              textareaRef={mentionTextareaRef}
              members={mentionMembers}
              onPick={handleMentionPick}
            />
            <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
              <PromptInputTextarea
                ref={mentionTextareaRef}
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder={agentName ? `Message ${agentName}...` : "Send a message..."}
                className="min-h-[44px]"
              />
              <PromptInputFooter className="justify-between p-2 gap-2">
                <div className="flex items-center gap-1">
                  <AttachmentButton agentId={agentId} sessionId={sessionId} />
                </div>
                <PromptInputSubmit
                  disabled={!isStreaming && (!input.trim() || connectionStatus !== "connected")}
                  status={chatStatus}
                  onStop={stopGeneration}
                />
              </PromptInputFooter>
            </PromptInput>
          </AttachmentZone>
        </div>
      </div>

      <RightDrawer>
        <RightPanel
          key={drawer.activeTab}
          agentId={agentId}
          workspaceId={workspaceId}
          files={files}
          initialTab={drawer.activeTab}
          hideTabs
          style={{ width: "100%", height: "100%" }}
        />
      </RightDrawer>

      <RightRail className={cn(pushOpen && "border-l-0")} />
      <SlashPalette agentSlug={agentSlug} onCommand={handleSlashCommand} />
      <ArtifactPane agentId={agentId} />
      <ConversationSearch turns={turns} />
      <ExportDialog turns={turns} agentName={agentName} />
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
        ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400"
        : status === "connecting"
          ? "bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400"
          : "bg-red-50 text-red-700 dark:bg-red-950/30 dark:text-red-400"
    )}>
      {status === "connected" ? (
        <Wifi className="h-3 w-3" />
      ) : status === "connecting" ? (
        <Loader2 className="h-3 w-3 animate-spin" />
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
    UI:      { label: "UI",      className: "bg-blue-500/15 text-blue-300" },
    CLI:     { label: "CLI",     className: "bg-violet-500/15 text-violet-300" },
    WEBHOOK: { label: "Hook",    className: "bg-amber-500/15 text-amber-300" },
    CRON:    { label: "Cron",    className: "bg-amber-500/15 text-amber-300" },
    AGENT:   { label: "Agent",   className: "bg-fuchsia-500/15 text-fuchsia-300" },
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
}

function StreamingIndicator({ isStreaming, turns }: StreamingIndicatorProps) {
  if (!isStreaming || turns.length === 0 || turns[turns.length - 1]?.role !== "user") return null
  return (
    <div className="flex items-center gap-2 px-4 py-3 text-muted-foreground text-sm animate-in fade-in">
      <span className="inline-flex gap-0.5">
        <span className="animate-bounce [animation-delay:0ms]">·</span>
        <span className="animate-bounce [animation-delay:150ms]">·</span>
        <span className="animate-bounce [animation-delay:300ms]">·</span>
      </span>
      <span>Agent is thinking</span>
    </div>
  )
}
