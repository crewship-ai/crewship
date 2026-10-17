"use client"

import { useMemo, useState } from "react"
import { ArrowDownUp, Check, ChevronLeft, Hand, LayoutDashboard, List, ListChecks, Pause, Pencil, Play, PenLine, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { cn } from "@/lib/utils"
import type { LucideIcon } from "lucide-react"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarFilterButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
  SidebarViewButton,
} from "@/components/layout/sidebar-kit"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import { useUserPreference } from "@/hooks/use-user-preference"
import {
  matchesRoutineFilters,
  routineFilterInput,
  type RoutineFilterState,
  type RoutineLifecycleFilter,
  type RoutineStatusFilter,
} from "@/lib/routine-filters"
import type { Pipeline } from "@/hooks/use-pipelines"
import { isAwaitingApproval, useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import {
  EMPTY_PANEL_INDEX,
  ROUTINE_TRIGGER_KINDS,
  groupRoutines,
  sortRoutines,
  type RoutineGroupBy,
  type RoutineTriggerKind,
  type RoutinePanelIndex,
  type RoutineSort,
} from "@/lib/routine-panel-index"
import { RoutineGlyph } from "./routine-glyph"
import type { PeekRun } from "./routine-run-peek"
import { runBarTone } from "@/lib/routine-page"
import { formatDurationMs } from "@/lib/activity-stream"

// The Routines secondary panel (Harbor 2): Views first — Overview, Needs
// you, Running now, All runs, Drafts — then every routine grouped by the
// crew that owns it. A row is the routine's glyph, its name, a pencil when
// it has changes that are not live, and at most one status pill. Facets
// (Status, Last run, Trigger, Crew) live in the Filter panel; group (Crew,
// Trigger, None) and sort (Next run, Last run, Name, Spend) in View. Trigger,
// next run and spend come from the panel index the layout composes from the
// schedule, webhook, budget and chat command lists (lib/routine-panel-index).
// Folders are not offered: routines have no folders (they are an Artifacts
// concept), so the crew is the grouping the data actually has.

export type RoutinesView = "overview" | "needs" | "live" | "runs" | "drafts"

export const ROUTINE_VIEWS: { id: RoutinesView; label: string; icon: LucideIcon }[] = [
  { id: "overview", label: "Overview", icon: LayoutDashboard },
  { id: "needs", label: "Needs you", icon: Hand },
  { id: "live", label: "Running now", icon: Play },
  { id: "runs", label: "All runs", icon: List },
  { id: "drafts", label: "Drafts", icon: PenLine },
]

export interface PanelCrew {
  id: string
  name: string
}

interface RoutinesExplorerProps {
  routines: Pipeline[]
  search: string
  onSearchChange: (value: string) => void
  selectedSlug: string | null
  onSelectRoutine: (slug: string) => void
  filters: RoutineFilterState
  onChange: (next: RoutineFilterState) => void
  /** The list view on screen; null while a routine or a run is open. */
  view: RoutinesView | null
  onViewChange: (view: RoutinesView) => void
  /** Counts beside the views; a null count is not shown. */
  counts: { needs: number | null; live: number | null; drafts: number | null }
  crews: ReadonlyMap<string, PanelCrew>
  /** Triggers, next start and spend per routine; empty until loaded. */
  index?: RoutinePanelIndex
  /** While a run is open the panel lists the runs of its routine (v67 rtPanel). */
  runsOf?: {
    routineName: string
    runs: PeekRun[]
    current: string
    onPeek: (runId: string) => void
    onBack: () => void
  }
  /** Select several (PNL-03). Absent on a phone, where there is no selection. */
  bulk?: {
    /** Turning off is Owner and Admin on the server. */
    canTurnOff: boolean
    onTurnOff: (slugs: string[]) => Promise<void> | void
    onRunNow: (slugs: string[]) => Promise<void> | void
  }
}

/** The one pill a row carries: a live run first, then the routine's own
 *  state, then how its last run ended. Nothing when all is well. */
export function routineRowState(
  routine: Pipeline,
  live?: { status: string } | null,
): { tone: StatusTone; label: string; live?: boolean } | null {
  if (live) return isAwaitingApproval(live.status) ? { tone: "warn", label: "Waiting", live: true } : { tone: "blue", label: "Running", live: true }
  if (routine.status === "proposed") return { tone: "purple", label: "Proposed" }
  if (routine.status === "disabled") return { tone: "muted", label: "Off" }
  if (routine.last_invocation_status?.toLowerCase() === "failed" || routine.last_run_outcome === "FAILED") return { tone: "danger", label: "Failed" }
  return null
}

const LIFECYCLE: { id: Exclude<RoutineLifecycleFilter, "all">; label: string }[] = [
  { id: "active", label: "Active" },
  { id: "draft", label: "Has draft changes" },
  { id: "proposed", label: "Proposed" },
  { id: "disabled", label: "Turned off" },
]
const LAST_RUN: { id: Exclude<RoutineStatusFilter, "all">; label: string }[] = [
  { id: "completed", label: "Completed" },
  { id: "awaiting", label: "Waiting for a person" },
  { id: "failed", label: "Could not finish" },
  { id: "running", label: "Running" },
  { id: "never", label: "Never run" },
]

const GROUPS: { id: RoutineGroupBy; label: string }[] = [
  { id: "crew", label: "Crew" },
  { id: "trigger", label: "Trigger" },
  { id: "none", label: "None" },
]
const SORTS: { id: RoutineSort; label: string }[] = [
  { id: "next", label: "Next run" },
  { id: "recent", label: "Last run" },
  { id: "name", label: "Name" },
  { id: "spend", label: "Spend" },
]

export function RoutinesExplorer({
  routines,
  search,
  onSearchChange,
  selectedSlug,
  onSelectRoutine,
  filters,
  onChange,
  view,
  onViewChange,
  counts,
  crews,
  index = EMPTY_PANEL_INDEX,
  bulk,
  runsOf,
}: RoutinesExplorerProps) {
  if (runsOf) return <RoutineRunsPanel {...runsOf} />
  return (
    <RoutinesList
      {...{ routines, search, onSearchChange, selectedSlug, onSelectRoutine, filters, onChange, view, onViewChange, counts, crews, index, bulk }}
    />
  )
}

type RunBucket = "all" | "waiting" | "running" | "completed" | "stopped" | "failed"
const RUN_BUCKETS: { id: RunBucket; label: string }[] = [
  { id: "all", label: "All" },
  { id: "waiting", label: "Waiting for a person" },
  { id: "running", label: "Running" },
  { id: "completed", label: "Completed" },
  { id: "stopped", label: "Stopped" },
  { id: "failed", label: "Could not finish" },
]
const DOT: Record<Exclude<RunBucket, "all">, string> = {
  waiting: "bg-warn",
  running: "bg-primary",
  completed: "bg-success",
  stopped: "bg-muted-foreground/50",
  failed: "bg-destructive",
}

function runBucket(r: PeekRun): Exclude<RunBucket, "all"> {
  if (r.status === "cancelled") return "stopped"
  const { tone } = runBarTone(r)
  return tone === "bad" ? "failed" : tone === "warn" ? "waiting" : tone === "info" ? "running" : "completed"
}

/** The runs of the open routine: status rows with counts, then the runs by day. */
function RoutineRunsPanel({ routineName, runs, current, onPeek, onBack }: NonNullable<RoutinesExplorerProps["runsOf"]>) {
  const [bucket, setBucket] = useState<RunBucket>("all")
  const now = new Date()
  const startOfDay = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
  const day = (r: PeekRun) => {
    const t = Date.parse(r.started_at)
    return t >= startOfDay ? "Today" : t >= startOfDay - 6 * 86_400_000 ? "This week" : "Earlier"
  }
  const rows = runs.filter((r) => bucket === "all" || runBucket(r) === bucket)
  const count = (b: RunBucket) => (b === "all" ? runs.length : runs.filter((r) => runBucket(r) === b).length)
  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-col gap-1 border-b border-hairline px-2 py-2">
        <Button type="button" variant="ghost" size="sm" className="w-fit gap-1 coarse:h-[44px]" onClick={onBack}>
          <ChevronLeft className="h-3.5 w-3.5" aria-hidden />
          All routines
        </Button>
        <div className="px-2">
          <p className="text-micro uppercase tracking-wider text-muted-foreground">Routine</p>
          <p className="truncate text-body font-semibold text-foreground">{routineName}</p>
          <p className="text-label text-muted-foreground">
            {runs.length} {runs.length === 1 ? "run" : "runs"} · newest first
          </p>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto pb-1">
        <SidebarSection label="Status">
          <div role="group" aria-label="Run status">
            {RUN_BUCKETS.map((b) => (
              <SidebarRow key={b.id} as="div" selected={bucket === b.id} onSelect={() => setBucket(b.id)}>
                <span className="min-w-0 flex-1 truncate">{b.label}</span>
                <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{count(b.id)}</span>
              </SidebarRow>
            ))}
          </div>
        </SidebarSection>
        {(["Today", "This week", "Earlier"] as const).map((g) => {
          const list = rows.filter((r) => day(r) === g)
          if (!list.length) return null
          return (
            <SidebarSection key={g} label={g} count={list.length}>
              <div role="group" aria-label={g}>
                {list.map((r) => {
                  const b = runBucket(r)
                  const t = new Date(r.started_at)
                  const label = RUN_BUCKETS.find((x) => x.id === b)!.label
                  return (
                    <button
                      key={r.id}
                      type="button"
                      aria-current={r.id === current ? "true" : undefined}
                      aria-label={`Peek at run ${r.id.slice(0, 8)} · ${label}`}
                      onClick={() => onPeek(r.id)}
                      className={cn(
                        "mx-1.5 flex w-[calc(100%-0.75rem)] items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-foreground/[0.04] coarse:min-h-[44px]",
                        r.id === current && "bg-foreground/[0.06]",
                      )}
                    >
                      <i aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", DOT[b])} />
                      <span className="w-12 shrink-0 font-mono text-micro tabular-nums text-muted-foreground">
                        {g === "Today" ? t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }) : t.toLocaleDateString(undefined, { day: "numeric", month: "short" })}
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-label text-foreground/85">
                          {label}
                          {r.outcome === "NO_CHANGE" ? " · nothing changed" : ""}
                        </span>
                        <span className="block truncate text-micro text-muted-foreground-soft">
                          #{r.id.slice(0, 8)} · {r.triggered_via === "manual" ? "started by hand" : r.triggered_via || "manual"}
                        </span>
                      </span>
                      <span className="shrink-0 font-mono text-micro tabular-nums text-muted-foreground">{r.duration_ms > 0 ? formatDurationMs(r.duration_ms) : ""}</span>
                    </button>
                  )
                })}
              </div>
            </SidebarSection>
          )
        })}
        {rows.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground-soft">No runs match.</p>}
      </div>
    </div>
  )
}

