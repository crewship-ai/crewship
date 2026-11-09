import { apiFetch } from "@/lib/api-fetch"

/**
 * Mention names for one Document and one reader (#5155). A schema 5 mention
 * stores only a user id; the server names a person only when they can read
 * the Document, and only to a reader who can read it. Anything else stays
 * unnamed and is shown as UNKNOWN_MEMBER, which is also what is shown while a
 * name is loading or when the lookup fails: no state reveals more than that.
 */
export const UNKNOWN_MEMBER = "Unknown member"

export interface DocumentPerson { id: string; name: string }

export interface DocumentPeople {
  /** The name to show: a resolved name, or UNKNOWN_MEMBER. Asks the server once per id. */
  label(id: string): string
  /** Called whenever names arrive; returns the unsubscribe function. */
  subscribe(listener: () => void): () => void
  /** People who can read this Document whose name contains `query`, for the @-mention picker. */
  search(query: string, signal?: AbortSignal): Promise<DocumentPerson[]>
}

type Fetcher = (url: string, init?: { signal?: AbortSignal }) => Promise<Response>

const ID = /^[A-Za-z0-9_-]{1,64}$/
const BATCH = 100

function people(value: unknown): DocumentPerson[] {
  const list = (value as { people?: unknown } | null)?.people
  if (!Array.isArray(list)) throw new Error("invalid people response")
  return list.filter((person): person is DocumentPerson =>
    typeof person?.id === "string" && ID.test(person.id) && typeof person?.name === "string" && person.name.length > 0 && [...person.name].length <= 200)
}

export function createDocumentPeople(workspaceId: string, slug: string, fetcher: Fetcher = (url, init) => apiFetch(url, { ...init, cache: "no-store" })): DocumentPeople {
  const base = `/api/v1/pages/${encodeURIComponent(slug)}/document/people`
  const names = new Map<string, string | null>()
  const pending = new Set<string>()
  const listeners = new Set<() => void>()
  let scheduled = false
  const notify = () => { for (const listener of [...listeners]) listener() }
  const flush = async () => {
    scheduled = false
    const ids = [...pending].slice(0, BATCH)
    ids.forEach(id => pending.delete(id))
    if (pending.size) schedule()
    if (!ids.length) return
    try {
      const response = await fetcher(`${base}?ids=${encodeURIComponent(ids.join(","))}&workspace_id=${encodeURIComponent(workspaceId)}`)
      const found = response.ok ? people(await response.json()) : []
      const named = new Map(found.map(person => [person.id, person.name]))
      for (const id of ids) names.set(id, named.get(id) ?? null)
    } catch {
      for (const id of ids) names.set(id, null)
    }
    notify()
  }
  const schedule = () => { if (!scheduled) { scheduled = true; queueMicrotask(() => { void flush() }) } }
  return {
    label(id) {
      if (!ID.test(id)) return UNKNOWN_MEMBER
      const name = names.get(id)
      if (name === undefined && !pending.has(id)) { pending.add(id); schedule() }
      return name || UNKNOWN_MEMBER
    },
    subscribe(listener) { listeners.add(listener); return () => { listeners.delete(listener) } },
    async search(query, signal) {
      const response = await fetcher(`${base}?q=${encodeURIComponent(query.slice(0, 100))}&workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error("People could not be loaded.")
      const found = people(await response.json())
      for (const person of found) names.set(person.id, person.name)
      if (found.length) notify()
      return found
    },
  }
}

/** A reader with no lookup: every mention shows UNKNOWN_MEMBER. */
export const NO_DOCUMENT_PEOPLE: DocumentPeople = { label: () => UNKNOWN_MEMBER, subscribe: () => () => {}, search: async () => [] }

/** A calendar day in the reader's locale, independent of their time zone. */
export function formatDocumentDate(value: string, locale?: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return value
  // setUTCFullYear keeps years 1-99 literal (Date.UTC would read them as 1900-1999).
  const date = new Date(0)
  date.setUTCFullYear(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeZone: "UTC" }).format(date)
}

/** Today as YYYY-MM-DD in the reader's own time zone, the default for a new date. */
export function todayDocumentDate(now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`
}
