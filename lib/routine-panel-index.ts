// What the Routines panel knows about each routine beyond its list row: how
// it starts, when it starts next and what it spent this month.
//
// The list endpoint carries none of the three, and there is no aggregate for
// them. The panel composes them in the browser from lists the page loads
// anyway (routines.md R01): GET /pipeline-schedules (schedules, next_run_at),
// GET /pipeline-webhooks, GET /pipelines/budget-summary (spend this month),
// the slash-command catalog (`routine.run:<slug>` = a chat command) and the
// row's own `linked_issue_count` (issues bound to the routine start it).

export type RoutineTriggerKind = "schedule" | "webhook" | "issue" | "chat"

/** The Trigger facet's values, in the order the panel lists them. */
export const ROUTINE_TRIGGER_KINDS: { id: RoutineTriggerKind; label: string }[] = [
  { id: "schedule", label: "Schedule" },
  { id: "webhook", label: "Webhook" },
  { id: "issue", label: "Issue start" },
  { id: "chat", label: "Chat command" },
]

/** The group of a routine that nothing starts but the Run button. */
export const MANUAL_ONLY_LABEL = "Only when someone presses Run"

export interface RoutinePanelIndex {
  /** Slug → the ways it starts, in ROUTINE_TRIGGER_KINDS order. */
  triggers: ReadonlyMap<string, readonly RoutineTriggerKind[]>
  /** Slug → the earliest next start of its enabled schedules. */
  nextRun: ReadonlyMap<string, string>
  /** Slug → spend this month in USD. */
  spend: ReadonlyMap<string, number>
}

export const EMPTY_PANEL_INDEX: RoutinePanelIndex = { triggers: new Map(), nextRun: new Map(), spend: new Map() }

interface IndexRoutine {
  id: string
  slug: string
  linked_issue_count?: number
}
interface IndexTarget {
  target_pipeline_slug?: string
  target_pipeline_id?: string
}
interface IndexSchedule extends IndexTarget {
  enabled: boolean
  next_run_at?: string
}

const CHAT_PREFIX = "routine.run:"

export function buildRoutinePanelIndex(input: {
  routines: readonly IndexRoutine[]
  schedules?: readonly IndexSchedule[]
  webhooks?: readonly IndexTarget[]
  /** Ids of the slash-command catalog; routine commands are `routine.run:<slug>`. */
  chatCommandIds?: readonly string[]
  spend?: { routines: readonly { slug: string; spent_usd: number }[] } | null
}): RoutinePanelIndex {
  const slugById = new Map(input.routines.map((r) => [r.id, r.slug]))
  const known = new Set(input.routines.map((r) => r.slug))
  const slugOf = (t: IndexTarget) => {
    const slug = t.target_pipeline_slug || (t.target_pipeline_id ? slugById.get(t.target_pipeline_id) : undefined)
    return slug && known.has(slug) ? slug : null
  }
  const kinds = new Map<string, Set<RoutineTriggerKind>>()
  const add = (slug: string | null, kind: RoutineTriggerKind) => {
    if (!slug) return
    const set = kinds.get(slug) ?? new Set<RoutineTriggerKind>()
    set.add(kind)
    kinds.set(slug, set)
  }
  const nextRun = new Map<string, string>()
  for (const s of input.schedules ?? []) {
    const slug = slugOf(s)
    add(slug, "schedule")
    if (!slug || !s.enabled || !s.next_run_at || !Number.isFinite(Date.parse(s.next_run_at))) continue
    const current = nextRun.get(slug)
    if (!current || Date.parse(s.next_run_at) < Date.parse(current)) nextRun.set(slug, s.next_run_at)
  }
  for (const w of input.webhooks ?? []) add(slugOf(w), "webhook")
  for (const r of input.routines) if ((r.linked_issue_count ?? 0) > 0) add(r.slug, "issue")
  for (const id of input.chatCommandIds ?? []) {
    if (!id.startsWith(CHAT_PREFIX)) continue
    const slug = id.slice(CHAT_PREFIX.length)
    add(known.has(slug) ? slug : null, "chat")
  }
  const triggers = new Map<string, RoutineTriggerKind[]>()
  for (const r of input.routines) {
    const set = kinds.get(r.slug)
    triggers.set(r.slug, set ? ROUTINE_TRIGGER_KINDS.map((k) => k.id).filter((k) => set.has(k)) : [])
  }
  const spend = new Map<string, number>()
  for (const row of input.spend?.routines ?? []) if (known.has(row.slug) && row.spent_usd > 0) spend.set(row.slug, row.spent_usd)
  return { triggers, nextRun, spend }
}

