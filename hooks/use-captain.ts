"use client"

import { useCallback, useEffect, useRef } from "react"
import { useCaptainStore, type CaptainMessage } from "./use-captain-store"
import { useWorkspace } from "./use-workspace"

let msgCounter = 0
function nextId() {
  return `cap_${Date.now()}_${++msgCounter}`
}

interface HistoryMessage {
  role: string
  content: string
  tool_calls?: { id: string; name: string; input: string }[]
  tool_call_id?: string
  tool_name?: string
}

function convertHistory(raw: HistoryMessage[]): CaptainMessage[] {
  const result: CaptainMessage[] = []
  for (const msg of raw) {
    if (msg.role === "user") {
      result.push({ id: nextId(), role: "user", content: msg.content, timestamp: Date.now() })
    } else if (msg.role === "assistant") {
      const toolCalls = msg.tool_calls?.map((tc) => ({ id: tc.id, name: tc.name }))
      result.push({
        id: nextId(),
        role: "assistant",
        content: msg.content,
        timestamp: Date.now(),
        toolCalls: toolCalls?.length ? toolCalls : undefined,
      })
    } else if (msg.role === "tool") {
      // Merge tool result into previous assistant message
      const last = result[result.length - 1]
      if (last?.role === "assistant" && msg.tool_call_id) {
        last.toolResults = [
          ...(last.toolResults ?? []),
          { id: msg.tool_call_id, name: msg.tool_name ?? "", content: msg.content },
        ]
      }
    }
  }
  return result
}

/**
 * High-level hook for the Captain AI assistant panel.
 * Manages SSE-streamed chat, conversation history, badge count polling, and abort/clear actions.
 */
export function useCaptain() {
  const store = useCaptainStore()
  const { workspaceId } = useWorkspace()
  const abortRef = useRef<AbortController | null>(null)

  // Load history once
  useEffect(() => {
    if (store.historyLoaded || !workspaceId) return

    async function loadHistory() {
      try {
        const res = await fetch(`/api/v1/captain/history?workspace_id=${workspaceId}`, {
          credentials: "include",
        })
        if (!res.ok) return
        const data = await res.json()
        store.setMessages(data.messages?.length ? convertHistory(data.messages) : [])
      } catch {
        // Silently fail
      } finally {
        store.setHistoryLoaded(true)
      }
    }

    loadHistory()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, store.historyLoaded])

  // Poll context for badge every 60s
  useEffect(() => {
    if (!workspaceId) return

    async function fetchContext() {
      try {
        const res = await fetch(`/api/v1/captain/context?workspace_id=${workspaceId}`, {
          credentials: "include",
        })
        if (!res.ok) return
        const data = await res.json()
        store.setBadgeCount((data.pending_escalations ?? 0) + (data.pending_proposals ?? 0))
      } catch {
        // Silently fail
      }
    }

    fetchContext()
    const interval = setInterval(fetchContext, 60_000)
    return () => clearInterval(interval)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  const sendMessage = useCallback(
    async (text: string) => {
      if (!workspaceId || store.isStreaming) return

      // Add user message
      store.addMessage({ id: nextId(), role: "user", content: text, timestamp: Date.now() })

      // Add empty assistant message for streaming
      store.addMessage({ id: nextId(), role: "assistant", content: "", timestamp: Date.now() })
      store.setStreaming(true)
      store.setActiveToolCall(null)

      const controller = new AbortController()
      abortRef.current = controller

      try {
        const res = await fetch(`/api/v1/captain/chat?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          credentials: "include",
          body: JSON.stringify({ message: text }),
          signal: controller.signal,
        })

        if (!res.ok) {
          const err = await res.text()
          store.appendToLast(`Error: ${err}`)
          store.setStreaming(false)
          return
        }

        const reader = res.body!.getReader()
        const decoder = new TextDecoder()
        let buffer = ""

        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const frames = buffer.split("\n\n")
          buffer = frames.pop()!

          for (const frame of frames) {
            const dataLine = frame.split("\n").find((l) => l.startsWith("data: "))
            if (!dataLine) continue

            try {
              const event = JSON.parse(dataLine.slice(6))

              switch (event.type) {
                case "text":
                  store.appendToLast(event.content)
                  break
                case "tool_call":
                  store.addToolCallToLast({ id: event.id, name: event.name })
                  store.setActiveToolCall(event.name)
                  break
                case "tool_result":
                  store.addToolResultToLast({
                    id: event.id,
                    name: event.name,
                    content: event.content,
                  })
                  store.setActiveToolCall(null)
                  break
                case "done":
                  store.setStreaming(false)
                  store.setActiveToolCall(null)
                  break
                case "warning":
                  store.appendToLast(`\n\n_${event.content}_`)
                  break
                case "error":
                  store.appendToLast(`\n\n_Error: ${event.content}_`)
                  store.setStreaming(false)
                  store.setActiveToolCall(null)
                  break
              }
            } catch {
              // Skip malformed JSON
            }
          }
        }
      } catch (err) {
        if ((err as Error).name !== "AbortError") {
          store.appendToLast("\n\n_Connection error._")
        }
      } finally {
        store.setStreaming(false)
        store.setActiveToolCall(null)
        abortRef.current = null
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [workspaceId, store.isStreaming]
  )

  const stopGeneration = useCallback(() => {
    abortRef.current?.abort()
    store.setStreaming(false)
    store.setActiveToolCall(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const clearHistory = useCallback(async () => {
    if (!workspaceId) return
    try {
      await fetch(`/api/v1/captain/history?workspace_id=${workspaceId}`, {
        method: "DELETE",
        credentials: "include",
      })
    } catch {
      // Silently fail
    }
    store.clearMessages()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  return {
    messages: store.messages,
    isStreaming: store.isStreaming,
    activeToolCall: store.activeToolCall,
    badgeCount: store.badgeCount,
    isOpen: store.isOpen,
    toggle: store.toggle,
    setOpen: store.setOpen,
    sendMessage,
    stopGeneration,
    clearHistory,
  }
}
