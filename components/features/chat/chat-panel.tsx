"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  Send,
  PanelRightOpen,
  Bot,
  User,
  Wrench,
  Brain,
  AlertCircle,
  Loader2,
  Wifi,
  WifiOff,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Textarea } from "@/components/ui/textarea"
import { useChat, type ChatMessage, type StreamEventType } from "@/hooks/use-chat"

const WS_URL = process.env.NEXT_PUBLIC_WS_URL ?? "ws://localhost:8080/ws"

interface ChatPanelProps {
  agentId: string
  sessionId: string
}

function MessageIcon({ role, eventType }: { role: string; eventType?: StreamEventType }) {
  if (role === "user") {
    return (
      <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary/10">
        <User className="h-4 w-4 text-primary" />
      </div>
    )
  }
  if (eventType === "thinking") {
    return (
      <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-amber-100 dark:bg-amber-950">
        <Brain className="h-4 w-4 text-amber-600" />
      </div>
    )
  }
  if (role === "tool" || eventType === "tool_call") {
    return (
      <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-cyan-100 dark:bg-cyan-950">
        <Wrench className="h-4 w-4 text-cyan-600" />
      </div>
    )
  }
  if (eventType === "error" || role === "system") {
    return (
      <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-destructive/10">
        <AlertCircle className="h-4 w-4 text-destructive" />
      </div>
    )
  }
  return (
    <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-emerald-100 dark:bg-emerald-950">
      <Bot className="h-4 w-4 text-emerald-600" />
    </div>
  )
}

function MessageLabel({ msg }: { msg: ChatMessage }) {
  const time = msg.timestamp.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })

  if (msg.role === "user") return <p className="text-xs text-muted-foreground">You · {time}</p>
  if (msg.eventType === "thinking") return <p className="text-xs text-muted-foreground">Agent · Thinking</p>
  if (msg.role === "tool") return <p className="text-xs text-muted-foreground">Tool Call</p>
  if (msg.eventType === "error") return <p className="text-xs text-muted-foreground">Error</p>
  return <p className="text-xs text-muted-foreground">Agent · {time}</p>
}

function MessageContent({ msg }: { msg: ChatMessage }) {
  if (msg.role === "tool" || msg.eventType === "tool_call") {
    return (
      <Card className="py-2">
        <CardContent className="p-3 font-mono text-xs whitespace-pre-wrap break-all">
          {msg.content}
        </CardContent>
      </Card>
    )
  }
  if (msg.eventType === "thinking") {
    return <p className="text-sm text-muted-foreground italic">{msg.content}</p>
  }
  return (
    <div className="text-sm whitespace-pre-wrap">
      {msg.content}
      {msg.isStreaming && <span className="inline-block w-1.5 h-4 bg-foreground/70 animate-pulse ml-0.5 align-text-bottom" />}
    </div>
  )
}

export function ChatPanel({ agentId, sessionId }: ChatPanelProps) {
  const [token, setToken] = useState<string | null>(null)
  const [input, setInput] = useState("")
  const messagesEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    fetch("/api/v1/ws-token")
      .then((r) => r.json())
      .then((data: { token?: string }) => {
        if (data.token) setToken(data.token)
      })
      .catch(() => {})
  }, [])

  const { messages, sendMessage, isStreaming, connectionStatus } = useChat({
    wsUrl: WS_URL,
    token,
    sessionId,
  })

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [messages])

  const handleSend = useCallback(() => {
    if (!input.trim() || isStreaming) return
    sendMessage(input)
    setInput("")
  }, [input, isStreaming, sendMessage])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault()
        handleSend()
      }
    },
    [handleSend],
  )

  return (
    <div className="flex flex-col h-full">
      {/* Connection status */}
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
        <div className="flex-1 overflow-y-auto p-4 sm:p-6 space-y-4">
          {messages.length === 0 && (
            <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
              <Bot className="h-12 w-12 mb-3 opacity-30" />
              <p className="text-sm">Send a message to start the conversation</p>
            </div>
          )}

          {messages.map((msg) => (
            <div key={msg.id} className="flex gap-3 max-w-2xl">
              <MessageIcon role={msg.role} eventType={msg.eventType} />
              <div className="space-y-1 min-w-0 flex-1">
                <MessageLabel msg={msg} />
                <MessageContent msg={msg} />
              </div>
            </div>
          ))}

          <div ref={messagesEndRef} />
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

      {/* Input area */}
      <div className="border-t bg-background p-4 sm:px-6">
        <div className="flex items-end gap-2 max-w-2xl">
          <Textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={`Message agent ${agentId}...`}
            className="min-h-[44px] max-h-32 resize-none"
            rows={1}
            disabled={connectionStatus !== "connected"}
          />
          <Button
            size="icon"
            className="shrink-0 h-10 w-10"
            onClick={handleSend}
            disabled={!input.trim() || isStreaming || connectionStatus !== "connected"}
          >
            {isStreaming ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Send className="h-4 w-4" />
            )}
          </Button>
        </div>
      </div>
    </div>
  )
}
