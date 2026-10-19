"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { Activity, AlertTriangle, Hand, List, ListChecks, Play, ScrollText, Webhook } from "lucide-react"

import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarFilterButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import {
  ACTIVITY_VIEWS,
  CHANGE_KIND_LABEL,
  WORK_SOURCE_LABEL,
  WORK_STATUS_LABEL,
  activeFacetCount,
  viewOfStatus,
  type ActivityView,
  type ChangeKind,
  type FeedFilters,
  type WhatFilter,
  type WorkSource,
  type WorkStatus,
} from "@/lib/activity-feed"

const VIEW_ICON: Record<ActivityView, LucideIcon> = { all: List, need: Hand, live: Play, failed: AlertTriangle }

/** The ledgers under Views. `log` and `runs` are the Journal's, OWNER / ADMIN only. */
export type LedgerSection = "work" | "deliveries" | "log" | "runs"

export interface PanelOption {
  id: string
  label: string
  count: number
}

/**
 * The Activity panel (SecondaryPanel content): search, Filter and the views.
 * Counts are for the selected time range.
 */
export function ActivityPanel({
  filters,
  onFilters,
  viewCounts,
  routines,
  agents,
  crews,
  skills = [],
  sources,
  onOpenLedger,
  ledgerCounts,
  journalLedgers = false,
}: {
  filters: FeedFilters
  onFilters: (f: FeedFilters) => void
  viewCounts: Record<ActivityView, number>
  routines: PanelOption[]
  agents: PanelOption[]
  crews: PanelOption[]
  /** Skills the listed work used; absent on a server without them. */
  skills?: PanelOption[]
  sources: { id: WorkSource; count: number }[]
  /** The work queue and webhook deliveries keep their place under Views. */
  onOpenLedger?: (section: LedgerSection) => void
  ledgerCounts?: { work?: number; deliveries?: number }
  /**
   * Show the technical log and fleet runs — the Journal's Timeline and Runs
   * (F-02). The caller passes true only for OWNER / ADMIN, the Journal's gate.
   */
  journalLedgers?: boolean
}) {
  const closeSheet = useCloseSecondaryPanelSheet()
  const view = viewOfStatus(filters.status) ?? (filters.status ? null : "all")
  const set = (patch: Partial<FeedFilters>) => onFilters({ ...filters, ...patch })
  const toggle = <K extends keyof FeedFilters>(k: K, v: FeedFilters[K]) => set({ [k]: filters[k] === v ? null : v } as Partial<FeedFilters>)
  const facets = activeFacetCount(filters)
  const facetBlock = useSidebarFacets()
  // The views (Needs a person, Running, Could not finish) are places, not facets: Clear keeps them.
  const clearAll = () =>
    onFilters({ ...filters, what: null, routine: null, agent: null, crew: null, source: null, issue: null, skill: null, status: viewOfStatus(filters.status) ? filters.status : null })
  const nameOf = (xs: PanelOption[], id: string) => xs.find((x) => x.id === id)?.label ?? id
  type Chip = { key: keyof FeedFilters; label: string }
  const chips = ([
    filters.what ? { key: "what" as const, label: `What: ${filters.what === "work" ? "Runs and agent work" : filters.what === "changes" ? "Every change" : CHANGE_KIND_LABEL[filters.what]}` } : null,
    filters.status && !viewOfStatus(filters.status) ? { key: "status" as const, label: `Status: ${WORK_STATUS_LABEL[filters.status]}` } : null,
    filters.routine ? { key: "routine" as const, label: `Routine: ${nameOf(routines, filters.routine)}` } : null,
    filters.agent ? { key: "agent" as const, label: `Who: ${nameOf(agents, filters.agent)}` } : null,
    filters.crew ? { key: "crew" as const, label: `Crew: ${nameOf(crews, filters.crew)}` } : null,
    filters.skill ? { key: "skill" as const, label: `Skill: ${nameOf(skills, filters.skill)}` } : null,
    filters.source ? { key: "source" as const, label: `Work started by: ${WORK_SOURCE_LABEL[filters.source]}` } : null,
  ] as (Chip | null)[]).filter((c): c is Chip => c != null)

  const whats: { id: WhatFilter; label: string }[] = [
    { id: "work", label: "Runs and agent work" },
    { id: "changes", label: "Every change" },
    ...(Object.keys(CHANGE_KIND_LABEL) as ChangeKind[]).map((k) => ({ id: k as WhatFilter, label: CHANGE_KIND_LABEL[k] })),
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={filters.q}
          onValueChange={(q) => set({ q })}
          placeholder="Search runs, issues, people…"
          aria-label="Search activity"
        />
        <SidebarFilterButton activeCount={facets} {...facetBlock.triggerProps}>
          Filter
        </SidebarFilterButton>
      </SidebarToolbar>
      {!facetBlock.open && (
        <SidebarActiveChips>
          {chips.map((c) => (
            <SidebarActiveChip key={c.key} label={c.label} onRemove={() => set({ [c.key]: null } as Partial<FeedFilters>)}>
              {c.label}
            </SidebarActiveChip>
          ))}
        </SidebarActiveChips>
      )}
      {/* The facets open inside the panel, under the toolbar (PNL-02, v67 panel() → pk-fx). */}
      <SidebarFacetBlock {...facetBlock.blockProps} label="Filter activity" activeCount={facets} onClear={clearAll}>
        <SidebarInlineFacet
          label="What"
          values={whats.map((w) => ({ value: w.id, label: w.label }))}
          selected={filters.what}
          onToggle={(v) => toggle("what", v as WhatFilter)}
          onClear={() => set({ what: null })}
        />
        <SidebarInlineFacet
          label="Status"
          values={(Object.keys(WORK_STATUS_LABEL) as WorkStatus[]).map((st) => ({ value: st, label: WORK_STATUS_LABEL[st] }))}
          selected={filters.status}
          onToggle={(v) => toggle("status", v as WorkStatus)}
          onClear={() => set({ status: null })}
        />
        {routines.length > 0 && (
          <SidebarInlineFacet
            label="Routine"
            values={routines.map((r) => ({ value: r.id, label: r.label, count: r.count }))}
            selected={filters.routine}
            onToggle={(v) => toggle("routine", v)}
            onClear={() => set({ routine: null })}
          />
        )}
        {agents.length > 0 && (
          <SidebarInlineFacet
            label="Who"
            values={agents.map((a) => ({ value: a.id, label: a.label, count: a.count }))}
            selected={filters.agent}
            onToggle={(v) => toggle("agent", v)}
            onClear={() => set({ agent: null })}
          />
        )}
        {crews.length > 0 && (
          <SidebarInlineFacet
            label="Crew"
            values={crews.map((c) => ({ value: c.id, label: c.label, count: c.count }))}
            selected={filters.crew}
            onToggle={(v) => toggle("crew", v)}
            onClear={() => set({ crew: null })}
          />
        )}
        {skills.length > 0 && (
          <SidebarInlineFacet
            label="Skill"
            values={skills.map((s) => ({ value: s.id, label: s.label, count: s.count }))}
            selected={filters.skill}
            onToggle={(v) => toggle("skill", v)}
            onClear={() => set({ skill: null })}
          />
        )}
        {sources.length > 0 && (
          <SidebarInlineFacet
            label="Work started by"
            values={sources.map((src) => ({ value: src.id, label: WORK_SOURCE_LABEL[src.id], count: src.count }))}
            selected={filters.source}
            onToggle={(v) => toggle("source", v as WorkSource)}
            onClear={() => set({ source: null })}
          />
        )}
      </SidebarFacetBlock>

      <div className="min-h-0 flex-1 overflow-y-auto pb-3">
        <SidebarSection label="Views">
          <ul aria-label="Views">
            {ACTIVITY_VIEWS.map((v) => {
              const Icon = VIEW_ICON[v.key]
              return (
                <SidebarRow
                  key={v.key}
                  selected={view === v.key}
                  onSelect={() => {
                    set({ status: v.status })
                    closeSheet()
                  }}
                >
                  <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="min-w-0 flex-1 truncate">{v.label}</span>
                  <span className="text-micro tabular-nums text-muted-foreground">{viewCounts[v.key]}</span>
                </SidebarRow>
              )
            })}
          </ul>
        </SidebarSection>
        {onOpenLedger && (
          <SidebarSection label="Ledgers">
            <ul aria-label="Ledgers">
            <SidebarRow onSelect={() => onOpenLedger("work")}>
              <ListChecks className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">Work queue</span>
              {ledgerCounts?.work != null && <span className="font-mono text-micro text-muted-foreground">{ledgerCounts.work}</span>}
            </SidebarRow>
            <SidebarRow onSelect={() => onOpenLedger("deliveries")}>
              <Webhook className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">Webhook deliveries</span>
              {ledgerCounts?.deliveries != null && <span className="font-mono text-micro text-muted-foreground">{ledgerCounts.deliveries}</span>}
            </SidebarRow>
            {journalLedgers && (
              <>
                <SidebarRow onSelect={() => onOpenLedger("log")}>
                  <ScrollText className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="min-w-0 flex-1 truncate">Technical log</span>
                  <span className="text-micro text-muted-foreground">Admins</span>
                </SidebarRow>
                <SidebarRow onSelect={() => onOpenLedger("runs")}>
                  <Activity className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="min-w-0 flex-1 truncate">Fleet runs</span>
                  <span className="text-micro text-muted-foreground">Admins</span>
                </SidebarRow>
              </>
            )}
            </ul>
          </SidebarSection>
        )}
      </div>
    </div>
  )
}
