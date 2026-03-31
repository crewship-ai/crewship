"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useWebSocket, type WSStatus } from "@/hooks/use-websocket"

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

export type TurnPartType = "text" | "thinking" | "tool_call" | "tool_result" | "status" | "error" | "result" | "system_init" | "image"

export interface TurnPart {
  id: string
  type: TurnPartType
  content: string
  isStreaming?: boolean
  metadata?: Record<string, unknown>
  timestamp: Date
}

export interface ChatTurn {
  id: string
  role: "user" | "assistant" | "system"
  parts: TurnPart[]
  isStreaming: boolean
  timestamp: Date
}

// --- Legacy types (kept for history loading compatibility) ---

export type MessageRole = "user" | "assistant" | "system" | "tool"
export type StreamEventType = "text" | "tool_call" | "tool_result" | "thinking" | "status" | "done" | "error" | "system" | "result" | "image"
export type AssignmentEventType = "assignment_created" | "assignment_running" | "assignment_completed" | "assignment_failed"

export interface ChatMessage {
  id: string
  role: MessageRole
  content: string
  toolName?: string
  eventType?: StreamEventType
  timestamp: Date
  isStreaming?: boolean
  metadata?: Record<string, unknown>
}

interface UseChatOptions {
  wsUrl: string
  token: string | null
  sessionId: string
}

