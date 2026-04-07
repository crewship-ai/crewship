"use client"

import { useCallback, useEffect, useState, useRef } from "react"
import {
  Bot,
  AlertCircle,
  Wifi,
  WifiOff,
  Loader2,
  PanelRightOpen,
  PanelRightClose,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"

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

import { TurnRenderer } from "./turn-renderer"
import { RightPanel } from "./right-panel"
import type { FileEntry } from "./chat-tree-row"

function getWsUrl(): string {
  if (typeof window === "undefined") return ""
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${window.location.host}/ws`
}

interface ChatPanelProps {
  agentId: string
  sessionId: string
  agentName?: string
  /** Pre-populate the chat input with this text on first render. */
  initialInput?: string
  /** Mobile-only: which panel to show full-screen. Undefined = desktop mode. */
  mobilePanel?: "chat" | "files" | "files-only" | "more"
}

const defaultSuggestions = [
  "Help me get started",
  "What can you do?",
  "Show me your skills",
  "Run a quick task",
]

/** Chat panel with split view: conversation on the left, tabbed panel on the right. */
export function ChatPanel({ agentId, sessionId, agentName, initialInput, mobilePanel }: ChatPanelProps) {
  const { workspaceId } = useWorkspace()
  const [token, setToken] = useState<string | null>(null)
  const [authError, setAuthError] = useState(false)
  const [input, setInput] = useState(initialInput ?? "")
  const [sessionReady, setSessionReady] = useState(false)

  useEffect(() => {
    setSessionReady(false)
  }, [sessionId])

  // Pre-populate input when a new session is started with a prefill value
  useEffect(() => {
    if (initialInput) setInput(initialInput)
  }, [initialInput])

  const [showPreview, setShowPreview] = useState(true)
  const [files, setFiles] = useState<FileEntry[]>([])
  const [splitRatio, setSplitRatio] = useState(60)
  const containerRef = useRef<HTMLDivElement>(null)

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
    fetch(`/api/v1/chats/${sessionId}/messages`, { credentials: "include" })
      .then((r) => r.ok ? r.json() : null)
      .then((data: { messages?: { id: string; role: string; content: string; ts: string }[] } | null) => {
        if (!data?.messages?.length) return
        setSessionReady(true)
        loadHistory(data.messages.map((m) => ({
          id: m.id,
          role: m.role as "user" | "assistant" | "system" | "tool",
          content: m.content,
          timestamp: new Date(m.ts),
        })))
      })
      .catch(() => {})
  }, [sessionId, loadHistory])

  const ensureSession = useCallback(async () => {
    if (sessionReady || !workspaceId || !sessionId) return
    try {
      const res = await fetch(
        `/api/v1/agents/${agentId}/chats?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session_id: sessionId }) },
      )
      if (res.ok) setSessionReady(true)
    } catch { /* ignore */ }
  }, [agentId, workspaceId, sessionId, sessionReady])

  // Fetch files for the preview panel
  useEffect(() => {
    if (!workspaceId || !showPreview || !sessionId) return
    fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data: FileEntry[] | null) => setFiles(data ?? []))
      .catch(() => {})
  }, [agentId, workspaceId, showPreview, sessionId])

  const handleSubmit = useCallback(async (message: PromptInputMessage) => {
    const text = message.text?.trim()
    if (!text || isStreaming) return
    await ensureSession()
    sendMessage(text)
    setInput("")
  }, [isStreaming, sendMessage, ensureSession])

  const handleSuggestionClick = useCallback(async (suggestion: string) => {
    if (isStreaming) return
    await ensureSession()
    sendMessage(suggestion)
  }, [isStreaming, sendMessage, ensureSession])

  const handleCopy = useCallback((content: string) => {
    navigator.clipboard.writeText(content).catch(() => {})
  }, [])

  // Drag resize handler
  const handleDragStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startRatio = splitRatio

    const handleMove = (ev: MouseEvent) => {
      if (!containerRef.current) return
      const containerWidth = containerRef.current.offsetWidth
      const dx = ev.clientX - startX
      const newRatio = Math.min(80, Math.max(30, startRatio + (dx / containerWidth) * 100))
      setSplitRatio(newRatio)
    }

    const handleUp = () => {
      document.removeEventListener("mousemove", handleMove)
      document.removeEventListener("mouseup", handleUp)
    }

    document.addEventListener("mousemove", handleMove)
    document.addEventListener("mouseup", handleUp)
  }, [splitRatio])

  const handleResizeKeyDown = useCallback((e: React.KeyboardEvent) => {
    const step = 2
    if (e.key === "ArrowLeft") {
      e.preventDefault()
      setSplitRatio((r) => Math.max(30, r - step))
    } else if (e.key === "ArrowRight") {
      e.preventDefault()
      setSplitRatio((r) => Math.min(80, r + step))
    }
  }, [])

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
              <ConversationContent>
                {turns.length === 0 && (
                  <ConversationEmptyState
                    icon={<Bot className="h-12 w-12" />}
                    title="Start a conversation"
                    description={agentName ? `Send a message to ${agentName}` : "Send a message or pick a suggestion below"}
                  />
                )}
                {turns.map((turn, idx) => (
                  <TurnRenderer
                    key={turn.id}
                    turn={turn}
                    onCopy={handleCopy}
                    onFileClick={() => {}}
                    isLastAssistant={turn.role === "assistant" && idx === turns.length - 1}
                    onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? regenerateLastTurn : undefined}
                  />
                ))}
                <StreamingIndicator isStreaming={isStreaming} turns={turns} />
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>
        {turns.length === 0 && !authError && (
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

  // Desktop: full split layout
  return (
    <div ref={containerRef} className="flex h-full">
      <div className="flex flex-col overflow-hidden" style={{ width: showPreview ? `${splitRatio}%` : "100%" }}>
        <div className="flex items-center gap-2 px-4 md:px-6 h-[41px] border-b shrink-0">
          <ConnectionBadge status={connectionStatus} />
          <span className="text-micro text-muted-foreground ml-auto font-mono">
            {sessionId.slice(0, 8)}
          </span>
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            onClick={() => setShowPreview(!showPreview)}
            title={showPreview ? "Hide panel" : "Show panel"}
          >
            {showPreview ? <PanelRightClose className="h-3.5 w-3.5" /> : <PanelRightOpen className="h-3.5 w-3.5" />}
          </Button>
        </div>
        <div className="flex-1 flex flex-col overflow-hidden min-h-0">
          {authError ? (
            <AuthErrorState />
          ) : (
            <Conversation>
              <ConversationContent>
                {turns.length === 0 && (
                  <ConversationEmptyState
                    icon={<Bot className="h-12 w-12" />}
                    title="Start a conversation"
                    description={agentName ? `Send a message to ${agentName}` : "Send a message or pick a suggestion below"}
                  />
                )}
                {turns.map((turn, idx) => (
                  <TurnRenderer
                    key={turn.id}
                    turn={turn}
                    onCopy={handleCopy}
                    onFileClick={() => {}}
                    isLastAssistant={turn.role === "assistant" && idx === turns.length - 1}
                    onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? regenerateLastTurn : undefined}
                  />
                ))}
                <StreamingIndicator isStreaming={isStreaming} turns={turns} />
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>
        {turns.length === 0 && !authError && (
          <div className="px-4 md:px-6 pb-2 shrink-0">
            <Suggestions>
              {defaultSuggestions.map((s) => (
                <Suggestion key={s} suggestion={s} onClick={() => handleSuggestionClick(s)}>{s}</Suggestion>
              ))}
            </Suggestions>
          </div>
        )}
        <div className="p-3 md:px-6 shrink-0">
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

      {showPreview && (
        <div
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize chat pane"
          tabIndex={0}
          className="w-1 bg-border hover:bg-primary/40 focus-visible:bg-primary/40 cursor-col-resize shrink-0 transition-colors outline-none"
          onMouseDown={handleDragStart}
          onKeyDown={handleResizeKeyDown}
          title="Drag to resize"
        />
      )}

      {showPreview && (
        <RightPanel
          agentId={agentId}
          workspaceId={workspaceId}
          files={files}
          style={{ width: `${100 - splitRatio}%` }}
        />
      )}
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
