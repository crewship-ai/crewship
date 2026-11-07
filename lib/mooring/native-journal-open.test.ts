import { afterEach, describe, expect, it, vi } from "vitest"
import { DocumentStorageBusy, DocumentStorageError } from "@/lib/document-queue"
import { JournalOpenAbandoned, openJournalWithRetry } from "./native-journal-open"

const journal = () => ({ close: vi.fn().mockResolvedValue(undefined) })
const deferred = <T,>() => { let resolve!: (v: T) => void, reject!: (e: unknown) => void; const promise = new Promise<T>((r, f) => { resolve = r; reject = f }); return { promise, resolve, reject } }
const noSleep = () => Promise.resolve()

afterEach(() => { vi.useRealTimers() })

describe("openJournalWithRetry (#5104)", () => {
  it("returns the first journal that opens and stops trying", async () => {
    const j = journal(), open = vi.fn().mockRejectedValueOnce(new DocumentStorageError("slow")).mockResolvedValueOnce(j)
    await expect(openJournalWithRetry(open, { alive: () => true, sleep: noSleep })).resolves.toBe(j)
    expect(open).toHaveBeenCalledTimes(2)
  })

  it("gives up after the bounded number of attempts with the storage error", async () => {
    const failure = new DocumentStorageError("unavailable"), open = vi.fn().mockRejectedValue(failure)
    await expect(openJournalWithRetry(open, { alive: () => true, sleep: noSleep })).rejects.toBe(failure)
    expect(open).toHaveBeenCalledTimes(3)
  })

  it("does not retry when another tab is the active writer", async () => {
    const open = vi.fn().mockRejectedValue(new DocumentStorageBusy("busy"))
    await expect(openJournalWithRetry(open, { alive: () => true, sleep: noSleep })).rejects.toBeInstanceOf(DocumentStorageBusy)
    expect(open).toHaveBeenCalledOnce()
  })

  it("opens nothing more once the editor is gone", async () => {
    let alive = true
    const open = vi.fn().mockImplementation(async () => { alive = false; throw new DocumentStorageError("slow") })
    await expect(openJournalWithRetry(open, { alive: () => alive, sleep: noSleep })).rejects.toBeInstanceOf(JournalOpenAbandoned)
    expect(open).toHaveBeenCalledOnce()
  })

  it("closes a journal that opens after the editor is gone and never returns it", async () => {
    let alive = true
    const j = journal(), open = vi.fn().mockImplementation(async () => { alive = false; return j })
    await expect(openJournalWithRetry(open, { alive: () => alive, sleep: noSleep })).rejects.toBeInstanceOf(JournalOpenAbandoned)
    expect(j.close).toHaveBeenCalledOnce()
  })

  it("abandons an attempt past its deadline; its late journal is closed, the next attempt's is returned", async () => {
    vi.useFakeTimers()
    const late = deferred<ReturnType<typeof journal>>(), first = journal(), second = journal()
    const open = vi.fn().mockReturnValueOnce(late.promise).mockResolvedValueOnce(second)
    const result = openJournalWithRetry(open, { alive: () => true, attemptMs: 5000, backoffMs: [500] })
    await vi.advanceTimersByTimeAsync(5000)
    await vi.advanceTimersByTimeAsync(500)
    await expect(result).resolves.toBe(second)
    late.resolve(first)
    await vi.runAllTimersAsync()
    expect(first.close).toHaveBeenCalledOnce()
    expect(second.close).not.toHaveBeenCalled()
    expect(open).toHaveBeenCalledTimes(2)
  })

  it("a missed deadline aborts that attempt's signal, so the storage layer lets go", async () => {
    vi.useFakeTimers()
    const signals: AbortSignal[] = [], j = journal()
    const open = vi.fn().mockImplementationOnce((signal: AbortSignal) => { signals.push(signal); return new Promise(() => {}) }).mockImplementationOnce(async (signal: AbortSignal) => { signals.push(signal); return j })
    const result = openJournalWithRetry(open, { alive: () => true, attemptMs: 5000, backoffMs: [500] })
    await vi.advanceTimersByTimeAsync(5500)
    await expect(result).resolves.toBe(j)
    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)
  })

  it("an editor that goes away aborts the attempt in flight and opens nothing more", async () => {
    const editor = new AbortController(), signals: AbortSignal[] = []
    const open = vi.fn().mockImplementation((signal: AbortSignal) => { signals.push(signal); return new Promise((_, reject) => signal.addEventListener("abort", () => reject(new DocumentStorageError("aborted")))) })
    const result = openJournalWithRetry(open, { alive: () => !editor.signal.aborted, signal: editor.signal, sleep: noSleep })
    await Promise.resolve()
    editor.abort()
    await expect(result).rejects.toBeInstanceOf(JournalOpenAbandoned)
    expect(signals[0].aborted).toBe(true)
    expect(open).toHaveBeenCalledOnce()
  })

  it("a late journal after the last attempt failed is closed and the failure stands", async () => {
    vi.useFakeTimers()
    const late = deferred<ReturnType<typeof journal>>(), j = journal()
    const open = vi.fn().mockReturnValue(late.promise)
    const result = openJournalWithRetry(open, { alive: () => true, attempts: 1, attemptMs: 5000 })
    const settled = expect(result).rejects.toThrow("journal_open_attempt_expired")
    await vi.advanceTimersByTimeAsync(5000)
    await settled
    late.resolve(j)
    await vi.runAllTimersAsync()
    expect(j.close).toHaveBeenCalledOnce()
  })
})
