"use client"

import { useMemo } from "react"
import { Bot, Copy, ExternalLink, Layers, MoreHorizontal, Plus, Star, User, UserRound } from "lucide-react"

import { cn } from "@/lib/utils"
import { toast } from "@/lib/toast"
import { getIssueWorker } from "@/lib/issue-execution"
import { getCrewIconDef, iconColorProps } from "@/lib/entities"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarFilterButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { PRIORITY_TEXT, QUICK_VIEWS, STATUS_TEXT, normalStatus, type QuickView } from "@/components/features/issues/issues-view-model"
import { activeFilterCount, type IssueFilters } from "@/hooks/use-issue-filters"
import type { IssuePriority, Mission, MissionStatus, Project, SavedView } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

/** What the panel has selected: a quick view, a saved view or a project. */
export type IssuesPanelSelection =
  | { kind: "quick"; id: QuickView }
  | { kind: "saved"; id: string }
  | { kind: "project"; id: string }

const STATUS_FACET: MissionStatus[] = ["IN_PROGRESS", "REVIEW", "TODO", "BACKLOG", "DONE", "FAILED", "CANCELLED"]
const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]
const QUICK_ICON = { mine: User, all: Layers } as const

const HEALTH_DOT: Record<string, string> = {
  on_track: "bg-success",
  at_risk: "bg-warn",
  off_track: "bg-destructive",
}

/** Every row of the panel's Views, Projects and Saved views, in key order (1–9). */
export function panelKeyOrder(projects: Project[], savedViews: SavedView[]): IssuesPanelSelection[] {
  return [
    ...QUICK_VIEWS.map((v) => ({ kind: "quick", id: v.id }) as IssuesPanelSelection),
    ...savedViews.map((v) => ({ kind: "saved", id: v.id }) as IssuesPanelSelection),
    ...projects.map((p) => ({ kind: "project", id: p.id }) as IssuesPanelSelection),
  ].slice(0, 9)
}

function count<T>(items: Mission[], key: (i: Mission) => T | null | undefined): Map<T, number> {
  const m = new Map<T, number>()
  for (const i of items) {
    const k = key(i)
    if (k != null) m.set(k, (m.get(k) ?? 0) + 1)
  }
  return m
}

export interface IssuesPanelProps {
  /** The loaded issues (before the facets), for the options and their counts. */
  issues: Mission[]
  quickCounts: Record<QuickView, number>
  projects: Project[]
  savedViews: SavedView[]
  crews: CrewSummary[]
  search: string
  onSearchChange: (v: string) => void
  filters: IssueFilters
  onFilters: (patch: Partial<IssueFilters>) => void
  onClearFilters: () => void
  /** Null on the issue page, which shows an issue rather than a view. */
  selection: IssuesPanelSelection | null
  onSelect: (s: IssuesPanelSelection) => void
  onNewProject: () => void
  /** Save the current filters as a view. Hidden when absent. */
  onSaveView?: () => void
}

/**
 * The Issues secondary panel: search and Filter in its toolbar, then Views,
 * Projects and Saved views. Facets AND together; each one toggles its own
 * value and leaves the others alone. Active facets show as removable chips.
 */
