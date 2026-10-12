// #2861 — a restricted chat has no WebSocket. Messages this tab did not send
// (another participant of a shared chat, another tab) must still appear: a
// bounded poll of the allowlisted history endpoint notices them.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, renderHook } from "@testing-library/react"

import { serverAllowsRestricted } from "@/lib/__tests__/restricted-route-oracle"

const requests: string[] = []
let messages: { id: string }[] = []
let status = 200

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input))
    return { ok: status === 200, status, json: async () => ({ messages }) } as unknown as Response
  }),
}))

import { RESTRICTED_CHAT_POLL_MS, messagesSignature, useRestrictedChatPoll } from "@/hooks/use-restricted-chat-poll"

let visibility: DocumentVisibilityState = "visible"
beforeEach(() => {
  vi.useFakeTimers()
  requests.length = 0
  messages = [{ id: "m1" }]
  status = 200
  visibility = "visible"
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility })
})
afterEach(() => { vi.useRealTimers() })

function setup(overrides: Partial<Parameters<typeof useRestrictedChatPoll>[0]> = {}) {
  let known: string | null = messagesSignature([{ id: "m1" }])
  // Stands in for the chat panel: a reload brings the screen up to date.
  const onChange = vi.fn(() => { known = messagesSignature(messages) })
  const hook = renderHook((props: { paused: boolean }) => useRestrictedChatPoll({
    enabled: true, sessionId: "chat-1", workspaceId: "ws-a", paused: props.paused,
    getKnownSignature: () => known, onChange, ...overrides,
  }), { initialProps: { paused: false } })
  return { ...hook, onChange }
}

const tick = () => act(async () => { await vi.advanceTimersByTimeAsync(RESTRICTED_CHAT_POLL_MS) })

describe("useRestrictedChatPoll", () => {
  it("shows a message another participant wrote, without a WebSocket", async () => {
    const { onChange } = setup()
    await tick()
    expect(onChange).not.toHaveBeenCalled()
    messages = [{ id: "m1" }, { id: "m2-from-teammate" }]
    await tick()
    expect(onChange).toHaveBeenCalledTimes(1)
    await tick()
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(requests.every((u) => serverAllowsRestricted("GET", u))).toBe(true)
    expect(requests[0]).toBe("/api/v1/chats/chat-1/messages?workspace_id=ws-a")
  })

  it("does not poll while hidden, catches up when visible again", async () => {
    const { onChange } = setup()
    visibility = "hidden"
    messages = [{ id: "m1" }, { id: "m2" }]
    await tick(); await tick()
    expect(requests).toHaveLength(0)
    visibility = "visible"
    await act(async () => { document.dispatchEvent(new Event("visibilitychange")); await vi.advanceTimersByTimeAsync(0) })
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it("does not poll while this tab's own reply streams", async () => {
    const { rerender } = setup()
    rerender({ paused: true })
    await tick(); await tick()
    expect(requests).toHaveLength(0)
  })

  it("stops on unmount and for good on 404", async () => {
    const first = setup()
    first.unmount()
    await tick()
    expect(requests).toHaveLength(0)

    status = 404
    setup()
    await tick(); await tick(); await tick()
    expect(requests).toHaveLength(1)
  })

  it("does nothing when disabled (a trusted chat with a socket)", async () => {
    setup({ enabled: false })
    await tick()
    expect(requests).toHaveLength(0)
  })
})
