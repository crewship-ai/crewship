import { LIBRARY_KINDS, repeatedSeries, type LibraryItem, type LibraryKind } from "./library-items"

/** The places the panel offers. Empty and Repeated runs only show; nothing is archived or deleted. */
export type LibraryView = "home" | "all" | "attention" | "opened" | "mine" | "favorites" | "empty" | "series"

/** What only this person knows: when they last opened each item, and what they starred. Kept per user. */
export interface Personal {
  opened?: ReadonlyMap<string, number>
  favorites?: ReadonlySet<string>
}

export type Access = "private" | "open"
export type VersionState = "shared" | "unshared" | "unversioned"

export interface LibraryFilters {
  view: LibraryView
  q: string
  kinds: LibraryKind[]
  access: Access[]
  version: VersionState[]
  owners: string[]
  /** A folder slug, "" for Unfiled, or null for every folder. */
  folder: string | null
}

export const EMPTY_FILTERS: LibraryFilters = { view: "home", q: "", kinds: [], access: [], version: [], owners: [], folder: null }

export type SortKey = "updated" | "name" | "kind"
export type GroupKey = "none" | "kind" | "folder"

/** Project, and Word or Sheet stored natively, carry a version head; only they can say what is shared. */
export const versioned = (item: LibraryItem) => item.projectBacked || item.native

export function versionOf(item: LibraryItem): VersionState {
  if (!versioned(item)) return "unversioned"
  return item.sharedVersion ? "shared" : "unshared"
}

/** Made but never shared: the one thing the library can flag from the list alone. */
export const needsAttention = (item: LibraryItem) => !item.empty && versionOf(item) === "unshared"

export const OWNER_NONE = "—"
const ownerKey = (item: LibraryItem) => item.ownerLabel ?? OWNER_NONE

export interface ParsedQuery {
  text: string
  kinds: LibraryKind[]
  is: string[]
  owner: string[]
}

const KIND_WORDS = new Map<string, LibraryKind>(LIBRARY_KINDS.flatMap(({ kind, label, plural }) =>
  [[kind, kind], [label.toLowerCase(), kind], [plural.toLowerCase(), kind]] as [string, LibraryKind][]))

/** `kind:word is:private owner:you budget` — operators first-class, the rest is free text. */
export function parseQuery(q: string): ParsedQuery {
  const parsed: ParsedQuery = { text: "", kinds: [], is: [], owner: [] }
  const words: string[] = []
  for (const token of q.trim().split(/\s+/).filter(Boolean)) {
    const match = /^(kind|is|owner):(.+)$/i.exec(token)
    if (!match) { words.push(token); continue }
    const [, op, raw] = match
    const value = raw.toLowerCase()
    if (op.toLowerCase() === "kind") {
      const kind = KIND_WORDS.get(value)
      if (kind) parsed.kinds.push(kind)
      else words.push(token)
    } else if (op.toLowerCase() === "is") parsed.is.push(value)
    else parsed.owner.push(value)
  }
  parsed.text = words.join(" ").toLowerCase()
  return parsed
}

function matchesIs(item: LibraryItem, is: string): boolean {
  switch (is) {
    case "private": return item.private
    case "shared": return versionOf(item) === "shared"
    case "unshared": case "draft": return versionOf(item) === "unshared"
    case "empty": return item.empty
    default: return true
  }
}

/** Every filter but the view; facet counts use it with their own facet left out. */
export function matches(item: LibraryItem, filters: LibraryFilters, skip?: "kinds" | "access" | "version" | "owners"): boolean {
  const query = parseQuery(filters.q)
  if (query.text && !item.name.toLowerCase().includes(query.text) && !item.slug.toLowerCase().includes(query.text)) return false
  if (query.kinds.length && !query.kinds.includes(item.kind)) return false
  if (!query.is.every(is => matchesIs(item, is))) return false
  if (query.owner.length && !query.owner.some(owner => (item.ownerLabel ?? "").toLowerCase().includes(owner))) return false
  if (skip !== "kinds" && filters.kinds.length && !filters.kinds.includes(item.kind)) return false
  if (skip !== "access" && filters.access.length && !filters.access.includes(item.private ? "private" : "open")) return false
  if (skip !== "version" && filters.version.length && !filters.version.includes(versionOf(item))) return false
  if (skip !== "owners" && filters.owners.length && !filters.owners.includes(ownerKey(item))) return false
  if (filters.folder !== null && (item.folder?.slug ?? "") !== filters.folder) return false
  return true
}

/** What a view holds before filters: empty items only in Empty, repeated runs only in their view. */
export function inView(items: LibraryItem[], view: LibraryView, personal: Personal = {}): LibraryItem[] {
  const live = items.filter(item => !item.empty)
  switch (view) {
    case "empty": return items.filter(item => item.empty)
    case "series": return repeatedSeries(live).flatMap(series => series.items)
    case "attention": return live.filter(needsAttention)
    case "mine": return live.filter(item => item.ownerLabel === "You")
    case "favorites": return live.filter(item => personal.favorites?.has(item.slug))
    case "opened": {
      const opened = personal.opened
      return opened ? live.filter(item => opened.has(item.slug)).sort((a, b) => opened.get(b.slug)! - opened.get(a.slug)!) : []
    }
    default: return live
  }
}

