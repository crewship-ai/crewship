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

/** Discriminator for the content type of a turn part (text, tool call, thinking, etc.). */
export type TurnPartType =
  | "text"
  | "thinking"
  | "tool_call"
  | "tool_result"
  | "status"
  | "error"
  | "result"
  | "system_init"
  | "image"
  | "crew_provisioning"

/** A single content block within a chat turn (e.g. a text fragment, tool call, or thinking block). */
export interface TurnPart {
  id: string
  type: TurnPartType
  content: string
  isStreaming?: boolean
  metadata?: Record<string, unknown>
  timestamp: Date
}

/** A complete turn in the conversation (user message or multi-part assistant response). */
export interface ChatTurn {
  id: string
  role: "user" | "assistant" | "system"
  parts: TurnPart[]
  isStreaming: boolean
  timestamp: Date
  /** Per-turn metadata. Currently only `trace_id` is consumed (by the
   *  feedback store to link signals back to the OTel trace that
   *  produced the message). Backend wiring is not yet shipped — see
   *  the open follow-up in the feedback guide — so this field is
   *  populated only when the WebSocket event carries it, and is
   *  always optional for downstream readers. */
  metadata?: { trace_id?: string }
}

// --- Legacy types (kept for history loading compatibility) ---

/** @deprecated Legacy message role; use ChatTurn for new code. */
export type MessageRole = "user" | "assistant" | "system" | "tool"

/** @deprecated Legacy stream event type; use TurnPartType for new code. */
export type StreamEventType =
  | "text"
  | "tool_call"
  | "tool_result"
  | "thinking"
  | "status"
  | "done"
  | "error"
  | "system"
  | "result"
  | "image"
  | "crew_provisioning"

/** WebSocket event types for agent-to-agent task assignment lifecycle. */
export type AssignmentEventType = "assignment_created" | "assignment_running" | "assignment_completed" | "assignment_failed"

/** Safely render an assignment-event payload field as a string. The
 *  backend has historically sent both `target: "viktor"` and
 *  `target: { slug: "viktor" }`, and naive template-literal interpolation
 *  of the latter renders "[object Object]" in the chat. Prefer a
 *  human-shaped field, fall back to empty.
 *
 *  We intentionally do NOT serialize the whole object: the result lands in
 *  user-visible chat messages, and a backend payload may carry tokens,
 *  emails, or other PII. If none of slug/name/id is present the safer
 *  move is to render nothing rather than dump the object.
 *  Exported for unit tests.
 */
export function assignmentField(v: unknown): string {
  if (v == null) return ""
  if (typeof v === "string") return v
  if (typeof v === "number" || typeof v === "boolean") return String(v)
  if (typeof v === "object") {
    const obj = v as Record<string, unknown>
    if (typeof obj.slug === "string") return obj.slug
    if (typeof obj.name === "string") return obj.name
    if (typeof obj.id === "string") return obj.id
    return ""
  }
  return String(v)
}

/** @deprecated Legacy flat chat message; use ChatTurn/TurnPart for new code. Kept for history loading compatibility. */
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
  /** Async callback that fetches the current WS ticket. Replaces the
   *  previous `token: string | null` pre-fetched once at mount; the
   *  hook now re-fetches on every (re)connect so a stale ticket from
   *  before a backend restart can't trap the connection in an infinite
   *  retry loop. */
  getToken: () => Promise<string | null>
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

/**
 * Full-featured chat hook that manages a WebSocket-based conversation with an agent.
 * Handles streaming text/thinking/tool events, turn grouping, history loading,
 * message editing, regeneration, and stop/cancel.
 */
