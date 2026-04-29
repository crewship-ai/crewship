"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { AnimatePresence } from "motion/react"
import {
  Bot,
  AlertCircle,
  Wifi,
  WifiOff,
  Loader2,
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
import { useChat } from "@/hooks/use-chat"
import { useWorkspace } from "@/hooks/use-workspace"
import { useDrawerStore } from "@/stores/drawer-store"

import { TurnRenderer } from "./turn-renderer"
import { RightPanel } from "./right-panel"
import { RightRail } from "./right-rail"
import { RightDrawer } from "./right-drawer"
import { SlashPalette } from "./composer/slash-palette"
import { AttachmentZone, AttachmentButton } from "./composer/attachment-zone"
import { ArtifactPane } from "./artifact/artifact-pane"
import { FollowUps } from "./suggestions/follow-ups"
import { ConversationSearch } from "./search/conversation-search"
import { ExportDialog } from "./export/export-dialog"
import { ReconnectBanner } from "./messages/reconnect-banner"
import type { FileEntry } from "./chat-tree-row"
import { useComposerStore } from "@/stores/composer-store"
import { getSuggestions } from "@/lib/agent-suggestions"

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
   *  Required by SlashPalette commands like /new-session that navigate
   *  back to the agent route — passing the display name there breaks
   *  for agents whose name has spaces or non-URL-safe characters. */
  agentSlug?: string
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
}

const noopFileClick = () => {}

/** Chat panel with split view: conversation on the left, tabbed panel on the right. */
export function ChatPanel({ agentId, sessionId, agentName, agentSlug, agentRole, sessionOrigin, initialInput, mobilePanel }: ChatPanelProps) {
  const suggestionPack = getSuggestions(agentRole)
  const defaultSuggestions = suggestionPack.empty
  const followUpPrompts = suggestionPack.followUps
  const { workspaceId } = useWorkspace()
  const [token, setToken] = useState<string | null>(null)
  const [authError, setAuthError] = useState(false)
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

  useEffect(() => {
    fetch("/api/v1/ws-token", { credentials: "include" })
      .then((r) => {
        if (r.status === 401) { setAuthError(true); return null }
        return r.json()
      })
      .then((data: { token?: string } | null) => {
        if (data?.token) setToken(data.token)
      })
      .catch(() => {})
  }, [])

  const { turns, sendMessage, stopGeneration, regenerateLastTurn, editAndResend, loadHistory, isStreaming, connectionStatus } = useChat({
    wsUrl: getWsUrl(),
    token,
    sessionId,
  })

  useEffect(() => {
    if (!sessionId) return
    let cancelled = false
    fetch(`/api/v1/chats/${sessionId}/messages`, { credentials: "include" })
      .then(async (r) => {
        // 404 means the chat row doesn't exist yet — it's a brand-new
        // session. Don't mark it ready; ensureSession() must still run
        // on first send to create the chat row.
        if (r.status === 404) {
          return { exists: false, messages: [] as { id: string; role: string; content: string; ts: string }[] }
        }
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        const data = await r.json()
        return {
          exists: true,
          messages: (data?.messages ?? []) as { id: string; role: string; content: string; ts: string }[],
        }
      })
      .then(({ exists, messages }) => {
        if (cancelled) return
        setSessionReady(exists)
        // Always replace — including with [] for an empty (newly created)
        // session — so the visible turns swap atomically from old session
        // → new session with no blank gap in between.
        loadHistory(messages.map((m) => ({
          id: m.id,
          role: m.role as "user" | "assistant" | "system" | "tool",
          content: m.content,
          timestamp: new Date(m.ts),
        })))
      })
      .catch(() => {
        if (!cancelled) {
          setSessionReady(false)
          loadHistory([])
        }
      })
      .finally(() => {
        if (!cancelled) setHistoryLoading(false)
      })
    return () => { cancelled = true }
  }, [sessionId, loadHistory])

  const ensureSession = useCallback(async () => {
    if (sessionReady || !workspaceId || !sessionId) return
    try {
      const res = await fetch(
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
    fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`)
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
    setInput("")
    composer.clearDraft(sessionId)
    composer.clearAttachments(sessionId)
  }, [isStreaming, sendMessage, ensureSession, composer, sessionId])

  const handleSuggestionClick = useCallback(async (suggestion: string) => {
    if (isStreaming) return
    await ensureSession()
    sendMessage(suggestion)
  }, [isStreaming, sendMessage, ensureSession])

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
          <span className="text-micro text-muted-foreground ml-auto font-mono">
            {sessionId.slice(0, 8)}
          </span>
        </div>
        <div className="flex-1 flex flex-col overflow-hidden min-h-0">
          {authError ? (
            <AuthErrorState />
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
                      onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? regenerateLastTurn : undefined}
                      onEditUserMessage={!isStreaming ? editAndResend : undefined}
                      animateAfter={animateAfter}
                    />
                  ))}
                </AnimatePresence>
                <StreamingIndicator isStreaming={isStreaming} turns={turns} />
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>
        {turns.length === 0 && !authError && !historyLoading && (
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
          {authError ? (
            <AuthErrorState />
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
                      onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? regenerateLastTurn : undefined}
                      onEditUserMessage={!isStreaming ? editAndResend : undefined}
                      animateAfter={animateAfter}
                    />
                  ))}
                </AnimatePresence>
                <StreamingIndicator isStreaming={isStreaming} turns={turns} />
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>
        {turns.length === 0 && !authError && !historyLoading && (
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
            <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
              <PromptInputTextarea
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
      <SlashPalette agentSlug={agentSlug ?? agentName} onCommand={handleSlashCommand} />
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

function AuthErrorState() {
  return (
    <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
      <AlertCircle className="h-12 w-12 mb-3 opacity-30" />
      <p className="text-body">Session expired. Please log in again.</p>
    </div>
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
