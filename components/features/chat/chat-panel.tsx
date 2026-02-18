"use client"

import { useCallback, useEffect, useState, useRef } from "react"
import {
  Bot,
  AlertCircle,
  Wifi,
  WifiOff,
  Loader2,
  Copy,
  ThumbsUp,
  ThumbsDown,
  PanelRightOpen,
  PanelRightClose,
  FileText,
  AlertTriangle,
  Crown,
  CheckCircle2,
  Clock,
  Download,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
  ConversationEmptyState,
} from "@/components/ai-elements/conversation"
import {
  Message,
  MessageContent,
  MessageResponse,
  MessageActions,
  MessageAction,
} from "@/components/ai-elements/message"
import {
  PromptInput,
  PromptInputTextarea,
  PromptInputFooter,
  PromptInputSubmit,
  type PromptInputMessage,
} from "@/components/ai-elements/prompt-input"
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from "@/components/ai-elements/reasoning"
import { Tool, ToolContent, ToolHeader } from "@/components/ai-elements/tool"
import { Suggestion, Suggestions } from "@/components/ai-elements/suggestion"
import {
  Artifact,
  ArtifactHeader,
  ArtifactTitle,
  ArtifactContent,
  ArtifactClose,
} from "@/components/ai-elements/artifact"
import { CodeBlock } from "@/components/ai-elements/code-block"
import {
  FileTree,
  FileTreeFolder,
  FileTreeFile,
} from "@/components/ai-elements/file-tree"
import { useChat, type ChatMessage } from "@/hooks/use-chat"
import { useWorkspace } from "@/hooks/use-workspace"
import type { BundledLanguage } from "shiki"

function getWsUrl(): string {
  if (process.env.NEXT_PUBLIC_WS_URL) return process.env.NEXT_PUBLIC_WS_URL
  if (typeof window === "undefined") return "ws://localhost:8080/ws"
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${window.location.host}/ws`
}

interface ChatPanelProps {
  agentId: string
  sessionId: string
  agentName?: string
}

interface FileEntry {
  path: string
  name: string
  size: number
  is_dir: boolean
  mod_time: string
}

const defaultSuggestions = [
  "Help me get started",
  "What can you do?",
  "Show me your skills",
  "Run a quick task",
]

function formatFileSize(bytes: number): string {
  if (bytes === 0) return "0 B"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const value = bytes / Math.pow(1024, i)
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[i]}`
}

function getLanguage(name: string): string {
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const map: Record<string, string> = {
    ts: "typescript", tsx: "tsx", js: "javascript", jsx: "jsx",
    py: "python", go: "go", rs: "rust", sh: "bash",
    json: "json", yaml: "yaml", yml: "yaml", xml: "xml",
    html: "html", css: "css", md: "markdown", txt: "text",
    sql: "sql", toml: "toml",
  }
  return map[ext] ?? "text"
}

