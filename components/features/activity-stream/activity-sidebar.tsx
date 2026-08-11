"use client"

// Left rail for /activity.
//
// The rail is NAVIGATION and nothing else: one line of status segments, then
// the workflow list. That is the whole column.
//
// It used to stack four unrelated things in it — a status bucket list, a crew
// list, 17 issues, 39 routines — and then the workflows underneath, so a place
// you go and a way to narrow what you see looked like the same kind of row.
// "Failed" existed twice (a bucket here, `severity: error` in the popover) and
// a filter that matched nothing left 56 rows all reading 0. Every narrowing now
// lives in the filter popover, where a narrowing belongs; the decisions behind
// that split are pure functions in lib/activity-rail.ts.
//
// SidebarFilterPopover owns the panel, so a pick never closes it and never
// clears a sibling facet (#1776).

import * as React from "react"
import { motion } from "motion/react"

import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"

import {
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarSearch,
  SidebarSection,
  SidebarRow,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { CircleDot, Workflow } from "lucide-react"

import { StatusIcon } from "@/components/features/issues/status-icon"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import type { ChainSummary } from "@/hooks/use-chains"
import { chainTouched } from "@/lib/chain-touched"
import { relTime } from "@/lib/time"
import {
  ACTIVITY_SOURCES,
  formatDurationMs,
  railInventory,
  type ActivityScope,
  type ActivitySource,
} from "@/lib/activity-stream"
import {
  DEFAULT_RANGE,
  RAIL_SEVERITIES,
  TIME_RANGES,
  activeFilterCount,
  clearedFilters,
  filterFacets,
  railSegments,
  railSources,
  type RailSegment,
  type TimeRangeKey,
} from "@/lib/activity-rail"
import {
  ACTIVITY_LENSES,
  agentLens,
  bucketChains,
  chainScopeCounts,
  chainStatus,
  isComposed,
  issueLens,
  routineLens,
  workflowHandle,
  workflowSentence,
  type ChainStatus,
  type LensKey,
} from "@/lib/activity-lenses"
import { cn } from "@/lib/utils"

// Re-exported so the shell keeps importing the range table from the rail it
// belongs to; the values themselves moved to lib/activity-rail.ts, where the
// "24h is the default, not a filter" rule can be tested.
export { TIME_RANGES }
export type { TimeRangeKey }

/**
 * What the rail is currently pointed at.
 *
 * A focus is a lens on the SAME activity feed, not a different page: pick an
 * issue and the whole surface — cards, chart, counts — narrows to that
 * issue's activity. That is the difference the user asked for between this
 * rail and the ones on /issues and /routines, which navigate to the entity
 * itself. It is why the issue and routine lists sit in the filter popover
 * now: they were always narrowings wearing navigation's clothes.
 */
export interface EntityFocus {
  kind: "issue" | "routine" | "crew"
  id: string
  label: string
}

export interface SidebarIssue {
  id: string
  identifier?: string
  title: string
  status: string
  priority?: string | null
  assignee_id?: string | null
  assignee_name?: string | null
}

export interface SidebarRoutine {
  id: string
  slug: string
  name: string
  icon?: string | null
  color?: string | null
  invocation_count?: number
  last_invocation_status?: string | null
}

export interface SidebarCrew {
  id: string
  name: string
  icon?: string | null
  color?: string | null
}

export interface FacetState {
  scope: ActivityScope | "all"
  sources: ActivitySource[]
  severities: string[]
  crewIDs: string[]
  agentIDs: string[]
  range: TimeRangeKey
  /** Put the per-minute container/exec telemetry back into the feed. */
  showTelemetry: boolean
}

export const EMPTY_FACETS: FacetState = {
  scope: "all",
  sources: [],
  severities: [],
  crewIDs: [],
  agentIDs: [],
  range: DEFAULT_RANGE,
  showTelemetry: false,
}

function toggle<T>(list: T[], value: T): T[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value]
}

function Dot({ token }: { token: string }) {
  return (
    <span
      aria-hidden
      className="h-1.5 w-1.5 shrink-0 rounded-full"
      style={{ background: `var(${token})` }}
    />
  )
}

function Count({ n, dim }: { n: number; dim?: boolean }) {
  return (
    <span
      className={cn(
        "ml-auto shrink-0 font-mono text-[10px] tabular-nums",
        dim ? "text-muted-foreground-soft" : "text-muted-foreground",
      )}
    >
      {n}
    </span>
  )
}

