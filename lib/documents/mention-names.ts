import { apiFetch } from "@/lib/api-fetch"

/**
 * The one rule for showing a schema-5/6 mention, on the Page and on the Canvas:
 * a name only when the Document's people lookup (GET …/document/people?ids=)
 * names that person to this reader, otherwise "@Unknown member". The server
 * releases a name only for someone who can read this Document, and only to a
 * reader of it; this client never uses the workspace member list.
 *
 * Ids rendered in the same turn are asked together, in requests of at most 100
 * ids (the lookup's bound). An answer is kept for this session and asked again
 * after REFRESH_MS while anything still shows it, so a revoked reader's name
 * disappears without a reload. A rate limit is waited out as the server asks,
 * and a network or server failure is retried with backoff, both at most
 * MAX_ATTEMPTS times; a refusal (401/403/404) leaves the neutral label.
 */

export const UNKNOWN_MEMBER = "Unknown member"
const ID = /^[A-Za-z0-9_-]{1,64}$/
const BATCH = 100
export const REFRESH_MS = 5 * 60_000
const MAX_ATTEMPTS = 5

export interface MentionNames {
  /** The name this reader may see, or undefined while unknown or not released. */
  name(userId: string): string | undefined
  /** Ask for these ids; listeners hear when any answer arrives. */
  want(userIds: Iterable<string>): void
  subscribe(listener: () => void): () => void
  /** Changes whenever a name arrives, for external-store reads. */
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
  const attempts = new Map<string, number>()
  const queued = new Set<string>(), asking = new Set<string>()
  const listeners = new Set<() => void>()
  let version = 0, quietUntil = 0, disposed = false
  let timer: ReturnType<typeof setTimeout> | undefined, refresher: ReturnType<typeof setInterval> | undefined
  const notify = () => { version++; for (const listener of [...listeners]) listener() }
  // A new id never cuts a wait short.
  const schedule = (delay: number) => {
    if (timer !== undefined) return
    timer = setTimeout(() => { timer = undefined; void flush() }, Math.max(delay, quietUntil - Date.now()))
  }
  const settle = (id: string, name: string | null) => { settled.set(id, { name, at: Date.now() }); attempts.delete(id) }
  /** Asks for one batch; returns how long to wait before asking again (0 when settled). */
  const ask = async (ids: string[]): Promise<number> => {
    let response: Response
    try {
      response = await fetcher(`${base}?ids=${encodeURIComponent(ids.join(","))}&workspace_id=${encodeURIComponent(workspaceId)}`, { cache: "no-store" })
    } catch { response = new Response(null, { status: 599 }) }
    if (response.status === 429 || response.status >= 500) {
      const tries = Math.max(...ids.map(id => (attempts.get(id) ?? 0) + 1))
      ids.forEach(id => attempts.set(id, tries))
      if (tries >= MAX_ATTEMPTS) { ids.forEach(id => settle(id, settled.get(id)?.name ?? null)); return 0 }
      const after = Number(response.headers.get("Retry-After"))
      return response.status === 429 && Number.isFinite(after) && after > 0 ? Math.min(after, 60) * 1000 : Math.min(2 ** tries, 60) * 1000
    }
    const named = new Map<string, string>()
    if (response.ok) {
      const body = await response.json().catch(() => null) as { people?: unknown } | null
      for (const person of Array.isArray(body?.people) ? body.people : []) {
        const { id, name } = (person ?? {}) as { id?: unknown; name?: unknown }
        if (typeof id === "string" && typeof name === "string" && name.trim() && ids.includes(id)) named.set(id, name)
      }
    }
    for (const id of ids) settle(id, named.get(id) ?? null)
    return 0
  }
  const flush = async () => {
    const ids = [...queued].sort()
    queued.clear()
    let wait = 0
    for (let start = 0; start < ids.length; start += BATCH) {
      const batch = ids.slice(start, start + BATCH)
      if (wait) { batch.forEach(id => queued.add(id)); continue }
      batch.forEach(id => asking.add(id))
      wait = await ask(batch)
      batch.forEach(id => asking.delete(id))
      if (wait) batch.forEach(id => queued.add(id))
    }
    if (wait) quietUntil = Date.now() + wait
    notify()
    if (queued.size && !disposed) schedule(wait)
  }
  const stale = (id: string) => { const entry = settled.get(id); return !entry || Date.now() - entry.at >= REFRESH_MS }
  const want = (userIds: Iterable<string>) => {
    disposed = false
    if (listeners.size && refresher === undefined) refresher = setInterval(refresh, REFRESH_MS)
    let added = false
    for (const id of userIds) {
      if (!ID.test(id)) { settled.set(id, { name: null, at: Infinity }); continue }
      if (!stale(id) || asking.has(id) || queued.has(id)) continue
      queued.add(id); added = true
    }
    if (added) schedule(0)
  }
  // While something shows names, answers older than REFRESH_MS are asked again; the old name stays until then.
  const refresh = () => want([...settled.keys()].filter(id => ID.test(id)))
  return {
    name: userId => settled.get(userId)?.name ?? undefined,
    want,
    subscribe(listener) {
      listeners.add(listener)
      if (refresher === undefined) refresher = setInterval(refresh, REFRESH_MS)
      return () => {
        listeners.delete(listener)
        if (!listeners.size && refresher !== undefined) { clearInterval(refresher); refresher = undefined }
      }
    },
    version: () => version,
    dispose() {
      disposed = true
      if (timer !== undefined) clearTimeout(timer)
      if (refresher !== undefined) clearInterval(refresher)
      timer = refresher = undefined
      queued.clear()
    },
  }
}
