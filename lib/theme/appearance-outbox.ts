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
 * - On unload whatever is left goes out with `keepalive`.
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
  schedule: (fn: () => Promise<void>, ms: number) => void
  onStatus: (s: OutboxStatus) => void
}

function transient(status: number): boolean {
  return status >= 500 || status === 401 || status === 408 || status === 429
}

export function createAppearanceOutbox(deps: OutboxDeps) {
  let entries: Entry[] = load()
  let attempt = 0
  let running: Promise<void> | null = null
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

  async function run(): Promise<void> {
    for (const e of [...entries]) {
      if (e.failed) continue
      let res: { ok: boolean; status: number }
      try {
        res = await deps.send(e.key, e.body, false)
      } catch {
        res = { ok: false, status: 0 }
      }
      if (res.ok) {
        entries = entries.filter((x) => x !== e)
        save()
        continue
      }
      if (res.status === 0 || transient(res.status)) {
        // Stop here so the order holds: a later key never overtakes an earlier one.
        if (attempt < RETRY_DELAYS_MS.length) deps.schedule(() => flush(), RETRY_DELAYS_MS[attempt++])
        report()
        return
      }
      e.failed = true
      save()
    }
    attempt = 0
    report()
  }

  function flush(): Promise<void> {
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
      for (const e of entries) if (!e.failed) void deps.send(e.key, e.body, true).catch(() => undefined)
    },
    pending(key: string): boolean {
      return entries.some((e) => e.key === key)
    },
    status,
  }
}

export type AppearanceOutbox = ReturnType<typeof createAppearanceOutbox>
