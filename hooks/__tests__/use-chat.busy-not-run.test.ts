// A message sent while the agent was busy elsewhere vanished on reload. The server now saves it as not run
// (agent_busy with its message_id); the chat shows the same note the history holds (notrun-<id>) with Send again, which
// runs that saved message once more (send_message with resend_of) and never adds a second one. A busy reply without
// a message_id is the old bounce: nothing was saved.
import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, renderHook } from "@testing-library/react"

const transport = vi.hoisted(() => ({ status: "connected", send: vi.fn(), onMessage: undefined as undefined | ((message: unknown) => void) }))
vi.mock("@/hooks/use-websocket", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/use-websocket")>(),
  useWebSocket: (options: { onMessage?: (message: unknown) => void }) => {
    transport.onMessage = options.onMessage
    return { status: transport.status, send: transport.send, retry: vi.fn() }
  },
}))

import { resendableNotRun, useChat, type ChatTurn } from "@/hooks/use-chat"

const SAVED = "Your message is saved. The agent is busy with another run, so it did not run yet. Send it again once the agent is free."
const refs = [{ page_id: "page-1", version: "1" }]
const event = (type: string, content: string, metadata: Record<string, unknown>) =>
  transport.onMessage?.({ type: "chat_event", channel: "session:chat-1", payload: { type, content, metadata } })
const sends = () => transport.send.mock.calls.map(([frame]) => frame).filter(frame => frame.type === "send_message").map(frame => JSON.parse(frame.payload))

function start() {
  const refused = vi.fn()
  const hook = renderHook(() => useChat({ wsUrl: "ws://test/ws", getToken: async () => "t", sessionId: "chat-1", currentUserId: "user-1", onSendRefused: refused }))
  act(() => { hook.result.current.sendMessage("Doplň Loan ID", { artifact_refs: refs }) })
  return { ...hook, refused }
}

beforeEach(() => { transport.status = "connected"; transport.send.mockReset() })

describe("a message the busy agent did not run", () => {
  it("shows the saved message's note, not an agent error, and keeps the composer as it is", () => {
    const { result, refused } = start()
    act(() => { event("agent_busy", SAVED, { chat_id: "chat-1", message_id: "m1", status: "not_run" }) })
    const last = result.current.turns.at(-1)!
    expect(last).toMatchObject({ id: "notrun-m1", role: "system", isStreaming: false })
    expect(last.parts[0]).toMatchObject({ type: "error", content: SAVED })
    expect(result.current.turns.filter(turn => turn.role === "assistant")).toEqual([])
    expect(result.current.isStreaming).toBe(false)
    expect(refused).toHaveBeenCalledWith("busy_saved")
    expect(resendableNotRun(result.current.turns)).toBe("m1")
  })

  it("Send again runs the saved message with its Sheet, without a second message", () => {
    const { result } = start()
    act(() => { event("agent_busy", SAVED, { chat_id: "chat-1", message_id: "m1", status: "not_run" }) })
    transport.send.mockClear()
    let sent: boolean | undefined
    act(() => { sent = result.current.resendNotRun("m1") })
    expect(sent).toBe(true)
    expect(sends()).toEqual([{ session_id: "chat-1", content: "Doplň Loan ID", metadata: { resend_of: "m1", artifact_refs: refs } }])
    expect(result.current.turns.filter(turn => turn.role === "user")).toHaveLength(1)
    expect(result.current.turns.some(turn => turn.id === "notrun-m1")).toBe(false)
    expect(result.current.isStreaming).toBe(true)
    // Busy again: the same message keeps its one note.
    act(() => { event("agent_busy", SAVED, { chat_id: "chat-1", message_id: "m1", status: "not_run" }) })
    expect(result.current.turns.filter(turn => turn.id === "notrun-m1")).toHaveLength(1)
    expect(result.current.turns.filter(turn => turn.role === "user")).toHaveLength(1)
  })

  it("a busy reply without a saved message is the old refusal: the composer gets its message back", () => {
    const { result, refused } = start()
    act(() => { event("agent_busy", "The agent is busy with another run right now. Please wait for it to finish.", { chat_id: "chat-1" }) })
    expect(refused).toHaveBeenCalledWith("busy")
    expect(result.current.turns.some(turn => turn.id.startsWith("notrun-"))).toBe(false)
    expect(resendableNotRun(result.current.turns)).toBeUndefined()
  })

  it("offers Send again from history only while no agent reply follows the note", () => {
    const at = new Date()
    const user: ChatTurn = { id: "m1", role: "user", parts: [{ id: "p", type: "text", content: "Doplň", timestamp: at }], isStreaming: false, timestamp: at, metadata: { artifact_refs: refs } }
    const note: ChatTurn = { id: "notrun-m1", role: "system", parts: [{ id: "n", type: "error", content: SAVED, timestamp: at }], isStreaming: false, timestamp: at }
    const reply: ChatTurn = { id: "a1", role: "assistant", parts: [{ id: "r", type: "text", content: "Hotovo", timestamp: at }], isStreaming: false, timestamp: at }
    expect(resendableNotRun([user, note])).toBe("m1")
    expect(resendableNotRun([user, note, reply])).toBeUndefined()
    expect(resendableNotRun([user])).toBeUndefined()
  })

  // Every run the server refused after saving the message (runtime, build, review) is not run too.
  it.each([
    ["error", { reason: "runtime_unavailable" }],
    ["crew_provisioning", { status: "failed" }],
  ])("a %s for a saved message gets the same note and Send again", (type, extra) => {
    const { result, refused } = start()
    act(() => { event("user_message", "Doplň Loan ID", { author_user_id: "user-1", message_id: "m2" }) })
    act(() => { event(type, "Your message was saved but did not run: no runtime is available.", { ...extra, message_id: "m2", status: "not_run", not_run_reason: "runtime_unavailable" }) })
    expect(result.current.turns.filter(turn => turn.id === "notrun-m2")).toHaveLength(1)
    expect(resendableNotRun(result.current.turns)).toBe("m2")
    expect(refused).not.toHaveBeenCalledWith("refused", expect.anything())
  })

  it("an error before the server saved the message gives it back to the composer, with the server's words", () => {
    const { refused } = start()
    act(() => { event("error", "The Sheet changed after you allowed it. Allow it again.", { reason: "consent_stale" }) })
    expect(refused).toHaveBeenCalledWith("refused", "The Sheet changed after you allowed it. Allow it again.")
  })

  it("an error during a run of a saved message leaves the composer alone", () => {
    const { refused } = start()
    act(() => { event("user_message", "Doplň Loan ID", { author_user_id: "user-1", message_id: "m3" }) })
    act(() => { event("error", "The agent stopped.", {}) })
    expect(refused).not.toHaveBeenCalled()
  })
})
