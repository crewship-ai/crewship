// What the Routines panel knows about each routine beyond its list row: how
// it starts, when it starts next and what it spent this month.
//
// The list endpoint carries none of the three, and there is no aggregate for
// them. The panel composes them in the browser from lists the page loads
// anyway (routines.md R01): GET /pipeline-schedules (schedules, next_run_at),
// GET /pipeline-webhooks, GET /pipelines/budget-summary (spend this month),
// the slash-command catalog (`routine.run:<slug>` = a chat command) and the
// row's own `linked_issue_count` (issues bound to the routine start it).

import { isCostShown } from "@/lib/cost-visibility"

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
  /** spent_usd is absent below ADMIN (#3590); such rows add no spend. */
  spend?: { routines: readonly { slug: string; spent_usd?: number }[] } | null
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
  for (const row of input.spend?.routines ?? []) if (known.has(row.slug) && isCostShown(row.spent_usd) && row.spent_usd > 0) spend.set(row.slug, row.spent_usd)
  return { triggers, nextRun, spend }
}

/** The first way a routine starts, in facet order; "manual" when only Run does. */
export function primaryTrigger(kinds: readonly RoutineTriggerKind[] | undefined): RoutineTriggerKind | "manual" {
  return kinds?.[0] ?? "manual"
}

export type RoutineSort = "next" | "recent" | "name" | "spend"
export type RoutineGroupBy = "folder" | "crew" | "trigger" | "none"

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
  /** By folder: the folder behind the section (absent for No folder). */
  folderId?: string
  /** By folder: 0 for a top-level folder, 1 for its subfolder… */
  depth?: number
}

/** A folder as the panel knows it (GET .../pipeline-folders). */
export interface PanelFolder {
  id: string
  name: string
  icon?: string
  color?: string
  parent_id?: string
}

/** Folders nest at most this many levels (the server's maxFolderDepth). */
export const ROUTINE_FOLDER_MAX_DEPTH = 3

/** Levels from the top down to this folder, itself included (a top-level
 *  folder is 1). A parent the list does not have ends the chain, and so does
 *  a chain longer than any legal tree, so bad data cannot loop. */
export function folderDepth(folders: ReadonlyMap<string, PanelFolder>, id: string): number {
  let depth = 0
  for (let cur = folders.get(id); cur && depth <= ROUTINE_FOLDER_MAX_DEPTH; cur = cur.parent_id ? folders.get(cur.parent_id) : undefined) depth++
  return depth
}

/** "Ops / Daily": the folder's name after its parents'. */
export function folderPath(folders: ReadonlyMap<string, PanelFolder>, id: string): string {
  const parts: string[] = []
  for (let cur = folders.get(id); cur && parts.length <= ROUTINE_FOLDER_MAX_DEPTH; cur = cur.parent_id ? folders.get(cur.parent_id) : undefined) parts.unshift(cur.name)
  return parts.join(" / ")
}

function childrenOf(folders: ReadonlyMap<string, PanelFolder>): Map<string | null, PanelFolder[]> {
  const out = new Map<string | null, PanelFolder[]>()
  for (const f of folders.values()) {
    const parent = f.parent_id && folders.has(f.parent_id) && f.parent_id !== f.id ? f.parent_id : null
    out.set(parent, [...(out.get(parent) ?? []), f])
  }
  for (const list of out.values()) list.sort((a, b) => a.name.localeCompare(b.name))
  return out
}

/** Levels in the folder and everything under it (a folder alone is 1). */
function subtreeHeight(children: Map<string | null, PanelFolder[]>, id: string, guard = 0): number {
  if (guard > ROUTINE_FOLDER_MAX_DEPTH) return guard
  return 1 + Math.max(0, ...(children.get(id) ?? []).map((c) => subtreeHeight(children, c.id, guard + 1)))
}

