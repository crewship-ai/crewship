/** Browser adapter + actual shared SDK; fake parent/port, not a GridCraft engine test. */
import { afterEach, expect, it, vi } from "vitest"
import { connectGridCraft } from "./gridcraft-native"
const origin = "https://studio.example.test", bundle = "a".repeat(64)
let client: ReturnType<typeof connectGridCraft> | undefined
afterEach(() => { client?.close(); client = undefined; vi.restoreAllMocks(); vi.useRealTimers() })
it("uses the real shared handshake, forwards native bytes and reports applied state", () => {
  vi.useFakeTimers()
  const hello = vi.spyOn(window.parent, "postMessage").mockImplementation(() => {})
  const inbound = vi.fn(), status = vi.fn()
  client = connectGridCraft(origin, bundle, inbound, status)
  window.dispatchEvent(new Event("load")); vi.runAllTimers()
  expect(hello).toHaveBeenCalledWith({ type: "gridcraft:hello", protocol: 1, bundle }, origin)
  const port = { onmessage: null as ((e: { data: string }) => void) | null, postMessage: vi.fn(), start: vi.fn(), close: vi.fn() }
  window.dispatchEvent(new MessageEvent("message", { source: window.parent, origin, data: { type: "gridcraft:connect", protocol: 1 }, ports: [port as unknown as MessagePort] }))
  const snapshot = JSON.stringify({ protocol: 1, type: "snapshot", rev: 1, snapshot: { sheets: [] } })
  port.onmessage!({ data: snapshot })
  expect(inbound).toHaveBeenCalledWith(snapshot)
  expect(status).not.toHaveBeenCalledWith("saved", expect.anything())
  client.reportStatus("saved", 1, "")
  expect(status).toHaveBeenLastCalledWith("saved", "")
  const mutation = JSON.stringify({ protocol: 1, type: "mutate", seq: 1, ops: [] })
  expect(client.send(mutation)).toBe(true)
  expect(port.postMessage).toHaveBeenCalledWith(mutation)
  port.postMessage.mockImplementation(() => { throw Error("closed") })
  expect(client.send(mutation)).toBe(false)
  client.reportStatus("saved", 1, "")
  expect(status).toHaveBeenLastCalledWith("error", "The editor connection is unavailable")
})
