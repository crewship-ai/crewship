"use client"

/**
 * The /pages filter rail — zone 2 of PRD §9b.1.
 *
 *   [icon rail]  [filter rail 280px]              [main]
 *   AppSidebar   SidebarToolbar / Search /        Overview · or a single page
 *                FilterButton / Collapse
 *                ── STATUS ─────── All · Fresh · Stale · Failed · Never produced
 *                ── OWNER  ─────── per crew
 *                ── PAGES  ─────── the list, per-item icon + right-side badge
 *
 * Every control here comes from `components/layout/sidebar-kit.tsx`. That is a
 * hard requirement, not a preference: #1776 is open on five surfaces that each
 * hand-rolled this popover and drifted — Credentials' `set({category});
 * setFilterOpen(false)` makes combining two facets impossible — and #1777
 * lifted the panel into the kit with Issues as the parity proof. Pages is the
 * SECOND surface on the shared panel, never the sixth hand-rolled one.
 *
 * The two behaviours that buys, and which nothing in this file re-implements:
 * a pick never closes the panel, and a pick never touches a sibling facet.
 * Both facets are multi-select.
 */

import * as React from "react"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"
import {
  matchesPageFilters,
  ownerFacets,
  stateFacetCounts,
  togglePageFilter,
  type PageFilters,
  type PageView,
} from "@/hooks/use-pages"
import type { PanelState } from "@/components/features/pages/panels/types"
import { PAGE_STATE_META, PAGE_STATE_ORDER } from "@/components/features/pages/page-state"

export interface PagesRailProps {
  pages: PageView[]
  search: string
  onSearchChange: (value: string) => void
  filters: PageFilters
  onFiltersChange: (next: PageFilters) => void
  selectedSlug: string | null
  onSelectPage: (slug: string) => void
  onToggleCollapse?: () => void
}

function Count({ n, dim = false }: { n: number; dim?: boolean }) {
  return (
    <span
      className={cn(
        "ml-auto shrink-0 text-[10px] tabular-nums",
        dim ? "text-muted-foreground-soft/50" : "text-muted-foreground-soft",
      )}
    >
      {n}
    </span>
  )
}

export function PagesRail({
  pages,
  search,
  onSearchChange,
  filters,
  onFiltersChange,
  selectedSlug,
  onSelectPage,
  onToggleCollapse,
}: PagesRailProps) {
  // Facet counts are computed against the WHOLE list, never the filtered view.
  // Counting the filtered view makes every unpicked option read 0 the moment
  // one is picked, which is a menu that argues with itself.
  const stateCounts = React.useMemo(() => stateFacetCounts(pages), [pages])
  const owners = React.useMemo(() => ownerFacets(pages), [pages])

  const displayed = React.useMemo(
    () => pages.filter((p) => matchesPageFilters(p, filters, search)),
    [pages, filters, search],
  )

  const activeCount = filters.states.length + filters.owners.length
  const ownerLabel = (ref: string) => owners.find((o) => o.ref === ref)?.label ?? ref

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <div data-pages-search className="min-w-0 flex-1">
          <SidebarSearch
            value={search}
            onValueChange={onSearchChange}
            placeholder="Search pages, owners…"
            aria-label="Search pages"
          />
        </div>
        <SidebarFilterPopover
          label="Filter pages"
          activeCount={activeCount}
          // Anchored to the trigger's right edge inside a 280px rail that
          // clips its overflow, so the panel is kept narrower than the
          // trigger's distance from the rail's left edge.
          panelClassName="w-[228px]"
          onClear={() => onFiltersChange({ states: [], owners: [] })}
        >
          <SidebarFacet
            first
            label="Status"
            resetLabel="All"
            resetActive={filters.states.length === 0}
            onReset={() => onFiltersChange({ ...filters, states: [] })}
          >
            {PAGE_STATE_ORDER.map((state: PanelState) => {
              const meta = PAGE_STATE_META[state]
              const n = stateCounts[state]
              const Icon = meta.icon
              return (
                <SidebarFacetOption
                  key={state}
                  active={filters.states.includes(state)}
                  onToggle={() =>
                    onFiltersChange({
                      ...filters,
                      states: togglePageFilter(filters.states, state),
                    })
                  }
                >
                  <Icon className={cn("h-3.5 w-3.5 shrink-0", meta.tone, n === 0 && "opacity-40")} />
                  <span className="truncate">{meta.label}</span>
                  <Count n={n} dim={n === 0} />
                </SidebarFacetOption>
              )
            })}
          </SidebarFacet>

          {owners.length > 0 && (
            <SidebarFacet
              label="Owner"
              resetLabel="All crews"
              resetActive={filters.owners.length === 0}
              onReset={() => onFiltersChange({ ...filters, owners: [] })}
            >
              {owners.map((o) => (
                <SidebarFacetOption
                  key={o.ref}
                  active={filters.owners.includes(o.ref)}
                  onToggle={() =>
                    onFiltersChange({
                      ...filters,
                      owners: togglePageFilter(filters.owners, o.ref),
                    })
                  }
                >
                  <CONCEPT_ICON.crews className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
                  <span className="truncate">{o.label}</span>
                  <Count n={o.count} />
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}
        </SidebarFilterPopover>
        {onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />}
      </SidebarToolbar>

      {/* What is currently narrowing the list, removable one at a time. With
          two multi-select facets the count badge alone cannot say WHICH picks
          are active. */}
      <SidebarActiveChips>
        {filters.states.map((s) => (
          <SidebarActiveChip
            key={`state-${s}`}
            onRemove={() =>
              onFiltersChange({ ...filters, states: filters.states.filter((v) => v !== s) })
            }
          >
            {PAGE_STATE_META[s].label}
          </SidebarActiveChip>
        ))}
        {filters.owners.map((ref) => (
          <SidebarActiveChip
            key={`owner-${ref}`}
            onRemove={() =>
              onFiltersChange({ ...filters, owners: filters.owners.filter((v) => v !== ref) })
            }
          >
            {ownerLabel(ref)}
          </SidebarActiveChip>
        ))}
      </SidebarActiveChips>

      <div className="flex min-h-0 flex-1 flex-col">
        <SidebarSection label="Pages" count={displayed.length} />
        <div className="min-h-0 flex-1 overflow-y-auto pb-1">
          {displayed.map((page) => {
            const meta = page.state ? PAGE_STATE_META[page.state] : null
            const Icon = meta?.icon ?? CONCEPT_ICON.pages
            return (
              <SidebarRow
                key={page.id || page.slug}
                selected={selectedSlug === page.slug}
                onSelect={() => onSelectPage(page.slug)}
              >
                <Icon
                  className={cn("h-3.5 w-3.5 shrink-0", meta?.tone ?? "text-muted-foreground-soft")}
                  aria-hidden
                />
                <span className="min-w-0 flex-1 truncate text-foreground/80" title={page.slug}>
                  {page.name}
                </span>
                {/* The badge is the panel count and only ever the panel count.
                    A badge that means one thing on some rows and another on
                    the rest is unreadable at a glance. */}
                {page.tally.total > 0 && (
                  <span className="shrink-0 rounded-full bg-white/[0.05] px-1.5 py-px text-[10px] tabular-nums text-muted-foreground">
                    {page.tally.total}
                  </span>
                )}
              </SidebarRow>
            )
          })}
          {displayed.length === 0 && (
            <p className="px-3 py-6 text-center text-[11px] text-muted-foreground-soft">
              {pages.length === 0
                ? "No pages yet. Create one with crewship page create --file page.yaml, then push a payload to a panel."
                : "No page matches. Clear a facet above, or search for another name."}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
