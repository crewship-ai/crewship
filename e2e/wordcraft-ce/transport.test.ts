// @vitest-environment node
import { EventEmitter } from "node:events"
import type { Page } from "@playwright/test"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { WordCraftTransportObserver } from "./latency"

const transportURL = "wss://writer.example.test/api/v1/pages/example/mooring/ws"
const command = { type: "command", op_id: "operation-1", command: "text.insert", base_revision: "1", target: { block_id: "paragraph-1", start: 0, end: 0 }, params: { text: "W" } }
const receipt = { type: "command_result", op_id: "operation-1", revision: "2", status: "saved", durability: "committed" }

/** Synthetic CDP event fixture. It validates the observer, never an engine or
 * live latency. No server, browser, environment or real credential is used. */
function fixture() {
  const session = Object.assign(new EventEmitter(), { send: vi.fn().mockResolvedValue(undefined), detach: vi.fn().mockResolvedValue(undefined) })
  let pageURL = "about:blank"
  const context = { newCDPSession: vi.fn().mockResolvedValue(session) }
  const page = { context: () => context, url: () => pageURL } as unknown as Page
  const observer = new WordCraftTransportObserver(page, { timeoutMs: 100 })
  const navigate = () => { pageURL = "https://writer.example.test/pages/example" }
  const socket = (id = "socket-1", url = `${transportURL}?ticket=synthetic-admission-placeholder`) => session.emit("Network.webSocketCreated", { requestId: id, url })
  const emit = (direction: "Sent" | "Received", payload: unknown, id = "socket-1") => session.emit(`Network.webSocketFrame${direction}`, { requestId: id, response: { opcode: 1, payloadData: JSON.stringify(payload) } })
  return { observer, session, navigate, socket, emit }
}

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

describe("passive WordCraft C1 transport oracle", () => {
  it("observes exact frames after navigation, retains original fields and scrubs the admission query", async () => {
    const f = fixture()
    await f.observer.start()
    expect(f.session.send).toHaveBeenCalledExactlyOnceWith("Network.enable")
    f.navigate()
    f.socket()
    const cursor = f.observer.cursor()
    f.emit("Sent", command)
    const sent = await f.observer.commandSince(cursor, "text.insert")
    expect(sent).toMatchObject({ ...command, transportId: "socket-1", transportURL })
    expect(Number.isFinite(sent.sentAt)).toBe(true)
    expect(JSON.stringify(sent)).not.toContain("ticket")
    expect(JSON.stringify(sent)).not.toContain("synthetic-admission-placeholder")
    f.emit("Received", receipt)
    const saved = await f.observer.saved(command.op_id, sent.transportId)
    expect(saved).toMatchObject({ ...receipt, transportId: sent.transportId, transportURL })
    expect(saved.savedAt).toBeGreaterThanOrEqual(sent.sentAt)
    await f.observer.close()
    expect(f.session.detach).toHaveBeenCalledExactlyOnceWith()
    // The fixture saw only passive Network.enable and detach, no send/injection.
    expect(f.session.send).toHaveBeenCalledTimes(1)
  })

  it("waits for the source socket even when another socket reuses an op_id", async () => {
    const f = fixture()
    await f.observer.start()
    f.navigate()
    f.socket("socket-1")
    f.socket("socket-2")
    f.emit("Sent", command)
    const sent = await f.observer.commandSince(0, command.command)
    let settled = false
    const pending = f.observer.saved(command.op_id, sent.transportId).then(value => { settled = true; return value })
    f.emit("Received", receipt, "socket-2")
    await Promise.resolve()
    expect(settled).toBe(false)
    f.emit("Received", receipt, "socket-1")
    expect((await pending).transportId).toBe("socket-1")
    await f.observer.close()
  })

  it("rejects a result without a durable committed status", async () => {
    const f = fixture()
    await f.observer.start()
    f.navigate()
    f.socket()
    const rejected = expect(f.observer.saved(command.op_id)).rejects.toThrow("lacks a committed Saved receipt")
    f.emit("Received", { ...receipt, durability: "buffered" })
    await rejected
    await f.observer.close()
  })

  it("rejects malformed command fields rather than measuring a different packet", async () => {
    const f = fixture()
    await f.observer.start()
    f.navigate()
    f.socket()
    const rejected = expect(f.observer.commandSince(0, command.command)).rejects.toThrow("does not match the acceptance transport contract")
    f.emit("Sent", { ...command, base_revision: "18446744073709551616" })
    await rejected
    await f.observer.close()
  })

  it("times out absent commands and ignores unrelated sockets or unknown packet shapes", async () => {
    const f = fixture()
    await f.observer.start()
    f.navigate()
    f.socket("other-origin", "wss://unrelated.example.test/api/v1/pages/example/mooring/ws")
    f.socket("other-path", "wss://writer.example.test/unrelated/ws")
    f.socket()
    const rejected = expect(f.observer.commandSince(0, command.command)).rejects.toThrow("no actual C1 command frame")
    f.emit("Sent", command, "other-origin")
    f.emit("Sent", command, "other-path")
    f.emit("Sent", { type: "unrelated", command: command.command })
    await vi.advanceTimersByTimeAsync(100)
    await rejected
    expect(f.observer.cursor()).toBe(0)
    await f.observer.close()
  })

  it("uses a cursor to exclude earlier commands and rejects missing observer lifecycle", async () => {
    const f = fixture()
    await expect(f.observer.commandSince(0, command.command)).rejects.toThrow("not_started")
    await f.observer.start()
    await expect(f.observer.start()).rejects.toThrow("already_started")
    f.navigate()
    f.socket()
    f.emit("Sent", command)
    const cursor = f.observer.cursor()
    f.emit("Sent", { ...command, op_id: "operation-2", base_revision: "2" })
    expect((await f.observer.commandSince(cursor, command.command)).op_id).toBe("operation-2")
    await expect(f.observer.commandSince(-1, command.command)).rejects.toThrow("invalid_command_observation_cursor")
    await f.observer.close()
  })
})
