/**
 * Server writes for the appearance preference keys (#3334).
 *
 * Picking a style changes two keys: `appearance` (theme, accent …) and
 * `appearance.style`. Both are already applied locally before anything goes
 * to the network; this outbox only gets them to the server, reliably and in a
 * fixed order — `appearance` first, then `appearance.style` — so a device that
 * reads a half-finished pair sees the new accent with the old style rather
 * than a style without the theme it needs (and lib/theme/appearance-style
 * renders Harbor for that pair until the rest arrives).
 *
 * - One pending body per key; a newer one replaces it.
 * - Pending writes are kept in localStorage and replayed on the next load.
 * - A transient failure (network, 5xx, 401, 408, 429) is retried after 5 s,
 *   30 s and 2 min, then waits for a kick (page load, focus, back online).
 * - Any other 4xx is permanent: the entry stays, flagged, the status becomes
 *   "failed" ("Not synced · Retry") and the local look is NOT rolled back.
 *   A failed entry blocks every entry after it: `appearance.style` is never
 *   written over an `appearance` the server refused.
 * - The queue is read afresh after every request, so a value picked while a
 *   request is on the wire goes out in the same run.
 * - `pause()` (sync switched off) cancels a scheduled retry and stops the run
 *   after the request already on the wire; nothing is sent until `resume()`.
 * - On unload whatever is left goes out with `keepalive` (not while paused).
 */

export const OUTBOX_STORAGE_KEY = "crewship-appearance-outbox"
export const RETRY_DELAYS_MS = [5000, 30000, 120000] as const

/** Write order: lower first. Unknown keys go last. */
const ORDER: Record<string, number> = { appearance: 0, "appearance.style": 1 }

export type OutboxStatus = "synced" | "pending" | "failed"

interface Entry {
  key: string
  body: string
  failed?: boolean
}

interface StorageLike {
  getItem(k: string): string | null
  setItem(k: string, v: string): void
}

export interface OutboxDeps {
  storage: StorageLike
  send: (key: string, body: string, keepalive: boolean) => Promise<{ ok: boolean; status: number }>
  /** Runs `fn` after `ms`; returns a function that cancels it. */
  schedule: (fn: () => Promise<void>, ms: number) => (() => void) | void
  onStatus: (s: OutboxStatus) => void
}

function transient(status: number): boolean {
  return status >= 500 || status === 401 || status === 408 || status === 429
}

export function createAppearanceOutbox(deps: OutboxDeps) {
  let entries: Entry[] = load()
  let attempt = 0
  let running: Promise<void> | null = null
  let paused = false
  let cancelRetry: (() => void) | null = null
  let last: OutboxStatus = entries.length ? (entries.some((e) => e.failed) ? "failed" : "pending") : "synced"

  function load(): Entry[] {
    try {
      const raw = deps.storage.getItem(OUTBOX_STORAGE_KEY)
      const v = raw ? JSON.parse(raw) : []
      return Array.isArray(v) ? v.filter((e) => e && typeof e.key === "string" && typeof e.body === "string") : []
    } catch {
      return []
    }
  }
  function save(): void {
    try {
      deps.storage.setItem(OUTBOX_STORAGE_KEY, JSON.stringify(entries))
    } catch {
      // Blocked storage: pending writes live for this page only.
    }
  }
  function sort(): void {
    entries.sort((a, b) => (ORDER[a.key] ?? 9) - (ORDER[b.key] ?? 9))
  }
  function status(): OutboxStatus {
    if (entries.some((e) => e.failed)) return "failed"
    return entries.length ? "pending" : "synced"
  }
  function report(): void {
    const s = status()
    if (s !== last) {
      last = s
      deps.onStatus(s)
    }
  }

  function scheduleRetry(): void {
    if (attempt >= RETRY_DELAYS_MS.length) return
    cancelRetry?.()
    cancelRetry = deps.schedule(() => {
      cancelRetry = null
      return flush()
    }, RETRY_DELAYS_MS[attempt++]) || null
  }

  async function run(): Promise<void> {
    // Always the head of the (sorted) queue, re-read after every request.
    for (;;) {
      if (paused) break
      const e = entries[0]
      // Nothing left, or the head was refused: it blocks everything after it.
      if (!e || e.failed) break
      let res: { ok: boolean; status: number }
      try {
        res = await deps.send(e.key, e.body, false)
      } catch {
        res = { ok: false, status: 0 }
      }
      if (res.ok) {
        // A newer body for the same key may have replaced `e` meanwhile; it
        // stays queued and goes out on the next turn of this loop.
        entries = entries.filter((x) => x !== e)
        save()
        continue
      }
      if (res.status === 0 || transient(res.status)) {
        if (!paused) scheduleRetry()
        report()
        return
      }
      // The server refused this exact body. A newer one for the key still
      // deserves a try; only the refused body is flagged.
      if (entries.includes(e)) {
        e.failed = true
        save()
      }
    }
    attempt = 0
    report()
  }

  function flush(): Promise<void> {
    if (paused) return Promise.resolve()
    if (!running) running = run().finally(() => (running = null))
    return running
  }

  return {
    enqueue(key: string, body: string): void {
      entries = entries.filter((e) => e.key !== key)
      entries.push({ key, body })
      sort()
      save()
      report()
    },
    flush,
    /** Page load, focus, back online: start over with the retry ladder. */
    kick(): Promise<void> {
      attempt = 0
      return flush()
    },
    /** "Not synced · Retry". */
    retry(): Promise<void> {
      for (const e of entries) delete e.failed
      attempt = 0
      save()
      return flush()
    },
    flushKeepalive(): void {
      if (paused) return
      // Same order rule as a run: stop at the first refused entry.
      for (const e of entries) {
        if (e.failed) break
        void deps.send(e.key, e.body, true).catch(() => undefined)
      }
    },
    /** Sync switched off: no request leaves until `resume()`. */
    pause(): void {
      paused = true
      cancelRetry?.()
      cancelRetry = null
    },
    /** Sync switched on again; call `kick()` to send what is owed. */
    resume(): void {
      paused = false
    },
    pending(key: string): boolean {
      return entries.some((e) => e.key === key)
    },
    status,
  }
}

export type AppearanceOutbox = ReturnType<typeof createAppearanceOutbox>
