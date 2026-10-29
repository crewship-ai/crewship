/** Synthetic private-port/native-handle tests, not native/GPU or live proof. */
import { afterEach, expect, it, vi } from "vitest"
import { startWordCraftCommandChild, type WordCraftCommandNative } from "./wordcraft-command-child"
import type { ChildWindow } from "./wordcraft-child"

const digest = "a".repeat(64)
const projection = { generation: "g1", revision: 0, ids: ["doc:g1:block:1"], native_json: '{"opaque":18446744073709551615}' }
function setup(write = true) {
  vi.useFakeTimers()
  const parent = { postMessage: vi.fn() }
  let connect!: (e: MessageEvent) => void
  const window = { parent, addEventListener: (_: string, f: (e: MessageEvent) => void) => { connect = f }, removeEventListener: vi.fn() } as unknown as ChildWindow
  const sent: string[] = []
  const port = { postMessage: (raw: string) => sent.push(raw), start: vi.fn(), close: vi.fn(), onmessage: null as ((e: { data: unknown }) => void) | null }
  let callbacks!: Parameters<WordCraftCommandNative["start_c1_commands"]>[2]
  let queued = false, pending = false
  const handle = { project_confirmed: vi.fn(() => { queued = true }), projection_pending: () => queued, command_pending: () => pending, stop: vi.fn() }
  const native = { start_c1_commands: vi.fn((_raw: string, _ids: string, cb: typeof callbacks) => { callbacks = cb; return handle }) }
  const child = startWordCraftCommandChild(native, digest, async () => {}, window)
  connect({ source: parent, ports: [port], data: { type: "wordcraft-command:connect", protocol: 1, bundle: digest } } as unknown as MessageEvent)
  const send = (value: object) => port.onmessage?.({ data: JSON.stringify({ protocol: 1, ...value }) })
  send({ type: "wordcraft-command:bootstrap", projection, write })
  return { child, native, handle, send, sent, parent, callbacks: () => callbacks, consume: () => { queued = false; pending = false }, capture: (raw = '{"registry_id":"text.insert","command":"text.insert","params":{"text":"č"},"target":{"block_id":"doc:g1:block:1","unit":"utf8_bytes","start":0,"end":0}}') => { pending = true; return callbacks.command(raw) } }
}
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
it("bootstraps only actual native JSON/IDs and captures an exact registry proposal on the private channel", async () => {
  const f = setup(); await Promise.resolve()
  expect(f.native.start_c1_commands.mock.calls[0].slice(0, 2)).toEqual([projection.native_json, JSON.stringify(projection.ids)])
  expect(f.parent.postMessage).toHaveBeenCalledExactlyOnceWith({ protocol: 1, type: "wordcraft-command:hello", bundle: digest }, "*")
  expect(f.callbacks().can_edit()).toBe(true)
  const raw = '{"params":{"text":"č"},"registry_id":"text.insert","target":null,"command":"text.insert"}'
  expect(f.capture(raw)).toBe(true)
  expect(JSON.parse(f.sent.at(-1)!)).toEqual({ protocol: 1, type: "wordcraft-command:command", raw, revision: 0 })
  expect(f.callbacks().can_edit()).toBe(false)
  expect(f.capture()).toBe(false)
  expect(f.sent.some(s => /cookie|credential|module_url|https:/.test(s))).toBe(false)
  f.child.close()
})
it("waits for the real native queue getter to consume own projection before releasing input", async () => {
  const f = setup(); await Promise.resolve(); f.capture()
  f.send({ type: "wordcraft-command:projection", projection: { ...projection, revision: 1 }, selection: '{"anchor":{},"focus":{}}' })
  await vi.advanceTimersByTimeAsync(30_000)
  expect(f.sent.map(s => JSON.parse(s).type)).not.toContain("wordcraft-command:applied")
  expect(f.callbacks().can_edit()).toBe(false)
  f.consume(); await vi.advanceTimersByTimeAsync(25)
  expect(JSON.parse(f.sent.at(-1)!)).toEqual({ protocol: 1, type: "wordcraft-command:applied", own: true, revision: 1 })
  expect(f.callbacks().can_edit()).toBe(true)
  f.child.close()
})
it("a peer cannot clear original pending or invent a local Saved acknowledgement", async () => {
  const f = setup(); await Promise.resolve(); f.capture()
  f.send({ type: "wordcraft-command:projection", projection: { ...projection, revision: 1 }, selection: null })
  f.consume(); await vi.advanceTimersByTimeAsync(25)
  expect(JSON.parse(f.sent.at(-1)!)).toMatchObject({ type: "wordcraft-command:applied", own: false })
  expect(f.callbacks().can_edit()).toBe(false)
  expect(f.sent.some(s => /saved/i.test(s))).toBe(false)
  f.child.close()
})
it("unknown pauses input while retaining the original handle, allowing exact later own projection", async () => {
  const f = setup(); await Promise.resolve(); f.capture()
  f.send({ type: "wordcraft-command:pause" })
  expect(f.callbacks().can_edit()).toBe(false); expect(f.handle.stop).not.toHaveBeenCalled()
  f.send({ type: "wordcraft-command:projection", projection: { ...projection, revision: 1 }, selection: '{"anchor":{},"focus":{}}' })
  f.consume(); await vi.advanceTimersByTimeAsync(25)
  expect(f.native.start_c1_commands).toHaveBeenCalledOnce(); expect(f.callbacks().can_edit()).toBe(true)
  f.child.close()
})
it("reader remains read only and no message can choose a module URL or authority", async () => {
  const f = setup(false); await Promise.resolve()
  expect(f.callbacks().can_edit()).toBe(false); expect(f.capture()).toBe(false)
  f.send({ type: "wordcraft-command:bootstrap", projection, write: true, module_url: "https://attacker.invalid" })
  expect(f.handle.stop).toHaveBeenCalledOnce()
  expect(f.native.start_c1_commands).toHaveBeenCalledOnce()
  f.child.close()
})
it("stops on native apply failure rather than emitting completion", async () => {
  const f = setup(); await Promise.resolve(); f.capture()
  f.handle.project_confirmed.mockImplementation(() => { throw Error("native refusal") })
  f.send({ type: "wordcraft-command:projection", projection: { ...projection, revision: 1 }, selection: "{}" })
  expect(f.handle.stop).toHaveBeenCalledOnce()
  expect(f.sent.map(s => JSON.parse(s).type)).not.toContain("wordcraft-command:applied")
  f.child.close()
})