function RoutinesList({
  routines,
  search,
  onSearchChange,
  selectedSlug,
  onSelectRoutine,
  filters,
  onChange,
  view,
  onViewChange,
  counts,
  crews,
  index = EMPTY_PANEL_INDEX,
  bulk,
}: Omit<RoutinesExplorerProps, "runsOf">) {
  const [selecting, setSelecting] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(() => new Set())
  const [confirmOff, setConfirmOff] = useState(false)
  const [busy, setBusy] = useState(false)
  const toggle = (slug: string) =>
    setPicked((current) => {
      const next = new Set(current)
      if (next.has(slug)) next.delete(slug)
      else next.add(slug)
      return next
    })
  const leaveSelecting = () => {
    setSelecting(false)
    setPicked(new Set())
  }
  const pickedSlugs = () => [...picked]
  const runBulk = async (action: () => Promise<void> | void) => {
    setBusy(true)
    try {
      await action()
      setPicked(new Set())
    } finally {
      setBusy(false)
    }
  }
  const { bySlug: liveBySlug } = useActiveRoutineRuns()
  const [storedGroup, setGroupBy] = useUserPreference<RoutineGroupBy>("routines.panel.group", "crew")
  const [storedSort, setSortBy] = useUserPreference<RoutineSort>("routines.panel.sort", "recent")
  // A stored value from another version of the panel falls back, never breaks it.
  const groupBy = GROUPS.some((g) => g.id === storedGroup) ? storedGroup : "crew"
  const sortBy = SORTS.some((o) => o.id === storedSort) ? storedSort : "recent"
  const input = (p: Pipeline) => routineFilterInput(p, index.triggers.get(p.slug))

  // Facet counts are taken over the whole list, so picking one value does
  // not zero every other.
  const facetCounts = useMemo(() => {
    const none = { ...filters, status: "all" as const, lifecycle: "all" as const, crewId: null, trigger: null }
    const count = (patch: Partial<RoutineFilterState>) =>
      routines.filter((p) => matchesRoutineFilters(input(p), { ...none, ...patch }, liveBySlug, search)).length
    return {
      lifecycle: Object.fromEntries(LIFECYCLE.map((o) => [o.id, count({ lifecycle: o.id })])),
      lastRun: Object.fromEntries(LAST_RUN.map((o) => [o.id, count({ status: o.id })])),
      trigger: Object.fromEntries(ROUTINE_TRIGGER_KINDS.map((o) => [o.id, count({ trigger: o.id })])),
      crew: Object.fromEntries([...crews.keys()].map((id) => [id, count({ crewId: id })])),
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routines, filters, liveBySlug, search, crews, index])

  const displayed = useMemo(
    () => sortRoutines(routines.filter((p) => matchesRoutineFilters(input(p), filters, liveBySlug, search)), sortBy, index),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [routines, filters, liveBySlug, search, sortBy, index],
  )

  const groups = useMemo(() => groupRoutines(displayed, groupBy, index, crews), [displayed, groupBy, index, crews])

  const active = {
    lifecycle: filters.lifecycle && filters.lifecycle !== "all" ? filters.lifecycle : null,
    status: filters.status !== "all" ? filters.status : null,
    trigger: filters.trigger ?? null,
    crewId: filters.crewId ?? null,
  }
  const triggerLabel = (id: string) => ROUTINE_TRIGGER_KINDS.find((k) => k.id === id)?.label ?? id
  const activeCount =
    (active.lifecycle ? 1 : 0) + (active.status ? 1 : 0) + (active.trigger ? 1 : 0) + (active.crewId ? 1 : 0) + (filters.showTestRoutines ? 1 : 0) + (filters.showEphemeral ? 1 : 0)
  const clear = () => onChange({ ...filters, status: "all", lifecycle: "all", crewId: null, trigger: null, showTestRoutines: false, showEphemeral: false })
  const facets = useSidebarFacets()
  const viewCount = (id: RoutinesView) => (id === "needs" ? counts.needs : id === "live" ? counts.live : id === "drafts" ? counts.drafts : null)

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        {/* data-routines-search keeps the `/` shortcut (routines-layout). */}
        <div data-routines-search className="min-w-0 flex-1">
          <SidebarSearch value={search} onValueChange={onSearchChange} placeholder="Search routines, agents…" />
        </div>
        <SidebarFilterButton activeCount={activeCount} {...facets.triggerProps}>
          Filter
        </SidebarFilterButton>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarViewButton aria-label="View: group and sort">
              <ArrowDownUp className="h-3.5 w-3.5" />
            </SidebarViewButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-48">
            <DropdownMenuLabel>Group by</DropdownMenuLabel>
            <DropdownMenuRadioGroup value={groupBy} onValueChange={(v) => setGroupBy(v as RoutineGroupBy)}>
              {GROUPS.map((g) => (
                <DropdownMenuRadioItem key={g.id} value={g.id}>
                  {g.label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>Sort</DropdownMenuLabel>
            <DropdownMenuRadioGroup value={sortBy} onValueChange={(v) => setSortBy(v as RoutineSort)}>
              {SORTS.map((o) => (
                <DropdownMenuRadioItem key={o.id} value={o.id}>
                  {o.label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        {bulk && (
          <SidebarViewButton
            aria-label="Select several"
            aria-pressed={selecting}
            title="Select several"
            onClick={() => (selecting ? leaveSelecting() : setSelecting(true))}
          >
            <ListChecks className="h-3.5 w-3.5" />
          </SidebarViewButton>
        )}
      </SidebarToolbar>

      {!facets.open && (
      <SidebarActiveChips>
        {active.lifecycle && (
          <SidebarActiveChip label={LIFECYCLE.find((o) => o.id === active.lifecycle)!.label} onRemove={() => onChange({ ...filters, lifecycle: "all" })}>
            {LIFECYCLE.find((o) => o.id === active.lifecycle)!.label}
          </SidebarActiveChip>
        )}
        {active.status && (
          <SidebarActiveChip label={LAST_RUN.find((o) => o.id === active.status)!.label} onRemove={() => onChange({ ...filters, status: "all" })}>
            {LAST_RUN.find((o) => o.id === active.status)!.label}
          </SidebarActiveChip>
        )}
        {active.trigger && (
          <SidebarActiveChip label={triggerLabel(active.trigger)} onRemove={() => onChange({ ...filters, trigger: null })}>
            {triggerLabel(active.trigger)}
          </SidebarActiveChip>
        )}
        {active.crewId && crews.get(active.crewId) && (
          <SidebarActiveChip label={crews.get(active.crewId)!.name} onRemove={() => onChange({ ...filters, crewId: null })}>
            {crews.get(active.crewId)!.name}
          </SidebarActiveChip>
        )}
      </SidebarActiveChips>
      )}

      {/* RT-01 (kit COM-01): the facets open inside the panel, under the
          toolbar — never a floating popover — at most six values each. */}
      <SidebarFacetBlock {...facets.blockProps} label="Filter routines" activeCount={activeCount} onClear={clear}>
        <SidebarInlineFacet
          label="Status"
          defaultOpen
          values={LIFECYCLE.map((o) => ({ value: o.id, label: o.label, count: facetCounts.lifecycle[o.id] }))}
          selected={active.lifecycle}
          onToggle={(v) => onChange({ ...filters, lifecycle: active.lifecycle === v ? "all" : (v as RoutineLifecycleFilter) })}
          onClear={() => onChange({ ...filters, lifecycle: "all" })}
        />
        <SidebarInlineFacet
          label="Last run"
          defaultOpen
          values={LAST_RUN.map((o) => ({ value: o.id, label: o.label, count: facetCounts.lastRun[o.id] }))}
          selected={active.status}
          onToggle={(v) => onChange({ ...filters, status: active.status === v ? "all" : (v as RoutineStatusFilter) })}
          onClear={() => onChange({ ...filters, status: "all" })}
        />
        <SidebarInlineFacet
          label="Trigger"
          defaultOpen
          values={ROUTINE_TRIGGER_KINDS.map((o) => ({ value: o.id, label: o.label, count: facetCounts.trigger[o.id] }))}
          selected={active.trigger}
          onToggle={(v) => onChange({ ...filters, trigger: active.trigger === v ? null : (v as RoutineTriggerKind) })}
          onClear={() => onChange({ ...filters, trigger: null })}
        />
        {crews.size > 0 && (
          <SidebarInlineFacet
            label="Crew"
            defaultOpen
            values={[...crews.values()]
              .sort((a, b) => a.name.localeCompare(b.name))
              .map((c) => ({ value: c.id, label: c.name, count: facetCounts.crew[c.id] }))}
            selected={active.crewId}
            onToggle={(v) => onChange({ ...filters, crewId: active.crewId === v ? null : v })}
            onClear={() => onChange({ ...filters, crewId: null })}
          />
        )}
        <SidebarInlineFacet
          label="Visibility"
          values={[
            { value: "test", label: "Show test recipes" },
            { value: "ephemeral", label: "Show one-off routines" },
          ]}
          selected={[...(filters.showTestRoutines ? ["test"] : []), ...(filters.showEphemeral ? ["ephemeral"] : [])]}
          onToggle={(v) => onChange(v === "test" ? { ...filters, showTestRoutines: !filters.showTestRoutines } : { ...filters, showEphemeral: !filters.showEphemeral })}
          onClear={() => onChange({ ...filters, showTestRoutines: false, showEphemeral: false })}
        />
      </SidebarFacetBlock>

      <div className="min-h-0 flex-1 overflow-y-auto pb-1">
        <SidebarSection label="Views">
          <div role="group" aria-label="Views">
            {ROUTINE_VIEWS.map((v, i) => {
              const Icon = v.icon
              const n = viewCount(v.id)
              return (
                <SidebarRow key={v.id} as="div" selected={view === v.id} onSelect={() => onViewChange(v.id)}>
                  <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="min-w-0 flex-1 truncate" title={`${v.label} · ${i + 1}`}>{v.label}</span>
                  {n != null && n > 0 && <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{n}</span>}
                </SidebarRow>
              )
            })}
          </div>
        </SidebarSection>

        {groups.map((g) => (
          <SidebarSection key={g.id} label={g.label} count={g.rows.length}>
            <div role="group" aria-label={g.label}>
              {g.rows.map((routine) => {
                const state = routineRowState(routine, liveBySlug.get(routine.slug))
                const content = (
                  <span data-routine-row className="flex min-w-0 flex-1 items-center gap-2" title={routine.description || routine.slug}>
                    <RoutineGlyph routine={routine} variant="bare" />
                    <span className="min-w-0 flex-1 truncate text-foreground/85">{routine.name || routine.slug}</span>
                    {routine.draft && (
                      <span role="img" aria-label="Changes not live" title="Changes not live" className="shrink-0 text-muted-foreground">
                        <Pencil className="h-3 w-3" aria-hidden />
                      </span>
                    )}
                    {state && (
                      <StatusPill tone={state.tone} label={state.label} live={state.live} data-testid={state.live ? "routine-live-chip" : undefined} />
                    )}
                  </span>
                )
                if (selecting) {
                  const checked = picked.has(routine.slug)
                  return (
                    <button
                      key={routine.id}
                      type="button"
                      role="checkbox"
                      aria-checked={checked}
                      aria-label={`Select ${routine.name || routine.slug}`}
                      onClick={() => toggle(routine.slug)}
                      className={cn(
                        "mx-1.5 flex w-[calc(100%-0.75rem)] items-center gap-2 rounded-md px-2 py-1.5 text-left text-body transition-colors hover:bg-foreground/[0.04] coarse:min-h-[44px]",
                        checked && "bg-primary/10",
                      )}
                    >
                      <span
                        aria-hidden
                        className={cn(
                          "flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-sm border",
                          checked ? "border-primary bg-primary text-primary-foreground" : "border-control-border",
                        )}
                      >
                        {checked && <Check className="h-3 w-3" />}
                      </span>
                      {content}
                    </button>
                  )
                }
                return (
                  <SidebarRow key={routine.id} as="div" selected={selectedSlug === routine.slug} onSelect={() => onSelectRoutine(routine.slug)}>
                    {content}
                  </SidebarRow>
                )
              })}
            </div>
          </SidebarSection>
        ))}

        {displayed.length === 0 && (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground-soft">
            {routines.length === 0 ? "No routines yet." : "Nothing matches these filters."}
          </p>
        )}
      </div>

      {/* PNL-03: the bulk bar sits at the bottom of the panel while selecting. */}
      {selecting && bulk && (
        <div role="toolbar" aria-label="Selected routines" className="flex shrink-0 items-center gap-1 border-t border-hairline bg-card px-2 py-1.5">
          <span className="min-w-0 flex-1 truncate px-1 text-label text-muted-foreground">{picked.size ? `${picked.size} selected` : "Pick routines"}</span>
          {bulk.canTurnOff && (
            <Button type="button" variant="ghost" size="sm" className="gap-1 coarse:h-[44px]" disabled={!picked.size || busy} onClick={() => setConfirmOff(true)}>
              <Pause className="h-3 w-3" aria-hidden />
              Turn off
            </Button>
          )}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="gap-1 coarse:h-[44px]"
            disabled={!picked.size || busy}
            title="Starts the selected routines that need no answer; a routine with a required input is skipped"
            onClick={() => void runBulk(() => bulk.onRunNow(pickedSlugs()))}
          >
            <Play className="h-3 w-3" aria-hidden />
            Run now
          </Button>
          <Button type="button" variant="ghost" size="icon-sm" aria-label="Done selecting" title="Done selecting" onClick={leaveSelecting}>
            <X className="h-3.5 w-3.5" aria-hidden />
          </Button>
        </div>
      )}
      <ConfirmDialog
        open={confirmOff}
        onOpenChange={setConfirmOff}
        title={`Turn off ${picked.size} ${picked.size === 1 ? "routine" : "routines"}?`}
        description="They cannot run until each is turned on again."
        consequences={[
          { tone: "lost", text: "Schedules, webhooks and the Run button stop starting them." },
          { tone: "kept", text: "Their runs, versions and settings stay." },
        ]}
        confirmLabel="Turn off"
        destructive
        onConfirm={async () => {
          if (bulk) await runBulk(() => bulk.onTurnOff(pickedSlugs()))
        }}
      />
    </div>
  )
}
