"use client"

import * as React from "react"
import { Shield, Database, History, CalendarClock, Plus, HardDrive, KeyRound, ChevronDown, ChevronRight, X, Check } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { SettingsSegmented, SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { LogsHistogram, type BucketRange } from "@/components/features/logs/logs-histogram"
import { TimeRangePicker, type TimeRange } from "@/components/features/logs/time-range-picker"
import type { JournalEntry } from "@/lib/types/journal"
import { SidebarSearch } from "@/components/layout/sidebar-kit"
import { initialBackupsSection, type BackupsSection } from "@/app/(dashboard)/admin/navigation"
import { useBackupsScope, type RunFilter, type RunQuery, type SectionCtx } from "./backups-console"
import { describePlanWhen, formatAgo } from "./backups-model"
import { useBackupRuns } from "./use-backup-runs"
import { useBackupPlans } from "./use-backup-plans"
import { useBackupsOverview } from "./use-backups-overview"
import { BackupsOverview } from "./backups-overview"
import { BackupsHistory, matches, matchesQuery, runsInScope } from "./backups-history"
import { BackupsSchedules } from "./backups-schedules"
import { BackupsStorage } from "./backups-storage"
import { BackupsRecovery } from "./backups-recovery"
import { BackupsKeys } from "./backups-keys"
import { WorkspaceBackups } from "./backups-workspace"

/**
 * Admin › Backups, built like People & workspaces and Security: "← Admin", a
 * panel with three views (Overview · Runs · Restore), the plans, every
 * workspace with its newest backup, and the two instance settings; the middle
 * shows one thing at a time.
 *
 * Overview is the whole instance. Runs is built like the Audit log: the time
 * range in the sub-bar, a chart of runs by result whose bars narrow the table,
 * and the filters (scope, result, kind, proof) above it; ticked runs are
 * pinned or downloaded from the selection bar. A workspace opens its own
 * backups. Section, filters, plan, run, scope and workspace live in the URL
 * (?section=&status=&kind=&proof=&period=&plan=&run=&scope=&ws=&workspace=).
 */

const RUN_FACETS: { key: RunFilter; label: string; dot: string }[] = [
  { key: "all", label: "All runs", dot: "bg-muted-foreground" },
  { key: "failed", label: "Failed", dot: "bg-destructive" },
  { key: "incomplete", label: "Incomplete", dot: "bg-warn" },
  { key: "manual", label: "Manual", dot: "bg-primary" },
  { key: "pinned", label: "Pinned", dot: "bg-purple" },
]

type RecoveryView = NonNullable<SectionCtx["recoveryView"]>

function isRunFilter(v: string | null): v is RunFilter {
  return RUN_FACETS.some((f) => f.key === v)
}

interface View {
  section: BackupsSection
  /** A workspace's own backups (slug), over the section. */
  workspace: string
  status: RunFilter
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
    plan: p.get("plan"),
    recovery: rec === "history" || rec === "drills" ? rec : "new",
    query: { q: p.get("q") ?? "", kind: oneOf(p.get("kind"), KINDS), proof: oneOf(p.get("proof"), PROOFS), period: oneOf(p.get("period"), PERIODS) },
  }
}


