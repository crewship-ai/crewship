import { afterEach, expect, it, vi } from "vitest"
import { PreviewSnapshotChannel } from "../preview-runtime"

afterEach(() => vi.useRealTimers())
it("bounds stalled delivery, coalesces updates and ignores unrelated acknowledgements", () => {
  vi.useFakeTimers()
  const port = { onmessage: null as ((event: { data: unknown }) => void) | null, start: vi.fn(), close: vi.fn(), postMessage: vi.fn() }
  const channel = new PreviewSnapshotChannel(port as unknown as MessagePort)
  const snapshot = (name: string) => ({ name, slug: "test", panels: [] })
  channel.push(snapshot("first"))
  vi.advanceTimersByTime(100)
  for (let i = 0; i < 1000; i++) channel.push(snapshot(String(i)))
  vi.advanceTimersByTime(10000)
  expect(port.postMessage).toHaveBeenCalledTimes(1)
  port.onmessage?.({ data: { type: "crewship.pages.snapshot-ack/v1", seq: 999 } })
  vi.advanceTimersByTime(100)
  expect(port.postMessage).toHaveBeenCalledTimes(1)
  port.onmessage?.({ data: { type: "crewship.pages.snapshot-ack/v1", seq: 1 } })
  vi.advanceTimersByTime(100)
  expect(port.postMessage).toHaveBeenLastCalledWith({ type: "crewship.pages.snapshot/v1", seq: 2, snapshot: snapshot("999") })
  expect(() => channel.push(snapshot("x".repeat(1024 * 1024)))).toThrow(/1 MiB/)
  channel.close()
  channel.push(snapshot("closed"))
  vi.runAllTimers()
  expect(port.postMessage).toHaveBeenCalledTimes(2)
  expect(port.close).toHaveBeenCalledOnce()
  expect(port.onmessage).toBeNull()
})
it("cancels a pending delivery when the frame is removed", () => {
  vi.useFakeTimers()
  const port = { onmessage: null, start: vi.fn(), close: vi.fn(), postMessage: vi.fn() }
  const channel = new PreviewSnapshotChannel(port as unknown as MessagePort)
  channel.push({ slug: "test", name: "test", panels: [] })
  channel.close()
  vi.runAllTimers()
  expect(port.postMessage).not.toHaveBeenCalled()
})