export function applyFilters(items: LibraryItem[], filters: LibraryFilters, personal: Personal = {}): LibraryItem[] {
  return inView(items, filters.view, personal).filter(item => matches(item, filters))
}

export function sortItems(items: LibraryItem[], sort: SortKey): LibraryItem[] {
  const byUpdated = (a: LibraryItem, b: LibraryItem) => (b.updatedAt?.getTime() ?? 0) - (a.updatedAt?.getTime() ?? 0)
  const kindRank = (kind: LibraryKind) => LIBRARY_KINDS.findIndex(entry => entry.kind === kind)
  return [...items].sort(sort === "name" ? (a, b) => a.name.localeCompare(b.name) || byUpdated(a, b)
    : sort === "kind" ? (a, b) => kindRank(a.kind) - kindRank(b.kind) || byUpdated(a, b)
      : byUpdated)
}

export function groupItems(items: LibraryItem[], group: GroupKey): { key: string; label: string; items: LibraryItem[] }[] {
  if (group === "none") return [{ key: "all", label: "", items }]
  const groups = new Map<string, { key: string; label: string; items: LibraryItem[] }>()
  for (const item of items) {
    const key = group === "kind" ? item.kind : item.folder?.slug ?? ""
    const label = group === "kind" ? LIBRARY_KINDS.find(entry => entry.kind === item.kind)!.plural : item.folder?.name ?? "Unfiled"
    const entry = groups.get(key) ?? { key, label, items: [] }
    entry.items.push(item)
    groups.set(key, entry)
  }
  return [...groups.values()]
}

export interface FacetCounts {
  kinds: [LibraryKind, number][]
  access: [Access, number][]
  version: [VersionState, number][]
  owners: [string, number][]
}

/** Counts over the view with every other facet applied, so a value's count is what choosing it would show. */
export function facetCounts(items: LibraryItem[], filters: LibraryFilters, personal: Personal = {}): FacetCounts {
  const base = inView(items, filters.view, personal)
  const count = <K extends string>(skip: "kinds" | "access" | "version" | "owners", key: (item: LibraryItem) => K) => {
    const counts = new Map<K, number>()
    for (const item of base) if (matches(item, filters, skip)) counts.set(key(item), (counts.get(key(item)) ?? 0) + 1)
    return counts
  }
  const kinds = count("kinds", item => item.kind)
  const access = count("access", item => (item.private ? "private" : "open") as Access)
  const version = count("version", versionOf)
  const owners = count("owners", ownerKey)
  return {
    kinds: LIBRARY_KINDS.map(({ kind }) => [kind, kinds.get(kind) ?? 0] as [LibraryKind, number]).filter(([kind, n]) => n > 0 || filters.kinds.includes(kind)),
    access: (["open", "private"] as const).map(key => [key, access.get(key) ?? 0] as [Access, number]),
    version: (["shared", "unshared", "unversioned"] as const).map(key => [key, version.get(key) ?? 0] as [VersionState, number]).filter(([key, n]) => n > 0 || filters.version.includes(key)),
    owners: [...owners.entries()].sort((a, b) => b[1] - a[1]),
  }
}

export const activeFacetCount = (filters: LibraryFilters) =>
  filters.kinds.length + filters.access.length + filters.version.length + filters.owners.length + (filters.folder !== null ? 1 : 0)

const VIEWS: readonly LibraryView[] = ["home", "all", "attention", "opened", "mine", "favorites", "empty", "series"]
const UNFILED = "~unfiled"
const list = (value: string | null) => (value ? value.split(",").map(entry => entry.trim()).filter(Boolean) : [])

/** The filters as URL parameters, so a view survives reload and can be shared. Defaults are left out. */
export function filtersToParams(filters: LibraryFilters, params: URLSearchParams = new URLSearchParams()): URLSearchParams {
  const next = new URLSearchParams(params)
  for (const key of ["view", "q", "kind", "access", "version", "owner", "folder"]) next.delete(key)
  if (filters.view !== EMPTY_FILTERS.view) next.set("view", filters.view)
  if (filters.q) next.set("q", filters.q)
  if (filters.kinds.length) next.set("kind", filters.kinds.join(","))
  if (filters.access.length) next.set("access", filters.access.join(","))
  if (filters.version.length) next.set("version", filters.version.join(","))
  if (filters.owners.length) next.set("owner", filters.owners.join(","))
  if (filters.folder !== null) next.set("folder", filters.folder === "" ? UNFILED : filters.folder)
  return next
}

/** Reads what `filtersToParams` wrote; an unknown value is dropped, never guessed. */
export function filtersFromParams(params: URLSearchParams | null | undefined): LibraryFilters {
  if (!params) return EMPTY_FILTERS
  const view = params.get("view") as LibraryView | null
  const kinds = new Set(LIBRARY_KINDS.map(entry => entry.kind as string))
  const folder = params.get("folder")
  return {
    view: view && VIEWS.includes(view) ? view : EMPTY_FILTERS.view,
    q: params.get("q") ?? "",
    kinds: list(params.get("kind")).filter(kind => kinds.has(kind)) as LibraryKind[],
    access: list(params.get("access")).filter(access => access === "private" || access === "open") as Access[],
    version: list(params.get("version")).filter(version => version === "shared" || version === "unshared" || version === "unversioned") as VersionState[],
    owners: list(params.get("owner")),
    folder: folder === null ? null : folder === UNFILED ? "" : folder,
  }
}
