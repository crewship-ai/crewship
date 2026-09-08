import { describe, it, expect, vi } from "vitest"
import { NotificationSoundController } from "../notification-sound-controller"

function fixture() {
  let now = 1000000
  let enabled = true
  const room = { id: "room", workspace_id: "ws", muted: false, last_read_sequence: 0 }
  const message = { id: "m1", sequence: 1, kind: "message", author_user_id: "other", author_agent_id: "", source_kind: "", created_at: new Date(now + 100).toISOString() }
  const row = { id: "i1", workspace_id: "ws", state: "unread", kind: "waitpoint", priority: "high", blocking: false, source_missing: false, created_at: message.created_at }
  const get = vi.fn(async (path: string) => path.startsWith("inbox?") ? { rows: [row] } : path.includes("/messages?") ? { messages: [message] } : room)
  const deliver = vi.fn(async (_candidate: unknown, _current: () => boolean) => true)
  const controller = new NotificationSoundController("ws", "me", { get: get as never, deliver, enabled: () => enabled, now: () => now })
  now += 200
  return { controller, get, deliver, room, message, row, advance: (ms: number) => { now += ms }, disable: () => { enabled = false } }
}

describe("NotificationSoundController authorized fresh events", () => {
  it("fetches actual human message and deduplicates duplicate invalidations", async () => {
    const f = fixture()
    await f.controller.handle("conversation.updated", { conversation_id: "room" })
    await f.controller.handle("conversation.updated", { conversation_id: "room" })
    expect(f.get.mock.calls.every(([path]) => path.includes("workspace_id=ws"))).toBe(true)
    expect(f.deliver).toHaveBeenCalledTimes(1)
    expect(f.deliver.mock.calls[0][0]).toEqual({ key: "message:room:m1", category: "chat", conversation: "room" })
  })
  it.each(["own", "agent", "activity", "joined", "muted", "read", "old", "wrong-workspace"])("does not sound for %s", async reason => {
    const f = fixture()
    if (reason === "own") f.message.author_user_id = "me"
    if (reason === "agent") f.message.author_agent_id = "agent"
    if (reason === "activity") f.message.source_kind = "activity"
    if (reason === "joined") f.message.kind = "agent_joined"
    if (reason === "muted") f.room.muted = true
    if (reason === "read") f.room.last_read_sequence = 1
    if (reason === "old") f.message.created_at = new Date(900000).toISOString()
    if (reason === "wrong-workspace") f.room.workspace_id = "other"
    await f.controller.handle("conversation.updated", { conversation_id: "room" })
    expect(f.deliver).not.toHaveBeenCalled()
  })
  it("ignores the inbox projection of the same chat without fetching", async () => {
    const f = fixture()
    await f.controller.handle("inbox.updated", { conversation_id: "room" })
    expect(f.get).not.toHaveBeenCalled()
  })
  it("alerts important unread inbox once across its different producer events", async () => {
    const f = fixture()
    await f.controller.handle("inbox.updated")
    await f.controller.handle("pipeline.waitpoint.created")
    expect(f.deliver).toHaveBeenCalledTimes(1)
  })
  it.each(["read", "resolved", "message", "routine", "missing", "old", "wrong-workspace"])("keeps %s inbox items silent", async reason => {
    const f = fixture()
    if (reason === "read" || reason === "resolved") f.row.state = reason
    if (reason === "message") f.row.kind = "message"
    if (reason === "routine") { f.row.kind = "schedule_missed"; f.row.priority = "medium" }
    if (reason === "missing") f.row.source_missing = true
    if (reason === "old") f.row.created_at = new Date(900000).toISOString()
    if (reason === "wrong-workspace") f.row.workspace_id = "other"
    await f.controller.handle("inbox.updated")
    expect(f.deliver).not.toHaveBeenCalled()
  })
  it("does not replay history after reconnect or a settings change", async () => {
    const f = fixture()
    await f.controller.handle("realtime.reconnected")
    await f.controller.handle("conversation.updated", { conversation_id: "room" })
    await f.controller.handle("inbox.updated")
    expect(f.deliver).not.toHaveBeenCalled()
  })
  it("revocation and disabled preferences are silent", async () => {
    const f = fixture()
    f.get.mockRejectedValueOnce(new Error("403"))
    await f.controller.handle("inbox.updated")
    f.disable()
    await f.controller.handle("inbox.updated")
    expect(f.get).toHaveBeenCalledTimes(1)
    expect(f.deliver).not.toHaveBeenCalled()
  })
  it("agent reply Inbox uses Chat sound and persisted reply identity, not aggregate item ID", async () => {
    const f = fixture()
    const row = { ...f.row, kind: "message", sender_type: "agent", priority: "medium", payload: { chat_id: "agent-chat", replied_at: f.row.created_at } }
    f.get.mockResolvedValue({ rows: [row] } as never)
    await f.controller.handle("inbox.updated")
    expect(f.deliver.mock.calls[0][0]).toEqual({ key: `agent-reply:agent-chat:${Date.parse(row.payload.replied_at)}`, category: "chat" })
    await f.controller.handle("inbox.updated")
    expect(f.deliver).toHaveBeenCalledTimes(1)
    f.advance(2500)
    row.payload.replied_at = new Date(Date.parse(row.payload.replied_at) + 2500).toISOString()
    await f.controller.handle("inbox.updated")
    expect(f.deliver).toHaveBeenCalledTimes(2)
  })
  it.each(["missing timestamp", "old reply", "read", "conversation projection"])("does not sound agent Inbox with %s", async reason => {
    const f = fixture()
    const row = { ...f.row, kind: "message", sender_type: "agent", payload: { chat_id: "agent-chat", replied_at: f.row.created_at, conversation_id: "" } }
    if (reason === "missing timestamp") row.payload.replied_at = ""
    if (reason === "old reply") row.payload.replied_at = new Date(900000).toISOString()
    if (reason === "read") row.state = "read"
    if (reason === "conversation projection") row.payload.conversation_id = "room"
    f.get.mockResolvedValue({ rows: [row] } as never)
    await f.controller.handle("inbox.updated")
    expect(f.deliver).not.toHaveBeenCalled()
  })
  it("cancels an in-flight read on workspace/unmount reset", async () => {
    const f = fixture()
    let resolve!: (value: unknown) => void
    f.get.mockImplementationOnce(() => new Promise(r => { resolve = r }) as never)
    const pending = f.controller.handle("inbox.updated")
    f.controller.dispose()
    resolve({ rows: [f.row] })
    await pending
    expect(f.deliver).not.toHaveBeenCalled()
  })
  it("drops delayed events rather than playing a reconnect backlog", async () => {
    const f = fixture()
    f.advance(61000)
    await f.controller.handle("inbox.updated")
    expect(f.deliver).not.toHaveBeenCalled()
  })
})
