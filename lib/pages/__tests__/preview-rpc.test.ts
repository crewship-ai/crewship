import { afterEach, expect, it, vi } from "vitest"
import { PreviewSnapshotChannel } from "../preview-runtime"

afterEach(() => vi.useRealTimers())
function fixture(handler?: ConstructorParameters<typeof PreviewSnapshotChannel>[1]) {
  const port = { onmessage: null as ((event: { data: unknown }) => void) | null, start: vi.fn(), close: vi.fn(), postMessage: vi.fn() }
  const channel = new PreviewSnapshotChannel(port as unknown as MessagePort, handler)
  const send = (id: number, method = "runAction", params: unknown = {}) => port.onmessage?.({ data: { type: "crewship.pages.request/v1", id, method, params } })
  return { port, channel, send }
}
it("rejects executable requests in preview and unknown or oversized requests", async () => {
  const preview = fixture()
  preview.send(1)
  expect(preview.port.postMessage).toHaveBeenCalledWith(expect.objectContaining({ id: 1, error: expect.stringMatching(/published/) }))
  const handler = vi.fn()
  const runtime = fixture(handler)
  runtime.send(1, "fetch")
  runtime.send(2, "runAction", { text: "x".repeat(33 * 1024) })
  expect(handler).not.toHaveBeenCalled()
  expect(runtime.port.postMessage).toHaveBeenCalledTimes(2)
  preview.channel.close(); runtime.channel.close()
})
it("allows one pending RPC and aborts it without posting a response after close", async () => {
  let signal: AbortSignal | undefined
  let resolve!: (value: unknown) => void
  const handler = vi.fn((_request, value: AbortSignal) => { signal = value; return new Promise(done => { resolve = done }) })
  const runtime = fixture(handler)
  runtime.send(1); runtime.send(2)
  expect(handler).toHaveBeenCalledTimes(1)
  expect(runtime.port.postMessage).toHaveBeenCalledWith(expect.objectContaining({ id: 2, error: expect.stringMatching(/active/) }))
  runtime.channel.close()
  expect(signal?.aborted).toBe(true)
  resolve({ pending_id: "run" })
  await Promise.resolve(); await Promise.resolve()
  expect(runtime.port.postMessage).toHaveBeenCalledTimes(1)
})
