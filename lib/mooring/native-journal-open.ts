import { DocumentStorageBusy } from "@/lib/document-queue"

/** Total open attempts for the private draft journal of one take-over. */
export const JOURNAL_OPEN_ATTEMPTS = 3
/** Pauses between attempts; a slow disk often answers the next open. */
export const JOURNAL_OPEN_BACKOFF_MS = [500, 1500] as const
/** One attempt's own deadline. The storage layer bounds its open too; this
 * also covers a journal wrapper that never settles. */
export const JOURNAL_OPEN_ATTEMPT_MS = 5000

export class JournalOpenAbandoned extends Error {
  constructor() { super("journal_open_abandoned") }
}

type Closable = { close(): Promise<void> | void }

/**
 * Opens the private draft journal with a bounded number of attempts.
 *
 * An attempt that misses its deadline is abandoned: if its journal arrives
 * later it is closed at once and never returned, so a late answer cannot
 * unlock an editor or touch whoever holds the document by then. `alive`
 * is checked before every attempt and after every answer; once it is false
 * nothing more is opened and an opened journal is closed.
 */
export async function openJournalWithRetry<J extends Closable>(
  open: (signal: AbortSignal) => Promise<J>,
  options: { alive: () => boolean; signal?: AbortSignal; attempts?: number; backoffMs?: readonly number[]; attemptMs?: number; sleep?: (ms: number) => Promise<void> },
): Promise<J> {
  const attempts = options.attempts ?? JOURNAL_OPEN_ATTEMPTS
  const backoff = options.backoffMs ?? JOURNAL_OPEN_BACKOFF_MS
  const attemptMs = options.attemptMs ?? JOURNAL_OPEN_ATTEMPT_MS
  const sleep = options.sleep ?? ((ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms)))
  let last: unknown
  for (let attempt = 0; attempt < attempts; attempt++) {
    if (attempt > 0) await sleep(backoff[Math.min(attempt - 1, backoff.length - 1)] ?? 0)
    if (!options.alive() || options.signal?.aborted) throw new JournalOpenAbandoned()
    // Each attempt is cancelled when it misses its deadline or the editor goes
    // away, so the storage layer releases its lock, IDB connection and key request.
    const attemptAbort = new AbortController()
    const follow = () => attemptAbort.abort()
    options.signal?.addEventListener("abort", follow, { once: true })
    try {
      const journal = await bounded(open(attemptAbort.signal), attemptMs, attemptAbort)
      if (!options.alive() || options.signal?.aborted) { await Promise.resolve(journal.close()).catch(() => {}); throw new JournalOpenAbandoned() }
      return journal
    } catch (cause) {
      if (options.signal?.aborted) throw new JournalOpenAbandoned()
      if (cause instanceof JournalOpenAbandoned) throw cause
      last = cause
      // Another tab of this browser is the active writer: a retry cannot help.
      if (cause instanceof DocumentStorageBusy) break
    } finally { options.signal?.removeEventListener("abort", follow) }
  }
  throw last instanceof Error ? last : new Error("journal_open_failed")
}

class AttemptExpired extends Error {
  constructor() { super("journal_open_attempt_expired") }
}

function bounded<J extends Closable>(opening: Promise<J>, ms: number, attempt: AbortController): Promise<J> {
  return new Promise<J>((resolve, reject) => {
    let settled = false
    const timer = setTimeout(() => { settled = true; attempt.abort(); reject(new AttemptExpired()) }, ms)
    opening.then(
      journal => {
        // Arrived after the deadline: nobody will use it.
        if (settled) { void Promise.resolve(journal.close()).catch(() => {}); return }
        settled = true; clearTimeout(timer); resolve(journal)
      },
      cause => { if (settled) return; settled = true; clearTimeout(timer); reject(cause) },
    )
    // An opener that ignores its signal still settles this attempt at once.
    attempt.signal.addEventListener("abort", () => { if (settled) return; settled = true; clearTimeout(timer); reject(new AttemptExpired()) }, { once: true })
  })
}
