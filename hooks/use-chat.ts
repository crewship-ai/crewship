"use client"

import { useCallback, useRef, useState } from "react"
import { useWebSocket, type WSStatus } from "@/hooks/use-websocket"

export type MessageRole = "user" | "assistant" | "system" | "tool"
export type StreamEventType = "text" | "tool_call" | "tool_result" | "thinking" | "done" | "error"
export type AssignmentEventType = "assignment_created" | "assignment_running" | "assignment_completed" | "assignment_failed"

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
    (msg: { type: string; payload?: string | Record<string, unknown>; channel?: string; [key: string]: unknown }) => {
      // Handle assignment lifecycle events (broadcast directly to session channel)
      const assignmentTypes: AssignmentEventType[] = ["assignment_created", "assignment_running", "assignment_completed", "assignment_failed"]
      if (assignmentTypes.includes(msg.type as AssignmentEventType)) {
        const channelSessionId = msg.channel?.startsWith("session:") ? msg.channel.slice(8) : undefined
        if (channelSessionId && channelSessionId !== sessionId) return
        const payload = (typeof msg.payload === "object" && msg.payload !== null)
          ? msg.payload as Record<string, unknown>
          : {}
        let content = ""
        switch (msg.type as AssignmentEventType) {
          case "assignment_created":
            content = `[Assignment] Assigning task to @${payload.target}: ${payload.task}`
            break
          case "assignment_running":
            content = `[Assignment] @${payload.target} is working on the task...`
            break
          case "assignment_completed":
            content = `[Assignment] @${payload.target} completed the task.`
            if (payload.result) content += `\nResult: ${payload.result}`
            break
          case "assignment_failed":
            content = `[Assignment] @${payload.target} failed: ${payload.error}`
            break
        }
        setMessages((prev) => [
          ...prev,
          {
            id: crypto.randomUUID(),
            role: "system" as MessageRole,
            content,
            eventType: undefined,
            timestamp: new Date(),
          },
        ])
        return
      }

      if (msg.type !== "chat_event") return

      // Server sends: { type: "chat_event", channel: "session:xxx", payload: { type, content } }
      const payload = (typeof msg.payload === "object" && msg.payload !== null)
        ? msg.payload as Record<string, unknown>
        : {}

      const eventType = (payload.type as StreamEventType) ?? undefined
      const content = (payload.content as string) ?? ""

      // Filter by session from channel (format: "session:{id}")
      const channelSessionId = msg.channel?.startsWith("session:") ? msg.channel.slice(8) : undefined
      if (channelSessionId && channelSessionId !== sessionId) return

      switch (eventType) {
        case "text":
          streamBufferRef.current += content
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (last?.isStreaming) {
              return [
                ...prev.slice(0, -1),
                { ...last, content: streamBufferRef.current },
              ]
            }
            streamBufferRef.current = content
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
              content: content || "Thinking...",
              eventType: "thinking",
              timestamp: new Date(),
            },
          ])
          break

        case "tool_call":
        case "tool_result":
          setMessages((prev) => [
            ...prev,
            {
              id: crypto.randomUUID(),
              role: "tool",
              content,
              eventType,
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
              content: content || "An error occurred",
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

  const loadHistory = useCallback((history: ChatMessage[]) => {
    setMessages(history)
    setIsStreaming(false)
    streamBufferRef.current = ""
  }, [])

  return {
    messages,
    sendMessage,
    loadHistory,
    isStreaming,
    connectionStatus: status as WSStatus,
  }
}
