import { beforeEach, expect, it, vi } from "vitest"
import { act, renderHook } from "@testing-library/react"

const transport = vi.hoisted(() => ({ onMessage: undefined as undefined | ((message: unknown) => void) }))
vi.mock("@/hooks/use-websocket", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/use-websocket")>(),
  useWebSocket: (options: { onMessage?: (message: unknown) => void }) => {
    transport.onMessage = options.onMessage
    return { status: "connected", send: vi.fn(), retry: vi.fn() }
  },
}))

import { useChat } from "@/hooks/use-chat"

const refusal = "This agent's crew is not allowed to create artifact projects in this workspace"
const emit = (content: string, metadata?: Record<string, unknown>) =>
  transport.onMessage?.({ type: "chat_event", channel: "session:chat-1", payload: { type: "error", content, metadata } })
beforeEach(() => { transport.onMessage = undefined })

it("tells the composer when the server refuses a Project send for crew policy", async () => {
  const onSendRefused = vi.fn()
  const { result } = renderHook(() => useChat({ wsUrl: "ws://test/ws", getToken: async () => "t", sessionId: "chat-1", currentUserId: "user-1", onSendRefused }))
  await act(async () => { emit(refusal, { code: "artifact_crew_not_allowed" }) })
  expect(onSendRefused).toHaveBeenCalledWith("artifact_crew_not_allowed")
  expect(JSON.stringify(result.current.turns)).toContain(refusal)
})

it("does not report an ordinary error as a typed refusal", async () => {
  const onSendRefused = vi.fn()
  renderHook(() => useChat({ wsUrl: "ws://test/ws", getToken: async () => "t", sessionId: "chat-1", currentUserId: "user-1", onSendRefused }))
  await act(async () => { emit("failed to save message") })
  expect(onSendRefused).not.toHaveBeenCalled()
})