/** Convert flat ChatMessage history into turns for display */
function messagesToTurns(messages: ChatMessage[]): ChatTurn[] {
  const turns: ChatTurn[] = []
  for (const msg of messages) {
    if (msg.role === "user") {
      turns.push({
        id: msg.id,
        role: "user",
        parts: [{ id: msg.id, type: "text", content: msg.content, timestamp: msg.timestamp }],
        isStreaming: false,
        timestamp: msg.timestamp,
      })
    } else if (msg.role === "system") {
      turns.push({
        id: msg.id,
        role: "system",
        parts: [{ id: msg.id, type: msg.eventType === "error" ? "error" : "text", content: msg.content, timestamp: msg.timestamp }],
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

export function useChat({ wsUrl, token, sessionId }: UseChatOptions) {
  const [turns, setTurns] = useState<ChatTurn[]>([])
  const [isStreaming, setIsStreaming] = useState(false)
  const textBufferRef = useRef("")
  const thinkingBufferRef = useRef("")

  // Reset state when session changes
  useEffect(() => {
    setTurns([])
    setIsStreaming(false)
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
  }, [sessionId])

  const handleMessage = useCallback(
    (msg: { type: string; payload?: string | Record<string, unknown>; channel?: string; [key: string]: unknown }) => {
      // Handle assignment lifecycle events
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
        return
      }

      if (msg.type !== "chat_event") return

      const payload = (typeof msg.payload === "object" && msg.payload !== null)
        ? msg.payload as Record<string, unknown>
        : {}

      const eventType = (payload.type as StreamEventType) ?? undefined
      const content = (payload.content as string) ?? ""
      const metadata = (payload.metadata as Record<string, unknown>) ?? undefined

      // Filter by session
      const channelSessionId = msg.channel?.startsWith("session:") ? msg.channel.slice(8) : undefined
      if (channelSessionId && channelSessionId !== sessionId) return

      switch (eventType) {
        case "status":
          setTurns((prev) => {
            const last = prev[prev.length - 1]
            if (last?.role === "assistant" && last.isStreaming) {
              // Add status part to current assistant turn
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
          break

        case "thinking": {
          // Streaming deltas (thinking_delta from backend) accumulate into one part.
          // Complete thinking blocks create separate parts.
          const isStreamingDelta = metadata?.streaming === true
          if (isStreamingDelta) {
            thinkingBufferRef.current += content
          }
          setTurns((prev) => {
            const last = prev[prev.length - 1]
            if (last?.role === "assistant" && last.isStreaming) {
              if (isStreamingDelta) {
                // Find existing streaming thinking part to accumulate into
                const thinkingIdx = last.parts.findLastIndex(
                  (p) => p.type === "thinking" && p.isStreaming
                )
                if (thinkingIdx >= 0) {
                  const updatedParts = [...last.parts]
                  updatedParts[thinkingIdx] = {
                    ...updatedParts[thinkingIdx],
                    content: thinkingBufferRef.current,
                  }
                  return [...prev.slice(0, -1), { ...last, parts: updatedParts }]
                }
                // First thinking delta — remove status parts, create new streaming thinking part
                thinkingBufferRef.current = content
                const cleanedParts = last.parts.filter((p) => p.type !== "status")
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
              // Complete thinking block — remove status parts, create a new non-streaming part
              const cleanedParts = last.parts.filter((p) => p.type !== "status")
              return [
                ...prev.slice(0, -1),
                {
                  ...last,
                  parts: [
                    ...cleanedParts,
                    { id: uuid(), type: "thinking" as TurnPartType, content, isStreaming: false, timestamp: new Date() },
                  ],
                },
              ]
            }
            // Create new assistant turn — remove any orphaned status-only turns
            if (isStreamingDelta) {
              thinkingBufferRef.current = content
            }
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
                parts: [{ id: uuid(), type: "thinking" as TurnPartType, content, isStreaming: !isStreamingDelta ? false : true, timestamp: new Date() }],
                isStreaming: true,
                timestamp: new Date(),
              },
            ]
          })
          break
        }

        case "text":
          textBufferRef.current += content
          setTurns((prev) => {
            const last = prev[prev.length - 1]
            if (last?.role === "assistant" && last.isStreaming) {
              // Find existing streaming text part
              const textIdx = last.parts.findLastIndex(
                (p) => p.type === "text" && p.isStreaming
              )
              if (textIdx >= 0) {
                const updatedParts = [...last.parts]
                updatedParts[textIdx] = {
                  ...updatedParts[textIdx],
                  content: textBufferRef.current,
                }
                return [...prev.slice(0, -1), { ...last, parts: updatedParts }]
              }
              // First text arriving: remove status parts + close streaming thinking
              const cleanedParts = last.parts
                .filter((p) => p.type !== "status")
                .map((p) =>
                  p.type === "thinking" && p.isStreaming ? { ...p, isStreaming: false } : p
                )
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
          break

        case "tool_call":
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
          break

        case "tool_result":
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
              // Try to mark matching tool_call as completed via tool_use_id
              const toolUseId = metadata?.tool_use_id as string | undefined
              const updatedParts = toolUseId
                ? last.parts.map((p) => {
                    if (p.type === "tool_call" && p.metadata?.tool_id === toolUseId) {
                      return { ...p, metadata: { ...p.metadata, completed: true } }
                    }
                    return p
                  })
                : last.parts
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
          break

        case "image":
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
          break

        case "result":
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
          break

        case "system": {
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
          } else {
            // Other system events (sidecar security logs, etc.) — add as status-like parts
            setTurns((prev) => {
              const last = prev[prev.length - 1]
              if (last?.role === "assistant" && last.isStreaming) {
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
              return prev
            })
          }
          break
        }

        case "done":
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
              const finalParts = last.parts
                .map((p) => (p.isStreaming ? { ...p, isStreaming: false } : p))
                // Remove status parts once done (they were just progress indicators)
                .filter((p) => p.type !== "status")
              return [
                ...cleaned.slice(0, -1),
                { ...last, parts: finalParts, isStreaming: false },
              ]
            }
            return cleaned
          })
          textBufferRef.current = ""
          thinkingBufferRef.current = ""
          setIsStreaming(false)
          break

        case "error":
          setTurns((prev) => {
            const last = prev[prev.length - 1]
            const errorPart: TurnPart = {
              id: uuid(),
              type: "error",
              content: content || "An error occurred",
              timestamp: new Date(),
            }
            if (last?.role === "assistant" && last.isStreaming) {
              return [
                ...prev.slice(0, -1),
                { ...last, parts: [...last.parts, errorPart], isStreaming: false },
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
  }, [])

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
    isStreaming,
    connectionStatus: status as WSStatus,
  }
}
