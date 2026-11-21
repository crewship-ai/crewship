"use client"

import { useMemo, useState } from "react"
import { Activity, Bot, Building2, Cpu, KeyRound, Link2, Shield, UserRound, X, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { isAdminTier } from "@/lib/permissions/tiers"
import { DrillNavItem, DrillNavSection } from "@/components/layout/drill-page"
import { SettingsDrillPage } from "@/components/features/settings/settings-drill-page"
import { SidebarFilterButton, SidebarSearch } from "@/components/layout/sidebar-kit"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import { UserAvatar } from "@/components/ui/user-avatar"
import { CrewAuditSection } from "@/components/features/settings/sections/crew-audit-section"
import type { BucketRange } from "@/components/features/logs/logs-histogram"
import { CONCEPT_ICON } from "@/lib/concept-icons"

import {
  AUDIT_CATEGORIES,
  AUDIT_RESULTS,
  AUDIT_SOURCES,
  DEFAULT_AUDIT_FILTERS,
  activeFilterChips,
  rangeBounds,
  windowBounds,
  shortRangeLabel,
  sourceSupports,
  type AuditSource,
} from "./audit-filters"
import { addDays, facetCounts, tallyByDay, utcDay } from "./audit-activity"
import { AuditHistogram } from "./audit-histogram"
import { AuditRangeBar, AuditTimePicker } from "./audit-time"
import { useAuditActivity } from "./use-audit-activity"
import { useAuditFilters } from "./use-audit-filters"
import { JournalIntegrityDialog, JournalIntegrityLine, useJournalIntegrity } from "./journal-integrity"
import { AuditRetentionDialog, retentionWords, useAuditRetention } from "./audit-retention"
import { PageNoAccess } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { formatStatus } from "@/lib/format-status"
import { useWorkspacePeople } from "./use-workspace-people"

const TRAIL_ICONS: Record<AuditSource, LucideIcon> = { workspace: Building2, crews: Link2, credentials: KeyRound, keeper: Shield }
/** Each category wears the product's icon for what it records (lib/concept-icons). */
const CATEGORY_ICON: Record<string, LucideIcon> = {
  all: CONCEPT_ICON.activity,
  agent_run: CONCEPT_ICON.runs,
  AGENT: Bot,
  CREW: CONCEPT_ICON.crews,
  CREW_LINK: Link2,
  CREDENTIAL: CONCEPT_ICON.credentials,
  WorkspaceMember: UserRound,
  WORKSPACE: Building2,
}
const RESULT_DOT: Record<string, string> = { completed: "bg-success", failed: "bg-destructive", cancelled: "bg-muted-foreground" }
/** The activity window: long enough to cover every preset up to 30 days. */
const RECENT_DAYS = 35

/**
 * Settings › Audit log as a nested page. The time range sits on the sub-bar,
 * the Crew Journal's picker with a month calendar behind "Custom…". The side
 * panel names the four records (All changes, Between crews, Secret use,
 * Keeper decisions); under All changes it lists what changed, by category,
 * and its Filter holds Who and Run result, each value with the number of
 * events picking it would show. The head carries the integrity line and
 * "Check now". One Events card holds the record's name, the count, the display
 * chips, the chart and the table. Filters live in the URL (useAuditFilters).
 */
export function AuditLogPage() {
  const { workspaceId, role, loading } = useWorkspace()
  const isMobile = useIsMobile()
  // GET /api/v1/audit is ADMIN+; the Settings row is hidden below that, and a
  // typed-in URL gets a sentence instead of a 403.
  const allowed = loading || isAdminTier(role)
  const [filters, setFilters] = useAuditFilters()
  const integrity = useJournalIntegrity(workspaceId ?? "")
  const retention = useAuditRetention()
  const [retentionOpen, setRetentionOpen] = useState(false)
  // The name of the entity a person asked for "everything about"; the URL holds only its id.
  const [entityName, setEntityName] = useState("")
  const facets = useSidebarFacets()
  const people = useWorkspacePeople(workspaceId ?? "")
  const supports = sourceSupports(filters.source)

  // Anchor the chosen window across facet/slice changes and activity arrivals.
  // Re-reading the clock on selection moves relative buckets beneath it.
  const now = useMemo(() => new Date(), [filters.range, filters.from, filters.to])
  const today = utcDay(now)
  const bounds = rangeBounds(filters, now)
  const recentFrom = addDays(today, -(RECENT_DAYS - 1))
  const activity = useAuditActivity(workspaceId, filters.source, recentFrom, today, allowed && !loading)
  const tally = useMemo(() => tallyByDay(activity.events), [activity.events])
  // Counts are shown only when the sample covers the whole range and is
  // complete; a number that is secretly a floor is worse than none. Search is
  // matched on the server in ways the page cannot repeat, so it hides them too.
  const countable = !activity.loading && !activity.truncated && !filters.q.trim() && !!bounds.from && bounds.from.slice(0, 10) >= recentFrom
  const counts = useMemo(() => facetCounts(activity.events, filters, rangeBounds(filters, now)), [activity.events, filters, now])
  const n = (v: number | undefined) => (countable ? v ?? 0 : undefined)

  const pickSource = (source: AuditSource) =>
    // A new trail keeps the time range; the other filters belong to the old one.
    setFilters({ ...DEFAULT_AUDIT_FILTERS, source, range: filters.range, from: filters.from, to: filters.to })
  // The chart: the Journal's histogram over the picked range, fed the events
  // the other filters keep, each with a Journal severity.
  const win = windowBounds(filters, now)
  const customWindow = win.from ? { fromMs: Date.parse(win.from), toMs: win.to ? Date.parse(win.to) : now.getTime() } : null
  const chartTimes = useMemo(() => activity.events
    .filter((e) => (filters.category === "all" || e.entityType === filters.category)
      && (!filters.userId || e.userId === filters.userId)
      && (!filters.entityId || e.entityId === filters.entityId)
      && (!filters.result || e.action === `agent.run.${filters.result}`))
    .map((e) => Date.parse(e.at)),
  [activity.events, filters.category, filters.userId, filters.result, filters.entityId])
  const slice: BucketRange | null = filters.sliceFrom && filters.sliceTo ? { fromMs: Date.parse(filters.sliceFrom), toMs: Date.parse(filters.sliceTo) } : null
  const pickSlice = (r: BucketRange | null) => setFilters(r
    ? { sliceFrom: new Date(r.fromMs).toISOString(), sliceTo: new Date(r.toMs).toISOString() }
    : { sliceFrom: "", sliceTo: "" })
  // People busiest first while the counts can be trusted (`countable`),
  // otherwise by name; the facet lists the top few and "All N…" the rest.
  const ranked = useMemo(() => {
    if (!countable) return people
    return [...people].sort((a, b) => (counts.person[b.id] ?? 0) - (counts.person[a.id] ?? 0))
  }, [people, counts, countable])
  const clearFilters = () => { setEntityName(""); setFilters({ q: "", userId: "", category: "all", result: "", entityId: "", sliceFrom: "", sliceTo: "" }) }

  const personName = Object.fromEntries(people.map((p) => [p.id, p.label]))
  const avatarById = useMemo(() => Object.fromEntries(people.map((p) => [p.id, p.avatarUrl])), [people])
  const chips = activeFilterChips(filters, personName, entityName)
  const filterCount = chips.filter((c) => c.key !== "range").length

  const facetCount = Number(Boolean(filters.userId)) + Number(Boolean(filters.result))
  const toolbar = (
    <>
      <SidebarSearch
        value={filters.q}
        onValueChange={(q) => setFilters({ q })}
        placeholder={supports.search ? "Search action or person…" : "Search works in All changes"}
        aria-label="Search the audit log"
        className={cn(!supports.search && "pointer-events-none opacity-50")}
      />
      {supports.person && <SidebarFilterButton activeCount={facetCount} {...facets.triggerProps}>Filter</SidebarFilterButton>}
    </>
  )

  // Who and Run result live in the panel's Filter (the facet kit): the top
  // values with their counts, then "All N…".
  const facetBlock = supports.person ? (
    <SidebarFacetBlock {...facets.blockProps} label="Filter the audit log" activeCount={facetCount} onClear={() => setFilters({ userId: "", result: "" })} className="mt-2">
      {people.length > 0 && (
        <SidebarInlineFacet
          label="Who"
          allLabel={`All ${people.length}…`}
          values={ranked.map((p) => ({
            value: p.id,
            label: p.label,
            text: p.label,
            count: n(counts.person[p.id]),
            icon: <UserAvatar name={p.label} email="" src={p.avatarUrl ?? null} className="h-5 w-5 shrink-0" textClassName="text-micro" />,
          }))}
          selected={filters.userId || null}
          onToggle={(id) => setFilters({ userId: filters.userId === id ? "" : id })}
          onClear={() => setFilters({ userId: "" })}
        />
      )}
      {people.length > 0 && (
        <p className="flex items-center gap-1.5 px-2 pb-1 text-micro text-muted-foreground-soft">
          <Cpu className="h-3 w-3" aria-hidden />System events have no person to pick.
        </p>
      )}
      <SidebarInlineFacet
        label="Run result"
        values={AUDIT_RESULTS.map((r) => ({
          value: r.value,
          label: r.label,
          text: r.label,
          count: n(counts.result[r.value]),
          icon: <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", RESULT_DOT[r.value])} aria-hidden />,
        }))}
        selected={filters.result || null}
        onToggle={(v) => setFilters({ result: filters.result === v ? "" : (v as typeof filters.result) })}
        onClear={() => setFilters({ result: "" })}
      />
    </SidebarFacetBlock>
  ) : null

  const recordHint = AUDIT_SOURCES.find((x) => x.value === filters.source)?.hint
  const nav = supports.category ? (
    <DrillNavSection collapsible={false} label="What changed">
      {AUDIT_CATEGORIES.map((c, i) => {
        const count = n(counts.category[c.value])
        const Icon = CATEGORY_ICON[c.value] ?? Activity
        return (
          <DrillNavItem key={c.value} index={i} selected={filters.category === c.value} onSelect={() => setFilters({ category: c.value })}
            label={c.label} meta={count} muted={count === 0}
            icon={<Icon className={cn("h-3.5 w-3.5 shrink-0", filters.category === c.value ? "text-foreground" : "text-muted-foreground")} aria-hidden />} />
        )
      })}
    </DrillNavSection>
  ) : (
    <p className="px-3 py-2.5 text-label text-muted-foreground">{recordHint}. This record filters by time only.</p>
  )

  // v67: the trails are one card of four rows under the way back, each on its
  // number key, with no sentence under them (the head says what is shown).
  const spaces = {
    label: "Audit record",
    value: filters.source,
    onChange: pickSource,
    items: AUDIT_SOURCES.map((s) => ({
      key: s.value,
      label: s.label,
      icon: TRAIL_ICONS[s.value],
      count: s.value === filters.source && filters.source === "workspace" ? n(counts.category.all) : undefined,
    })),
  }

  const mobileNav = (
    <div className="-mx-1 flex gap-1 overflow-x-auto px-1" role="tablist" aria-label="Record">
      {AUDIT_SOURCES.map((s) => (
        <button key={s.value} type="button" role="tab" aria-selected={filters.source === s.value} onClick={() => pickSource(s.value)}
          className={cn("h-8 shrink-0 rounded-md px-3 text-xs font-medium", filters.source === s.value ? "bg-[var(--selection-bg)] text-foreground" : "text-muted-foreground")}>
          {s.label}
        </button>
      ))}
    </div>
  )

  const records = AUDIT_SOURCES.find((x) => x.value === filters.source)?.label ?? ""

  return (
    <SettingsDrillPage
      panelId="drill-audit"
      title="Audit log"
      icon={Activity}
      description="Every change in this workspace, kept exactly as it happened. Admins and the owner."
      note={allowed ? <JournalIntegrityLine integrity={integrity} /> : undefined}
      headless={!allowed}
      toolbar={allowed ? toolbar : undefined}
      facets={allowed ? facetBlock : undefined}
      actions={!allowed ? undefined : isMobile
        ? <AuditTimePicker filters={filters} onChange={setFilters} bounds={win} tally={tally} now={now} />
        : <AuditRangeBar filters={filters} onChange={setFilters} bounds={win} tally={tally} now={now} />}
      spaces={allowed ? spaces : undefined}
      nav={allowed ? nav : null}
      mobileNav={mobileNav}
    >
      <div className="flex flex-col gap-3 p-4 md:p-6">
        {!allowed ? (
          <PageNoAccess
            area="Audit log"
            role={role ? formatStatus(role).label : undefined}
            needed="Admin"
            primary={{ label: "Back to Settings", href: "/settings" }}
          />
        ) : workspaceId && (
          <>
            {chips.length > 0 && <div className="flex flex-wrap items-center gap-1.5" data-slot="audit-chips">
              {chips.map((c) => (
                <span key={c.key} className="inline-flex h-6 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-micro font-medium text-primary-hover motion-safe:animate-in motion-safe:zoom-in-95">
                  {c.label}
                  <button type="button" aria-label={`Remove filter ${c.label}`}
                    onClick={() => {
                      if (c.key === "entityId") setEntityName("")
                      setFilters(c.key === "range" ? { range: DEFAULT_AUDIT_FILTERS.range, from: "", to: "", sliceFrom: "", sliceTo: "" } : c.key === "slice" ? { sliceFrom: "", sliceTo: "" } : { [c.key]: DEFAULT_AUDIT_FILTERS[c.key] })
                    }}
                    className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20 relative coarse:after:absolute coarse:after:-inset-4 coarse:after:content-['']">
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))}
              {filterCount > 0 && (
                <button type="button" onClick={clearFilters} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear filters</button>
              )}
            </div>}
            {/* One card for the record: header, chart, table. A click on the
                chart picks a bar, a drag a slice of time, and that slice
                narrows the table without changing the range. */}
            <CrewAuditSection
              workspaceId={workspaceId}
              filters={filters}
              onFiltersChange={setFilters}
              avatars={avatarById}
              title={records}
              rangeText={shortRangeLabel(filters)}
              onEntity={(id, label) => { setEntityName(label); setFilters({ entityId: id }) }}
              chart={<AuditHistogram times={chartTimes} window={customWindow} endsToday={filters.range !== "custom"} selected={slice} onSelect={pickSlice} />}
            />
            {retention && (
              <p data-slot="audit-retention" className="flex flex-wrap items-center gap-x-1 text-label text-muted-foreground">
                Kept {retentionWords(retention.audit)}; secret use {retentionWords(retention.secret)}.
                <Button variant="link" size="xs" onClick={() => setRetentionOpen(true)} className="h-auto p-0 text-label coarse:min-h-[44px]">Change how long</Button>
              </p>
            )}
            <JournalIntegrityDialog integrity={integrity} />
            <AuditRetentionDialog open={retentionOpen} onOpenChange={setRetentionOpen} />
          </>
        )}
      </div>
    </SettingsDrillPage>
  )
}