/** The Runs time range and the period filter it sets (days; "" = all time). */
const RANGE_PERIOD: Record<string, RunQuery["period"]> = { "24h": "1", "7d": "7", "30d": "30", "90d": "90", all: "" }
const PERIOD_RANGE: Record<string, TimeRange> = { "1": "24h", "7": "7d", "30": "30d", "90": "90d", "": "all" }

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
        <DropdownMenuLabel className="font-mono text-micro uppercase tracking-[0.08em] text-muted-foreground">{label}</DropdownMenuLabel>
        {options.map((o) => (
          <DropdownMenuItem key={o.key} onSelect={() => onChange(o.key)} className="gap-2 text-xs">
            <Check className={cn("h-3.5 w-3.5", o.key === value ? "opacity-100" : "opacity-0")} />
            <span className="flex-1">{o.label}</span>
            {o.count !== undefined && <span className="font-mono text-micro text-muted-foreground">{o.count}</span>}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function BackupsPage() {
  const { workspaceId, demo, workspaces, selected, scope, commit, backUpNow } = useBackupsScope()
  const [view, setView] = React.useState<View>(() => readView(typeof window === "undefined" ? "" : window.location.search))
  const [focus, setFocus] = React.useState<{ run: string | null; path: string | null }>(() => ({
    run: typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("run"),
    path: null,
  }))
  const [newPlanSignal, setNewPlanSignal] = React.useState(0)
  const [slice, setSlice] = React.useState<BucketRange | null>(null)

  const update = React.useCallback((next: Partial<View>) => {
    setView((cur) => {
      const v = { ...cur, ...next }
      const url = new URL(window.location.href)
      const set = (k: string, val: string | null) => (val ? url.searchParams.set(k, val) : url.searchParams.delete(k))
      set("section", v.section === "overview" ? null : v.section)
      set("workspace", v.workspace || null)
      set("status", v.section === "history" && v.status !== "all" ? v.status : null)
      set("plan", v.section === "schedules" ? v.plan : null)
      set("view", v.section === "recovery" && v.recovery !== "new" ? v.recovery : null)
      if (v.section !== "history") url.searchParams.delete("run")
      set("q", v.query.q.trim() || null)
      set("kind", v.query.kind || null)
      set("proof", v.query.proof || null)
      set("period", v.query.period || null)
      window.history.replaceState(window.history.state, "", url.toString())
      return v
    })
  }, [])
  const go = (section: BackupsSection, extra: Partial<View> = {}) => update({ section, workspace: "", ...extra })

  const runs = useBackupRuns(scope, selected, workspaces)
  const plans = useBackupPlans()
  const overview = useBackupsOverview("instance", selected, workspaces)
  // Each workspace's newest backup, for the panel and the Overview's table:
  // the server lists it for a workspaces scope, so ask for all of them.
  const everyId = React.useMemo(() => new Set(workspaces.map((w) => w.id)), [workspaces])
  const coverageRes = useBackupsOverview("workspaces", everyId, workspaces)
  const coverage = coverageRes.data?.workspaces ?? []
  const attention = overview.data?.needs_attention?.length ?? 0

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
  // Overview is the whole instance, whatever scope the Runs filter holds.
  const instanceCtx: SectionCtx = { ...ctx, scope: "instance" }

  const newPlan = () => {
    setNewPlanSignal((x) => x + 1)
    go("schedules", { plan: "new" })
  }
  const openWorkspace = (id: string) => {
    const w = workspaces.find((x) => x.id === id)
    if (w) update({ workspace: w.slug })
  }
  const ws = workspaces.find((w) => w.slug === view.workspace || w.id === view.workspace)

  const setQuery = (next: Partial<RunQuery>) => update({ query: { ...view.query, ...next } })
  const needle = view.query.q.trim().toLowerCase()
  const shownPlans = (plans.data ?? []).filter((p) => !needle || onHistory || p.name.toLowerCase().includes(needle))
  const shownWorkspaces = workspaces.filter((w) => !needle || onHistory || w.name.toLowerCase().includes(needle) || w.slug.includes(needle))
  const filterCount = onHistory ? [view.status !== "all", view.query.kind, view.query.proof].filter(Boolean).length : 0

  // What each Runs filter would show, the others held: the count beside an
  // option is the number of rows picking it opens.
  const scoped = runsInScope(runs.data ?? [], { scope, selected }, runs.source === "legacy")
  const countWith = (q: Partial<RunQuery>, status: RunFilter = view.status) =>
    scoped.filter((r) => matches(r, status) && matchesQuery(r, { ...view.query, ...q })).length
  const facetCount = { all: countWith({}, "all") }

  // The chart: every run of the range under the other filters, by result.
  const chartEntries = React.useMemo(() => scoped
    .filter((r) => matches(r, view.status) && matchesQuery(r, { ...view.query, period: "" }))
    .map((r) => ({ ts: r.started_at, severity: r.status === "failed" ? "error" : r.status === "incomplete" || r.incomplete.length > 0 ? "warn" : "info" }) as unknown as JournalEntry),
  // eslint-disable-next-line react-hooks/exhaustive-deps -- scoped is derived from these
  [runs.data, scope, selected, view.status, view.query])
  const range: TimeRange = PERIOD_RANGE[view.query.period] ?? "all"

  // Runs' scope filter: the whole instance, every workspace, or one.
  const scopeValue = scope === "instance" ? "instance" : selected.size === workspaces.length ? "all" : selected.size === 1 ? [...selected][0] : "some"
  const scopeOptions = [
    { key: "instance", label: "Whole instance" },
    { key: "all", label: "All workspaces" },
    ...(scopeValue === "some" ? [{ key: "some", label: `${selected.size} workspaces` }] : []),
    ...workspaces.map((w) => ({ key: w.id, label: w.name })),
  ]
  const setScope = (v: string) => {
    if (v === "instance") commit("instance", selected)
    else if (v === "all") commit("workspaces", new Set(workspaces.map((w) => w.id)))
    else if (v !== "some") commit("workspaces", new Set([v]))
  }
  const scopeMenu = <FilterMenu label="Scope" value={scopeValue} options={scopeOptions} onChange={setScope} />

  const segView = ws ? null : view.section === "history" ? "runs" : view.section === "recovery" ? "restore" : view.section === "overview" ? "overview" : null
  const VIEWS = [
    { key: "overview", label: "Overview", section: "overview" as const },
    { key: "runs", label: "Runs", section: "history" as const },
    { key: "restore", label: "Restore", section: "recovery" as const },
  ]
  const workspaceDot = (id: string) => {
    const c = coverage.find((x) => x.workspace_id === id)
    return c?.status === "ok" ? "bg-success" : c?.status === "warn" ? "bg-warn" : "bg-destructive"
  }
  const workspaceSub = (id: string) => {
    const c = coverage.find((x) => x.workspace_id === id)
    if (!c) return coverageRes.status === "loading" ? "…" : "No backup yet"
    return c.last_backup_at ? `Backed up ${formatAgo(c.last_backup_at)}` : "Never backed up"
  }

  const toolbar = (
    <SidebarSearch value={view.query.q} onValueChange={(q) => setQuery({ q })} placeholder={onHistory ? "Search runs…" : "Search plans, workspaces…"} />
  )

  const nav = (
    <>
      <div className="mx-2 mt-1 grid grid-cols-3 gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label="View">
        {VIEWS.map((v) => (
          <button key={v.key} type="button" aria-pressed={segView === v.key} onClick={() => go(v.section)} data-drill-close
            className={cn("flex h-7 min-w-0 items-center justify-center rounded-md px-1 text-label font-medium transition-colors",
              segView === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
            <span className="truncate">{v.label}</span>
            {v.key === "overview" && attention > 0 && <span className="ml-1 font-mono text-micro text-destructive">{attention}</span>}
          </button>
        ))}
      </div>
      <DrillNavSection label="Plans" count={shownPlans.length}>
        {shownPlans.map((p, i) => (
          <DrillNavItem key={p.id} index={i} selected={!ws && view.section === "schedules" && view.plan === p.id} muted={!p.enabled}
            onSelect={() => go("schedules", { plan: p.id })}
            icon={<CalendarClock className="h-3.5 w-3.5" />} label={p.name} sub={describePlanWhen(p)} meta={p.enabled ? "on" : "off"} />
        ))}
        <DrillNavItem index={shownPlans.length} selected={!ws && view.section === "schedules" && view.plan === "new"} onSelect={newPlan}
          icon={<Plus className="h-3.5 w-3.5 text-primary-hover" />} label={<span className="text-primary-hover">New plan</span>} />
      </DrillNavSection>
      <DrillNavSection label="Workspaces" count={shownWorkspaces.length}>
        {shownWorkspaces.map((w, i) => (
          <DrillNavItem key={w.id} index={i} selected={ws?.id === w.id} onSelect={() => openWorkspace(w.id)} title={`${w.name} · ${w.slug}`}
            icon={<WorkspaceTile id={w.id} name={w.name} logoUrl={w.logoUrl} />} label={w.name} sub={workspaceSub(w.id)}
            meta={<span className={cn("inline-block h-1.5 w-1.5 rounded-full", workspaceDot(w.id))} aria-hidden />} />
        ))}
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
    <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-surface-subtle p-0.5" role="tablist" aria-label="View">
      {VIEWS.map((v) => (
        <button key={v.key} type="button" role="tab" aria-selected={segView === v.key} onClick={() => go(v.section)}
          className={cn("flex h-8 items-center justify-center rounded-md text-xs font-medium", segView === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground")}>
          {v.label}
        </button>
      ))}
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
        <WorkspaceBackups ws={ws} coverage={coverage.find((c) => c.workspace_id === ws.id)} ctx={ctx}
          onOpenRuns={(run) => {
            commit("workspaces", new Set([ws.id]))
            setFocus({ run: run ?? null, path: null })
            go("history")
          }} />
      </>
    )
  } else if (view.section === "overview") {
    body = <BackupsOverview ctx={instanceCtx} coverage={coverage} onOpenWorkspace={openWorkspace} />
  } else if (view.section === "history") {
    const chips = [
      slice && { key: "slice", label: `${new Date(slice.fromMs).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })} – ${new Date(slice.toMs).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}`, clear: () => setSlice(null) },
    ].filter(Boolean) as { key: string; label: string; clear: () => void }[]
    body = (
      <>
        <div className="flex flex-wrap items-center gap-1.5" data-slot="runs-filters">
          {scopeMenu}
          <FilterMenu label="Result" value={view.status} onChange={(v) => update({ status: v })} any="All runs"
            options={RUN_FACETS.map((f) => ({ key: f.key, label: f.label, count: countWith({}, f.key) }))} />
          <FilterMenu label="Kind" value={view.query.kind} onChange={(v) => setQuery({ kind: v })}
            options={[{ key: "" as RunQuery["kind"], label: "Any kind", count: countWith({ kind: "" }) }, ...KINDS.map((k) => ({ key: k.key, label: k.label, count: countWith({ kind: k.key }) }))]} />
          <FilterMenu label="Proof" value={view.query.proof} onChange={(v) => setQuery({ proof: v })}
            options={[{ key: "" as RunQuery["proof"], label: "Any proof", count: countWith({ proof: "" }) }, ...PROOFS.map((k) => ({ key: k.key, label: k.label, count: countWith({ proof: k.key }) }))]} />
          {chips.map((c) => (
            <span key={c.key} className="inline-flex h-7 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-label text-primary-hover">
              {c.label}
              <button type="button" aria-label={`Remove filter ${c.label}`} onClick={c.clear} className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20"><X className="h-3 w-3" /></button>
            </span>
          ))}
          {(filterCount > 0 || slice) && (
            <button type="button" className="px-1.5 text-label text-muted-foreground hover:text-foreground"
              onClick={() => { setSlice(null); update({ status: "all", query: { ...view.query, kind: "", proof: "" } }) }}>Clear filters</button>
          )}
        </div>
        {/* The Crew Journal's chart: runs stacked by result across the range;
            a click picks a bar, a drag a span, and that narrows the table. */}
        <div className="overflow-hidden rounded-card border border-border bg-card [&>div]:border-b-0 [&>div]:bg-transparent [&>div]:px-4 [&>div]:py-3" data-slot="runs-histogram">
          <LogsHistogram entries={chartEntries} timeRange={range} selected={slice} onSelect={setSlice} height={72} />
        </div>
        <BackupsHistory ctx={ctx} />
      </>
    )
  } else if (view.section === "recovery") {
    body = (
      <>
        <div className="flex flex-wrap items-center gap-2">
          <SettingsSegmented label="Restore" value={view.recovery} onChange={(v) => update({ recovery: v })}
            options={[{ value: "new", label: "New restore" }, { value: "history", label: "History" }, { value: "drills", label: "Drills" }]} />
          {view.recovery === "new" && scopeMenu}
        </div>
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

  const wide = !ws && (view.section === "history" || view.section === "recovery")

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      title="Backups"
      icon={Database}
      description="What is kept, where, and whether a restore really works"
      toolbar={toolbar}
      filterCount={filterCount}
      nav={nav}
      mobileNav={mobileNav}
      actions={
        <>
          {onHistory && (
            <TimeRangePicker value={range} onChange={(r) => { setSlice(null); setQuery({ period: RANGE_PERIOD[r] ?? "" }) }}
              presets={["24h", "7d", "30d", "90d", "all"]} triggerClassName="h-7" />
          )}
          {/* Icons only on a phone, so the sub-bar keeps room for Filters. */}
          <Button size="sm" variant="outline" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="New plan" onClick={newPlan}><Plus /><span className="max-md:hidden">New plan</span></Button>
          <Button size="sm" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="Back up now" onClick={() => backUpNow()}><History /><span className="max-md:hidden">Back up now</span></Button>
        </>
      }
    >
      <div data-slot="backups-page" data-section={ws ? "workspace" : view.section} className={cn("mx-auto space-y-4 p-4 md:p-6", wide ? "max-w-5xl" : "max-w-3xl")}>
        {demo && <p className="text-xs text-muted-foreground">Demo data · nothing is sent</p>}
        {onHistory && (
          <SettingsSummary slot="runs-scope">
            <SummaryItem>{scope === "instance" ? "Whole-instance runs" : selected.size === workspaces.length ? "Every workspace's runs" : `${selected.size === 1 ? workspaces.find((w) => selected.has(w.id))?.name : `${selected.size} workspaces`}`}</SummaryItem>
            <SummaryItem n={facetCount.all}>{facetCount.all === 1 ? "run" : "runs"} in range</SummaryItem>
          </SettingsSummary>
        )}
        {body}
      </div>
    </DrillPage>
  )
}
