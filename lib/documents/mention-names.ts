import { apiFetch } from "@/lib/api-fetch"

/**
 * The one rule for showing a schema-5/6 mention, on the Page and on the Canvas:
 * a name only when the Document's people lookup (GET …/document/people?ids=)
 * names that person to this reader, otherwise "@Unknown member". The server
 * releases a name only for someone who can read this Document, and only to a
 * reader of it; this client never uses the workspace member list.
 *
 * Every id rendered in the same turn goes out in one request (at most 100 ids,
 * the lookup's bound), each id at most once per session, and a rate limit is
 * waited out as the server asks. Any other failure leaves the neutral label.
 */

export const UNKNOWN_MEMBER = "Unknown member"
const ID = /^[A-Za-z0-9_-]{1,64}$/
const BATCH = 100

export interface MentionNames {
  /** The name this reader may see, or undefined while unknown or not released. */
  name(userId: string): string | undefined
  /** Ask for these ids; listeners hear when any answer arrives. */
  want(userIds: Iterable<string>): void
  subscribe(listener: () => void): () => void
  /** Changes whenever a name arrives, for external-store reads. */
  version(): number
}

type Fetcher = (url: string, init?: RequestInit) => Promise<Response>

export function mentionLabel(names: MentionNames | undefined, userId: string): string {
  return "@" + (names?.name(userId) || UNKNOWN_MEMBER)
}

export function createMentionNames(workspaceId: string, pageId: string, fetcher: Fetcher = apiFetch): MentionNames {
  const base = `/api/v1/pages/${encodeURIComponent(`@${pageId}`)}/document/people`
  const settled = new Map<string, string | null>()
  const queued = new Set<string>(), asking = new Set<string>()
  const listeners = new Set<() => void>()
  let version = 0, timer: ReturnType<typeof setTimeout> | undefined, quietUntil = 0
  const notify = () => { version++; for (const listener of [...listeners]) listener() }
  // A new id never cuts a rate-limit wait short.
  const schedule = (delay: number) => {
    if (timer !== undefined) return
    timer = setTimeout(() => { timer = undefined; void flush() }, Math.max(delay, quietUntil - Date.now()))
  }
  const ask = async (ids: string[]): Promise<number> => {
    let response: Response
    try {
      response = await fetcher(`${base}?ids=${encodeURIComponent(ids.join(","))}&workspace_id=${encodeURIComponent(workspaceId)}`, { cache: "no-store" })
    } catch { response = new Response(null, { status: 599 }) }
    if (response.status === 429) {
      const after = Number(response.headers.get("Retry-After"))
      return Number.isFinite(after) && after > 0 ? Math.min(after, 60) * 1000 : 1000
    }
    const named = new Map<string, string>()
    if (response.ok) {
      const body = await response.json().catch(() => null) as { people?: unknown } | null
      for (const person of Array.isArray(body?.people) ? body.people : []) {
        const { id, name } = (person ?? {}) as { id?: unknown; name?: unknown }
        if (typeof id === "string" && typeof name === "string" && name.trim() && ids.includes(id)) named.set(id, name)
      }
    }
    for (const id of ids) settled.set(id, named.get(id) ?? null)
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
    if (queued.size) schedule(wait)
  }
  return {
    name: userId => settled.get(userId) ?? undefined,
    want(userIds) {
      let added = false
      for (const id of userIds) {
        if (!ID.test(id)) { settled.set(id, null); continue }
        if (settled.has(id) || asking.has(id) || queued.has(id)) continue
        queued.add(id); added = true
      }
      if (added) schedule(0)
    },
    subscribe(listener) { listeners.add(listener); return () => { listeners.delete(listener) } },
    version: () => version,
  }
}
