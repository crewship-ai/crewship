import { describe, it, expect, vi } from "vitest"
import {
  cancelJobsForKey,
  cancelJobsForScope,
  currentScopeEpoch,
  enterScope,
  queueFor,
} from "@/lib/chat-attachment-jobs"

/** Two tasks on one queue: the first is held, the second waits behind it. */
function held(key: string) {
  let release: () => void = () => {}
  const first = vi.fn(() => new Promise<void>((r) => (release = r)))
  const second = vi.fn(async () => {})
  const q = queueFor(key)
  const p1 = q.enqueue("1", first)
  const p2 = q.enqueue("2", second)
  return { first, second, release: () => release(), done: Promise.all([p1, p2]) }
}

describe("attachment job guards, one at a time", () => {
  it("cancelling a key skips the queued task (cancelAll, not the epoch)", async () => {
    const h = held("u::w::chat")
    await Promise.resolve()
    cancelJobsForKey("u::w::chat")
    h.release()
    await h.done
    expect(h.first).toHaveBeenCalled()
    expect(h.second).not.toHaveBeenCalled()
  })

  it("a key match is exact: chat-1 does not cancel chat-10", async () => {
    const h = held("u::w::chat-10")
    await Promise.resolve()
    cancelJobsForKey("u::w::chat-1")
    h.release()
    await h.done
    expect(h.second).toHaveBeenCalled()
  })

  it("a scope cancels every key under its delimited prefix only", async () => {
    const mine = held("u::w::c1")
    const other = held("u2::w::c1")
    await Promise.resolve()
    cancelJobsForScope("u::w::")
    mine.release()
    other.release()
    await Promise.all([mine.done, other.done])
    expect(mine.second).not.toHaveBeenCalled()
    expect(other.second).toHaveBeenCalled()
  })

  it("a prefix without the delimiter is refused", () => {
    expect(() => cancelJobsForScope("u")).toThrow()
    expect(() => cancelJobsForScope("")).toThrow()
  })

  it("entering a scope bumps the epoch only when the scope changes", () => {
    enterScope("a::w::")
    const e = currentScopeEpoch()
    expect(enterScope("a::w::").changed).toBe(false)
    expect(currentScopeEpoch()).toBe(e)
    const r = enterScope("b::w::")
    expect(r).toEqual({ changed: true, previous: "a::w::" })
    expect(currentScopeEpoch()).toBe(e + 1)
    enterScope("a::w::") // A -> B -> A is three distinct epochs
    expect(currentScopeEpoch()).toBe(e + 2)
  })
})
