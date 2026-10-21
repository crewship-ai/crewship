import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { SaveEvent } from "../save-state"
import { createWriteQueue, type SendFailure, type WriteQueueOptions } from "../write-queue"

interface Row { id: string; attempted: boolean; body: string }
const row = (id: string, body = id): Row => ({ id, attempted: false, body })

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

function setup(options: Partial<WriteQueueOptions<Row>> = {}) {
  const events: SaveEvent[] = []
  const put = vi.fn(), remove = vi.fn()
  const queue = createWriteQueue<Row>({ persist: { put, remove }, onEvent: event => events.push(event), random: () => 1, ...options })
  return { queue, events, put, remove }
}

describe("FIFO with acknowledgement", () => {
  it("sends the oldest write first and removes it only after the receipt", async () => {
    const { queue, events, remove } = setup()
    queue.enqueue(row("a")); queue.enqueue(row("b"))
    let resolve!: (value: string) => void
    const sent = queue.deliver(write => new Promise<string>(done => { expect(write.id).toBe("a"); resolve = done }))
    expect(queue.size).toBe(2)
    expect(remove).not.toHaveBeenCalled()
    resolve("ok")
    expect(await sent).toBe(true)
    expect(queue.writes().map(write => write.id)).toEqual(["b"])
    expect(remove).toHaveBeenCalledWith(expect.objectContaining({ id: "a" }))
    expect(events).toEqual([{ type: "send" }, { type: "ack", pending: 1 }])
  })
  it("keeps one write in flight", async () => {
    const { queue } = setup()
    queue.enqueue(row("a"))
    const send = vi.fn(() => new Promise<void>(() => {}))
    void queue.deliver(send)
    expect(await queue.deliver(send)).toBe(false)
    expect(send).toHaveBeenCalledTimes(1)
    expect(queue.wait()).toBe(Infinity)
  })
  it("reports a replayed receipt as a duplicate ack", async () => {
    const { queue, events } = setup({ duplicate: (result: unknown) => (result as { duplicate?: boolean }).duplicate === true })
    queue.enqueue(row("a"))
    await queue.deliver(async () => ({ duplicate: true }))
    expect(events.at(-1)).toEqual({ type: "duplicate-ack", pending: 0 })
  })
  it("does not acknowledge when the caller declines the receipt", async () => {
    const { queue, events, remove } = setup()
    queue.enqueue(row("a"))
    expect(await queue.deliver(async () => "late", () => false)).toBe(false)
    expect(queue.size).toBe(1)
    expect(remove).not.toHaveBeenCalled()
    expect(events).toEqual([{ type: "send" }])
  })
  it("treats a receipt that fails verification as a failure", async () => {
    const { queue, events } = setup({ classify: () => ({ kind: "unconfirmed" }) })
    queue.enqueue(row("a"))
    await expect(queue.deliver(async () => "x", () => { throw new Error("bad receipt") })).rejects.toThrow("bad receipt")
    expect(queue.size).toBe(1)
    expect(events.at(-1)).toEqual({ type: "unconfirmed" })
  })
})

describe("coalescing and persistence", () => {
  it("merges into the newest write only while it was never sent", async () => {
    const { queue, put } = setup()
    queue.enqueue(row("a"))
    expect(queue.coalesce(newest => { newest.body += "+1" })).toBe(true)
    expect(queue.head()?.body).toBe("a+1")
    expect(put).toHaveBeenCalledTimes(2)
    void queue.deliver(() => new Promise(() => {}))
    expect(queue.coalesce(newest => { newest.body += "+2" })).toBe(false)
    expect(queue.head()?.body).toBe("a+1")
  })
})

describe("retry keeps the mutation identity", () => {
  it("resends the same write object, id and body", async () => {
    const { queue } = setup()
    queue.enqueue(row("a", "body"))
    const seen: Row[] = []
    const send = vi.fn(async (write: Row) => { seen.push(write); if (seen.length === 1) throw new Error("503") })
    await expect(queue.deliver(send)).rejects.toThrow("503")
    await vi.advanceTimersByTimeAsync(60_000)
    await queue.deliver(send)
    expect(seen).toHaveLength(2)
    expect(seen[1]).toBe(seen[0])
    expect(seen[1]).toMatchObject({ id: "a", body: "body", attempted: true })
  })
})