/**
 * The status line: All · Running · Waiting · Failed, one line, one choice.
 *
 * This replaces the STATUS section — five full-width rows for what is a single
 * mutually-exclusive pick. The tones are the ones the overview cards already
 * read for the same four buckets (--info / --warn / --destructive / --success),
 * so a failure is one colour across the page; the bucket-list glyphs went with
 * the bucket list.
 *
 * A segment with no number is one this query cannot count, not one holding
 * nothing — see railSegments.
 */
function ScopeSegments({
  segments,
  scope,
  onScope,
}: {
  segments: RailSegment[]
  scope: FacetState["scope"]
  onScope: (s: FacetState["scope"]) => void
}) {
  return (
    <div
      role="group"
      aria-label="Activity status"
      className="mx-2 mb-1.5 flex shrink-0 items-center gap-0.5 rounded-md border border-white/[0.08] bg-white/[0.04] p-0.5"
    >
      {segments.map((s) => {
        const selected = scope === s.key
        const empty = s.count === 0
        return (
          <button
            key={s.key}
            type="button"
            aria-pressed={selected}
            title={s.hint}
            onClick={() => onScope(s.key)}
            className={cn(
              "flex min-w-0 flex-1 items-center justify-center gap-1 rounded px-1 py-1 text-[11px] transition-colors",
              selected
                ? "bg-primary/15 text-primary"
                : empty
                  ? "text-foreground/40 hover:bg-white/[0.04] hover:text-foreground"
                  : "text-muted-foreground hover:bg-white/[0.04] hover:text-foreground",
            )}
          >
            <span className="truncate">{s.label}</span>
            {s.count != null && s.count > 0 && (
              <span
                className="shrink-0 font-mono text-[10px] tabular-nums"
                style={selected ? undefined : { color: `var(${s.token})` }}
              >
                {s.count}
              </span>
            )}
          </button>
        )
      })}
    </div>
  )
}

/**
 * Which catalogue the rail is listing, and the row grammar for each.
 *
 * The rail listed workflows and nothing else, which answered "which routine
 * ran" and left "what happened to ENG-7" and "what did my agents do" with
 * nowhere to be asked. Before that it stacked four catalogues and 56 rows read
 * 0. The lens is the third option: one list at a time, and each list holds only
 * the members that were ACTIVE in this window — an issue nobody touched today
 * is in /issues, not here. See lib/activity-lenses.
 */
