"use client"

import { useCallback, useRef, useState } from "react"
import { useWebSocket, type WSStatus } from "@/hooks/use-websocket"

export type MessageRole = "user" | "assistant" | "system" | "tool"
export type StreamEventType = "text" | "tool_call" | "thinking" | "done" | "error"

export interface ChatMessage {
  id: string
  role: MessageRole
  content: string
  toolName?: string
  eventType?: StreamEventType
  timestamp: Date
  isStreaming?: boolean
}

interface UseChatOptions {
  wsUrl: string
  token: string | null
  sessionId: string
}

export function useChat({ wsUrl, token, sessionId }: UseChatOptions) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [isStreaming, setIsStreaming] = useState(false)
  const streamBufferRef = useRef("")

  const handleMessage = useCallback(
    (msg: { type: string; payload?: string; [key: string]: unknown }) => {
      if (msg.type !== "chat_event") return

      const event = msg as {
        type: string
        event_type: StreamEventType
        content?: string
        session_id?: string
      }

      if (event.session_id && event.session_id !== sessionId) return

      switch (event.event_type) {
        case "text":
          streamBufferRef.current += event.content ?? ""
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (last?.isStreaming) {
              return [
                ...prev.slice(0, -1),
                { ...last, content: streamBufferRef.current },
              ]
            }
            streamBufferRef.current = event.content ?? ""
            return [
              ...prev,
              {
                id: crypto.randomUUID(),
                role: "assistant",
                content: streamBufferRef.current,
                eventType: "text",
                timestamp: new Date(),
                isStreaming: true,
              },
            ]
          })
          break

        case "thinking":
          setMessages((prev) => [
            ...prev,
            {
              id: crypto.randomUUID(),
              role: "assistant",
              content: event.content ?? "Thinking...",
              eventType: "thinking",
              timestamp: new Date(),
            },
          ])
          break

        case "tool_call":
          setMessages((prev) => [
            ...prev,
            {
              id: crypto.randomUUID(),
              role: "tool",
              content: event.content ?? "",
              eventType: "tool_call",
              timestamp: new Date(),
            },
          ])
          break

        case "done":
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (last?.isStreaming) {
              return [...prev.slice(0, -1), { ...last, isStreaming: false }]
            }
            return prev
          })
          streamBufferRef.current = ""
          setIsStreaming(false)
          break

        case "error":
          setMessages((prev) => [
            ...prev,
            {
              id: crypto.randomUUID(),
              role: "system",
              content: event.content ?? "An error occurred",
              eventType: "error",
              timestamp: new Date(),
            },
          ])
          streamBufferRef.current = ""
          setIsStreaming(false)
          break
      }
    },
    [sessionId],
  )

  const { status, send } = useWebSocket({
    url: wsUrl,
    token,
    onMessage: handleMessage,
  })

  const sendMessage = useCallback(
    (content: string) => {
      if (!content.trim() || isStreaming) return

      const userMsg: ChatMessage = {
        id: crypto.randomUUID(),
        role: "user",
        content: content.trim(),
        timestamp: new Date(),
      }

      setMessages((prev) => [...prev, userMsg])
      setIsStreaming(true)
      streamBufferRef.current = ""

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

  return {
    messages,
    sendMessage,
    isStreaming,
    connectionStatus: status as WSStatus,
  }
}
