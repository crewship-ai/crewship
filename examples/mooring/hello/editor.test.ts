/** Actual SDK + Hello DOM. Only the capability transport is synthetic. */
import { beforeEach, expect, it, vi } from "vitest"
import type { EditorSDKOptions } from "../../../lib/mooring/editor-sdk"
import type { CapabilityPort, WindowEnvelope } from "../../../lib/mooring/channel"
const transport = vi.hoisted(() => ({ receive: undefined as undefined | ((message: object) => void) }))
vi.mock("../../../lib/mooring/editor-sdk", async importOriginal => {
  const actual = await importOriginal<typeof import("../../../lib/mooring/editor-sdk")>()
  return { ...actual, createEditorSDK(options: EditorSDKOptions) {
    const parent = {}
    let listener: (event: WindowEnvelope & { ports?: CapabilityPort[] }) => void = () => {}
    const port: CapabilityPort = { onmessage: null, postMessage() {}, start() {}, close() {} }
    const sdk = actual.createEditorSDK({ ...options, parentOrigin: "https://studio.example.test", bundle: "a".repeat(64), environment: { parent, hello() {}, listen(fn) { listener = fn; return () => {} } } })
    listener({ source: parent, origin: "https://studio.example.test", data: { type: "mooring:connect", protocol: 1 }, ports: [port] })
    transport.receive = message => port.onmessage?.({ data: JSON.stringify({ protocol: 1, ...message }) })
    return sdk
  } }
})
beforeEach(() => { vi.resetModules(); document.body.innerHTML = '<textarea></textarea><button>Save</button><output></output><div id="confirmed"></div><div id="revision"></div>' })
async function submitted() {
  await import("./editor")
  transport.receive!({ type: "snapshot", rev: 1, generation: 1, snapshot: { text: "initial" } })
  const input = document.querySelector("textarea")!
  input.value = "mine"; input.dispatchEvent(new Event("input")); document.querySelector("button")!.click()
  return input
}
function settle() {
  transport.receive!({ type: "snapshot", rev: 3, generation: 1, snapshot: { text: "agent newer" } })
  transport.receive!({ type: "ack", seq: 1, rev: 2, receipt: { rev: 2, outcome: "committed", issued_by: "crewship", durability: "node" } })
}
it("never labels stale input Saved after a delayed ACK", async () => {
  const input = await submitted(); settle()
  expect(document.querySelector("output")!.textContent).toBe("Saved")
  expect(input.value).toBe("agent newer")
})
it.each(["new typing", "mine"])("preserves newer human draft %s after a delayed ACK", async text => {
  const input = await submitted()
  input.value = text; input.dispatchEvent(new Event("input")); settle()
  expect(input.value).toBe(text)
  expect(document.querySelector("output")!.textContent).toContain("Not saved")
})
