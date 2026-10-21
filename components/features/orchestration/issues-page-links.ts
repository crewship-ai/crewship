// Links between /issues/<id> and /issues.
//
// v67 draws the full issue page inside Issues, with the Issues panel on the
// left (issues.js:182-187 `panel: issuesPanel(...)`). On /issues/<id> the
// panel cannot filter a list in place — there is no list on that page — so
// each pick is a link that /issues reads once on arrival and then strips:
// ?view= (a quick view), ?saved= (a saved view), ?q= (search) and
// ?create=project. Facets travel as /issues already reads them (?status=…).
//
// ↑/↓ on the issue page step through the list the reader came from: the
// facets carried in ?from=/issues?…, in the reader's own grouping and order.

import { filtersFromParams, filtersToParams, type IssueFilters } from "@/hooks/use-issue-filters"
import { getIssueWorker } from "@/lib/issue-execution"
import type { Mission } from "@/lib/types/mission"
import { groupIssues, orderIssues, isQuickView, type GroupBy, type OrderBy, type QuickView } from "@/components/features/issues/issues-view-model"
import type { IssuesPanelSelection } from "./issues-panel"

const withQuery = (params: URLSearchParams) => {
  const q = params.toString()
  return q ? `/issues?${q}` : "/issues"
}

/** Where a panel row on the issue page leads. */
export function panelHref(s: IssuesPanelSelection): string {
  const p = new URLSearchParams()
  if (s.kind === "quick" && s.id !== "all") p.set("view", s.id)
  if (s.kind === "saved") p.set("saved", s.id)
  if (s.kind === "project") p.set("project", s.id)
  return withQuery(p)
}

/** /issues with these facets. */
export function filtersHref(filters: IssueFilters): string {
  return withQuery(filtersToParams(filters, new URLSearchParams()))
}

/** /issues with this search. */
export function searchHref(q: string): string {
  const p = new URLSearchParams()
  if (q.trim()) p.set("q", q.trim())
  return withQuery(p)
}

export interface IssuesArrival {
  quick: QuickView | null
  savedId: string | null
  search: string | null
  createProject: boolean
  /** The query without the one-time keys, for history.replaceState. */
  rest: string
}

const ARRIVAL_KEYS = ["view", "saved", "q"] as const

/** The one-time keys a link from the issue page carries into /issues. */
export function arrivalFromParams(params: URLSearchParams): IssuesArrival {
  const view = params.get("view")
  const rest = new URLSearchParams(params)
  for (const k of ARRIVAL_KEYS) rest.delete(k)
  const createProject = params.get("create") === "project"
  if (createProject) rest.delete("create")
  return {
    quick: isQuickView(view) ? view : null,
    savedId: params.get("saved") || null,
    search: params.get("q") || null,
    createProject,
    rest: rest.toString(),
  }
}

function matchesFilters(i: Mission, f: IssueFilters): boolean {
  if (f.statuses.length > 0 && !f.statuses.includes(i.status)) return false
  if (f.priority && (i.priority || "none") !== f.priority) return false
  if (f.crewId && i.crew_id !== f.crewId) return false
  const w = getIssueWorker(i)
  if (f.agentId && w.id !== f.agentId) return false
  if (f.ownerId && i.owner?.id !== f.ownerId) return false
  if (f.holder === "agent" && !(w.id && !w.isHuman)) return false
  if (f.holder === "person" && !(w.id && w.isHuman)) return false
  if (f.label && !(i.labels ?? []).some((l) => l.name === f.label)) return false
  return true
}

/** The issues ↑/↓ step through, in the order the list showed them. */
export function issueSequence(
  issues: Mission[],
  fromHref: string | null | undefined,
  view: { groupBy: GroupBy; orderBy: OrderBy },
): Mission[] {
  let list = issues
  if (fromHref && /^\/issues(\?|$)/.test(fromHref)) {
    const q = fromHref.indexOf("?")
    const filters = filtersFromParams(new URLSearchParams(q >= 0 ? fromHref.slice(q + 1) : ""))
    list = list.filter((i) => matchesFilters(i, filters))
  }
  return groupIssues(orderIssues(list, view.orderBy), view.groupBy).flatMap((g) => g.issues)
}

/** The identifiers either side of this one; wraps, like the peek. */
export function issueNeighbours(sequence: Mission[], identifier: string): { prev: string | null; next: string | null } {
  const ids = sequence.map((i) => i.identifier ?? i.id)
  const at = ids.indexOf(identifier)
  if (at < 0 || ids.length < 2) return { prev: null, next: null }
  return { prev: ids[(at - 1 + ids.length) % ids.length], next: ids[(at + 1) % ids.length] }
}