export function useChat({ wsUrl, getToken, sessionId }: UseChatOptions) {
  const [turns, setTurns] = useState<ChatTurn[]>([])
  const [isStreaming, setIsStreaming] = useState(false)
  const textBufferRef = useRef("")
  const thinkingBufferRef = useRef("")
  // True between stopGeneration() and the next sendMessage/regenerate/edit.
  // Used to drop chat_event deltas that arrive after a local cancel — the
  // server's cancel ack races against in-flight packets, and without this
  // gate the late deltas re-create the cancelled assistant turn and the
  // typing indicator reappears. Only blocks AFTER an explicit cancel so
  // unsolicited stream events (multi-tab observation, history replay)
  // still flow through.
  const cancelledRef = useRef(false)

  // Reset stream-side state when session changes. We deliberately do NOT
  // reset turns here — that would cause a blank-canvas flash between the
  // old session and the new one's history fetch. The chat-panel calls
  // loadHistory() once the new session's messages arrive, which performs
  // an atomic replace (including the empty-array case for fresh sessions).
  useEffect(() => {
    setIsStreaming(false)
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    cancelledRef.current = false
  }, [sessionId])

  const handleAssignmentEvent = useCallback(
    (type: AssignmentEventType, payload: Record<string, unknown>) => {
      let content = ""
      const target = assignmentField(payload.target)
      const task = assignmentField(payload.task)
      const result = assignmentField(payload.result)
      const errMsg = assignmentField(payload.error)
      switch (type) {
        case "assignment_created":
          content = `[Assignment] Assigning task to @${target}: ${task}`
          break
        case "assignment_running":
          content = `[Assignment] @${target} is working on the task...`
          break
        case "assignment_completed":
          content = `[Assignment] @${target} completed the task.`
          if (result) content += `\nResult: ${result}`
          break
        case "assignment_failed":
          content = `[Assignment] @${target} failed: ${errMsg}`
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
    },
    [],
  )

  const handleStatusEvent = useCallback((content: string) => {
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
  }, [])

  const handleThinkingEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
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
    },
    [],
  )

  const handleTextEvent = useCallback((content: string) => {
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
  }, [])

  const handleToolCallEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
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
    },
    [],
  )

  const handleToolResultEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
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
    },
    [],
  )

  const handleImageEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
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
    },
    [],
  )

  const handleResultEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
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
    },
    [],
  )

  const handleSystemEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
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
    },
    [],
  )

  const handleDoneEvent = useCallback((metadata?: Record<string, unknown>) => {
    // The "done" WS event may carry { trace_id } in metadata — the
    // backend stamps the active OTel trace id there so the assistant
    // turn ties back to the routine run that produced it. Lifted onto
    // ChatTurn.metadata.trace_id so feedback POSTs from this turn can
    // include the trace id for eval-mining correlation.
    const traceID = metadata && typeof metadata.trace_id === "string" ? (metadata.trace_id as string) : undefined
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
        const finalTurn: ChatTurn = {
          ...last,
          parts: finalParts,
          isStreaming: false,
        }
        if (traceID) {
          finalTurn.metadata = { ...(last.metadata ?? {}), trace_id: traceID }
        }
        return [...cleaned.slice(0, -1), finalTurn]
      }
      return cleaned
    })
    textBufferRef.current = ""
    thinkingBufferRef.current = ""
    setIsStreaming(false)
  }, [])

  const handleCrewProvisioningEvent = useCallback(
    (content: string, metadata: Record<string, unknown> | undefined) => {
      // Auto-provision kicked off by chatbridge — render a system turn
      // carrying the crew_id so the chat surface can render the same
      // build progress card the toolbar popover shows. Replaces the
      // legacy red "Run `crewship crew provision …` first" error.
      setTurns((prev) => [
        ...prev,
        {
          id: uuid(),
          role: "system",
          parts: [
            {
              id: uuid(),
              type: "crew_provisioning",
              content: content || "Building crew image…",
              metadata,
              timestamp: new Date(),
            },
          ],
          isStreaming: false,
          timestamp: new Date(),
        },
      ])
      setIsStreaming(false)
    },
    [],
  )

  const handleErrorEvent = useCallback((content: string) => {
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
  }, [])

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
        handleAssignmentEvent(msg.type as AssignmentEventType, payload)
        return
      }

      if (msg.type !== "chat_event") return
      // Drop deltas arriving after a local cancel so the cancelled stream
      // can't resurrect itself. The server's cancel ack races against
      // in-flight packets — without this gate, those late deltas re-open
      // the just-closed assistant turn.
      if (cancelledRef.current) return

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
          handleStatusEvent(content)
          break
        case "thinking":
          handleThinkingEvent(content, metadata)
          break
        case "text":
          handleTextEvent(content)
          break
        case "tool_call":
          handleToolCallEvent(content, metadata)
          break
        case "tool_result":
          handleToolResultEvent(content, metadata)
          break
        case "image":
          handleImageEvent(content, metadata)
          break
        case "result":
          handleResultEvent(content, metadata)
          break
        case "system":
          handleSystemEvent(content, metadata)
          break
        case "done":
          handleDoneEvent(metadata)
          break
        case "crew_provisioning":
          handleCrewProvisioningEvent(content, metadata)
          break
        case "error":
          handleErrorEvent(content)
          break
      }
    },
    [
      sessionId,
      handleAssignmentEvent,
      handleStatusEvent,
      handleThinkingEvent,
      handleTextEvent,
      handleToolCallEvent,
      handleToolResultEvent,
      handleImageEvent,
      handleResultEvent,
      handleSystemEvent,
      handleDoneEvent,
      handleCrewProvisioningEvent,
      handleErrorEvent,
    ],
  )

  const { status, send } = useWebSocket({
    url: wsUrl,
    getToken,
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
      cancelledRef.current = false

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
    // Flip local streaming state immediately so the UI returns to the
    // input-ready state even if the WS is dropped before the server's
    // cancel ack arrives. Closing assistant turns mark them no longer
    // streaming so the typing indicator stops. The cancelled flag blocks
    // any deltas already in flight from re-opening the cancelled turn
    // or appending parts to it; cleared by the next sendMessage.
    setIsStreaming(false)
    cancelledRef.current = true
    setTurns((prev) =>
      prev.map((t) =>
        t.role === "assistant" && t.isStreaming
          ? {
              ...t,
              isStreaming: false,
              parts: t.parts.map((p) =>
                p.isStreaming ? { ...p, isStreaming: false } : p,
              ),
            }
          : t,
      ),
    )
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
    cancelledRef.current = false

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
      cancelledRef.current = false

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
