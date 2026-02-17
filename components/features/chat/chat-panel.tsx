"use client"

import { useCallback, useEffect, useState } from "react"
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
} from "lucide-react"
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
import { useChat, type ChatMessage } from "@/hooks/use-chat"
import { useOrg } from "@/hooks/use-org"

const WS_URL = process.env.NEXT_PUBLIC_WS_URL ?? "ws://localhost:8080/ws"

interface ChatPanelProps {
  agentId: string
  sessionId: string
}

const defaultSuggestions = [
  "Help me get started",
  "What can you do?",
  "Show me your skills",
  "Run a quick task",
]

export function ChatPanel({ agentId, sessionId }: ChatPanelProps) {
  const { orgId } = useOrg()
  const [token, setToken] = useState<string | null>(null)
  const [authError, setAuthError] = useState(false)
  const [input, setInput] = useState("")
  const [sessionReady, setSessionReady] = useState(false)

  useEffect(() => {
    fetch("/api/v1/ws-token")
      .then((r) => {
        if (r.status === 401) {
          setAuthError(true)
          return null
        }
        return r.json()
      })
      .then((data: { token?: string } | null) => {
        if (data?.token) setToken(data.token)
      })
      .catch(() => {})
  }, [])

  const { messages, sendMessage, loadHistory, isStreaming, connectionStatus } = useChat({
    wsUrl: WS_URL,
    token,
    sessionId,
  })

  useEffect(() => {
    fetch(`/api/v1/sessions/${sessionId}/messages`)
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

  // Best-effort session creation -- don't block message sending on it
  useEffect(() => {
    if (sessionReady || !orgId) return
    fetch(
      `/api/v1/agents/${agentId}/sessions?org_id=${encodeURIComponent(orgId)}`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ session_id: sessionId }),
      },
    )
      .then((res) => { if (res.ok) setSessionReady(true) })
      .catch(() => {})
  }, [agentId, orgId, sessionId, sessionReady])

  const handleSubmit = useCallback((message: PromptInputMessage) => {
    const text = message.text?.trim()
    if (!text || isStreaming) return
    sendMessage(text)
    setInput("")
  }, [isStreaming, sendMessage])

  const handleSuggestionClick = useCallback((suggestion: string) => {
    if (isStreaming) return
    sendMessage(suggestion)
  }, [isStreaming, sendMessage])

  const handleCopy = useCallback((content: string) => {
    navigator.clipboard.writeText(content).catch(() => {})
  }, [])

  const chatStatus = isStreaming ? "streaming" as const : "ready" as const

  return (
    <div className="flex flex-col h-full">
      {/* Connection status bar */}
      <div className="flex items-center gap-2 border-b px-4 sm:px-6 py-2 bg-muted/30">
        <div className="flex items-center gap-1.5">
          {connectionStatus === "connected" ? (
            <Wifi className="h-3 w-3 text-emerald-500" />
          ) : connectionStatus === "connecting" ? (
            <Loader2 className="h-3 w-3 text-amber-500 animate-spin" />
          ) : (
            <WifiOff className="h-3 w-3 text-muted-foreground" />
          )}
          <span className="text-xs text-muted-foreground capitalize">{connectionStatus}</span>
        </div>
        <span className="text-xs text-muted-foreground ml-auto">
          Session: <code className="text-[11px]">{sessionId.slice(0, 8)}</code>
        </span>
      </div>

      {/* Chat area */}
      <div className="flex-1 flex overflow-hidden">
        <div className="flex-1 overflow-hidden">
          {authError ? (
            <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
              <AlertCircle className="h-12 w-12 mb-3 opacity-30" />
              <p className="text-sm">Session expired. Redirecting to login...</p>
            </div>
          ) : (
            <Conversation>
              <ConversationContent>
                {messages.length === 0 && (
                  <ConversationEmptyState
                    icon={<Bot className="h-12 w-12" />}
                    title="Start a conversation"
                    description="Send a message or pick a suggestion below"
                  />
                )}

                {messages.map((msg) => (
                  <MessageBubble
                    key={msg.id}
                    msg={msg}
                    onCopy={handleCopy}
                  />
                ))}
              </ConversationContent>
              <ConversationScrollButton />
            </Conversation>
          )}
        </div>

        {/* File preview panel */}
        <div className="hidden lg:flex w-80 border-l flex-col">
          <div className="flex items-center justify-between px-4 py-2 border-b">
            <span className="text-xs font-medium">File Preview</span>
            <PanelRightOpen className="h-4 w-4 text-muted-foreground" />
          </div>
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground p-4">
            Select a file to preview
          </div>
        </div>
      </div>

      {/* Suggestions (show only when no messages) */}
      {messages.length === 0 && !authError && (
        <div className="px-4 sm:px-6 pb-2">
          <Suggestions>
            {defaultSuggestions.map((s) => (
              <Suggestion
                key={s}
                suggestion={s}
                onClick={() => handleSuggestionClick(s)}
              >
                {s}
              </Suggestion>
            ))}
          </Suggestions>
        </div>
      )}

      {/* Input area */}
      <div className="border-t bg-background p-4 sm:px-6">
        <div className="max-w-2xl">
          <PromptInput
            className="rounded-xl border"
            onSubmit={handleSubmit}
          >
            <PromptInputTextarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={`Message agent...`}
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
    </div>
  )
}

function MessageBubble({ msg, onCopy }: { msg: ChatMessage; onCopy: (content: string) => void }) {
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

  if (msg.role === "tool" || msg.eventType === "tool_call") {
    return (
      <Message from="assistant">
        <Tool defaultOpen={false}>
          <ToolHeader
            title={msg.toolName ?? "Tool Call"}
            type="tool-invocation"
            state="output-available"
          />
          <ToolContent>
            <pre className="text-xs whitespace-pre-wrap break-all">{msg.content}</pre>
          </ToolContent>
        </Tool>
      </Message>
    )
  }

  if (msg.eventType === "error" || msg.role === "system") {
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

  return (
    <Message from={msg.role === "user" ? "user" : "assistant"}>
      <MessageContent>
        {msg.role === "user" ? (
          <span>{msg.content}</span>
        ) : (
          <MessageResponse>
            {msg.isStreaming ? msg.content + " " : msg.content}
          </MessageResponse>
        )}
      </MessageContent>
      {msg.role === "assistant" && !msg.isStreaming && msg.content && (
        <MessageActions>
          <MessageAction
            tooltip="Copy"
            onClick={() => onCopy(msg.content)}
          >
            <Copy className="h-3.5 w-3.5" />
          </MessageAction>
          <MessageAction tooltip="Good response">
            <ThumbsUp className="h-3.5 w-3.5" />
          </MessageAction>
          <MessageAction tooltip="Bad response">
            <ThumbsDown className="h-3.5 w-3.5" />
          </MessageAction>
        </MessageActions>
      )}
    </Message>
  )
}
