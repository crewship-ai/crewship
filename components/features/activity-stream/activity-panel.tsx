"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { AlertTriangle, Hand, List, ListChecks, Play, Webhook } from "lucide-react"

import {
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
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
  sources,
  onOpenLedger,
  ledgerCounts,
}: {
  filters: FeedFilters
  onFilters: (f: FeedFilters) => void
  viewCounts: Record<ActivityView, number>
  routines: PanelOption[]
  agents: PanelOption[]
  crews: PanelOption[]
  sources: { id: WorkSource; count: number }[]
  /** The work queue and webhook deliveries keep their place under Views. */
  onOpenLedger?: (section: "work" | "deliveries") => void
  ledgerCounts?: { work?: number; deliveries?: number }
}) {
  const closeSheet = useCloseSecondaryPanelSheet()
  const view = viewOfStatus(filters.status) ?? (filters.status ? null : "all")
  const set = (patch: Partial<FeedFilters>) => onFilters({ ...filters, ...patch })
  const toggle = <K extends keyof FeedFilters>(k: K, v: FeedFilters[K]) => set({ [k]: filters[k] === v ? null : v } as Partial<FeedFilters>)
  const facets = activeFacetCount(filters)

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
        <SidebarFilterPopover
          label="Filter activity"
          activeCount={facets}
          onClear={() => onFilters({ ...filters, what: null, routine: null, agent: null, crew: null, source: null, issue: null, status: viewOfStatus(filters.status) ? filters.status : null })}
          panelClassName="w-[260px] max-h-[min(520px,70dvh)]"
        >
          <SidebarFacet first label="What" resetLabel="Everything" resetActive={!filters.what} onReset={() => set({ what: null })}>
            {whats.map((w) => (
              <SidebarFacetOption key={w.id} active={filters.what === w.id} onToggle={() => toggle("what", w.id)}>
                {w.label}
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Status" resetLabel="Any status" resetActive={!filters.status} onReset={() => set({ status: null })}>
            {(Object.keys(WORK_STATUS_LABEL) as WorkStatus[]).map((s) => (
              <SidebarFacetOption key={s} active={filters.status === s} onToggle={() => toggle("status", s)}>
                {WORK_STATUS_LABEL[s]}
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          {routines.length > 0 && (
            <SidebarFacet label="Routine" resetLabel="Every routine" resetActive={!filters.routine} onReset={() => set({ routine: null })}>
              {routines.map((r) => (
                <SidebarFacetOption key={r.id} active={filters.routine === r.id} onToggle={() => toggle("routine", r.id)}>
                  <span className="min-w-0 flex-1 truncate">{r.label}</span>
                  <span className="font-mono text-micro text-muted-foreground">{r.count}</span>
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}
          {agents.length > 0 && (
            <SidebarFacet label="Who" resetLabel="Anyone" resetActive={!filters.agent} onReset={() => set({ agent: null })}>
              {agents.map((a) => (
                <SidebarFacetOption key={a.id} active={filters.agent === a.id} onToggle={() => toggle("agent", a.id)}>
                  <span className="min-w-0 flex-1 truncate">{a.label}</span>
                  <span className="font-mono text-micro text-muted-foreground">{a.count}</span>
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}
          {crews.length > 0 && (
            <SidebarFacet label="Crew" resetLabel="Every crew" resetActive={!filters.crew} onReset={() => set({ crew: null })}>
              {crews.map((c) => (
                <SidebarFacetOption key={c.id} active={filters.crew === c.id} onToggle={() => toggle("crew", c.id)}>
                  <span className="min-w-0 flex-1 truncate">{c.label}</span>
                  <span className="font-mono text-micro text-muted-foreground">{c.count}</span>
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}
          {sources.length > 0 && (
            <SidebarFacet label="Work started by" resetLabel="Anything" resetActive={!filters.source} onReset={() => set({ source: null })}>
              {sources.map((s) => (
                <SidebarFacetOption key={s.id} active={filters.source === s.id} onToggle={() => toggle("source", s.id)}>
                  <span className="min-w-0 flex-1 truncate">{WORK_SOURCE_LABEL[s.id]}</span>
                  <span className="font-mono text-micro text-muted-foreground">{s.count}</span>
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}
        </SidebarFilterPopover>
      </SidebarToolbar>

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
                  <span className="font-mono text-micro tabular-nums text-muted-foreground">{viewCounts[v.key]}</span>
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
            </ul>
          </SidebarSection>
        )}
      </div>
    </div>
  )
}
