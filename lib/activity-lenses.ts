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

/**
 * A ref that arrived without a count still means one piece of work, not zero.
 *
 * Exported because three surfaces were each deciding this for themselves and
 * two of them decided differently: the rail's row read `×1`, the agent
 * drill-down's row read `×1`, and the strip at the top of that same drill-down
 * read `0 assignments` — one agent, one window, two numbers. A fallback that
 * lives in three places is a fallback with three values.
 */
export function assignmentsOf(a: ChainAgentRef): number {
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

// ---------------------------------------------------------------------------
// The one narrowing every surface reads.
// ---------------------------------------------------------------------------

/**
 * The loaded index page, at the two stages the screen needs it.
 *
 * Two stages rather than one, because the status segments have to survive their
 * own selection. They count over `searched`; every list and dashboard renders
 * `visible`. Counting over `visible` instead would make picking "Failed" render
 * "Failed 3 · Waiting 0 · Running 0" — three numbers describing what is left
 * after the pick rather than what there is to pick, and no way back out except
 * by guessing.
 */
export interface NarrowedChains {
  /** Search applied, status segment NOT. What the segment counts are over. */
  searched: ChainSummary[]
  /** Search AND status segment applied. What every list and dashboard shows. */
  visible: ChainSummary[]
}

/**
 * Narrows the loaded chain index by the rail's search box and status segment.
 *
 * This function exists because the narrowing used to be PRIVATE to the rail.
 * ActivitySidebar filtered its own copy while the shell handed the same
 * unnarrowed array to the three lens dashboards beside it, so typing in the
 * search box left the rail showing two rows next to a dashboard reporting
 * twenty — under a comment in lens-overviews.tsx promising that the two could
 * not disagree, because they read "the SAME ChainSummary[]". They did not. One
 * function, called once in the shell, is what makes that sentence true.
 *
 * `routineNameOf` resolves a slug to the routine's human name and is what the
 * search matches against, because it is what the row RENDERS: a reader
 * searching "follow up" is typing the words on their screen, and
 * `on-close-file-followup` is not those words. Optional — a caller without the
 * pipelines list loaded still gets slug, cause, handle and touched-noun
 * matching, which is the same graceful floor matchesQuery already had.
 *
 * Client-side, over the loaded page only, and deliberately: the index is one
 * grouped query with no search or status parameter. What that does NOT cover is
 * stated by the caller — see ActivitySidebar's window notice — rather than
 * implied by a confident-looking count.
 */
export function narrowChains(
  chains: ChainSummary[],
  query: string,
  scope: string,
  routineNameOf?: (slug: string) => string | undefined,
): NarrowedChains {
  const searched = chains.filter((c) =>
    matchesQuery(c, query, routineNameOf?.(c.routine_slug ?? "")),
  )
  return { searched, visible: chainsInScope(searched, scope) }
}

// ---------------------------------------------------------------------------
// What earns the word "workflow".
// ---------------------------------------------------------------------------

/** How many nouns a workflow's sentence names before it starts eliding. */
const MAX_REACH_NOUNS = 3

/**
 * Whether a chain COMPOSED anything, or is just one run wearing the word.
 *
 * On the live instance twelve of twenty-one rows in the Workflows lens were
 * `crewship routine run X` — one run, depth 0, no issue touched, no agent
 * dispatched. Nothing was bound to anything. Listing those as workflows makes
 * the word mean "a run", and once it means that the Workflows lens is the
 * Routines lens with worse naming: open "Classify support ticket" in either and
 * you see the same eight runs.
 *
 * A workflow is a process that BINDS two or more Crewship things together. So
 * the test is whether anything was bound:
 *
 *   runs > 1              a routine called another
 *   max_chain_depth > 0   something fired something
 *   agent_count > 0       a routine put an agent to work
 *   issue_count > 0       it reached into the tracker
 *
 * Plus two exceptions that are not about composition at all.
 *
 * The first is a chain that FAILED, is still running, or is waiting on a person.
 * A single run that broke is the reason somebody opened this page, and filing it
 * under its routine would hide exactly what the rail exists to surface.
 *
 * The second is a chain NO OTHER LENS CAN LIST. "It belongs in Routines
 * instead" is the argument this whole predicate rests on, and it holds only
 * while there is a Routines row to hold it: routineLens keys on
 * `routine_slug` and skips a chain without one, which is the honest thing for a
 * catalogue of routines to do. A chain whose root run was swept by retention
 * has no slug — so before this clause it was in the Workflows lens (dropped for
 * composing nothing) and in the Routines lens (dropped for having no routine),
 * which is to say nowhere. An index may cap, elide or defer a row; it may not
 * silently have no place for one.
 *
 * The rule, then, is "compose, or need me, or belong to no catalogue".
 *
 * Nothing is deleted by this — a bare run of a KNOWN routine is still a run, and
 * the Routines lens is where runs live. This only decides which list it is in.
 */
export function isComposed(c: ChainSummary): boolean {
  if (c.runs > 1) return true
  if (c.max_chain_depth > 0) return true
  if ((c.agent_count ?? 0) > 0) return true
  if ((c.issue_count ?? 0) > 0) return true
  if (chainStatus(c) !== "done") return true
  // No slug means routineLens has no row for it. See above.
  return !c.routine_slug?.trim()
}

/**
 * The one line that says what a workflow IS, as opposed to what routine it began
 * with.
 *
 * "Classify support ticket" is the routine's name. Two runs of it produce two
 * rows reading the same, and a reader comparing the Workflows lens to the
 * Routines lens finds the same words in both — which is what made the whole
 * lens feel redundant.
 *
 * A composed chain has something the routine does not: a shape. It was set off
 * by something, it ran something, it reached something. Written as one arrow
 * chain, that is unique to the run and readable at a glance:
 *
 *   file a follow-up when an issue closes → Follow up on close → riley → ENG-7
 *
 * The cause is dropped when it is a PERSON or when it is the routine itself:
 * "Demo User → Sweep" tells a reader nothing they were looking for in a list of
 * processes, and a chain whose cause resolves to its own routine would print the
 * same word twice.
 *
 * The reach is capped and says so. A chain that touched nine issues renders
 * three and "+6" — a cut list that declares the cut, the same rule chainTouched
 * follows one file over.
 */
export function workflowSentence(c: ChainSummary, routineName?: string): string {
  const parts: string[] = []

  const cause = c.started_by?.trim()
  const routine = workflowName(c, routineName)
  const causeIsWorthNaming =
    cause != null &&
    cause !== "" &&
    cause !== routine &&
    cause !== c.routine_slug &&
    c.started_by_kind !== "user" &&
    c.started_by_kind !== "unknown"
  if (causeIsWorthNaming) parts.push(cause)

  parts.push(routine)

  const agents = (c.agents ?? []).map((a) => a.name || a.slug || a.id)
  if (agents.length > 0) parts.push(reach(agents, c.agent_count))

  const issues = (c.issues ?? []).map((i) => i.identifier || i.id)
  if (issues.length > 0) parts.push(reach(issues, c.issue_count))

  return parts.join(" → ")
}

/** Up to MAX_REACH_NOUNS names, with the remainder declared rather than dropped. */
function reach(names: string[], total: number): string {
  const shown = names.slice(0, MAX_REACH_NOUNS)
  const more = Math.max(total, names.length) - shown.length
  return more > 0 ? `${shown.join(", ")} +${more}` : shown.join(", ")
}
