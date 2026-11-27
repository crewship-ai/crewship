// @vitest-environment node
// A message sent while its workbook is still becoming a Sheet waits (at most ~10 s) for the Sheet.
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { waitForSheet } from "../wait-for-sheet"

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

it("goes at once when nothing is being made", async () => {
  await expect(waitForSheet(() => true, 10_000)).resolves.toBe("ready")
})
it("waits until the Sheet is ready", async () => {
  let ready = false
  const waited = waitForSheet(() => ready, 10_000)
  await vi.advanceTimersByTimeAsync(3000)
  ready = true
  await vi.advanceTimersByTimeAsync(200)
  await expect(waited).resolves.toBe("ready")
})
it("lets the message go after the limit, with what it has", async () => {
  const waited = waitForSheet(() => false, 10_000)
  await vi.advanceTimersByTimeAsync(10_100)
  await expect(waited).resolves.toBe("timed_out")
})

// A Sheet that was not made never lets the message go by itself with the workbook as a plain file.
import { sheetSendGate } from "../wait-for-sheet"
it("waits without a limit when asked, until the person cancels", async () => {
  const stop = new AbortController()
  const waited = waitForSheet(() => false, Infinity, stop.signal)
  await vi.advanceTimersByTimeAsync(60_000)
  stop.abort()
  await vi.advanceTimersByTimeAsync(200)
  await expect(waited).resolves.toBe("cancelled")
})
it("holds the message back while a Sheet of it failed, unless the person chose to send the file", () => {
  const base = { making: 0, checking: false, sinceMadeMs: 5000, ridesAlong: false, failed: 0 }
  expect(sheetSendGate(base)).toBe("ready")
  expect(sheetSendGate({ ...base, making: 1 })).toBe("wait")
  expect(sheetSendGate({ ...base, checking: true })).toBe("wait")
  expect(sheetSendGate({ ...base, sinceMadeMs: 200 })).toBe("wait")
  expect(sheetSendGate({ ...base, sinceMadeMs: 200, ridesAlong: true })).toBe("ready")
  expect(sheetSendGate({ ...base, failed: 1 })).toBe("failed")
  expect(sheetSendGate({ ...base, failed: 1, making: 1 }), "a retry under way is waited for").toBe("wait")
})

// A Sheet of the message whose standing choice waits only for its version (the editor is opening) is waited for.
it("holds the message while the Sheet it should carry is still opening", () => {
  const base = { making: 0, checking: false, sinceMadeMs: 5000, ridesAlong: false, failed: 0 }
  expect(sheetSendGate({ ...base, opening: true })).toBe("wait")
  expect(sheetSendGate({ ...base, opening: true, ridesAlong: true })).toBe("ready")
  expect(sheetSendGate({ ...base, opening: false })).toBe("ready")
})
