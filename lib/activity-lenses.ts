// What the Activity rail lists, and how a row is named.
//
// The rail had ONE list — workflows — and before that it had four stacked
// catalogues (crews, issues, routines, statuses) that read 0 on 56 rows. Both
// were wrong in the same way: a catalogue and a stream are different objects.
//
//   A CATALOGUE is what EXISTS. /routines lists all 40, including the 29 that
//   never ran, sorted alphabetically, and it looks the same every day.
//   A STREAM is what HAPPENED. It is sorted by time, it changes under the
//   reader, and yesterday is somewhere else than today.
//
// Putting a catalogue into a stream is what produced the wall of zeros;
// deleting the catalogues is what left the rail able to answer "which routine
// ran" and nothing else. The rule here is the third option: a lens lists only
// the members of a catalogue that were ACTIVE in the loaded window. An issue
// with no events is not in Activity — it is in /issues.
//
// Every lens is derived from the SAME ChainSummary[] the rail already holds, so
// switching lens costs no fetch and no lens can disagree with another about
// what happened. Issues, agents and routines are three ways of grouping one
// list of causal runs, which is exactly what they are.
//
// KNOWN LIMIT, stated rather than implied: these are derived from CHAINS, so
// they see what the chain index sees. The index groups pipeline_runs, so an
// agent who only ever worked outside a routine is not in the Agents lens. That
// is a hole in the index (chains_list.go groups one table), not in this file —
// fixing it here would mean inventing rows from a second source and the two
// would drift.

import type { ChainAgentRef, ChainSummary } from "@/hooks/use-chains"

/** Which catalogue the rail is currently listing. */
export type LensKey = "workflows" | "issues" | "agents" | "routines"

export interface LensMeta {
  key: LensKey
  label: string
  /** Tooltip — what a row in this lens IS, in one sentence. */
  hint: string
}

/**
 * The four lenses, in reading order.
 *
 * Workflows first because a causal run is the unit this page is about; the
 * other three are ways of slicing the same runs, and each answers a question a
 * person actually arrives with — "what happened to ENG-7", "what did my agents
 * do", "how is that routine doing".
 */
export const ACTIVITY_LENSES: readonly LensMeta[] = [
  { key: "workflows", label: "Workflows", hint: "One causal run: the rule or person that started it, and everything it caused" },
  { key: "issues", label: "Issues", hint: "Issues something touched in this window — not the whole backlog" },
  { key: "agents", label: "Agents", hint: "Agents that took work in this window, and how much" },
  { key: "routines", label: "Routines", hint: "Routines that RAN in this window — not the catalogue of every routine" },
] as const

// ---------------------------------------------------------------------------
// Naming a workflow.
// ---------------------------------------------------------------------------

/**
 * The name on a workflow row.
 *
 * `routineName` is the routine's human name, resolved by the caller from the
 * loaded pipelines list. It is preferred over the slug for the same reason the
 * Routines rail prefers it: "Follow up on close" is what somebody typed and
 * what they will look for; `on-close-file-followup` is what the machine stored.
 *
 * The fallback chain never ends in an empty string. A row with no visible name
 * is a row nobody can click on purpose, and every step of the chain is
 * reachable: the pipelines list and the chain index are two independent
 * fetches (so a slug can resolve to no name), a chain rooted at agent work has
 * no routine at all, and `started_by` is "" on a chain whose root run was swept
 * by retention.
 */
export function workflowName(c: ChainSummary, routineName?: string): string {
  const named = routineName?.trim()
  if (named) return named
  const slug = c.routine_slug?.trim()
  if (slug) return slug
  const cause = c.started_by?.trim()
  if (cause) return cause
  return "Workflow"
}

/** How many trailing characters of the origin make the handle. */
const HANDLE_LENGTH = 8

