import type { SaveEvent } from "./save-state"

/**
 * Local-ahead writes waiting for a server receipt: FIFO, one in flight. A
 * write leaves the queue only after its receipt is accepted, and a retry sends
 * the same write object again, so its mutation id and body never change.
 */
export interface QueuedWrite { id: string; attempted: boolean }

/** How a failed send is treated. Terminal kinds halt the queue until `resume()`. */
export type SendFailure =
  | { kind: "transient" | "rate-limited"; retryAfterMs?: number }
  | { kind: "offline" | "conflict" | "rejected" | "unconfirmed" | "denied" }

export interface Pacing {
  /** Minimum time between the starts of two sends. */
  minGapMs: number
  baseDelayMs: number
  maxDelayMs: number
  /** After this long without a receipt the queue reports itself stuck… */
  retryBudgetMs: number
  /** …and retries only at this slower interval. */
  stuckRetryMs: number
}
export const DEFAULT_PACING: Pacing = { minGapMs: 250, baseDelayMs: 1000, maxDelayMs: 30_000, retryBudgetMs: 120_000, stuckRetryMs: 60_000 }
/** Failures after which the queue stops sending until `resume()`. */
export const haltsQueue = (kind: SendFailure["kind"]): kind is "conflict" | "rejected" | "unconfirmed" | "denied" => kind === "conflict" || kind === "rejected" || kind === "unconfirmed" || kind === "denied"
/** For callers that pace sends themselves (the Document poll loop). */
export const UNPACED: Pacing = { minGapMs: 0, baseDelayMs: 0, maxDelayMs: 0, retryBudgetMs: Infinity, stuckRetryMs: 0 }

export interface Timers { now(): number; setTimeout(run: () => void, ms: number): unknown; clearTimeout(handle: unknown): void }
const browserTimers: Timers = { now: () => Date.now(), setTimeout: (run, ms) => setTimeout(run, ms), clearTimeout: handle => clearTimeout(handle as ReturnType<typeof setTimeout>) }

export interface WriteQueueOptions<W extends QueuedWrite> {
  /** Persistence hook: `put` on every enqueue or merge, `remove` after the receipt. */
  persist?: { put(write: W): void; remove(write: W): void | Promise<void> }
  pacing?: Partial<Pacing>
  timers?: Timers
  /** Jitter source in [0, 1]; a delay is scaled into [50 %, 100 %]. */
  random?: () => number
  classify?: (cause: unknown) => SendFailure
  /** True when a receipt is a replay of the original (`duplicate: true`). */
  duplicate?: (result: unknown) => boolean
  onEvent?: (event: SaveEvent) => void
}

type Send<W> = (write: W) => Promise<unknown>
/** Throws to reject a receipt; returns false to leave the write queued without failing it. */
type Verify<W> = (result: unknown, write: W) => boolean | void

export interface WriteQueue<W extends QueuedWrite> {
  readonly size: number
  readonly halted: SendFailure["kind"] | undefined
  head(): W | undefined
  writes(): readonly W[]
  enqueue(write: W): void
  /** Drops a queued write that must never be sent again (the server refused it for good); never the one in flight. */
  remove(id: string): boolean
  /** Merges into the newest write while it was never sent; false when a new write is needed. */
  coalesce(merge: (newest: W) => void): boolean
  /** Milliseconds until the head may be sent; Infinity while one is in flight or the queue is halted. */
  wait(): number
  /** Sends the head once, unless one is in flight or the queue is halted. True once its receipt is accepted; a failure is recorded and rethrown. */
  deliver(send: Send<W>, verify?: Verify<W>): Promise<boolean>
  /** Drives `deliver` with the queue's own timers until stopped. */
  start(send: Send<W>, verify?: Verify<W>): void
  stop(): void
  resume(): void
}