/** The first way a routine starts, in facet order; "manual" when only Run does. */
export function primaryTrigger(kinds: readonly RoutineTriggerKind[] | undefined): RoutineTriggerKind | "manual" {
  return kinds?.[0] ?? "manual"
}

export type RoutineSort = "next" | "recent" | "name" | "spend"
export type RoutineGroupBy = "crew" | "trigger" | "none"

interface SortableRoutine {
  slug: string
  name: string
  last_invoked_at?: string
  updated_at?: string
}

const nameOf = (r: SortableRoutine) => r.name || r.slug
const byName = (a: SortableRoutine, b: SortableRoutine) => nameOf(a).localeCompare(nameOf(b))

/** A sorted copy. Rows without the sorted fact (no next start, nothing spent,
 *  never run) go last, by name. */
export function sortRoutines<T extends SortableRoutine>(rows: readonly T[], sort: RoutineSort, index: RoutinePanelIndex): T[] {
  const list = [...rows]
  if (sort === "name") return list.sort(byName)
  if (sort === "next") {
    const at = (r: T) => {
      const iso = index.nextRun.get(r.slug)
      return iso ? Date.parse(iso) : Number.POSITIVE_INFINITY
    }
    return list.sort((a, b) => at(a) - at(b) || byName(a, b))
  }
  if (sort === "spend") return list.sort((a, b) => (index.spend.get(b.slug) ?? 0) - (index.spend.get(a.slug) ?? 0) || byName(a, b))
  const ran = (r: T) => Date.parse(r.last_invoked_at ?? "") || 0
  return list.sort((a, b) => ran(b) - ran(a) || byName(a, b))
}

export interface RoutineGroup<T> {
  id: string
  label: string
  rows: T[]
}

const NO_CREW = "__none__"

/** The panel's sections. By crew: alphabetical, "No crew" last. By trigger:
 *  the facet order, "Only when someone presses Run" last; a routine sits
 *  under the first way it starts, so it is listed once. */
export function groupRoutines<T extends SortableRoutine & { author_crew_id?: string }>(
  rows: readonly T[],
  groupBy: RoutineGroupBy,
  index: RoutinePanelIndex,
  crews: ReadonlyMap<string, { id: string; name: string }>,
): RoutineGroup<T>[] {
  if (groupBy === "none") return [{ id: "all", label: "Routines", rows: [...rows] }]
  const map = new Map<string, T[]>()
  const keyOf = (r: T) =>
    groupBy === "trigger" ? primaryTrigger(index.triggers.get(r.slug)) : r.author_crew_id && crews.has(r.author_crew_id) ? r.author_crew_id : NO_CREW
  for (const r of rows) {
    const key = keyOf(r)
    map.set(key, [...(map.get(key) ?? []), r])
  }
  if (groupBy === "trigger") {
    const order = [...ROUTINE_TRIGGER_KINDS, { id: "manual" as const, label: MANUAL_ONLY_LABEL }]
    return order.filter((k) => map.has(k.id)).map((k) => ({ id: `trigger-${k.id}`, label: k.label, rows: map.get(k.id)! }))
  }
  return [...map.entries()]
    .map(([id, list]) => ({ id, label: id === NO_CREW ? "No crew" : crews.get(id)!.name, rows: list }))
    .sort((a, b) => (a.id === NO_CREW ? 1 : b.id === NO_CREW ? -1 : a.label.localeCompare(b.label)))
}
