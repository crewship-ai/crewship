"use client"

import { useMemo, useState } from "react"
import { Activity, Bot, Building2, ChevronRight, Cpu, KeyRound, Link2, Settings as SettingsIcon, Shield, SlidersHorizontal, UserRound, X, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { isAdminTier } from "@/lib/permissions/tiers"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"
import { UserAvatar } from "@/components/ui/user-avatar"
import { CrewAuditSection } from "@/components/features/settings/sections/crew-audit-section"
import { LogsHistogram, type BucketRange } from "@/components/features/logs/logs-histogram"
import type { TimeRange } from "@/components/features/logs/time-range-picker"
import type { JournalEntry } from "@/lib/types/journal"
import { CONCEPT_ICON } from "@/lib/concept-icons"

import {
  AUDIT_CATEGORIES,
  AUDIT_RESULTS,
  AUDIT_SOURCES,
  DEFAULT_AUDIT_FILTERS,
  activeFilterChips,
  rangeBounds,
  windowBounds,
  rangeLabel,
  sourceSupports,
  type AuditSource,
} from "./audit-filters"
import { addDays, auditSeverity, facetCounts, tallyByDay, utcDay } from "./audit-activity"
import { AuditTimePicker } from "./audit-time"
import { useAuditActivity } from "./use-audit-activity"
import { useAuditFilters } from "./use-audit-filters"
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
 * panel holds the other filters — the trail, category, person (busiest first,
 * with their photo) and run result, each value with the number of events
 * picking it would show. The table takes the full width under the
 * active-filter chips and a chart of exactly the range it lists. Filters live
 * in the URL (useAuditFilters).
 */
export function AuditLogPage() {
  const { workspaceId, role, loading } = useWorkspace()
  // GET /api/v1/audit is ADMIN+; the Settings row is hidden below that, and a
  // typed-in URL gets a sentence instead of a 403.
  const allowed = loading || isAdminTier(role)
  const [filters, setFilters] = useAuditFilters()
  const [groupRepeats, setGroupRepeats] = useState(true)
  const [highlightAccess, setHighlightAccess] = useState(true)
  const people = useWorkspacePeople(workspaceId ?? "")
  const supports = sourceSupports(filters.source)

  // One clock per render: the calendar, the histogram and the counts must
  // agree on what "today" and "last 7 days" mean.
  const now = useMemo(() => new Date(), [filters]) // eslint-disable-line react-hooks/exhaustive-deps
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
  const customWindow = filters.range === "custom" && win.from ? { fromMs: Date.parse(win.from), toMs: win.to ? Date.parse(win.to) : now.getTime() } : null
  const chartEntries = useMemo(() => activity.events
    .filter((e) => (filters.category === "all" || e.entityType === filters.category)
      && (!filters.userId || e.userId === filters.userId)
      && (!filters.result || e.action === `agent.run.${filters.result}`))
    .map((e) => ({ ts: e.at, severity: auditSeverity(e.action) }) as unknown as JournalEntry),
  [activity.events, filters.category, filters.userId, filters.result])
  const slice: BucketRange | null = filters.sliceFrom && filters.sliceTo ? { fromMs: Date.parse(filters.sliceFrom), toMs: Date.parse(filters.sliceTo) } : null
  const pickSlice = (r: BucketRange | null) => setFilters(r
    ? { sliceFrom: new Date(r.fromMs).toISOString(), sliceTo: new Date(r.toMs).toISOString() }
    : { sliceFrom: "", sliceTo: "" })
  const [showIdle, setShowIdle] = useState(false)
  // People busiest first, with their share of the events as a small bar.
  // While the counts can be trusted (`countable`), people with nothing in the
  // range fold into one line; otherwise everyone shows, by name.
  const ranked = useMemo(() => {
    if (!countable) return { active: people, idle: [] as typeof people, top: 1 }
    const withN = people.map((p) => ({ p, n: counts.person[p.id] ?? 0 }))
    const active = withN.filter((x) => x.n > 0 || x.p.id === filters.userId).sort((a, b) => b.n - a.n).map((x) => x.p)
    const idle = withN.filter((x) => x.n === 0 && x.p.id !== filters.userId).map((x) => x.p)
    return { active, idle, top: Math.max(1, ...withN.map((x) => x.n)) }
  }, [people, counts, countable, filters.userId])
  const clearFilters = () => setFilters({ q: "", userId: "", category: "all", result: "", sliceFrom: "", sliceTo: "" })

  const personName = Object.fromEntries(people.map((p) => [p.id, p.label]))
  const avatarById = useMemo(() => Object.fromEntries(people.map((p) => [p.id, p.avatarUrl])), [people])
  const chips = activeFilterChips(filters, personName)
  const filterCount = chips.filter((c) => c.key !== "range").length
  const viewChanges = Number(!groupRepeats) + Number(!highlightAccess)

  const toolbar = (
    <>
      <SidebarSearch
        value={filters.q}
        onValueChange={(q) => setFilters({ q })}
        placeholder={supports.search ? "Search action, type or person…" : "Search is for the workspace trail"}
        aria-label="Search the audit log"
        className={cn(!supports.search && "pointer-events-none opacity-50")}
      />
      <SidebarFilterPopover label="View options" icon={SlidersHorizontal} triggerLabel="View" activeCount={viewChanges}
        onClear={() => { setGroupRepeats(true); setHighlightAccess(true) }}>
        <SidebarFacetOption active={groupRepeats} onToggle={() => setGroupRepeats(!groupRepeats)}>Group repeated events</SidebarFacetOption>
        <SidebarFacetOption active={highlightAccess} onToggle={() => setHighlightAccess(!highlightAccess)}>Mark access changes</SidebarFacetOption>
      </SidebarFilterPopover>
    </>
  )

  const nav = (
    <>
      <DrillNavSection label="Trail">
        {AUDIT_SOURCES.map((s, i) => {
          const Icon = TRAIL_ICONS[s.value]
          return (
            <DrillNavItem key={s.value} index={i} selected={filters.source === s.value} onSelect={() => pickSource(s.value)} label={s.label} sub={s.hint}
              icon={<span className="icon-tile grid h-6 w-6 shrink-0 place-items-center rounded-md" aria-hidden><Icon className="h-3 w-3" /></span>} />
          )
        })}
      </DrillNavSection>


      {supports.category && (
        <DrillNavSection label="Category">
          {AUDIT_CATEGORIES.map((c, i) => {
            const count = n(counts.category[c.value])
            return (
              <DrillNavItem key={c.value} index={i} selected={filters.category === c.value} onSelect={() => setFilters({ category: c.value })}
                label={c.value === "all" ? "All events" : c.label} meta={count} muted={count === 0}
                icon={(() => { const Icon = CATEGORY_ICON[c.value] ?? Activity; return <Icon className={cn("h-3.5 w-3.5 shrink-0", filters.category === c.value ? "text-foreground" : "text-muted-foreground")} aria-hidden /> })()} />
            )
          })}
        </DrillNavSection>
      )}

      {supports.person && people.length > 0 && (
        <DrillNavSection label="People" count={countable ? `${ranked.active.length} active` : people.length}>
          {[...ranked.active, ...(showIdle ? ranked.idle : [])].map((p, i) => {
            const count = n(counts.person[p.id])
            return (
              <DrillNavItem key={p.id} index={i} pressed={filters.userId === p.id} selected={filters.userId === p.id}
                onSelect={() => setFilters({ userId: filters.userId === p.id ? "" : p.id })}
                label={p.label} meta={count} muted={count === 0}
                sub={countable && count ? <span className="mt-1 block h-[3px] rounded-full bg-primary/55" style={{ width: `${Math.max(6, (count / ranked.top) * 100)}%` }} aria-hidden /> : undefined}
                icon={<UserAvatar name={p.label} email="" src={p.avatarUrl ?? null} className="h-5 w-5 shrink-0" textClassName="text-micro" />} />
            )
          })}
          {ranked.idle.length > 0 && (
            <button type="button" onClick={() => setShowIdle(!showIdle)} aria-expanded={showIdle}
              className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-label text-muted-foreground hover:bg-[var(--row-hover-bg)] hover:text-foreground">
              <ChevronRight className={cn("h-3 w-3 transition-transform", showIdle && "rotate-90")} aria-hidden />
              {showIdle ? "Hide" : `${ranked.idle.length} more`} with no events in this range
            </button>
          )}
          <p className="flex items-center gap-1.5 px-2 pt-1 text-micro text-muted-foreground-soft">
            <Cpu className="h-3 w-3" aria-hidden />System events have no person to pick.
          </p>
        </DrillNavSection>
      )}

      {supports.result && (
        <DrillNavSection label="Run result">
          {AUDIT_RESULTS.map((r, i) => {
            const count = n(counts.result[r.value])
            return (
              <DrillNavItem key={r.value} index={i} pressed={filters.result === r.value} selected={filters.result === r.value}
                onSelect={() => setFilters({ result: filters.result === r.value ? "" : r.value })}
                label={r.label} meta={count} muted={count === 0}
                icon={<span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", RESULT_DOT[r.value])} aria-hidden />} />
            )
          })}
        </DrillNavSection>
      )}
    </>
  )

  const mobileNav = (
    <div className="-mx-1 flex gap-1 overflow-x-auto px-1" role="tablist" aria-label="Trail">
      {AUDIT_SOURCES.map((s) => (
        <button key={s.value} type="button" role="tab" aria-selected={filters.source === s.value} onClick={() => pickSource(s.value)}
          className={cn("h-8 shrink-0 rounded-md px-3 text-xs font-medium", filters.source === s.value ? "bg-[var(--selection-bg)] text-foreground" : "text-muted-foreground")}>
          {s.label}
        </button>
      ))}
    </div>
  )

  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Audit log"
      icon={Activity}
      description="Every state-changing action, immutably recorded"
      toolbar={allowed ? toolbar : undefined}
      actions={allowed ? <AuditTimePicker filters={filters} onChange={setFilters} bounds={win} tally={tally} now={now} /> : undefined}
      nav={allowed ? nav : null}
      mobileNav={mobileNav}
      filterCount={filterCount}
    >
      <div className="flex flex-col gap-3 p-4 md:p-6">
        {!allowed ? (
          <p className="rounded-card border border-border bg-card px-4 py-6 text-center text-sm text-muted-foreground">
            The audit log is readable by workspace Admins and the Owner.
          </p>
        ) : workspaceId && (
          <>
            {chips.length > 0 && <div className="flex flex-wrap items-center gap-1.5" data-slot="audit-chips">
              {chips.map((c) => (
                <span key={c.key} className="inline-flex h-6 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-micro font-medium text-primary-hover motion-safe:animate-in motion-safe:zoom-in-95">
                  {c.label}
                  <button type="button" aria-label={`Remove filter ${c.label}`}
                    onClick={() => setFilters(c.key === "range" ? { range: DEFAULT_AUDIT_FILTERS.range, from: "", to: "", sliceFrom: "", sliceTo: "" } : c.key === "slice" ? { sliceFrom: "", sliceTo: "" } : { [c.key]: DEFAULT_AUDIT_FILTERS[c.key] })}
                    className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20">
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))}
              {filterCount > 0 && (
                <button type="button" onClick={clearFilters} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear filters</button>
              )}
            </div>}
            {/* The Crew Journal's chart, as is: sixty bars across the range,
                stacked by severity; a click picks a bar, a drag a range, and
                that slice narrows the table without changing the range. */}
            <div className="overflow-hidden rounded-card border border-border bg-card [&>div]:border-b-0 [&>div]:bg-transparent [&>div]:px-4 [&>div]:py-3" data-slot="audit-histogram">
              <LogsHistogram entries={chartEntries} timeRange={filters.range as TimeRange} customRange={customWindow}
                selected={slice} onSelect={pickSlice} height={72} />
            </div>
            <CrewAuditSection
              workspaceId={workspaceId}
              filters={filters}
              onFiltersChange={setFilters}
              showSources={false}
              showToolbar={false}
              groupRepeats={groupRepeats}
              highlightAccess={highlightAccess}
              avatars={avatarById}
              title="Events"
              description={`${AUDIT_SOURCES.find((s) => s.value === filters.source)?.label ?? ""} trail · ${rangeLabel(filters)}`}
            />
          </>
        )}
      </div>
    </DrillPage>
  )
}