export function createWriteQueue<W extends QueuedWrite>(options: WriteQueueOptions<W> = {}): WriteQueue<W> {
  const pacing = { ...DEFAULT_PACING, ...options.pacing }
  const timers = options.timers ?? browserTimers
  const random = options.random ?? Math.random
  const classify = options.classify ?? (() => ({ kind: "transient" }) as SendFailure)
  const emit = (event: SaveEvent) => options.onEvent?.(event)
  const writes: W[] = []
  let inflight = false, halted: SendFailure["kind"] | undefined
  let lastSendAt = -Infinity, retryAt = 0, failures = 0, failingSince: number | undefined
  let driver: { send: Send<W>; verify?: Verify<W> } | undefined, timer: unknown

  const backoff = () => Math.min(pacing.maxDelayMs, pacing.baseDelayMs * 2 ** (failures - 1)) * (0.5 + random() / 2)
  function record(cause: unknown) {
    const failure = classify(cause), now = timers.now()
    failures++; failingSince ??= now
    if (haltsQueue(failure.kind)) { halted = failure.kind; emit({ type: failure.kind }); return }
    const stuck = now - failingSince >= pacing.retryBudgetMs
    const retryAfterMs = "retryAfterMs" in failure ? failure.retryAfterMs : undefined
    retryAt = now + Math.max(pacing.minGapMs, retryAfterMs ?? (stuck ? pacing.stuckRetryMs : backoff()))
    if (failure.kind === "offline") emit({ type: "offline" })
    else if (failure.kind === "rate-limited") emit({ type: "rate-limited", retryAfterMs })
    else emit({ type: "transient-fail", retryAfterMs, stuck })
  }

  function pump() {
    timers.clearTimeout(timer); timer = undefined
    if (!driver || inflight || halted || !writes.length) return
    const ms = queue.wait()
    if (ms > 0) { timer = timers.setTimeout(pump, ms); return }
    void queue.deliver(driver.send, driver.verify).then(pump, pump)
  }

  const queue: WriteQueue<W> = {
    get size() { return writes.length },
    get halted() { return halted },
    head: () => writes[0],
    writes: () => [...writes],
    enqueue(write) { writes.push(write); options.persist?.put(write); pump() },
    remove(id) {
      const index = writes.findIndex(write => write.id === id)
      if (index < 0 || (index === 0 && inflight)) return false
      const [write] = writes.splice(index, 1)
      void options.persist?.remove(write)
      return true
    },
    coalesce(merge) {
      const newest = writes.at(-1)
      if (!newest || newest.attempted) return false
      merge(newest); options.persist?.put(newest)
      return true
    },
    wait() {
      if (inflight || halted) return Infinity
      const now = timers.now()
      return Math.max(0, retryAt - now, lastSendAt + pacing.minGapMs - now)
    },
    async deliver(send, verify) {
      const write = writes[0]
      if (!write || inflight || halted) return false
      inflight = true; write.attempted = true; lastSendAt = timers.now()
      emit({ type: "send" })
      let accepted = false, duplicate = false
      try {
        const result = await send(write)
        accepted = verify?.(result, write) !== false
        duplicate = accepted && Boolean(options.duplicate?.(result))
      } catch (cause) {
        inflight = false; record(cause)
        throw cause
      }
      inflight = false
      // A declined receipt is not a failure, but the same write must not be resent at the minimum gap.
      if (!accepted) { failures++; retryAt = timers.now() + Math.max(pacing.minGapMs, backoff()); return false }
      writes.shift(); failures = 0; failingSince = undefined; retryAt = 0
      emit({ type: duplicate ? "duplicate-ack" : "ack", pending: writes.length })
      await options.persist?.remove(write)
      return true
    },
    start(send, verify) { driver = { send, verify }; pump() },
    stop() { driver = undefined; timers.clearTimeout(timer); timer = undefined },
    resume() { halted = undefined; failures = 0; failingSince = undefined; retryAt = 0; pump() },
  }
  return queue
}