function LensTabs({
  lens,
  counts,
  onLens,
}: {
  lens: LensKey
  counts: Record<LensKey, number>
  onLens: (l: LensKey) => void
}) {
  return (
    <div role="tablist" aria-label="Activity lens" className="mx-2 mb-1.5 flex shrink-0 gap-1 border-b border-white/[0.06]">
      {ACTIVITY_LENSES.map((l) => {
        const on = lens === l.key
        const n = counts[l.key]
        return (
          <button
            key={l.key}
            role="tab"
            type="button"
            aria-selected={on}
            title={l.hint}
            onClick={() => onLens(l.key)}
            className={cn(
              "-mb-px flex min-w-0 items-center gap-1 border-b-2 px-1.5 py-1.5 text-[11px] transition-colors",
              on
                ? "border-primary font-medium text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            <span className="truncate">{l.label}</span>
            {/* A zero is printed rather than hidden: an empty lens is an answer
                ("nothing touched an issue today"), and a tab with no number
                beside three that have one reads as still loading. */}
            <span className={cn("font-mono text-[10px] tabular-nums", n === 0 && "text-muted-foreground-soft")}>
              {n}
            </span>
          </button>
        )
      })}
    </div>
  )
}

/** Tone token per chain status — the same four the overview cards read. */
const STATUS_TOKEN: Record<ChainStatus, string> = {
  waiting: "--warn",
  failed: "--destructive",
  running: "--primary",
  done: "--success",
}

/**
 * One workflow, wearing its routine's face.
 *
 * The grammar is the Routines rail's, deliberately and to the pixel: the
 * routine's own icon and colour at 20px, a status dot notched into its corner,
 * and a halo while something is live. This row used to be a 6px grey dot beside
 * two lines of text — the same row for every workflow in the workspace — while
 * one screen away the same routine had a face. A workflow IS a run of a
 * routine; it should not need a second visual language.
 *
 * The handle on the right is what tells two runs of one routine apart when
 * everything else about them matches. See workflowHandle.
 */
function WorkflowRow({
  chain,
  routine,
  selected,
  index,
  onSelect,
}: {
  chain: ChainSummary
  routine?: SidebarRoutine
  selected: boolean
  /** Position in the list, for the entry stagger. */
  index: number
  onSelect: () => void
}) {
  const status = chainStatus(chain)
  const live = status === "running" || status === "waiting"
  const sentence = workflowSentence(chain, routine?.name)
  const touched = chainTouched(chain)
  const handle = workflowHandle(chain.origin)
  const startedBy = chain.started_by?.trim() || chain.triggered_via || "unknown trigger"

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        {/* Rows arrive in sequence, so a filter reads as the list narrowing
            rather than as the list being replaced. Capped: past a dozen rows a
            per-row stagger stops being a cascade and starts being a wait. The
            Routines rail makes exactly this call. */}
        <motion.div
          initial={{ opacity: 0, y: 4 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1], delay: Math.min(index, 12) * 0.018 }}
        >
    <SidebarRow selected={selected} onSelect={onSelect} className="!items-start !py-1.5">
      <span className="relative mt-0.5 shrink-0">
        {/* A halo, not a moved icon — the same call the Routines rail makes.
            Absolutely positioned so a live row costs no layout: a ring that
            changed the row's width would nudge every name beside it. */}
        {live && (
          <span
            aria-hidden
            className="absolute -inset-1 animate-ping rounded-lg opacity-60"
            style={{ background: `color-mix(in oklab, var(${STATUS_TOKEN[status]}) 22%, transparent)` }}
          />
        )}
        {routine ? (
          <CrewIcon
            icon={resolveRoutineIcon(routine)}
            color={resolveRoutineColor(routine)}
            size="sm"
            className="relative !h-5 !w-5 !rounded-md"
          />
        ) : (
          // No routine ran: an agent-rooted chain, or one whose routine is gone.
          // A neutral tile rather than a borrowed icon — wearing some other
          // routine's face would be the one thing worse than wearing none.
          <span
            aria-hidden
            className="relative flex h-5 w-5 items-center justify-center rounded-md bg-white/[0.07] text-[10px] text-muted-foreground"
          >
            <Workflow className="h-3 w-3" />
          </span>
        )}
        <span
          aria-hidden
          title={status}
          className="absolute -bottom-0.5 -right-0.5 z-10 h-2 w-2 rounded-full ring-2 ring-card"
          style={{ background: `var(${STATUS_TOKEN[status]})` }}
        />
      </span>

      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        {/* The SHAPE, not the routine's name. Two runs of one routine render
            the same word; what they did differs, and that is the line. */}
        <span className="truncate text-foreground/85" title={sentence}>
          {sentence}
        </span>
        <span className="flex items-center gap-1.5 truncate text-[10.5px] text-muted-foreground-soft">
          <span>{relTime(chain.last_activity)}</span>
          {chain.duration_ms != null && (
            <>
              <span aria-hidden>·</span>
              <span>{formatDurationMs(chain.duration_ms)}</span>
            </>
          )}
          {touched && (
            <>
              <span aria-hidden>·</span>
              <span className="truncate">{touched}</span>
            </>
          )}
        </span>
      </span>

      <span className="mt-0.5 shrink-0 font-mono text-[10px] text-muted-foreground-soft">{handle}</span>
    </SidebarRow>
        </motion.div>
      </TooltipTrigger>
      {/* What the two truncated lines drop. The Routines rail carries the same
          card for the same reason: a 280px column elides exactly the part that
          tells two rows apart, and hovering is where it comes back. */}
      <TooltipContent side="right" sideOffset={8}>
        <div className="space-y-0.5">
          <div className="font-medium">{sentence}</div>
          <div className="font-mono text-[10px] opacity-70">{handle}</div>
          <div className="text-[10px] opacity-70">
            {startedBy} · {status}
            {chain.runs > 1 && ` · ${chain.runs} runs`}
            {chain.max_chain_depth > 0 && ` · depth ${chain.max_chain_depth}`}
          </div>
          {touched && <div className="max-w-[280px] text-[10px] opacity-70">{touched}</div>}
        </div>
      </TooltipContent>
    </Tooltip>
  )
}

/** What an empty lens needs to say, and the facts that decide which sentence. */
export interface EmptyLensFacts {
  lens: LensKey
  /** Chains in the window that composed nothing — the Workflows lens's own case. */
  bareRuns: number
  /** Rows the index returned before any narrowing. 0 means an empty workspace. */
  loadedChainCount: number
  /** The search box emptied the window. */
  narrowedAway: boolean
  /** The status segment emptied what the search left. */
  scopedAway: boolean
  chainsHaveUnrecorded: boolean
}

/**
 * The sentence under an empty lens.
 *
 * Five distinct emptinesses reach this column and they want five different
 * actions from the reader, so they get five sentences. Collapsing them into one
 * "Nothing here" is what the rail did before, and it sent a reader hunting for
 * a broken page when the answer was "clear the search".
 *
 * Pure and exported because these are the branches worth testing: which
 * sentence a state produces is a decision, and asserting it through a mounted
 * component tests React.
 */
