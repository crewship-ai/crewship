"use client"

import { useMemo } from "react"
import { ArrowDownUp, Hand, LayoutDashboard, List, Pencil, Play, PenLine } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
  SidebarViewButton,
} from "@/components/layout/sidebar-kit"
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
  type RoutinePanelIndex,
  type RoutineSort,
} from "@/lib/routine-panel-index"
import { RoutineGlyph } from "./routine-glyph"

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
}: RoutinesExplorerProps) {
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
  const viewCount = (id: RoutinesView) => (id === "needs" ? counts.needs : id === "live" ? counts.live : id === "drafts" ? counts.drafts : null)

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        {/* data-routines-search keeps the `/` shortcut (routines-layout). */}
        <div data-routines-search className="min-w-0 flex-1">
          <SidebarSearch value={search} onValueChange={onSearchChange} placeholder="Search routines, agents…" />
        </div>
        <SidebarFilterPopover label="Filter routines" activeCount={activeCount} onClear={clear}>
          <SidebarFacet first label="Status" resetLabel="Any status" resetActive={!active.lifecycle} onReset={() => onChange({ ...filters, lifecycle: "all" })}>
            {LIFECYCLE.map((o) => (
              <SidebarFacetOption key={o.id} active={active.lifecycle === o.id} onToggle={() => onChange({ ...filters, lifecycle: active.lifecycle === o.id ? "all" : o.id })}>
                <span className="flex-1">{o.label}</span>
                <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.lifecycle[o.id]}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Last run" resetLabel="Any result" resetActive={!active.status} onReset={() => onChange({ ...filters, status: "all" })}>
            {LAST_RUN.map((o) => (
              <SidebarFacetOption key={o.id} active={active.status === o.id} onToggle={() => onChange({ ...filters, status: active.status === o.id ? "all" : o.id })}>
                <span className="flex-1">{o.label}</span>
                <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.lastRun[o.id]}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Trigger" resetLabel="Any trigger" resetActive={!active.trigger} onReset={() => onChange({ ...filters, trigger: null })}>
            {ROUTINE_TRIGGER_KINDS.map((o) => (
              <SidebarFacetOption key={o.id} active={active.trigger === o.id} onToggle={() => onChange({ ...filters, trigger: active.trigger === o.id ? null : o.id })}>
                <span className="flex-1">{o.label}</span>
                <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.trigger[o.id]}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          {crews.size > 0 && (
            <SidebarFacet label="Crew" resetLabel="Every crew" resetActive={!active.crewId} onReset={() => onChange({ ...filters, crewId: null })}>
              {[...crews.values()]
                .sort((a, b) => a.name.localeCompare(b.name))
                .map((c) => (
                  <SidebarFacetOption key={c.id} active={active.crewId === c.id} onToggle={() => onChange({ ...filters, crewId: active.crewId === c.id ? null : c.id })}>
                    <span className="flex-1 truncate">{c.name}</span>
                    <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.crew[c.id]}</span>
                  </SidebarFacetOption>
                ))}
            </SidebarFacet>
          )}
          <SidebarFacet label="Visibility" resetLabel="Workspace routines only" resetActive={!filters.showTestRoutines && !filters.showEphemeral} onReset={() => onChange({ ...filters, showTestRoutines: false, showEphemeral: false })}>
            <SidebarFacetOption active={!!filters.showTestRoutines} onToggle={() => onChange({ ...filters, showTestRoutines: !filters.showTestRoutines })}>
              Show test recipes
            </SidebarFacetOption>
            <SidebarFacetOption active={filters.showEphemeral} onToggle={() => onChange({ ...filters, showEphemeral: !filters.showEphemeral })}>
              Show one-off routines
            </SidebarFacetOption>
          </SidebarFacet>
        </SidebarFilterPopover>
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
      </SidebarToolbar>

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
                return (
                  <SidebarRow key={routine.id} as="div" selected={selectedSlug === routine.slug} onSelect={() => onSelectRoutine(routine.slug)}>
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
    </div>
  )
}