function formatTimestamp(date: Date): string {
  return date.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

/** Chat panel with split view: conversation on the left, file preview on the right. */
export function ChatPanel({ agentId, sessionId, agentName }: ChatPanelProps) {
  const { workspaceId } = useWorkspace()
  const [token, setToken] = useState<string | null>(null)
  const [authError, setAuthError] = useState(false)
  const [input, setInput] = useState("")
  const [sessionReady, setSessionReady] = useState(false)
  const [showPreview, setShowPreview] = useState(true)
  const [previewFile, setPreviewFile] = useState<string | null>(null)
  const [previewContent, setPreviewContent] = useState<string | null>(null)
  const [loadingPreview, setLoadingPreview] = useState(false)
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

  const { messages, sendMessage, loadHistory, isStreaming, connectionStatus } = useChat({
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

  const handleFileSelect = useCallback((path: string) => {
    const file = (files ?? []).find((f) => f.path === path)
    if (!file || file.is_dir) return
    setPreviewFile(path)
    setLoadingPreview(true)
    setPreviewContent(null)
    fetch(`/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(path)}`)
      .then((res) => res.ok ? res.text() : "(Unable to load)")
      .then((text) => setPreviewContent(text))
      .catch(() => setPreviewContent("(Network error)"))
      .finally(() => setLoadingPreview(false))
  }, [agentId, workspaceId, files])

  const handleFileClick = useCallback((fileRef: string) => {
    const file = (files ?? []).find((f) =>
      f.name === fileRef || f.path === fileRef || f.path.endsWith(`/${fileRef}`),
    )
    if (file) {
      setShowPreview(true)
      handleFileSelect(file.path)
    }
  }, [files, handleFileSelect])

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
  const selectedFileEntry = (files ?? []).find((f) => f.path === previewFile)

  return (
    <div ref={containerRef} className="flex h-full">
      {/* LEFT: Chat area */}
      <div className="flex flex-col overflow-hidden" style={{ width: showPreview ? `${splitRatio}%` : "100%" }}>
        {/* Connection status */}
        <div className="flex items-center gap-2 border-b px-4 md:px-6 py-1.5 bg-muted/20 shrink-0">
          <div className="flex items-center gap-1.5">
            {connectionStatus === "connected" ? (
              <Wifi className="h-3 w-3 text-emerald-500" />
            ) : connectionStatus === "connecting" ? (
              <Loader2 className="h-3 w-3 text-amber-500 animate-spin" />
            ) : (
              <WifiOff className="h-3 w-3 text-muted-foreground" />
            )}
            <span className="text-[11px] text-muted-foreground capitalize">{connectionStatus}</span>
          </div>
          <span className="text-[11px] text-muted-foreground ml-auto">
            Session: <code className="text-[10px]">{sessionId.slice(0, 8)}</code>
          </span>
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6 ml-2"
            onClick={() => setShowPreview(!showPreview)}
            title={showPreview ? "Hide file preview" : "Show file preview"}
          >
            {showPreview ? <PanelRightClose className="h-3.5 w-3.5" /> : <PanelRightOpen className="h-3.5 w-3.5" />}
          </Button>
        </div>

        {/* Messages */}
        <div className="flex-1 overflow-hidden">
          {authError ? (
            <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
              <AlertCircle className="h-12 w-12 mb-3 opacity-30" />
              <p className="text-sm">Session expired. Please log in again.</p>
            </div>
          ) : (
            <Conversation>
              <ConversationContent>
                {messages.length === 0 && (
                  <ConversationEmptyState
                    icon={<Bot className="h-12 w-12" />}
                    title="Start a conversation"
                    description={agentName ? `Send a message to ${agentName}` : "Send a message or pick a suggestion below"}
                  />
                )}
                {messages.map((msg) => (
                  <MessageBubble
                    key={msg.id}
                    msg={msg}
                    onCopy={handleCopy}
                    onFileClick={handleFileClick}
                  />
                ))}
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>

        {/* Suggestions */}
        {messages.length === 0 && !authError && (
          <div className="px-4 md:px-6 pb-2 shrink-0">
            <Suggestions>
              {defaultSuggestions.map((s) => (
                <Suggestion key={s} suggestion={s} onClick={() => handleSuggestionClick(s)}>{s}</Suggestion>
              ))}
            </Suggestions>
          </div>
        )}

        {/* Input */}
        <div className="border-t bg-background p-3 md:px-6 shrink-0">
          <PromptInput className="rounded-xl border" onSubmit={handleSubmit}>
            <PromptInputTextarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={agentName ? `Message ${agentName}...` : "Send a message..."}
              className="min-h-[44px]"
            />
            <PromptInputFooter className="justify-end p-2">
              <PromptInputSubmit
                disabled={!input.trim() || connectionStatus !== "connected"}
                status={chatStatus}
              />
            </PromptInputFooter>
          </PromptInput>
        </div>
      </div>

      {/* DRAG HANDLE */}
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

      {/* RIGHT: File Preview */}
      {showPreview && (
        <div className="flex flex-col bg-background overflow-hidden" style={{ width: `${100 - splitRatio}%` }}>
          {/* File tree header */}
          <div className="px-4 py-2.5 border-b flex items-center justify-between shrink-0">
            <div className="flex items-center gap-2">
              <FileText className="h-4 w-4 text-muted-foreground" />
              <span className="text-sm font-medium">Files</span>
              <span className="text-xs text-muted-foreground">/output/</span>
            </div>
            <div className="flex items-center gap-1">
              {selectedFileEntry && (
                <Button variant="ghost" size="sm" className="h-6 text-xs gap-1" asChild>
                  <a href={`/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(selectedFileEntry.path)}`} download={selectedFileEntry.name}>
                    <Download className="h-3 w-3" /> Download
                  </a>
                </Button>
              )}
            </div>
          </div>

          {/* File tree */}
          {files.length > 0 && (
            <div className="border-b px-3 py-2 bg-muted/30 max-h-40 overflow-y-auto shrink-0">
              <FileTree selectedPath={previewFile ?? undefined} onSelect={handleFileSelect}>
                {files.filter((f) => f.is_dir).map((d) => (
                  <FileTreeFolder key={d.path} name={d.name} path={d.path} />
                ))}
                {files.filter((f) => !f.is_dir).map((f) => (
                  <FileTreeFile key={f.path} name={f.name} path={f.path} />
                ))}
              </FileTree>
            </div>
          )}

          {/* Preview content */}
          <div className="flex-1 overflow-y-auto">
            {selectedFileEntry && !selectedFileEntry.is_dir ? (
              <Artifact>
                <ArtifactHeader>
                  <ArtifactTitle>{selectedFileEntry.name}</ArtifactTitle>
                  <div className="flex items-center gap-2">
                    <Badge variant="outline" className="text-[10px]">
                      {formatFileSize(selectedFileEntry.size)}
                    </Badge>
                    <ArtifactClose onClick={() => setPreviewFile(null)} />
                  </div>
                </ArtifactHeader>
                <ArtifactContent>
                  {loadingPreview ? (
                    <div className="flex items-center justify-center py-12">
                      <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                    </div>
                  ) : previewContent !== null ? (
                    <CodeBlock
                      code={previewContent}
                      language={getLanguage(selectedFileEntry.name) as BundledLanguage}
                      showLineNumbers
                    />
                  ) : null}
                </ArtifactContent>
              </Artifact>
            ) : (
              <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
                {files.length === 0 ? "No files yet" : "Select a file to preview"}
              </div>
            )}
          </div>

          {/* File count footer */}
          {files.length > 0 && (
            <div className="px-4 py-1.5 border-t text-[11px] text-muted-foreground shrink-0">
              {files.filter((f) => !f.is_dir).length} file{files.filter((f) => !f.is_dir).length !== 1 ? "s" : ""}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

interface MessageBubbleProps {
  msg: ChatMessage
  onCopy: (content: string) => void
  onFileClick: (fileName: string) => void
}

function MessageBubble({ msg, onCopy, onFileClick }: MessageBubbleProps) {
  // Thinking block
  if (msg.eventType === "thinking") {
    return (
      <Message from="assistant">
        <Reasoning isStreaming={msg.isStreaming} duration={0}>
          <ReasoningTrigger />
          <ReasoningContent>{msg.content}</ReasoningContent>
        </Reasoning>
      </Message>
    )
  }

  // Tool call / result
  if (msg.role === "tool" || msg.eventType === "tool_call" || msg.eventType === "tool_result") {
    const isResult = msg.eventType === "tool_result"
    return (
      <Message from="assistant">
        <Tool defaultOpen={false}>
          <ToolHeader
            title={msg.toolName ?? (isResult ? "Tool Result" : "Tool Call")}
            type="tool-invocation"
            state={isResult ? "output-available" : "input-streaming"}
          />
          <ToolContent>
            <pre className="text-xs whitespace-pre-wrap break-all">{msg.content}</pre>
          </ToolContent>
        </Tool>
      </Message>
    )
  }

  // Error / system
  if (msg.eventType === "error" || msg.role === "system") {
    // Rate limit warning
    if (msg.content.toLowerCase().includes("rate limit") || msg.content.toLowerCase().includes("429")) {
      return (
        <div className="flex items-center gap-2 px-3 py-2 bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-800 rounded-lg text-xs text-amber-700 dark:text-amber-400 max-w-md">
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <span>{msg.content}</span>
        </div>
      )
    }
    return (
      <Message from="assistant">
        <MessageContent className="border-destructive/50 bg-destructive/5 rounded-lg px-4 py-3">
          <div className="flex items-center gap-2 text-destructive text-sm">
            <AlertCircle className="h-4 w-4 shrink-0" />
            {msg.content}
          </div>
        </MessageContent>
      </Message>
    )
  }

  // Delegation block detection
  if (msg.role === "assistant" && msg.content.startsWith("[DELEGATED")) {
    return <DelegationBlock content={msg.content} />
  }

  // File creation notification detection
  if (msg.role === "assistant" && /file (created|written|saved)/i.test(msg.content)) {
    const fileMatch = msg.content.match(/[`"]?([a-zA-Z0-9_\-/.]+\.[a-zA-Z0-9]+)[`"]?/)
    if (fileMatch) {
      return (
        <Message from="assistant">
          <MessageContent>
            <MessageResponse>{msg.isStreaming ? msg.content + " " : msg.content}</MessageResponse>
          </MessageContent>
          <button
            onClick={() => onFileClick(fileMatch[1])}
            className="flex items-center gap-2 px-3 py-2 bg-primary/5 border border-primary/20 rounded-lg text-xs text-primary hover:bg-primary/10 max-w-md transition-colors"
          >
            <FileText className="h-4 w-4" />
            <span>File created: <span className="font-mono font-medium">{fileMatch[1]}</span></span>
            <span className="ml-auto font-medium">Preview &rarr;</span>
          </button>
        </Message>
      )
    }
  }

  // User message
  if (msg.role === "user") {
    return (
      <Message from="user">
        <MessageContent>
          <div className="flex items-start gap-2">
            <span>{msg.content}</span>
          </div>
        </MessageContent>
        <div className="text-[10px] text-muted-foreground ml-auto opacity-0 group-hover:opacity-100 transition-opacity">
          {formatTimestamp(msg.timestamp)}
        </div>
      </Message>
    )
  }

  // Assistant message with markdown
  return (
    <Message from="assistant">
      <MessageContent>
        <MessageResponse>
          {msg.isStreaming ? msg.content + " " : msg.content}
        </MessageResponse>
      </MessageContent>
      {!msg.isStreaming && msg.content && (
        <>
          <MessageActions>
            <MessageAction tooltip="Copy" onClick={() => onCopy(msg.content)}>
              <Copy className="h-3.5 w-3.5" />
            </MessageAction>
            <MessageAction tooltip="Good response">
              <ThumbsUp className="h-3.5 w-3.5" />
            </MessageAction>
            <MessageAction tooltip="Bad response">
              <ThumbsDown className="h-3.5 w-3.5" />
            </MessageAction>
          </MessageActions>
          <div className="text-[10px] text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity">
            {formatTimestamp(msg.timestamp)}
          </div>
        </>
      )}
    </Message>
  )
}

interface DelegationBlockProps {
  content: string
}

function DelegationBlock({ content }: DelegationBlockProps) {
  const targetMatch = content.match(/to\s+([^]]+?)(?:\s*\(|$)/)
  const taskMatch = content.match(/"([^"]+)"/)
  const completedMatch = content.match(/Completed in (.+)/)
  const isCompleted = !!completedMatch

  return (
    <div className="max-w-xl ml-8">
      <div className="bg-primary/5 border border-primary/20 border-l-4 border-l-[#4ECDC4] rounded-lg overflow-hidden">
        <div className="px-4 py-3">
          <div className="flex items-center gap-2 text-xs">
            <Crown className="h-3.5 w-3.5 text-amber-500" />
            <span className="font-medium text-muted-foreground">Delegated to</span>
            <span className="font-semibold">{targetMatch?.[1] ?? "Agent"}</span>
          </div>
          {taskMatch && (
            <div className="mt-1.5 text-xs text-muted-foreground bg-background rounded px-2.5 py-1.5 border">
              {taskMatch[1]}
            </div>
          )}
          <div className="mt-2 flex items-center gap-2">
            {isCompleted ? (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-400">
                <CheckCircle2 className="h-3 w-3" />
                Completed in {completedMatch[1]}
              </span>
            ) : (
              <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400">
                <Clock className="h-3 w-3 animate-spin" />
                In progress...
              </span>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
