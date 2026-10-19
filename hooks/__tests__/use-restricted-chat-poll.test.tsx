// #2861 — a restricted chat has no WebSocket. Messages this tab did not send
// (another participant of a shared chat, another tab) must still appear: a
// bounded poll of the allowlisted history endpoint notices them.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, render, renderHook, screen } from "@testing-library/react"
import { useCallback, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"

import { serverAllowsRestricted } from "@/lib/__tests__/restricted-route-oracle"

const requests: string[] = []
let messages: { id: string; checkpoint?: unknown }[] = []
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
  const onDenied = vi.fn()
  const hook = renderHook((props: { paused: boolean }) => useRestrictedChatPoll({
    enabled: true, sessionId: "chat-1", workspaceId: "ws-a", paused: props.paused,
    getKnownSignature: () => known, isSourceConfirmed: () => true, onDenied, onChange, ...overrides,
  }), { initialProps: { paused: false } })
  return { ...hook, onChange, onDenied }
}

const tick = () => act(async () => { await vi.advanceTimersByTimeAsync(RESTRICTED_CHAT_POLL_MS) })

describe("useRestrictedChatPoll", () => {
  it("never dispatches a URL dot-segment as a chat identifier", async () => {
    setup({sessionId:".."})
    await tick()
    expect(requests).toHaveLength(0)
  })
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

describe("stable checkpoint polling", () => {
  it("notices checkpoint seq/state changes while the last message and count stay unchanged", async () => {
    const checkpoint = { version: 1, run_id: "r", turn_id: "m1", through_seq: 1, turn_state: "running" }
    messages = [{ id: "m1", checkpoint }, { id: "unchanged-tail" }]
    const { onChange } = setup()
    await tick()
    expect(onChange).toHaveBeenCalledTimes(1)
    messages = [{ id: "m1", checkpoint: { ...checkpoint, through_seq: 2 } }, { id: "unchanged-tail" }]
    await tick()
    expect(onChange).toHaveBeenCalledTimes(2)
    messages = [{ id: "m1", checkpoint: { ...checkpoint, through_seq: 2, turn_state: "failed" } }, { id: "unchanged-tail" }]
    await tick()
    expect(onChange).toHaveBeenCalledTimes(3)
    await tick()
    expect(onChange).toHaveBeenCalledTimes(3)
  })
})

it("keeps bodies, summaries and private checkpoint fields out of the signature", () => {
  const message = { id: "m", content: "private body", checkpoint: { version: 1, run_id: "r", turn_id: "m", through_seq: 2, turn_state: "running", hash: "private hash", event: { payload: "private event" } } }
  const signature = messagesSignature([message])
  expect(signature).not.toContain("private")
  expect(messagesSignature([{ id: "legacy" }])).toBe("1:legacy")
})


describe("definitive source denial", () => {
  it.each([401, 403, 404] as const)("clears rendered protected history on confirmed source HTTP%d", async code => {
    const denied = vi.fn()
    function ProtectedTranscript() {
      const [content, setContent] = useState("Protected source content")
      const onDenied = useCallback((responseStatus: 401 | 403 | 404) => { denied(responseStatus); setContent("") }, [])
      useRestrictedChatPoll({
        enabled: true, sessionId: "private-chat", workspaceId: "ws-a", paused: false,
        getKnownSignature: () => "1:private-message", isSourceConfirmed: () => true,
        onChange: vi.fn(), onDenied,
      })
      return <p>{content}</p>
    }
    render(<ProtectedTranscript />)
    expect(screen.getByText("Protected source content")).toBeTruthy()
    status = code
    await tick()
    expect(screen.queryByText("Protected source content")).toBeNull()
    expect(denied).toHaveBeenCalledExactlyOnceWith(code)
    await tick()
    expect(requests).toHaveLength(1)
  })
})


it("does not treat an unconfirmed empty draft's 404 as source revocation", async () => {
  const { onDenied } = setup({ isSourceConfirmed: () => false, getKnownSignature: () => "0:" })
  status = 404
  await tick(); await tick()
  expect(onDenied).not.toHaveBeenCalled()
  expect(requests).toHaveLength(1)
})

it.each([401, 403] as const)("treats HTTP%d as definitive even without a confirmed source", async code => {
  const { onDenied } = setup({ isSourceConfirmed: () => false })
  status = code
  await tick()
  expect(onDenied).toHaveBeenCalledExactlyOnceWith(code)
})

it("keeps a transient server failure retryable without source denial", async () => {
  const { onDenied, onChange } = setup()
  status = 503
  await tick(); await tick()
  expect(onDenied).not.toHaveBeenCalled()
  expect(requests).toHaveLength(2)
  status = 200
  messages.push({ id: "after-recovery" })
  await tick()
  expect(onChange).toHaveBeenCalledTimes(1)
})

it("ignores a refused request from the previous principal instead of denying the new screen", async () => {
  let resolve!: (response: Response) => void
  vi.mocked(apiFetch).mockImplementationOnce(() => new Promise<Response>(done => { resolve = done }))
  const previousDenied = vi.fn()
  const currentDenied = vi.fn()
  const hook = renderHook(({ onDenied }) => useRestrictedChatPoll({
    enabled: true, sessionId: "same-chat", workspaceId: "ws-a", paused: false,
    getKnownSignature: () => "1:protected", isSourceConfirmed: () => true,
    onChange: vi.fn(), onDenied,
  }), { initialProps: { onDenied: previousDenied } })
  await tick()
  hook.rerender({ onDenied: currentDenied })
  await act(async () => { resolve({ status: 403, ok: false } as Response); await Promise.resolve() })
  expect(previousDenied).not.toHaveBeenCalled()
  expect(currentDenied).not.toHaveBeenCalled()
})
