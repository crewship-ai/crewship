"use client"

import { Building2, User, Users } from "lucide-react"
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
import {
  CALENDAR_SCOPE_LABELS,
  STATUS_FACETS,
  calendarCounts,
  type CalendarFilters,
  type CalendarItem,
  type CalendarScope,
  type StatusFacet,
} from "@/lib/calendar/model"

export interface CalendarCrew {
  id: string
  name: string
}

/**
 * The Calendar's secondary panel (CAL-PANEL): search over the loaded range,
 * Views (Mine / Workspace), Crews, and the Status filter. Every count ignores
 * its own facet, so picking one never zeroes the others; active filters show
 * as removable chips under the toolbar. Filter opens the facets inside the
 * panel, under the toolbar (kit facet block, PNL-02), not in a popover; the
 * Type facet waits for the aggregated dates (CAL-08) and stays hidden.
 */
export function CalendarPanel({
  items,
  filters,
  crews,
  onChange,
  onPicked,
}: {
  items: CalendarItem[]
  filters: CalendarFilters
  crews: CalendarCrew[]
  onChange: (patch: Partial<CalendarFilters>) => void
  /** Called after a pick that changes what the page shows (closes the phone sheet). */
  onPicked?: () => void
}) {
  const counts = calendarCounts(items, filters)
  const facets = useSidebarFacets()
  const toggleStatus = (id: StatusFacet) =>
    onChange({ statuses: filters.statuses.includes(id) ? filters.statuses.filter((s) => s !== id) : [...filters.statuses, id] })
  const crewName = (id: string) => crews.find((c) => c.id === id)?.name ?? "Crew"
  const scopeRow = (scope: CalendarScope, Icon: typeof User) => (
    <SidebarRow
      key={scope}
      selected={filters.scope === scope && !filters.crewId}
      onSelect={() => {
        onChange({ scope, crewId: null })
        onPicked?.()
      }}
      as="div"
    >
      <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1 truncate">{CALENDAR_SCOPE_LABELS[scope]}</span>
      <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{scope === "mine" ? counts.mine : counts.workspace}</span>
    </SidebarRow>
  )
  return (
    <>
      <SidebarToolbar>
        <div className="min-w-0 flex-1" data-calendar-search>
          <SidebarSearch value={filters.query} onValueChange={(query) => onChange({ query })} placeholder="Search dates, routines…" />
        </div>
        <SidebarFilterButton activeCount={filters.statuses.length} {...facets.triggerProps} />
      </SidebarToolbar>
      <SidebarActiveChips>
        {filters.crewId && (
          <SidebarActiveChip label={crewName(filters.crewId)} onRemove={() => onChange({ crewId: null })}>
            {crewName(filters.crewId)}
          </SidebarActiveChip>
        )}
        {/* While the block is open its Status header names the choice. */}
        {!facets.open && STATUS_FACETS.filter((f) => filters.statuses.includes(f.id)).map((f) => (
          <SidebarActiveChip key={f.id} label={f.label} onRemove={() => toggleStatus(f.id)}>
            {f.label}
          </SidebarActiveChip>
        ))}
      </SidebarActiveChips>
      <SidebarFacetBlock {...facets.blockProps} label="Filter calendar" activeCount={filters.statuses.length} onClear={() => onChange({ statuses: [] })}>
        <SidebarInlineFacet
          label="Status"
          values={STATUS_FACETS.map((f) => ({ value: f.id, label: f.label, count: counts.statuses[f.id] }))}
          selected={filters.statuses}
          onToggle={(id) => toggleStatus(id as StatusFacet)}
          onClear={() => onChange({ statuses: [] })}
        />
      </SidebarFacetBlock>
      <SidebarSection label="Views">
        {scopeRow("mine", User)}
        {scopeRow("workspace", Building2)}
      </SidebarSection>
      {crews.length > 0 && (
        <SidebarSection label="Crews" count={crews.length}>
          {crews.map((c) => (
            <SidebarRow
              key={c.id}
              as="div"
              selected={filters.crewId === c.id}
              onSelect={() => {
                onChange({ crewId: filters.crewId === c.id ? null : c.id })
                onPicked?.()
              }}
            >
              <Users className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">{c.name}</span>
              <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{counts.crews.get(c.id) ?? 0}</span>
            </SidebarRow>
          ))}
        </SidebarSection>
      )}
      <p className="px-3.5 pb-3 pt-4 text-label text-muted-foreground-soft">
        The calendar has no dates of its own. Each one lives on its routine, and you see only what you can open.
      </p>
    </>
  )
}
