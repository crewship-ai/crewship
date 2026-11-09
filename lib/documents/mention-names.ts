import { apiFetch } from "@/lib/api-fetch"

/**
 * The one rule for showing a schema-5/6 mention, on the Page and on the Canvas:
 * a name only when the Document's people lookup (GET …/document/people?ids=)
 * names that person to this reader, otherwise "@Unknown member". The server
 * releases a name only for someone who can read this Document, and only to a
 * reader of it; this client never uses the workspace member list.
 *
 * Ids rendered in the same turn are asked together, in requests of at most 100
 * ids (the lookup's bound). Each surface holds the ids it shows (`want`, then
 * `release`); an answer for a held id is asked again once it is REFRESH_MS
 * old, so a reader who lost access stops being named without a reload. A rate
 * limit is waited out as the server asks and a network or server failure is
 * retried with backoff, in at most MAX_ATTEMPTS requests; after that, or on a
 * refusal (401/403/404), the mention shows the neutral label.
 */

export const UNKNOWN_MEMBER = "Unknown member"
const ID = /^[A-Za-z0-9_-]{1,64}$/
const BATCH = 100
export const REFRESH_MS = 5 * 60_000
const TICK_MS = 30_000
const MAX_ATTEMPTS = 5

export interface MentionNames {
  /** The name this reader may see, or undefined while unknown or not released. */
  name(userId: string): string | undefined
  /** A surface shows these ids: ask for any not yet answered, and keep them fresh until released. */
  want(userIds: Iterable<string>): void
  /** The surface no longer shows these ids (once per `want`). */
  release(userIds: Iterable<string>): void
  subscribe(listener: () => void): () => void
  /** Changes whenever a shown name changes, for external-store reads. */
  version(): number
  /** Stops pending and periodic lookups (the session ended); a later `want` starts again. */
  dispose(): void
}

type Fetcher = (url: string, init?: RequestInit) => Promise<Response>

export function mentionLabel(names: MentionNames | undefined, userId: string): string {
  return "@" + (names?.name(userId) || UNKNOWN_MEMBER)
}

export function createMentionNames(workspaceId: string, pageId: string, fetcher: Fetcher = apiFetch): MentionNames {
  const base = `/api/v1/pages/${encodeURIComponent(`@${pageId}`)}/document/people`
  const settled = new Map<string, { name: string | null; at: number }>()
  const attempts = new Map<string, number>(), held = new Map<string, number>()
  const queued = new Set<string>(), asking = new Set<string>()
  const listeners = new Set<() => void>()
  let version = 0, quietUntil = 0, disposed = false
  let timer: ReturnType<typeof setTimeout> | undefined, ticker: ReturnType<typeof setInterval> | undefined
  const notify = () => { version++; for (const listener of [...listeners]) listener() }
  // A new id never cuts a wait short.
  const schedule = (delay: number) => {
    if (timer !== undefined || disposed) return
    timer = setTimeout(() => { timer = undefined; void flush() }, Math.max(delay, quietUntil - Date.now()))
  }
  /** Records an answer; true when what this id shows changed. */
  const settle = (id: string, name: string | null) => {
    const before = settled.get(id)
    settled.set(id, { name, at: Date.now() }); attempts.delete(id)
    return (before?.name ?? null) !== name
  }
  /** Asks for one batch: how long to wait before the next request, whether the batch must be asked again, and whether a shown name changed. */
  const ask = async (ids: string[]): Promise<{ wait: number; again: boolean; changed: boolean }> => {
    let response: Response
    try {
      response = await fetcher(`${base}?ids=${encodeURIComponent(ids.join(","))}&workspace_id=${encodeURIComponent(workspaceId)}`, { cache: "no-store" })
    } catch { response = new Response(null, { status: 599 }) }
    let changed = false
    if (response.status === 429 || response.status >= 500) {
      const tries = Math.max(...ids.map(id => (attempts.get(id) ?? 0) + 1))
      ids.forEach(id => attempts.set(id, tries))
      const after = Number(response.headers.get("Retry-After"))
      const wait = response.status === 429 && Number.isFinite(after) && after > 0 ? Math.min(after, 60) * 1000 : Math.min(2 ** tries, 60) * 1000
      if (tries < MAX_ATTEMPTS) return { wait, again: true, changed }
      // Given up: no name is kept on an answer that could not be confirmed, and the wait still applies.
      for (const id of ids) changed = settle(id, null) || changed
      return { wait, again: false, changed }
    }
    const named = new Map<string, string>()
    if (response.ok) {
      const body = await response.json().catch(() => null) as { people?: unknown } | null
      for (const person of Array.isArray(body?.people) ? body.people : []) {
        const { id, name } = (person ?? {}) as { id?: unknown; name?: unknown }
        if (typeof id === "string" && typeof name === "string" && name.trim() && ids.includes(id)) named.set(id, name)
      }
    }
    for (const id of ids) changed = settle(id, named.get(id) ?? null) || changed
    return { wait: 0, again: false, changed }
  }
  const flush = async () => {
    const ids = [...queued].sort()
    queued.clear()
    let wait = 0, changed = false
    for (let start = 0; start < ids.length; start += BATCH) {
      const batch = ids.slice(start, start + BATCH)
      if (wait || disposed) { if (!disposed) batch.forEach(id => queued.add(id)); continue }
      batch.forEach(id => asking.add(id))
      const answer = await ask(batch)
      batch.forEach(id => asking.delete(id))
      wait = answer.wait; changed = answer.changed || changed
      if (answer.again && !disposed) batch.forEach(id => queued.add(id))
    }
    if (wait) quietUntil = Date.now() + wait
    if (changed) notify()
    if (queued.size) schedule(wait)
  }
  const stale = (id: string) => { const entry = settled.get(id); return !entry || Date.now() - entry.at >= REFRESH_MS }
  const ask_ = (ids: Iterable<string>) => {
    for (const id of ids) if (ID.test(id) && stale(id) && !asking.has(id)) queued.add(id)
    if (queued.size) schedule(0)
  }
  // Held ids whose answer has grown old are asked again; the old name stays until the answer.
  const tick = () => ask_(held.keys())
  const syncTicker = () => {
    if (held.size && !disposed && ticker === undefined) ticker = setInterval(tick, TICK_MS)
    if ((!held.size || disposed) && ticker !== undefined) { clearInterval(ticker); ticker = undefined }
  }
  return {
    name: userId => settled.get(userId)?.name ?? undefined,
    want(userIds) {
      disposed = false
      const ids = [...userIds]
      for (const id of ids) {
        if (!ID.test(id)) { settled.set(id, { name: null, at: Infinity }); continue }
        held.set(id, (held.get(id) ?? 0) + 1)
      }
      syncTicker()
      ask_(ids)
    },
    release(userIds) {
      for (const id of userIds) {
        const count = held.get(id)
        if (count === undefined) continue
        if (count > 1) held.set(id, count - 1); else held.delete(id)
      }
      syncTicker()
    },
    subscribe(listener) { listeners.add(listener); return () => { listeners.delete(listener) } },
    version: () => version,
    dispose() {
      disposed = true
      if (timer !== undefined) clearTimeout(timer)
      timer = undefined
      queued.clear(); syncTicker()
    },
  }
}