/**
 * The short handle that tells two runs of one routine apart.
 *
 * Two runs of `on-close-file-followup` render the same name, the same icon and
 * often the same touched issues — the rail looked like a list of duplicates
 * until the second line carried nouns, and even then two runs an hour apart
 * differ only by a relative timestamp that keeps changing. The handle is the
 * one part of a row that is stable and unique, which is what makes a workflow
 * something a person can name out loud, search for, and paste into a message.
 *
 * Derived, never generated. A random-word name (the worktree pattern) would be
 * friendlier to say and would have to be STORED — a new column, a new writer,
 * and a name that exists only for rows written after the migration. The tail of
 * the id costs nothing, is stable for the row's whole life, and every workflow
 * that has ever existed already has one.
 *
 * The type prefix is dropped because it is the same on every row: `run_` in
 * `run_cmsj1i72g000134a24f6e` distinguishes nothing between two workflows. The
 * tail is taken rather than the head because ids from one workspace share a
 * long head — `cmsj1i72g` is a timestamp component, so heads collide between
 * rows created in the same window, which is exactly when two rows are hardest
 * to tell apart.
 */
export function workflowHandle(origin: string): string {
  const bare = origin.includes("_") ? origin.slice(origin.indexOf("_") + 1) : origin
  return bare.length <= HANDLE_LENGTH ? bare : bare.slice(-HANDLE_LENGTH)
}

// ---------------------------------------------------------------------------
// What state a workflow is in.
// ---------------------------------------------------------------------------

export type ChainStatus = "waiting" | "failed" | "running" | "done"

/**
 * The one word for a chain whose runs may be in several states at once.
 *
 * Precedence is waiting → failed → running → done, and the order is a claim
 * about what the reader should do, not about what is most recent:
 *
 *   waiting  is the only state a PERSON can resolve. A chain holding an
 *            approval outranks everything, because everything else will move
 *            on its own and this will not.
 *   failed   is the strongest fact about an outcome. A chain where one run
 *            broke and another is still going reads "running" under any other
 *            order, and "running" is reassuring about something already wrong.
 *   running  resolves itself.
 *   done     is everything else.
 *
 * `failed` (the boolean the index has always sent) is honoured on its own so
 * the function keeps working against a server that sends no live counts — the
 * two count fields are newer than the flag.
 */
export function chainStatus(c: ChainSummary): ChainStatus {
  if ((c.waiting_runs ?? 0) > 0) return "waiting"
  if (c.failed || (c.failed_runs ?? 0) > 0) return "failed"
  if ((c.running_runs ?? 0) > 0) return "running"
  return "done"
}

/** Non-terminal: something is still going, or something is still asked. */
export function isChainActive(c: ChainSummary): boolean {
  const s = chainStatus(c)
  return s === "waiting" || s === "running"
}

// ---------------------------------------------------------------------------
// When it happened.
// ---------------------------------------------------------------------------

export type BucketKey = "active" | "today" | "earlier"

export interface ChainBucket {
  key: BucketKey
  label: string
  chains: ChainSummary[]
}

const BUCKET_LABEL: Record<BucketKey, string> = {
  active: "Active now",
  today: "Today",
  earlier: "Earlier",
}

/**
 * Which section of the rail a chain belongs to.
 *
 * "Active now" is a STATE, not a time: a run that started three days ago and is
 * still waiting on a human is the most urgent row on the page, and filing it
 * under Earlier by its timestamp puts it where nobody looks. This is the one
 * place the two axes cross, and it crosses in the direction that matters.
 *
 * Today is the calendar day rather than the last 24 hours, because the reader
 * is comparing against their own day — "this morning" and "yesterday evening"
 * are eleven hours apart and belong in different sections even though a rolling
 * window would put them together.
 *
 * An unparseable timestamp lands in Earlier rather than being dropped. The
 * chain is real and only its clock is unreadable; a silently omitted row reads
 * as "it never ran", which is the one thing it is not.
 */
export function timeBucket(c: ChainSummary, now: number): BucketKey {
  if (isChainActive(c)) return "active"
  const at = Date.parse(c.last_activity)
  if (Number.isNaN(at)) return "earlier"
  const start = new Date(now)
  start.setHours(0, 0, 0, 0)
  return at >= start.getTime() ? "today" : "earlier"
}

