"use client"

import * as React from "react"
import { Shield, Database, List, CalendarClock, Plus, HardDrive, KeyRound, ChevronDown, ChevronRight, X, Check, LayoutDashboard, RotateCcw, Building2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { SettingsSegmented } from "@/components/features/settings/shared"
import { LogsHistogram, type BucketRange } from "@/components/features/logs/logs-histogram"
import { TimeRangePicker, type TimeRange } from "@/components/features/logs/time-range-picker"
import type { JournalEntry } from "@/lib/types/journal"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import { SidebarFilterButton, SidebarSearch } from "@/components/layout/sidebar-kit"
import { useIsMobile } from "@/hooks/use-mobile"
import { initialBackupsSection, type BackupsSection } from "@/app/(dashboard)/admin/navigation"
import { useBackupsScope, type RunFilter, type RunQuery, type SectionCtx } from "./backups-scope"
import { COVERAGE_LABEL, coverageCounts, coverageRows, describePlanWhen, runsCapNote, scopeLabel, type CoverageState } from "./backups-model"
import { useBackupRuns } from "./use-backup-runs"
import { useBackupPlans } from "./use-backup-plans"
import { useBackupsOverview } from "./use-backups-overview"
import { BackupsOverview, attentionItems, overviewSentence } from "./backups-overview"
import { BackupsHistory, matches, matchesQuery, runsInScope } from "./backups-history"
import { BackupsSchedules } from "./backups-schedules"
import { BackupsStorage } from "./backups-storage"
import { BackupsRecovery } from "./backups-recovery"
import { BackupsKeys } from "./backups-keys"
import { BackupsCoverage, COVERAGE_FILTERS } from "./backups-coverage"
import { BackUpNowDialog } from "./backups-run-dialogs"
import { BackupsScopePicker } from "./backups-scope-picker"
import { WorkspaceBackups } from "./backups-workspace"
import { BackupsSetup, SetupCard, setupState } from "./backups-setup"
import { useRecipients } from "./use-backup-settings"

/**
 * Admin › Backups, built like People & workspaces and Security: "← Admin" and a
 * panel with one scope control ("Showing: Whole instance | N workspaces"), the
 * views (Overview · Runs · Restore · Coverage), the plans, and the two
 * instance settings; the middle shows one thing at a time. The panel never
 * lists workspaces: hundreds of them are searched in the scope picker and paged
 * in Coverage.
 *
 * The scope drives Overview, Runs, Restore and Back up now. Runs is built like
 * the Audit log: the time range in the sub-bar, result chips with counts, a
 * chart of runs whose bars narrow the table, and the pickers above it; ticked
 * runs are pinned or downloaded from the selection bar. A workspace opens its
 * own backups. Section, filters, plan, run, scope and workspace live in the URL
 * (?section=&status=&kind=&proof=&period=&plan=&run=&scope=&ws=&workspace=).
 */

const RUN_FACETS: { key: RunFilter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "failed", label: "Failed" },
  { key: "incomplete", label: "Incomplete" },
  { key: "manual", label: "Manual" },
  { key: "pinned", label: "Pinned" },
]

/** Plans the panel lists before "All N plans…". */
const PANEL_PLANS = 8

type RecoveryView = NonNullable<SectionCtx["recoveryView"]>

function isRunFilter(v: string | null): v is RunFilter {
  return RUN_FACETS.some((f) => f.key === v)
}

function isCoverageState(v: string | null): v is CoverageState {
  return COVERAGE_FILTERS.some((f) => f === v)
}

interface View {
  section: BackupsSection
  /** A workspace's own backups (slug), over the section. */
  workspace: string
  status: RunFilter
  /** Coverage's Status facet; shares ?status= with the Runs result. */
  coverage: CoverageState | ""
  plan: string | null
  recovery: RecoveryView
  query: RunQuery
}

const KINDS: { key: RunQuery["kind"]; label: string }[] = [
  { key: "full", label: "Full" }, { key: "custom", label: "Partial" }, { key: "environments", label: "Environments" },
]
const PROOFS: { key: RunQuery["proof"]; label: string }[] = [
  { key: "3", label: "Test restore" }, { key: "2", label: "Contents checked or better" }, { key: "1", label: "Checksum or better" },
]
const PERIODS: { key: RunQuery["period"]; label: string }[] = [
  { key: "1", label: "Last 24 hours" }, { key: "7", label: "Last 7 days" }, { key: "30", label: "Last 30 days" }, { key: "90", label: "Last 90 days" },
]
const oneOf = <T extends string>(v: string | null, all: { key: T }[]): T | "" => (all.some((o) => o.key === v) ? (v as T) : "")