/** Where a folder can be moved: the top level, then every folder that is
 *  not itself or under it and leaves the whole moved subtree within
 *  ROUTINE_FOLDER_MAX_DEPTH. The server checks the same; this only keeps a
 *  refusal from being the first thing the person learns. */
export function folderMoveTargets(
  folders: ReadonlyMap<string, PanelFolder>,
  id: string,
): { id: string | null; label: string; folder?: PanelFolder }[] {
  const children = childrenOf(folders)
  const height = subtreeHeight(children, id)
  const under = new Set<string>([id])
  const walk = (fid: string) => {
    for (const c of children.get(fid) ?? []) if (!under.has(c.id)) (under.add(c.id), walk(c.id))
  }
  walk(id)
  const targets = [...folders.values()]
    .filter((f) => !under.has(f.id) && folderDepth(folders, f.id) + height <= ROUTINE_FOLDER_MAX_DEPTH)
    .map((f) => ({ id: f.id as string | null, label: folderPath(folders, f.id), folder: f }))
    .sort((a, b) => a.label.localeCompare(b.label))
  return [{ id: null, label: "Top level" }, ...targets]
}

const NO_CREW = "__none__"
const NO_FOLDER = "__nofolder__"

/** The panel's sections. By crew: alphabetical, "No crew" last. By trigger:
 *  the facet order, "Only when someone presses Run" last; a routine sits
 *  under the first way it starts, so it is listed once. */
export function groupRoutines<T extends SortableRoutine & { author_crew_id?: string; folder_id?: string }>(
  rows: readonly T[],
  groupBy: RoutineGroupBy,
  index: RoutinePanelIndex,
  crews: ReadonlyMap<string, { id: string; name: string }>,
  opts: { folders?: ReadonlyMap<string, PanelFolder>; includeEmptyFolders?: boolean } = {},
): RoutineGroup<T>[] {
  if (groupBy === "none") return [{ id: "all", label: "Routines", rows: [...rows] }]
  if (groupBy === "folder") {
    const folders = opts.folders ?? new Map()
    const map = new Map<string, T[]>()
    for (const r of rows) {
      // A folder that is gone (deleted since the list loaded) counts as unfiled.
      const key = r.folder_id && folders.has(r.folder_id) ? r.folder_id : NO_FOLDER
      map.set(key, [...(map.get(key) ?? []), r])
    }
    if (opts.includeEmptyFolders) for (const id of folders.keys()) if (!map.has(id)) map.set(id, [])
    // A listed subfolder brings its parents, so the tree reads from the top.
    for (const id of [...map.keys()]) {
      let depth = 0
      for (let p = folders.get(id)?.parent_id; p && folders.has(p) && depth < ROUTINE_FOLDER_MAX_DEPTH; p = folders.get(p)?.parent_id, depth++) {
        if (!map.has(p)) map.set(p, [])
      }
    }
    // Depth first: a folder, then its subfolders by name, then its next sibling.
    const children = childrenOf(folders)
    const out: RoutineGroup<T>[] = []
    const seen = new Set<string>()
    const visit = (parent: string | null, depth: number) => {
      for (const f of children.get(parent) ?? []) {
        if (seen.has(f.id)) continue
        seen.add(f.id)
        if (map.has(f.id)) out.push({ id: `folder-${f.id}`, label: f.name, rows: map.get(f.id)!, folderId: f.id, depth })
        visit(f.id, depth + 1)
      }
    }
    visit(null, 0)
    // A folder only reachable through a loop in bad data is still listed, at the top.
    for (const [id, list] of map) {
      if (id !== NO_FOLDER && !seen.has(id)) out.push({ id: `folder-${id}`, label: folders.get(id)!.name, rows: list, folderId: id, depth: 0 })
    }
    if (map.has(NO_FOLDER)) out.push({ id: `folder-${NO_FOLDER}`, label: "No folder", rows: map.get(NO_FOLDER)! })
    return out
  }
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