/**
 * Splits the list into sections, in reading order, dropping empty ones.
 *
 * Order within a bucket is the server's — newest first — and is preserved
 * rather than re-sorted: the index already ordered by last_activity, and a
 * second sort here is a second opinion that can disagree.
 */
export function bucketChains(chains: ChainSummary[], now: number): ChainBucket[] {
  const by: Record<BucketKey, ChainSummary[]> = { active: [], today: [], earlier: [] }
  for (const c of chains) by[timeBucket(c, now)].push(c)
  return (["active", "today", "earlier"] as const)
    .filter((k) => by[k].length > 0)
    .map((k) => ({ key: k, label: BUCKET_LABEL[k], chains: by[k] }))
}

// ---------------------------------------------------------------------------
// Search.
// ---------------------------------------------------------------------------

/**
 * Whether a workflow row survives the rail's search box.
 *
 * Matches everything the row can be RECOGNISED by, which is more than what the
 * row renders: the routine's name and slug, what started it, the handle, and
 * the identifiers and names of the issues and agents it touched. A reader
 * searching "ENG-7" is asking which workflow touched that issue, and answering
 * only from the routine name would return nothing while the answer is on
 * screen.
 *
 * Client-side and deliberately so: this narrows the loaded index page, not the
 * table. The rail's own copy says which, and the alternative — a server-side
 * search over a GROUP BY — is a second query shape for a list that is capped at
 * a screenful anyway.
 */
export function matchesQuery(c: ChainSummary, query: string, routineName?: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const hay: string[] = [
    workflowName(c, routineName),
    workflowHandle(c.origin),
    c.routine_slug ?? "",
    c.started_by ?? "",
    c.started_by_key ?? "",
    c.triggered_via ?? "",
  ]
  for (const i of c.issues ?? []) hay.push(i.identifier ?? "", i.title ?? "")
  for (const a of c.agents ?? []) hay.push(a.name ?? "", a.slug ?? "")
  return hay.some((h) => h.toLowerCase().includes(q))
}

// ---------------------------------------------------------------------------
// The three grouping lenses.
// ---------------------------------------------------------------------------

export interface IssueLensRow {
  id: string
  identifier?: string
  title?: string
  /** Some chain in this window AUTHORED it, not merely moved it. */
  created: boolean
  /** Origins of every chain that touched it — what selecting the row narrows to. */
  chains: string[]
}

/**
 * Issues something touched in this window, busiest first.
 *
 * Not the backlog: an issue nobody worked on today has no row here at all. That
 * is the difference between this and the /issues rail, and it is what keeps the
 * lens from becoming the wall of zeros the old rail was.
 */
export function issueLens(chains: ChainSummary[]): IssueLensRow[] {
  const by = new Map<string, IssueLensRow>()
  for (const c of chains) {
    for (const i of c.issues ?? []) {
      const row = by.get(i.id)
      if (row) {
        row.chains.push(c.origin)
        // Any chain that authored it makes the issue authored. `created` is the
        // stronger claim and one witness is enough to support it.
        row.created ||= i.created === true
        row.identifier ||= i.identifier
        row.title ||= i.title
      } else {
        by.set(i.id, {
          id: i.id,
          identifier: i.identifier,
          title: i.title,
          created: i.created === true,
          chains: [c.origin],
        })
      }
    }
  }
  return [...by.values()].sort(
    (a, b) => b.chains.length - a.chains.length || (a.identifier ?? a.id).localeCompare(b.identifier ?? b.id),
  )
}

export interface AgentLensRow {
  id: string
  name?: string
  slug?: string
  /** Pieces of work taken across every chain in the window. */
  assignments: number
  chains: string[]
}

/**
 * Agents that took work in this window, busiest first.
 *
 * This is the lens the whole trace layer exists for — "what did my agents
 * actually do" — and it is also the one most limited by where the index looks.
 * See the KNOWN LIMIT at the top of this file: an agent whose work no routine
 * dispatched is not in any chain, so it is not here either.
 */