function readView(search: string): View {
  const p = new URLSearchParams(search)
  const status = p.get("status")
  const rec = p.get("view")
  return {
    section: initialBackupsSection(search),
    workspace: p.get("workspace") ?? "",
    status: isRunFilter(status) ? status : "all",
    coverage: isCoverageState(status) ? status : "",
    plan: p.get("plan"),
    recovery: rec === "history" || rec === "drills" ? rec : "new",
    // Runs open on the last 30 days; ?period=all is every run.
    query: { q: p.get("q") ?? "", kind: oneOf(p.get("kind"), KINDS), proof: oneOf(p.get("proof"), PROOFS), plan: "",
      period: !p.has("period") ? "30" : p.get("period") === "all" ? "" : oneOf(p.get("period"), PERIODS) },
  }
}

/** The Runs time range and the period filter it sets (days; "" = all time). */
const RANGE_PERIOD: Record<string, RunQuery["period"]> = { "24h": "1", "7d": "7", "30d": "30", "90d": "90", all: "" }
const PERIOD_RANGE: Record<string, TimeRange> = { "1": "24h", "7": "7d", "30": "30d", "90": "90d", "": "all" }
const PERIOD_WORDS: Record<string, string> = { "1": "in the last 24 h", "7": "in the last 7 d", "30": "in the last 30 d", "90": "in the last 90 d", "": "in all" }

