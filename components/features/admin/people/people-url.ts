import type { Facet, PeopleOrder, View, WorkspaceOrder } from "./people-model"

/**
 * The page's state in the address bar, so a view, a person or a search can be
 * shared and survives a reload. Every parameter the page ever wrote keeps
 * working: the old `view=people&facet=admins` still lands on Instance admins.
 */
export interface UrlState {
  view: View
  facet: Facet
  person: string
  ws: string
  q: string
  /** Narrow the people to one workspace's members (the Workspace facet). */
  in: string
  /** The view a person was opened from, for "← People". */
  from: View
  sort: PeopleOrder
  wsort: WorkspaceOrder
}

export const EMPTY_URL: UrlState = { view: "people", facet: "all", person: "", ws: "", q: "", in: "", from: "people", sort: "hand", wsort: "busy" }

const VIEWS: readonly View[] = ["people", "hand", "admins", "suspended", "accounts", "workspaces", "access"]
const FACETS: readonly Facet[] = ["all", "active", "setup", "locked", "suspended", "noaccess"]
const oneOf = <T extends string>(list: readonly T[], v: string | null, fallback: T): T => (list.includes(v as T) ? (v as T) : fallback)

/** The old `facet` values that became views of their own. */
function legacy(view: View, facet: string | null): { view: View; facet: string | null } {
  if (view !== "people") return { view, facet }
  if (facet === "admins") return { view: "admins", facet: null }
  if (facet === "attention") return { view: "hand", facet: null }
  return { view, facet }
}

export function parseUrl(search: string): UrlState {
  const p = new URLSearchParams(search)
  const l = legacy(oneOf(VIEWS, p.get("view"), "people"), p.get("facet"))
  return {
    view: l.view,
    facet: oneOf(FACETS, l.facet, "all"),
    person: p.get("person") ?? "",
    ws: p.get("ws") ?? "",
    q: p.get("q") ?? "",
    in: p.get("in") ?? "",
    from: oneOf(VIEWS, p.get("from"), "people"),
    sort: oneOf<PeopleOrder>(["hand", "az", "recent"], p.get("sort"), "hand"),
    wsort: oneOf<WorkspaceOrder>(["busy", "az", "new"], p.get("wsort"), "busy"),
  }
}

/** Writes only what differs from the defaults, onto an existing URL. */
export function writeUrl(s: UrlState, href: string): string {
  const url = new URL(href)
  const set = (k: string, v: string, keep: boolean) => (keep ? url.searchParams.set(k, v) : url.searchParams.delete(k))
  set("view", s.view, s.view !== "people")
  set("facet", s.facet, s.facet !== "all")
  set("person", s.person, Boolean(s.person))
  set("ws", s.ws, Boolean(s.ws))
  set("q", s.q, Boolean(s.q))
  set("in", s.in, Boolean(s.in))
  set("from", s.from, Boolean(s.person) && s.from !== "people")
  set("sort", s.sort, s.sort !== "hand")
  set("wsort", s.wsort, s.wsort !== "busy")
  return url.toString()
}