export function agentLens(chains: ChainSummary[]): AgentLensRow[] {
  const by = new Map<string, AgentLensRow>()
  for (const c of chains) {
    for (const a of c.agents ?? []) {
      const row = by.get(a.id)
      if (row) {
        row.assignments += assignmentsOf(a)
        row.chains.push(c.origin)
        row.name ||= a.name
        row.slug ||= a.slug
      } else {
        by.set(a.id, {
          id: a.id,
          name: a.name,
          slug: a.slug,
          assignments: assignmentsOf(a),
          chains: [c.origin],
        })
      }
    }
  }
  return [...by.values()].sort(
    (a, b) => b.assignments - a.assignments || (a.name ?? a.id).localeCompare(b.name ?? b.id),
  )
}

/** A ref that arrived without a count still means one piece of work, not zero. */
function assignmentsOf(a: ChainAgentRef): number {
  return a.assignments > 0 ? a.assignments : 1
}

export interface RoutineLensRow {
  slug: string
  id?: string
  /** Runs across every chain of this routine — runs, not chains. */
  runs: number
  /** Any chain of this routine failed. */
  failed: boolean
  chains: string[]
  /** The most recent chain's last activity, for the row's timestamp. */
  lastActivity: string
}

/**
 * Routines that RAN in this window, busiest first.
 *
 * The count is runs rather than chains, because "this routine ran 12 times
 * today" is the sentence somebody wants; a chain can hold several runs of one
 * routine and counting chains would report 3.
 *
 * A chain no routine ran is omitted rather than given a blank row. It belongs
 * to the Agents lens, and an unnamed entry in a catalogue of names is worse
 * than an absence — the reader cannot tell it apart from a rendering bug.
 */
export function routineLens(chains: ChainSummary[]): RoutineLensRow[] {
  const by = new Map<string, RoutineLensRow>()
  for (const c of chains) {
    const slug = c.routine_slug?.trim()
    if (!slug) continue
    const row = by.get(slug)
    if (row) {
      row.runs += c.runs
      row.failed ||= chainStatus(c) === "failed"
      row.chains.push(c.origin)
      row.id ||= c.routine_id
      // The list arrives newest first, so the first one seen is the latest;
      // taking the max anyway keeps the field right if that ever changes.
      if (c.last_activity > row.lastActivity) row.lastActivity = c.last_activity
    } else {
      by.set(slug, {
        slug,
        id: c.routine_id,
        runs: c.runs,
        failed: chainStatus(c) === "failed",
        chains: [c.origin],
        lastActivity: c.last_activity,
      })
    }
  }
  return [...by.values()].sort((a, b) => b.runs - a.runs || a.slug.localeCompare(b.slug))
}

// ---------------------------------------------------------------------------
// The status line, over chains.
// ---------------------------------------------------------------------------

/**
 * How many chains sit in each status bucket.
 *
 * Shaped as the rail's own `Record<ActivityScope, number>` so it drops straight
 * into railSegments — which is the point. The segments used to count JOURNAL
 * ENTRIES while the list beneath them showed chains, so "Failed 9" sat above
 * three failed workflows and the two numbers described different objects. One
 * control above one list counts that list.
 *
 * `active` rather than `running` because that is the scope vocabulary the rest
 * of the page speaks; the segment renders it as "Running".
 */
export function chainScopeCounts(chains: ChainSummary[]): Record<"active" | "waiting" | "failed" | "done", number> {
  const c = { active: 0, waiting: 0, failed: 0, done: 0 }
  for (const ch of chains) {
    const s = chainStatus(ch)
    c[s === "running" ? "active" : s] += 1
  }
  return c
}

/**
 * The chains a status segment should show.
 *
 * "all" is every chain, including the ones that finished cleanly — the segment
 * is a narrowing, and a narrowing that silently drops the common case is a
 * filter pretending to be a default.
 */
export function chainsInScope(chains: ChainSummary[], scope: string): ChainSummary[] {
  if (scope === "all") return chains
  const want = scope === "active" ? "running" : scope
  return chains.filter((c) => chainStatus(c) === want)
}