/** A filter above a table, as in the Audit log: its value, a menu of the others. */
function FilterMenu<T extends string>({ label, value, options, onChange, any = "Any" }: {
  label: string
  value: T
  options: { key: T; label: string; count?: number }[]
  onChange: (v: T) => void
  any?: string
}) {
  const current = options.find((o) => o.key === value)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" size="sm" className="h-7 gap-1 px-2.5 text-xs" aria-label={`${label}: ${current?.label ?? any}`}>
          <span className="text-muted-foreground">{label}:</span>{current?.label ?? any}<ChevronDown className="h-3 w-3" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuLabel className=" text-micro text-muted-foreground">{label}</DropdownMenuLabel>
        {options.map((o) => (
          <DropdownMenuItem key={o.key} onSelect={() => onChange(o.key)} className="gap-2 text-xs">
            <Check className={cn("h-3.5 w-3.5", o.key === value ? "opacity-100" : "opacity-0")} />
            <span className="flex-1">{o.label}</span>
            {o.count !== undefined && <span className="text-micro tabular-nums text-muted-foreground">{o.count}</span>}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function BackupsPage() {
  const { workspaceId, demo, workspaces, selected, scope, commit, backUpNow } = useBackupsScope()
  const isMobile = useIsMobile()
  const [view, setView] = React.useState<View>(() => readView(typeof window === "undefined" ? "" : window.location.search))
  const [focus, setFocus] = React.useState<{ run: string | null; path: string | null }>(() => ({
    run: typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("run"),
    path: null,
  }))
  const [newPlanSignal, setNewPlanSignal] = React.useState(0)
  const [slice, setSlice] = React.useState<BucketRange | null>(null)
  const [allPlans, setAllPlans] = React.useState(false)
  const [nowOpen, setNowOpen] = React.useState(false)
  const facets = useSidebarFacets()

  const update = React.useCallback((next: Partial<View>) => {
    setView((cur) => {
      const v = { ...cur, ...next }
      const url = new URL(window.location.href)
      const set = (k: string, val: string | null) => (val ? url.searchParams.set(k, val) : url.searchParams.delete(k))
      set("section", v.section === "overview" ? null : v.section)
      set("workspace", v.workspace || null)
      set("status", v.section === "history" && v.status !== "all" ? v.status : v.section === "coverage" && v.coverage ? v.coverage : null)
      set("plan", v.section === "schedules" ? v.plan : null)
      set("view", v.section === "recovery" && v.recovery !== "new" ? v.recovery : null)
      if (v.section !== "history") url.searchParams.delete("run")
      set("q", v.query.q.trim() || null)
      set("kind", v.query.kind || null)
      set("proof", v.query.proof || null)
      set("period", v.query.period === "30" ? null : v.query.period || "all")
      window.history.replaceState(window.history.state, "", url.toString())
      return v
    })
  }, [])
  const go = (section: BackupsSection, extra: Partial<View> = {}) => update({ section, workspace: "", coverage: "", ...extra })

  // The one read of the runs: the Runs table, its chips and chart, the panel
  // count and Restore all use it (the server sends the newest 500).
  const runs = useBackupRuns(scope, selected, workspaces)
  const plans = useBackupPlans()
  // A first backup needs a key and a plan: until both exist the Overview
  // leads with the setup, and Back up now opens it instead of failing.
  const recipients = useRecipients()
  // Only what was read counts: a failed read says nothing about the setup.
  const setup = setupState(recipients.data, plans.data)
  const [setupOpen, setSetupOpen] = React.useState(false)
  // The Overview follows the scope; the panel badge and the head read the same answer.
  const overview = useBackupsOverview(scope, selected, workspaces)
  // Each workspace's newest backup for Coverage and its counts: the server
  // lists them for a workspaces scope, so ask for all of them.
  const everyId = React.useMemo(() => new Set(workspaces.map((w) => w.id)), [workspaces])
  // Asked once the workspaces are known: "every workspace" is then the whole list.
  const coverageRes = useBackupsOverview("workspaces", everyId, workspaces, workspaces.length > 0)
  const coverage = React.useMemo(() => coverageRes.data?.workspaces ?? [], [coverageRes.data])
  const coverageCount = React.useMemo(() => coverageCounts(coverageRows(coverageRes.data ? coverage : undefined, workspaces)), [coverageRes.data, coverage, workspaces])
  const attention = overview.data ? attentionItems(overview.data).length : 0

  const onHistory = view.section === "history" && !view.workspace
  const ctx: SectionCtx = {
    scope, selected, workspaces, currentWorkspaceId: workspaceId, demo,
    focusRun: focus.run, focusPath: focus.path, backUpNow, newPlanSignal,
    focusPlan: view.plan, runFilter: view.status, inDrill: true, recoveryView: view.recovery, runQuery: view.query,
    runSlice: onHistory ? slice : null,
    go: (section, opts) => {
      setFocus({ run: opts?.run ?? null, path: opts?.path ?? null })
      go(section)
    },
  }

  const newPlan = () => {
    setNewPlanSignal((x) => x + 1)
    go("schedules", { plan: "new" })
  }
  const openWorkspace = (id: string) => {
    const w = workspaces.find((x) => x.id === id)
    if (w) update({ workspace: w.slug })
  }
  const openCoverage = (state?: CoverageState) => go("coverage", { coverage: state ?? "" })
  const ws = workspaces.find((w) => w.slug === view.workspace || w.id === view.workspace)

  const setQuery = (next: Partial<RunQuery>) => update({ query: { ...view.query, ...next } })
  const needle = view.query.q.trim().toLowerCase()
  const shownPlans = (plans.data ?? []).filter((p) => !needle || p.name.toLowerCase().includes(needle))
  const panelPlans = allPlans || needle ? shownPlans : shownPlans.slice(0, PANEL_PLANS)
  const filterCount = onHistory ? [view.status !== "all", view.query.kind, view.query.proof, view.query.plan].filter(Boolean).length : view.section === "coverage" && view.coverage ? 1 : 0

  // What each Runs filter would show, the others held: the count beside an
  // option is the number of rows picking it opens.
  const scoped = runsInScope(runs.data ?? [], { scope, selected }, runs.source === "legacy")
  const countWith = (q: Partial<RunQuery>, status: RunFilter = view.status) =>
    scoped.filter((r) => matches(r, status) && matchesQuery(r, { ...view.query, ...q })).length
  const inRange = countWith({}, "all")
  const planNames = [...new Set(scoped.filter((r) => r.plan_name).map((r) => r.plan_name!))]

  // The chart: every run of the range under the other filters, by result.
  const chartEntries = React.useMemo(() => scoped
    .filter((r) => matches(r, view.status) && matchesQuery(r, { ...view.query, period: "" }))
    .map((r) => ({ ts: r.started_at, severity: r.status === "failed" ? "error" : r.status === "incomplete" || r.incomplete.length > 0 ? "warn" : "info" }) as unknown as JournalEntry),
  // eslint-disable-next-line react-hooks/exhaustive-deps -- scoped is derived from these
  [runs.data, scope, selected, view.status, view.query])
  const range: TimeRange = PERIOD_RANGE[view.query.period] ?? "all"

  const here = ws ? null : view.section === "history" ? "runs" : view.section === "recovery" ? "restore" : view.section === "overview" ? "overview" : view.section === "coverage" ? "coverage" : null
  const runsMeta = runs.data ? (runsCapNote(runs.data.length) ? `${runs.data.length}+` : String(scoped.length)) : undefined
  const VIEWS = [
    { key: "overview", label: "Overview", section: "overview" as const, icon: LayoutDashboard, meta: attention > 0 ? <span className="text-destructive" aria-label={`${attention} need attention`}>{attention}</span> : undefined },
    { key: "runs", label: "Runs", section: "history" as const, icon: List, meta: runsMeta },
    { key: "restore", label: "Restore", section: "recovery" as const, icon: RotateCcw, meta: undefined },
    { key: "coverage", label: "Coverage", section: "coverage" as const, icon: Building2, meta: coverageRes.data ? `${coverageCount.covered} of ${coverageCount.total - coverageCount.none}` : undefined },
  ]

  const toolbar = (
    <>
      <SidebarSearch value={view.query.q} onValueChange={(q) => setQuery({ q })}
        placeholder={onHistory ? "Search runs…" : view.section === "coverage" ? "Search workspaces…" : "Search plans…"} />
      {view.section === "coverage" && !ws && <SidebarFilterButton activeCount={filterCount} {...facets.triggerProps}>Filter</SidebarFilterButton>}
    </>
  )

  const scopePicker = <BackupsScopePicker scope={scope} selected={selected} workspaces={workspaces} onCommit={commit} />

  const panelFacets = (
    <>
      <div className="px-2 pt-2" data-slot="panel-scope">{scopePicker}</div>
      {view.section === "coverage" && !ws && (
        <SidebarFacetBlock {...facets.blockProps} label="Filter workspaces" activeCount={filterCount} onClear={() => update({ coverage: "" })} className="mt-2">
          <SidebarInlineFacet label="Status"
            values={COVERAGE_FILTERS.map((k) => ({ value: k, label: COVERAGE_LABEL[k], count: coverageCount[k] }))}
            selected={view.coverage || null}
            onToggle={(v) => update({ coverage: view.coverage === v ? "" : (v as CoverageState) })}
            onClear={() => update({ coverage: "" })} />
        </SidebarFacetBlock>
      )}
    </>
  )

  const nav = (
    <>
      <DrillNavSection label="Views">
        {VIEWS.map((v, i) => (
          <DrillNavItem key={v.key} index={i} selected={here === v.key} onSelect={() => go(v.section)} tile={v.icon} label={v.label} meta={v.meta} />
        ))}
      </DrillNavSection>
      <DrillNavSection label="Plans" count={shownPlans.length}>
        {panelPlans.map((p, i) => (
          <DrillNavItem key={p.id} index={i} selected={!ws && view.section === "schedules" && view.plan === p.id} muted={!p.enabled}
            onSelect={() => go("schedules", { plan: p.id })}
            icon={<CalendarClock className="h-3.5 w-3.5" />} label={p.name} sub={describePlanWhen(p)} meta={p.enabled ? "on" : "off"} />
        ))}
        {!needle && shownPlans.length > PANEL_PLANS && (
          <DrillNavItem index={panelPlans.length} onSelect={() => setAllPlans((v) => !v)} pressed={allPlans}
            label={<span className="text-muted-foreground">{allPlans ? "Fewer plans" : `All ${shownPlans.length} plans…`}</span>} />
        )}
        <DrillNavItem index={panelPlans.length + 1} selected={!ws && view.section === "schedules" && view.plan === "new"} onSelect={newPlan}
          icon={<Plus className="h-3.5 w-3.5 text-primary-hover" />} label={<span className="text-primary-hover">New plan</span>} />
      </DrillNavSection>
      <DrillNavSection label="Instance settings" count={2}>
        <DrillNavItem selected={!ws && view.section === "storage"} onSelect={() => go("storage")}
          icon={<HardDrive className="h-3.5 w-3.5" />} label="Storage" title="Instance setting · applies to every backup plan"
          sub={overview.data ? (overview.data.status.offsite_verified ? overview.data.status.where.value : "This server only") : "Where copies go"} />
        <DrillNavItem selected={!ws && view.section === "keys"} onSelect={() => go("keys")}
          icon={<KeyRound className="h-3.5 w-3.5" />} label="Keys & alerts" title="Instance setting · applies to every backup plan"
          sub="Encryption keys and who hears about a failure" />
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <div className="flex flex-col gap-2">
      {scopePicker}
      <div className="grid grid-cols-4 gap-1 rounded-lg border border-border bg-surface-subtle p-0.5" role="tablist" aria-label="View">
        {VIEWS.map((v) => (
          <button key={v.key} type="button" role="tab" aria-selected={here === v.key} onClick={() => go(v.section)}
            className={cn("flex h-8 items-center justify-center rounded-md text-xs font-medium coarse:h-[44px]", here === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground")}>
            {v.label}
          </button>
        ))}
      </div>
    </div>
  )

  const crumb = (backLabel: string, back: () => void, label: string) => (
    <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-label text-muted-foreground">
      <button type="button" className="hover:text-foreground" onClick={back}>{backLabel}</button>
      <ChevronRight className="size-3" /><span className="text-foreground">{label}</span>
    </nav>
  )

  let body: React.ReactNode
  if (ws) {
    body = (
      <>
        {crumb("Overview", () => go("overview"), ws.name)}
        <WorkspaceBackups ws={ws} coverage={coverage.find((c) => c.workspace_id === ws.id)}
          ctx={{ ...ctx, go: (section, opts) => {
            // Restore from a workspace's page restores that workspace.
            if (section === "recovery") commit("workspaces", new Set([ws.id]))
            ctx.go(section, opts)
          } }}
          onOpenRuns={(run) => {
            commit("workspaces", new Set([ws.id]))
            setFocus({ run: run ?? null, path: null })
            go("history")
          }} />
      </>
    )
  } else if (view.section === "overview") {
    body = (
      <>
        {setup.known && !setup.done && <SetupCard hasKey={setup.hasKey} hasPlan={setup.hasPlan} onStart={() => setSetupOpen(true)} />}
        <BackupsOverview ctx={ctx} resource={overview} coverage={scope === "instance" ? (coverageRes.data ? coverage : undefined) : undefined} onOpenCoverage={openCoverage} />
      </>
    )
  } else if (view.section === "coverage") {
    body = <BackupsCoverage resource={coverageRes} ctx={ctx} status={view.coverage} query={view.query.q} onOpenWorkspace={openWorkspace} />
  } else if (view.section === "history") {
    const chips = [
      slice && { key: "slice", label: `${new Date(slice.fromMs).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })} – ${new Date(slice.toMs).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}`, clear: () => setSlice(null) },
    ].filter(Boolean) as { key: string; label: string; clear: () => void }[]
    body = (
      <>
        <div className="flex flex-wrap items-center gap-1.5" data-slot="runs-filters">
          <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Result">
            {RUN_FACETS.map((f) => (
              <button key={f.key} type="button" aria-pressed={view.status === f.key} onClick={() => update({ status: f.key })} data-result={f.key}
                className={cn("inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-label transition-colors coarse:h-[44px]",
                  view.status === f.key ? "border-primary/40 bg-primary/10 text-primary-hover" : "border-border text-muted-foreground hover:text-foreground")}>
                {f.label}<span className="tabular-nums text-muted-foreground">{countWith({}, f.key)}</span>
              </button>
            ))}
          </div>
          <span className="flex flex-wrap items-center gap-1.5 sm:ml-auto">
            <FilterMenu label="Kind" value={view.query.kind} onChange={(v) => setQuery({ kind: v })}
              options={[{ key: "" as RunQuery["kind"], label: "Any kind", count: countWith({ kind: "" }) }, ...KINDS.map((k) => ({ key: k.key, label: k.label, count: countWith({ kind: k.key }) }))]} />
            <FilterMenu label="Proof" value={view.query.proof} onChange={(v) => setQuery({ proof: v })}
              options={[{ key: "" as RunQuery["proof"], label: "Any proof", count: countWith({ proof: "" }) }, ...PROOFS.map((k) => ({ key: k.key, label: k.label, count: countWith({ proof: k.key }) }))]} />
            <FilterMenu label="Plan" value={view.query.plan ?? ""} onChange={(v) => setQuery({ plan: v })} any="All plans"
              options={[{ key: "", label: "All plans", count: countWith({ plan: "" }) }, ...planNames.map((n) => ({ key: n, label: n, count: countWith({ plan: n }) }))]} />
          </span>
          {chips.map((c) => (
            <span key={c.key} className="inline-flex h-7 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-label text-primary-hover">
              {c.label}
              <button type="button" aria-label={`Remove filter ${c.label}`} onClick={c.clear} className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20 relative coarse:after:absolute coarse:after:-inset-4 coarse:after:content-['']"><X className="h-3 w-3" /></button>
            </span>
          ))}
          {(filterCount > 0 || slice) && (
            <button type="button" className="px-1.5 text-label text-muted-foreground hover:text-foreground"
              onClick={() => { setSlice(null); update({ status: "all", query: { ...view.query, kind: "", proof: "", plan: "" } }) }}>Clear filters</button>
          )}
        </div>
        {/* The Crew Journal's chart: runs stacked by result across the range;
            a click picks a bar, a drag a span, and that narrows the table. */}
        <div className="overflow-hidden rounded-card border border-border bg-card [&>div]:border-b-0 [&>div]:bg-transparent [&>div]:px-4 [&>div]:py-3" data-slot="runs-histogram">
          <LogsHistogram entries={chartEntries} timeRange={range} selected={slice} onSelect={setSlice} height={72} title="Runs · by result" />
        </div>
        <BackupsHistory ctx={ctx} resource={runs} />
      </>
    )
  } else if (view.section === "recovery") {
    body = (
      <>
        <SettingsSegmented label="Restore" value={view.recovery} onChange={(v) => update({ recovery: v })}
          options={[{ value: "new", label: "New restore" }, { value: "history", label: "History" }, { value: "drills", label: "Drills" }]} />
        <BackupsRecovery ctx={ctx} />
      </>
    )
  } else if (view.section === "schedules") {
    body = <BackupsSchedules ctx={ctx} />
  } else if (view.section === "storage") {
    body = <BackupsStorage ctx={ctx} />
  } else {
    body = <BackupsKeys ctx={ctx} />
  }

  const wide = !ws && (view.section === "history" || view.section === "recovery" || view.section === "overview" || view.section === "coverage")
  const scopeText = scopeLabel(scope, selected, workspaces)
  const description = ws ? undefined
    : view.section === "overview" && overview.data ? overviewSentence(overview.data)
    : onHistory ? `${scopeText} · ${inRange} ${inRange === 1 ? "run" : "runs"} ${PERIOD_WORDS[view.query.period]}`
    : view.section === "coverage" ? "Which workspaces a backup protects, how fresh it is, and how far it was checked. Least protected first."
    : "What is kept, where, and whether a restore really works"

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      panelId="drill-backups"
      title="Backups"
      icon={Database}
      description={description}
      toolbar={toolbar}
      facets={panelFacets}
      filterCount={filterCount}
      nav={nav}
      mobileNav={mobileNav}
      actions={
        <>
          {onHistory && (
            <TimeRangePicker value={range} onChange={(r) => { setSlice(null); setQuery({ period: RANGE_PERIOD[r] ?? "" }) }}
              presets={["24h", "7d", "30d", "90d", "all"]} triggerClassName="h-7" />
          )}
          {/* Icons only on a phone, so the actions fit the sub-bar. */}
          <Button size="sm" variant="outline" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="New plan" onClick={newPlan}><Plus /><span className="max-md:hidden">New plan</span></Button>
          <Button size="sm" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="Back up now"
            title={`Back up ${scopeText === "Whole instance" ? "the whole instance" : scopeText} now`}
            onClick={() => (setup.known && !setup.hasKey ? setSetupOpen(true) : setNowOpen(true))}><Database /><span className="max-md:hidden">Back up now</span></Button>
        </>
      }
    >
      <BackupsSetup open={setupOpen} onOpenChange={setSetupOpen} keys={recipients.data ?? []} plans={plans.data ?? []} demo={demo}
        onChanged={() => { recipients.reload?.(); plans.reload?.() }} onOpenRuns={() => go("history")} />
      <BackUpNowDialog open={nowOpen} onOpenChange={setNowOpen} ctx={{ scope, selected, workspaces, demo }} plans={plans.data ?? []} />
      <div data-slot="backups-page" data-section={ws ? "workspace" : view.section} data-mobile={isMobile || undefined} className={cn("mx-auto space-y-4 p-4 md:p-6", wide ? "max-w-5xl" : "max-w-3xl")}>
        {demo && <p className="text-xs text-muted-foreground">Demo data · nothing is sent</p>}
        {body}
      </div>
    </DrillPage>
  )
}