describe("pacing", () => {
  const pacing = { minGapMs: 100, baseDelayMs: 1000, maxDelayMs: 8000, retryBudgetMs: 10_000, stuckRetryMs: 30_000 }
  async function failOnce(queue: ReturnType<typeof setup>["queue"]) { await queue.deliver(async () => { throw new Error("no") }).catch(() => {}) }

  it.each([
    ["first failure, full jitter top", 1, () => 1, 1000],
    ["first failure, jitter floor", 1, () => 0, 500],
    ["third failure", 3, () => 1, 4000],
    ["capped", 6, () => 1, 8000],
  ] as const)("%s", async (_name, failures, random, expected) => {
    const { queue } = setup({ pacing: { ...pacing, retryBudgetMs: Infinity }, random })
    queue.enqueue(row("a"))
    for (let attempt = 0; attempt < failures; attempt++) { await vi.advanceTimersByTimeAsync(queue.wait()); await failOnce(queue) }
    expect(queue.wait()).toBe(expected)
  })
  it("keeps a minimum gap between sends", async () => {
    const { queue } = setup({ pacing })
    queue.enqueue(row("a")); queue.enqueue(row("b"))
    await queue.deliver(async () => {})
    expect(queue.wait()).toBe(100)
    await vi.advanceTimersByTimeAsync(100)
    expect(queue.wait()).toBe(0)
  })
  it.each([
    ["transient", { kind: "transient", retryAfterMs: 7000 }, { type: "transient-fail", retryAfterMs: 7000, stuck: false }],
    ["rate limited", { kind: "rate-limited", retryAfterMs: 7000 }, { type: "rate-limited", retryAfterMs: 7000 }],
  ] as [string, SendFailure, SaveEvent][])("honours retryAfterMs when %s", async (_name, failure, event) => {
    const { queue, events } = setup({ pacing, classify: () => failure })
    queue.enqueue(row("a"))
    await failOnce(queue)
    expect(queue.wait()).toBe(7000)
    expect(events.at(-1)).toEqual(event)
  })
  it("surfaces a spent retry budget and slows to the stuck interval", async () => {
    const { queue, events } = setup({ pacing })
    queue.enqueue(row("a"))
    await failOnce(queue)
    await vi.advanceTimersByTimeAsync(10_000)
    await failOnce(queue)
    expect(events.at(-1)).toEqual({ type: "transient-fail", retryAfterMs: undefined, stuck: true })
    expect(queue.wait()).toBe(30_000)
  })
  it.each(["conflict", "rejected", "denied", "unconfirmed"] as const)("halts on %s until resumed", async kind => {
    const { queue, events } = setup({ pacing, classify: () => ({ kind }) })
    queue.enqueue(row("a"))
    await failOnce(queue)
    expect(events.at(-1)).toEqual({ type: kind })
    expect(queue.wait()).toBe(Infinity)
    expect(queue.halted).toBe(kind)
    expect(await queue.deliver(async () => {})).toBe(false)
    queue.resume()
    await vi.advanceTimersByTimeAsync(pacing.minGapMs)
    expect(queue.wait()).toBe(0)
  })
  it("reports offline and keeps retrying", async () => {
    const { queue, events } = setup({ pacing, classify: () => ({ kind: "offline" }) })
    queue.enqueue(row("a"))
    await failOnce(queue)
    expect(events.at(-1)).toEqual({ type: "offline" })
    expect(queue.wait()).toBe(1000)
  })
})

describe("driven by its own timers", () => {
  it("drains the queue in order, retrying failures with backoff", async () => {
    const { queue, events } = setup({ pacing: { minGapMs: 50, baseDelayMs: 1000, maxDelayMs: 1000, retryBudgetMs: 60_000, stuckRetryMs: 60_000 } })
    const sent: string[] = []
    let fail = true
    queue.start(async write => { sent.push(write.id); if (write.id === "b" && fail) { fail = false; throw new Error("503") } })
    queue.enqueue(row("a")); queue.enqueue(row("b")); queue.enqueue(row("c"))
    await vi.advanceTimersByTimeAsync(0)
    expect(sent).toEqual(["a"])
    await vi.advanceTimersByTimeAsync(50) // the minimum gap
    expect(sent).toEqual(["a", "b"])
    await vi.advanceTimersByTimeAsync(999) // backoff after the failure
    expect(sent).toEqual(["a", "b"])
    await vi.advanceTimersByTimeAsync(1)
    expect(sent).toEqual(["a", "b", "b"])
    await vi.advanceTimersByTimeAsync(50)
    expect(sent).toEqual(["a", "b", "b", "c"])
    expect(queue.size).toBe(0)
    expect(events.filter(event => event.type === "ack")).toHaveLength(3)
    queue.stop()
  })
  it("stops sending after stop()", async () => {
    const { queue } = setup()
    const send = vi.fn(async () => {})
    queue.start(send); queue.stop()
    queue.enqueue(row("a"))
    await vi.advanceTimersByTimeAsync(10_000)
    expect(send).not.toHaveBeenCalled()
  })
})