export function emptyLensCopy(f: EmptyLensFacts): string {
  if (f.loadedChainCount === 0) {
    return f.chainsHaveUnrecorded
      ? "No workflows recorded yet. Runs from before chain recording cannot be grouped — the link was never written."
      : "No workflows yet. One appears the first time something causes something else."
  }
  if (f.narrowedAway) return "Nothing in this window matches the search. Clear it to see the rest."
  if (f.scopedAway) return "Nothing in this window has that status. Pick another, or All."
  // Past here the window HAS chains — the lens simply holds none of them, which
  // is an answer about the work rather than about the filters.
  switch (f.lens) {
    case "workflows":
      return f.bareRuns > 0
        ? `Nothing composed a process in this window. ${f.bareRuns} ${
            f.bareRuns === 1 ? "run" : "runs"
          } happened on their own — they are under Routines.`
        : "Nothing composed a process in this window."
    case "issues":
      return "Nothing touched an issue in this window. Issues with no activity are in Issues, not here."
    case "agents":
      return "No agent took work inside a workflow in this window. Work no routine dispatched has no chain to belong to, so it is not indexed here."
    case "routines":
      return "No routine ran in this window. The catalogue of every routine — including the ones that have never run — is on the Routines page."
  }
}

export interface ActivitySidebarProps {
  search: string
  onSearchChange: (v: string) => void
  facets: FacetState
  onChange: (next: FacetState) => void
  crews: SidebarCrew[]
  agents: { id: string; name: string; crew_id: string | null }[]
  issues: SidebarIssue[]
  routines: SidebarRoutine[]
  crewCounts: Record<string, number>
  issueCounts: Record<string, number>
  routineCounts: Record<string, number>
  focus: EntityFocus | null
  onFocus: (f: EntityFocus | null) => void
  /**
   * The workflow runs this whole page is looking at, newest first — the loaded
   * index window with the search box and the status segment already applied.
   *
   * Narrowed by the SHELL rather than here, and that is the point. The rail used
   * to filter its own private copy while the shell handed the unnarrowed array
   * to the three lens dashboards beside it, so a search left this column showing
   * two rows next to a dashboard reporting twenty. One narrowing, one call, both
   * halves of the screen reading its result. See narrowChains.
   */
  chains: ChainSummary[]
  /**
   * The same window with the search applied but NOT the status segment.
   *
   * The segments count over this, because a count has to survive its own
   * selection: counting over `chains` would make picking "Failed" render
   * "Failed 3 · Waiting 0 · Running 0" — what is left after the pick rather
   * than what there is to pick.
   */
  chainsBeforeStatus: ChainSummary[]
  /**
   * How many rows the index returned before any narrowing.
   *
   * Only used to tell two emptinesses apart, and they need opposite actions:
   * "nothing here matches, widen the question" versus "nothing has ever run in
   * this workspace".
   */
  loadedChainCount: number
  /**
   * The workspace holds more chains than this window. Every number in this
   * column describes the window, so when this is true the column says so.
   */
  chainsHaveMore: boolean
  chainsHaveUnrecorded: boolean
  /**
   * Routines by slug, for a row's icon, colour and human name.
   *
   * Built by the shell rather than here because the shell also needs it to
   * resolve names for the search — and a map built in two places is a map with
   * two contents the day one of them grows a rule.
   */
  routineBySlug: Map<string, SidebarRoutine>
  selectedChain: string | null
  onSelectChain: (origin: string | null) => void
  /** Which catalogue the rail is listing. Owned by the shell so it survives a drill-down. */
  lens: LensKey
  onLens: (l: LensKey) => void
  /**
   * A row in a non-workflow lens was picked.
   *
   * Deliberately kind-agnostic rather than reusing `onFocus`: EntityFocus knows
   * three kinds and an agent is not one of them, and widening it would mean the
   * rail's narrowing type and the walk's node type saying the same thing twice.
   * The shell turns this into one stop on the path, the same as a node clicked
   * out of a chain graph.
   */
  onOpenEntity: (kind: string, id: string, label: string) => void
  onToggleCollapse: () => void
}