export function IssuesPanel({
  issues,
  quickCounts,
  projects,
  savedViews,
  crews,
  search,
  onSearchChange,
  filters,
  onFilters,
  onClearFilters,
  selection,
  onSelect,
  onNewProject,
  onSaveView,
}: IssuesPanelProps) {
  const facets = useSidebarFacets()

  const facets_ = useMemo(() => {
    const delegates = new Map<string, { name: string; n: number }>()
    const people = new Map<string, { name: string; n: number }>()
    const owners = new Map<string, { name: string; n: number }>()
    const labels = new Map<string, { color: string; n: number }>()
    let noDelegate = 0
    for (const i of issues) {
      const w = getIssueWorker(i)
      if (w.id) {
        const into = w.isHuman ? people : delegates
        const cur = into.get(w.id)
        into.set(w.id, { name: w.name || cur?.name || (w.isHuman ? "A person" : "Agent"), n: (cur?.n ?? 0) + 1 })
      } else noDelegate++
      if (i.owner?.id) {
        const cur = owners.get(i.owner.id)
        owners.set(i.owner.id, { name: i.owner.name || cur?.name || "A person", n: (cur?.n ?? 0) + 1 })
      }
      for (const l of i.labels ?? []) {
        const cur = labels.get(l.name)
        labels.set(l.name, { color: l.color, n: (cur?.n ?? 0) + 1 })
      }
    }
    const byName = <V extends { name: string }>(m: Map<string, V>) => [...m.entries()].sort((a, b) => a[1].name.localeCompare(b[1].name))
    const holders = { agent: [...delegates.values()].reduce((a, d) => a + d.n, 0), person: [...people.values()].reduce((a, d) => a + d.n, 0) }
    return {
      status: count(issues, (i) => normalStatus(i.status)),
      priority: count(issues, (i) => i.priority || "none"),
      crew: count(issues, (i) => i.crew_id),
      delegates: byName(delegates),
      noDelegate,
      owners: byName(owners),
      labels: [...labels.entries()].sort((a, b) => a[0].localeCompare(b[0])),
      holders,
    }
  }, [issues])

  const nameOf = {
    crew: (id: string) => crews.find((c) => c.id === id)?.name ?? "Crew",
    worker: (id: string) => facets_.delegates.find(([k]) => k === id)?.[1].name ?? issues.map(getIssueWorker).find((w) => w.id === id)?.name ?? "Delegate",
    owner: (id: string) => facets_.owners.find(([k]) => k === id)?.[1].name ?? "Owner",
  }

  const n = activeFilterCount(filters)
  const toggleStatus = (s: MissionStatus) =>
    onFilters({ statuses: filters.statuses.includes(s) ? filters.statuses.filter((x) => x !== s) : [...filters.statuses, s] })
  const meta = (k: number | undefined) => <span className="text-label tabular-nums text-muted-foreground-soft">{k ?? 0}</span>

  const isOn = (s: IssuesPanelSelection) => selection !== null && selection.kind === s.kind && selection.id === s.id

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SidebarToolbar>
        {/* data-issues-search keeps the `/` shortcut's target. */}
        <div data-issues-search className="min-w-0 flex-1">
          <SidebarSearch value={search} onValueChange={onSearchChange} placeholder="Search issues, agents…" />
        </div>
        <SidebarFilterButton activeCount={n} aria-label="Filter issues" {...facets.triggerProps}>
          Filter
        </SidebarFilterButton>
      </SidebarToolbar>

      {/* COM-01: the facets open INSIDE the panel, under the toolbar (v67
          panel() → pk-fx), so two facets combine in one visit. */}
      <SidebarFacetBlock {...facets.blockProps} label="Filter issues" activeCount={n} onClear={onClearFilters}>
        <SidebarInlineFacet
          label="Status"
          values={STATUS_FACET.map((st) => ({ value: st, label: STATUS_TEXT[st], text: STATUS_TEXT[st], count: facets_.status.get(st) ?? 0, icon: <StatusIcon status={st} className="size-3.5 shrink-0" /> }))}
          selected={filters.statuses}
          onToggle={(v) => toggleStatus(v as MissionStatus)}
          onClear={() => onFilters({ statuses: [] })}
        />
        <SidebarInlineFacet
          label="Priority"
          values={PRIORITIES.map((pr) => ({ value: pr, label: PRIORITY_TEXT[pr], text: PRIORITY_TEXT[pr], count: facets_.priority.get(pr) ?? 0, icon: <PriorityIcon priority={pr} className="size-3.5 shrink-0" /> }))}
          selected={filters.priority}
          onToggle={(v) => onFilters({ priority: filters.priority === v ? null : (v as IssuePriority) })}
          onClear={() => onFilters({ priority: null })}
        />
        {facets_.delegates.length > 0 && (
          <SidebarInlineFacet
            label="Delegate"
            values={facets_.delegates.map(([id, d]) => ({ value: id, label: d.name, text: d.name, count: d.n, icon: <AgentAvatar seed={id} className="size-4 shrink-0 rounded-full" /> }))}
            selected={filters.agentId}
            onToggle={(v) => onFilters({ agentId: filters.agentId === v ? null : v })}
            onClear={() => onFilters({ agentId: null })}
          />
        )}
        {facets_.owners.length > 0 && (
          <SidebarInlineFacet
            label="Owner"
            values={facets_.owners.map(([id, o]) => ({ value: id, label: o.name, text: o.name, count: o.n, icon: <UserRound className="size-3.5 shrink-0" /> }))}
            selected={filters.ownerId}
            onToggle={(v) => onFilters({ ownerId: filters.ownerId === v ? null : v })}
            onClear={() => onFilters({ ownerId: null })}
          />
        )}
        <SidebarInlineFacet
          label="Who holds the work"
          values={[
            { value: "agent", label: "An agent", text: "An agent", count: facets_.holders.agent, icon: <Bot className="size-3.5 shrink-0" /> },
            { value: "person", label: "A person", text: "A person", count: facets_.holders.person, icon: <UserRound className="size-3.5 shrink-0" /> },
          ]}
          selected={filters.holder}
          onToggle={(v) => onFilters({ holder: filters.holder === v ? null : (v as "agent" | "person") })}
          onClear={() => onFilters({ holder: null })}
        />
        {facets_.labels.length > 0 && (
          <SidebarInlineFacet
            label="Labels"
            values={facets_.labels.map(([name, l]) => ({
              value: name,
              label: name,
              text: name,
              count: l.n,
              icon: <span aria-hidden className="size-2 shrink-0 rounded-full" style={{ background: l.color || "var(--muted-foreground)" }} />,
            }))}
            selected={filters.label}
            onToggle={(v) => onFilters({ label: filters.label === v ? null : v })}
            onClear={() => onFilters({ label: null })}
          />
        )}
        {crews.length > 0 && (
          <SidebarInlineFacet
            label="Crew"
            values={crews.map((c) => {
              const Icon = getCrewIconDef(c.icon || "users").icon
              const color = iconColorProps(c.color)
              return { value: c.id, label: c.name, text: c.name, count: facets_.crew.get(c.id) ?? 0, icon: <Icon className={cn("size-3.5 shrink-0", color.className)} style={color.style} /> }
            })}
            selected={filters.crewId}
            onToggle={(v) => onFilters({ crewId: filters.crewId === v ? null : v })}
            onClear={() => onFilters({ crewId: null })}
          />
        )}
      </SidebarFacetBlock>

      <div className="min-h-0 flex-1 overflow-y-auto pb-2">
        {!facets.open && <SidebarActiveChips className="pt-1">
          {filters.statuses.map((s) => (
            <SidebarActiveChip key={s} onRemove={() => toggleStatus(s)}>Status: {STATUS_TEXT[s] ?? s}</SidebarActiveChip>
          ))}
          {filters.priority && <SidebarActiveChip onRemove={() => onFilters({ priority: null })}>Priority: {PRIORITY_TEXT[filters.priority]}</SidebarActiveChip>}
          {filters.agentId && <SidebarActiveChip onRemove={() => onFilters({ agentId: null })}>Delegate: {nameOf.worker(filters.agentId)}</SidebarActiveChip>}
          {filters.ownerId && <SidebarActiveChip onRemove={() => onFilters({ ownerId: null })}>Owner: {nameOf.owner(filters.ownerId)}</SidebarActiveChip>}
          {filters.holder && <SidebarActiveChip onRemove={() => onFilters({ holder: null })}>Held by {filters.holder === "agent" ? "an agent" : "a person"}</SidebarActiveChip>}
          {filters.label && <SidebarActiveChip onRemove={() => onFilters({ label: null })}>Label: {filters.label}</SidebarActiveChip>}
          {filters.crewId && <SidebarActiveChip onRemove={() => onFilters({ crewId: null })}>Crew: {nameOf.crew(filters.crewId)}</SidebarActiveChip>}
        </SidebarActiveChips>}

        <SidebarSection label="Views" />
        {QUICK_VIEWS.map((v) => {
          const s: IssuesPanelSelection = { kind: "quick", id: v.id }
          const Icon = QUICK_ICON[v.id]
          return (
            <SidebarRow key={v.id} selected={isOn(s)} onSelect={() => onSelect(s)} as="div">
              <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">{v.label}</span>
              {meta(quickCounts[v.id])}
            </SidebarRow>
          )
        })}

        <SidebarSection
          label="Projects"
          count={projects.length || undefined}
          actions={
            <Button variant="ghost" size="icon-xs" aria-label="New project" onClick={onNewProject}>
              <Plus className="size-3.5" />
            </Button>
          }
        />
        {projects.length === 0 && (
          <p className="px-4 py-1 text-label text-muted-foreground-soft">Group issues under a goal with milestones.</p>
        )}
        {projects.map((p) => {
          const s: IssuesPanelSelection = { kind: "project", id: p.id }
          const Icon = getCrewIconDef(p.icon || "folder").icon
          const color = iconColorProps(p.color)
          const progress = Math.max(0, Math.min(100, Math.round(p.progress || 0)))
          return (
            <div key={p.id} className="group/proj">
              <div className="flex items-center">
                <SidebarRow selected={isOn(s)} onSelect={() => onSelect(s)} as="div" className="min-w-0 flex-1">
                  <span className="grid size-[18px] shrink-0 place-items-center rounded-md bg-foreground/[0.05]">
                    <Icon className={cn("size-3", color.className)} style={color.style} />
                  </span>
                  <span className="min-w-0 flex-1 truncate">{p.name}</span>
                  <span
                    aria-label={`Health: ${p.health.replace("_", " ")}`}
                    title={p.health.replace("_", " ")}
                    className={cn("size-2 shrink-0 rounded-full", HEALTH_DOT[p.health] ?? "bg-muted-foreground-soft")}
                  />
                  {meta(p.issue_count)}
                </SidebarRow>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon-xs" className="mr-1.5 opacity-0 focus-visible:opacity-100 group-hover/proj:opacity-100 coarse:opacity-100 data-[state=open]:opacity-100" aria-label={`${p.name} options`}>
                      <MoreHorizontal className="size-3.5" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className="w-44">
                    <DropdownMenuItem onSelect={() => onSelect(s)}>
                      <ExternalLink className="size-3.5" /> Open project
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      onSelect={() => {
                        const url = `${window.location.origin}/issues?project=${encodeURIComponent(p.id)}`
                        navigator.clipboard?.writeText(url).then(() => toast.success("Link copied"), () => toast.error("Could not copy the link"))
                      }}
                    >
                      <Copy className="size-3.5" /> Copy link
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              <div className="mb-1.5 ml-[2.6rem] mr-3 h-[3px] overflow-hidden rounded-sm bg-foreground/[0.08]" aria-hidden>
                <div className="h-full bg-primary" style={{ width: `${progress}%` }} />
              </div>
            </div>
          )
        })}

        <SidebarSection
          label="Saved views"
          count={savedViews.length || undefined}
          actions={
            onSaveView ? (
              <Button variant="ghost" size="icon-xs" aria-label="Save current view" onClick={onSaveView}>
                <Plus className="size-3.5" />
              </Button>
            ) : undefined
          }
        />
        {savedViews.length === 0 && (
          <p className="px-4 py-1 text-label text-muted-foreground-soft">Filter the list, then save it here.</p>
        )}
        {savedViews.map((v) => {
          const s: IssuesPanelSelection = { kind: "saved", id: v.id }
          return (
            <SidebarRow key={v.id} selected={isOn(s)} onSelect={() => onSelect(s)} as="div">
              <Star className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">{v.name}</span>
              {v.shared && <span className="text-label text-muted-foreground-soft">shared</span>}
            </SidebarRow>
          )
        })}
      </div>
    </div>
  )
}

/** The SubBar section name for the current selection. */
export function selectionLabel(s: IssuesPanelSelection, projects: Project[], savedViews: SavedView[]): string {
  if (s.kind === "quick") return QUICK_VIEWS.find((v) => v.id === s.id)?.label ?? "All issues"
  if (s.kind === "saved") return savedViews.find((v) => v.id === s.id)?.name ?? "Saved view"
  return projects.find((p) => p.id === s.id)?.name ?? "Project"
}