export function ActivitySidebar({
  search,
  onSearchChange,
  facets,
  onChange,
  crews,
  agents,
  issues,
  routines,
  crewCounts,
  issueCounts,
  routineCounts,
  focus,
  onFocus,
  chains,
  chainsBeforeStatus,
  loadedChainCount,
  chainsHaveMore,
  chainsHaveUnrecorded,
  routineBySlug,
  selectedChain,
  onSelectChain,
  lens,
  onLens,
  onOpenEntity,
  onToggleCollapse,
}: ActivitySidebarProps) {
  // See railInventory. Unfocused the popover answers "where is the activity";
  // focused it has to answer "where else can I go", or picking one issue
  // deletes every other option and there is no way back out except the crumb.
  const activeIssues = React.useMemo(
    () => railInventory(issues, issueCounts, (i) => i.id, focus != null),
    [issues, issueCounts, focus],
  )
  const activeRoutines = React.useMemo(
    () => railInventory(routines, routineCounts, (r) => r.slug, focus != null),
    [routines, routineCounts, focus],
  )
  const visibleAgents = React.useMemo(
    () =>
      facets.crewIDs.length === 0
        ? agents
        : agents.filter((a) => a.crew_id != null && facets.crewIDs.includes(a.crew_id)),
    [agents, facets.crewIDs],
  )

  // `chains` arrives already narrowed by the shell — search and status segment
  // both applied — so this column and the dashboard beside it cannot disagree
  // about what happened. See ActivitySidebarProps.chains.
  const scopedChains = chains

  // Only composed chains are workflows. A bare run — one routine, invoked,
  // nothing bound to anything — is a run of that routine and belongs in the
  // Routines lens, which is where it already appears. Twelve of twenty-one rows
  // here were that, which is what made this lens read as a worse-named copy of
  // Routines. See isComposed.
  const workflowChains = React.useMemo(() => scopedChains.filter(isComposed), [scopedChains])
  const bareRuns = scopedChains.length - workflowChains.length

  const lensIssues = React.useMemo(() => issueLens(scopedChains), [scopedChains])
  const lensAgents = React.useMemo(() => agentLens(scopedChains), [scopedChains])
  const lensRoutines = React.useMemo(() => routineLens(scopedChains), [scopedChains])
  const lensCounts = React.useMemo<Record<LensKey, number>>(
    () => ({
      workflows: workflowChains.length,
      issues: lensIssues.length,
      agents: lensAgents.length,
      routines: lensRoutines.length,
    }),
    [workflowChains.length, lensIssues.length, lensAgents.length, lensRoutines.length],
  )

  // Sections, not a flat list. A chain that has been parked on an approval since
  // Tuesday is the most urgent row on the page and sorts to the bottom by
  // timestamp; "Active now" is a state and outranks the clock. See timeBucket.
  //
  // `now` is read once per list build rather than per row: two rows computed
  // either side of local midnight would land in different buckets from the same
  // render, which is a boundary nobody can see and everybody would report.
  const buckets = React.useMemo(() => bucketChains(workflowChains, Date.now()), [workflowChains])

  // The segments count CHAINS, which is what the list under them holds. They
  // counted journal entries before, so "Failed 9" sat above three failed
  // workflows — one control, one list, two numbers describing different objects.
  //
  // Counted over `chainsBeforeStatus`, i.e. everything the search left, so a
  // number survives its own selection. Marked complete because the chain index
  // is fetched independently of the scope facet, so all four buckets of THIS
  // WINDOW are genuinely held whichever segment is picked — which is a
  // different claim from "these are all the chains there are", and the one
  // `chainsHaveMore` is rendered for.
  const chainCounts = React.useMemo(
    () => chainScopeCounts(chainsBeforeStatus),
    [chainsBeforeStatus],
  )
  const segments = React.useMemo(
    () => railSegments(facets.scope, chainCounts, chainsBeforeStatus.length, true),
    [facets.scope, chainCounts, chainsBeforeStatus.length],
  )

  const facetKeys = filterFacets({
    crews: crews.length,
    agents: visibleAgents.length,
    issues: activeIssues.length,
    routines: activeRoutines.length,
  })
  const has = (k: (typeof facetKeys)[number]) => facetKeys.includes(k)
  const first = (k: (typeof facetKeys)[number]) => facetKeys[0] === k

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder="Search activity…"
          aria-label="Search activity"
        />
        <SidebarFilterPopover
          label="Filter activity"
          activeCount={activeFilterCount(facets, focus != null)}
          // The panel is anchored to the trigger's right edge inside a 280px
          // rail that clips its overflow, so anything wider than the trigger's
          // distance from the rail's left edge loses its first characters —
          // "FILTERS" rendering as "TERS" is what 264px looked like.
          panelClassName="w-[228px]"
          onClear={() => {
            onChange(clearedFilters(facets))
            onFocus(null)
          }}
        >
          {has("crew") && (
            <SidebarFacet
              first={first("crew")}
              label="Crew"
              resetLabel="All crews"
              resetActive={facets.crewIDs.length === 0}
              onReset={() => onChange({ ...facets, crewIDs: [], agentIDs: [] })}
            >
              {crews.map((c) => {
                const n = crewCounts[c.id] ?? 0
                return (
                  <SidebarFacetOption
                    key={c.id}
                    active={facets.crewIDs.includes(c.id)}
                    onToggle={() => {
                      const crewIDs = toggle(facets.crewIDs, c.id)
                      // Dropping a crew drops its agents too — otherwise the
                      // filter keeps narrowing on someone no longer listed.
                      const stillVisible = new Set(
                        agents
                          .filter((a) => a.crew_id && crewIDs.includes(a.crew_id))
                          .map((a) => a.id),
                      )
                      onChange({
                        ...facets,
                        crewIDs,
                        agentIDs:
                          crewIDs.length === 0
                            ? facets.agentIDs
                            : facets.agentIDs.filter((id) => stillVisible.has(id)),
                      })
                    }}
                  >
                    {/* The crew's own icon + colour, the same derivation the
                        crew pages use — two surfaces drawing one crew
                        differently is worse than drawing none. */}
                    <CrewIcon
                      icon={c.icon ?? "users"}
                      color={c.color}
                      size="sm"
                      className={cn("!h-4 !w-4 !rounded shrink-0", n === 0 && "opacity-40")}
                    />
                    <span className="truncate">{c.name}</span>
                    <Count n={n} dim={n === 0} />
                  </SidebarFacetOption>
                )
              })}
            </SidebarFacet>
          )}

          {has("agent") && (
            <SidebarFacet
              first={first("agent")}
              label="Agent"
              resetLabel="All agents"
              resetActive={facets.agentIDs.length === 0}
              onReset={() => onChange({ ...facets, agentIDs: [] })}
            >
              {visibleAgents.map((a) => (
                <SidebarFacetOption
                  key={a.id}
                  active={facets.agentIDs.includes(a.id)}
                  onToggle={() => onChange({ ...facets, agentIDs: toggle(facets.agentIDs, a.id) })}
                >
                  <AgentAvatar seed={a.id} alt="" className="h-4 w-4 shrink-0 rounded-full" />
                  <span className="truncate">{a.name}</span>
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}

          {has("issue") && (
            <SidebarFacet
              first={first("issue")}
              label="Issue"
              resetLabel="Any issue"
              resetActive={focus?.kind !== "issue"}
              onReset={() => focus?.kind === "issue" && onFocus(null)}
            >
              {activeIssues.map((issue) => (
                <SidebarFacetOption
                  key={issue.id}
                  active={focus?.kind === "issue" && focus.id === issue.id}
                  onToggle={() =>
                    onFocus(
                      focus?.kind === "issue" && focus.id === issue.id
                        ? null
                        : {
                            kind: "issue",
                            id: issue.id,
                            label: issue.identifier || issue.title,
                          },
                    )
                  }
                >
                  <StatusIcon status={issue.status} className="h-3.5 w-3.5 shrink-0" />
                  {/* The identifier only when there is one. A fixed column
                      spent 42px of a 228px panel printing "--" for every
                      issue in a workspace that does not use identifiers. */}
                  {issue.identifier && (
                    <span className="shrink-0 font-mono text-[10px] text-foreground/50">
                      {issue.identifier}
                    </span>
                  )}
                  <span className="truncate" title={issue.title}>
                    {issue.title}
                  </span>
                  <Count n={issueCounts[issue.id] ?? 0} />
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}

          {has("routine") && (
            <SidebarFacet
              first={first("routine")}
              label="Routine"
              resetLabel="Any routine"
              resetActive={focus?.kind !== "routine"}
              onReset={() => focus?.kind === "routine" && onFocus(null)}
            >
              {activeRoutines.map((r) => (
                <SidebarFacetOption
                  key={r.id}
                  active={focus?.kind === "routine" && focus.id === r.slug}
                  onToggle={() =>
                    onFocus(
                      focus?.kind === "routine" && focus.id === r.slug
                        ? null
                        : { kind: "routine", id: r.slug, label: r.name },
                    )
                  }
                >
                  {/* Same icon + colour derivation as the routines rail and
                      the routine detail header. */}
                  <CrewIcon
                    icon={resolveRoutineIcon(r as never)}
                    color={resolveRoutineColor(r as never)}
                    size="sm"
                    className="!h-4 !w-4 !rounded shrink-0"
                  />
                  <span className="truncate">{r.name}</span>
                  <Count n={routineCounts[r.slug] ?? 0} />
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}

          <SidebarFacet
            first={first("range")}
            label="Time range"
            resetLabel="Past 24 hours"
            resetActive={facets.range === DEFAULT_RANGE}
            onReset={() => onChange({ ...facets, range: DEFAULT_RANGE })}
          >
            {TIME_RANGES.filter((r) => r.key !== DEFAULT_RANGE).map((r) => (
              <SidebarFacetOption
                key={r.key}
                active={facets.range === r.key}
                onToggle={() => onChange({ ...facets, range: r.key })}
              >
                {r.label}
              </SidebarFacetOption>
            ))}
          </SidebarFacet>

          <SidebarFacet
            label="Source"
            resetLabel="Everything"
            resetActive={facets.sources.length === 0}
            onReset={() => onChange({ ...facets, sources: [] })}
          >
            {railSources(ACTIVITY_SOURCES).map((s) => (
              <SidebarFacetOption
                key={s.key}
                active={facets.sources.includes(s.key)}
                onToggle={() => onChange({ ...facets, sources: toggle(facets.sources, s.key) })}
              >
                <Dot token={s.token} />
                <span className="truncate" title={s.hint}>
                  {s.label}
                </span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>

          {/* No "Error" here: that is the Failed segment above, and the same
              query. See RAIL_SEVERITIES. */}
          <SidebarFacet
            label="Severity"
            resetLabel="Any severity"
            resetActive={facets.severities.length === 0}
            onReset={() => onChange({ ...facets, severities: [] })}
          >
            {RAIL_SEVERITIES.map((s) => (
              <SidebarFacetOption
                key={s.key}
                active={facets.severities.includes(s.key)}
                onToggle={() =>
                  onChange({ ...facets, severities: toggle(facets.severities, s.key) })
                }
              >
                <Dot token={s.token} />
                {s.label}
              </SidebarFacetOption>
            ))}
          </SidebarFacet>

          <SidebarFacet
            label="Noise"
            resetLabel="Hide system telemetry"
            resetActive={!facets.showTelemetry}
            onReset={() => onChange({ ...facets, showTelemetry: false })}
          >
            <SidebarFacetOption
              active={facets.showTelemetry}
              onToggle={() => onChange({ ...facets, showTelemetry: !facets.showTelemetry })}
            >
              <span
                className="truncate"
                title="container.metrics, snapshots, exec output, status pings"
              >
                Show system telemetry
              </span>
            </SidebarFacetOption>
          </SidebarFacet>
        </SidebarFilterPopover>
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <LensTabs lens={lens} counts={lensCounts} onLens={onLens} />

      <ScopeSegments
        segments={segments}
        scope={facets.scope}
        onScope={(s) => onChange({ ...facets, scope: s })}
      />

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        {lensCounts[lens] === 0 ? (
          // Emptiness is measured on THE LENS THAT IS OPEN, not on the chain
          // list behind it. Measuring the chains meant an Issues lens with no
          // issue-touching chain fell through to the list branch and rendered a
          // section header reading "ISSUES TOUCHED 0" with nothing under it and
          // no sentence saying why — a bare zero over a blank column, which is
          // the exact wall of zeros this rail was rebuilt to delete.
          <p className="px-3 py-2 text-[11px] leading-snug text-muted-foreground-soft">
            {emptyLensCopy({
              lens,
              bareRuns,
              loadedChainCount,
              narrowedAway: loadedChainCount > 0 && chainsBeforeStatus.length === 0,
              scopedAway: chainsBeforeStatus.length > 0 && scopedChains.length === 0,
              chainsHaveUnrecorded,
            })}
          </p>
        ) : lens === "workflows" ? (
          // Radix needs a provider in scope or every TooltipTrigger renders a
          // plain child and the hover card silently never appears. Same delay as
          // the Routines rail so a pointer crossing the two feels like one app.
          <TooltipProvider delayDuration={400}>
          {buckets.map((b) => (
            // Not collapsible. A chevron whose one job is to hide what is
            // running is a trap, not a control.
            <SidebarSection
              key={b.key}
              label={b.label}
              count={b.chains.length}
              headerClassName={b.key === "active" ? "text-primary" : undefined}
            >
              {b.chains.map((c, i) => (
                <WorkflowRow
                  key={c.origin}
                  chain={c}
                  index={i}
                  routine={routineBySlug.get(c.routine_slug ?? "")}
                  selected={selectedChain === c.origin}
                  onSelect={() => onSelectChain(selectedChain === c.origin ? null : c.origin)}
                />
              ))}
            </SidebarSection>
          ))}
          </TooltipProvider>
        ) : lens === "issues" ? (
          <SidebarSection label="Issues touched" count={lensIssues.length}>
            {lensIssues.map((i) => (
              <SidebarRow
                key={i.id}
                selected={focus?.kind === "issue" && focus.id === i.id}
                onSelect={() => onOpenEntity("issue", i.id, i.identifier || i.title || i.id)}
              >
                <CircleDot className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                {i.identifier && (
                  <span className="shrink-0 font-mono text-[10px] text-foreground/50">{i.identifier}</span>
                )}
                <span className="truncate" title={i.title}>
                  {i.title || i.id}
                </span>
                {/* "created" is the strongest thing a window can say about an
                    issue — it exists BECAUSE of work that happened in it. */}
                {i.created && (
                  <span className="shrink-0 rounded bg-success/15 px-1 py-px text-[9px] uppercase tracking-wide text-success">
                    new
                  </span>
                )}
                <Count n={i.chains.length} />
              </SidebarRow>
            ))}
          </SidebarSection>
        ) : lens === "agents" ? (
          <SidebarSection label="Agents at work" count={lensAgents.length}>
            {lensAgents.map((a) => (
              <SidebarRow
                key={a.id}
                // No `selected` here, and that is now stated rather than faked.
                // This read `focus?.kind === ("agent" as never)`, which the cast
                // made compile and which is false for every value focus can
                // hold — EntityFocus knows issue, routine and crew. An agent row
                // opens a STOP on the path, not a focus, so the rail has nothing
                // to highlight from; the trail above the column is what says
                // where the reader is. The cast is gone because its only effect
                // was to hide that from the type checker.
                onSelect={() => onOpenEntity("agent", a.id, a.name || a.slug || a.id)}
              >
                <AgentAvatar seed={a.id} alt="" className="h-4 w-4 shrink-0 rounded-full" />
                <span className="truncate">{a.name || a.slug || a.id}</span>
                <span className="ml-auto shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                  ×{a.assignments}
                </span>
              </SidebarRow>
            ))}
          </SidebarSection>
        ) : (
          <SidebarSection label="Routines that ran" count={lensRoutines.length}>
            {lensRoutines.map((r) => {
              const known = routineBySlug.get(r.slug)
              return (
                <SidebarRow
                  key={r.slug}
                  selected={focus?.kind === "routine" && focus.id === r.slug}
                  onSelect={() => onOpenEntity("routine", r.slug, known?.name || r.slug)}
                >
                  <CrewIcon
                    icon={resolveRoutineIcon(known ?? { slug: r.slug })}
                    color={resolveRoutineColor(known ?? { slug: r.slug })}
                    size="sm"
                    className="!h-4 !w-4 !rounded shrink-0"
                  />
                  <span className="truncate">{known?.name || r.slug}</span>
                  {r.failed && (
                    <span aria-hidden title="a run failed" className="h-1.5 w-1.5 shrink-0 rounded-full bg-destructive" />
                  )}
                  {/* Runs, not chains: "it ran 12 times today" is the sentence
                      somebody wants, and one chain can hold several runs. */}
                  <Count n={r.runs} />
                </SidebarRow>
              )
            })}
          </SidebarSection>
        )}

        {lens === "workflows" && workflowChains.length > 0 && bareRuns > 0 && (
          <p className="px-3 pb-1 pt-1.5 text-[10.5px] leading-snug text-muted-foreground-soft">
            {bareRuns} {bareRuns === 1 ? "run" : "runs"} composed nothing and {bareRuns === 1 ? "is" : "are"}{" "}
            listed under Routines.
          </p>
        )}

        {/* The edge of what this column can see, stated where the numbers are.
            Every count above — four lens tabs, four status segments — is derived
            from ONE PAGE of the index, and without this line "Agents 4" over a
            workspace with a thousand workflows reads as a fact about the
            workspace while being a fact about the newest {loadedChainCount}. The
            server has always sent has_more; nothing read it until the counts
            started depending on it. */}
        {chainsHaveMore && loadedChainCount > 0 && (
          <p className="px-3 pb-1 pt-1.5 text-[10.5px] leading-snug text-muted-foreground-soft">
            Newest {loadedChainCount} workflows. Every count on this page describes these, not the
            whole workspace.
          </p>
        )}

        {scopedChains.length > 0 && chainsHaveUnrecorded && (
          <p className="px-3 pb-1 pt-1.5 text-[10.5px] leading-snug text-muted-foreground-soft">
            Older runs are not indexed here — the link that would group them was never written.
          </p>
        )}
      </div>
    </div>
  )
}
