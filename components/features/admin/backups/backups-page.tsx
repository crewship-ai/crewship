"use client"

import * as React from "react"
import {
  Shield, Database, Home, History, CalendarClock, Plus, RotateCcw, ListChecks, ShieldCheck, HardDrive, KeyRound, Globe, Users,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { WorkspaceScopeSection, type Scope } from "@/components/features/admin/workspace-scope"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"
import { BACKUP_SECTIONS, initialBackupsSection, type BackupsSection } from "@/app/(dashboard)/admin/navigation"
import { useBackupsScope, type RunFilter, type RunQuery, type SectionCtx } from "./backups-console"
import { INSTANCE_ONLY, describePlanWhen } from "./backups-model"
import { useBackupRuns } from "./use-backup-runs"
import { useBackupPlans } from "./use-backup-plans"
import { useBackupsOverview } from "./use-backups-overview"
import { BackupsOverview } from "./backups-overview"
import { BackupsHistory, matches, matchesQuery, runsInScope } from "./backups-history"
import { BackupsSchedules } from "./backups-schedules"
import { BackupsStorage } from "./backups-storage"
import { BackupsRecovery } from "./backups-recovery"
import { BackupsKeys } from "./backups-keys"

/**
 * Admin › Backups: a nested page like Security and People.
 *
 * The side panel holds what used to be spread over six Admin rows and a scope
 * strip — the scope (whole instance or selected workspaces), Overview, the
 * Runs facets, the plans as rows, Recovery and the two instance settings. The
 * content shows one section; section, facet, plan, run and scope live in the
 * URL (?section=&status=&plan=&run=&scope=&ws=), so a link shows what its
 * sender saw. Data retention is not a backup setting and stays in the Admin
 * console.
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
  { key: "1", label: "Last 24 hours" }, { key: "7", label: "Last 7 days" }, { key: "30", label: "Last 30 days" },
]
const oneOf = <T extends string>(v: string | null, all: { key: T }[]): T | "" => (all.some((o) => o.key === v) ? (v as T) : "")

function readView(search: string): View {
  const p = new URLSearchParams(search)
  const status = p.get("status")
  const rec = p.get("view")
  return {
    section: initialBackupsSection(search),
    status: isRunFilter(status) ? status : "all",
    plan: p.get("plan"),
    recovery: rec === "history" || rec === "drills" ? rec : "new",
    query: { q: p.get("q") ?? "", kind: oneOf(p.get("kind"), KINDS), proof: oneOf(p.get("proof"), PROOFS), period: oneOf(p.get("period"), PERIODS) },
  }
}

export function BackupsPage() {
  const { workspaceId, demo, workspaces, urlState, selected, scope, commit, backUpNow } = useBackupsScope()
  const [view, setView] = React.useState<View>(() => readView(typeof window === "undefined" ? "" : window.location.search))
  const [focus, setFocus] = React.useState<{ run: string | null; path: string | null }>(() => ({
    run: typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("run"),
    path: null,
  }))
  const [newPlanSignal, setNewPlanSignal] = React.useState(0)

  const update = React.useCallback((next: Partial<View>) => {
    setView((cur) => {
      const v = { ...cur, ...next }
      const url = new URL(window.location.href)
      const set = (k: string, val: string | null) => (val ? url.searchParams.set(k, val) : url.searchParams.delete(k))
      set("section", v.section === "overview" ? null : v.section)
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

  const runs = useBackupRuns(scope, selected, workspaces)
  const plans = useBackupPlans()
  const overview = useBackupsOverview(scope, selected, workspaces)
  // The same scope and facet rules Backup history filters with, so a count
  // here is the number of rows the facet opens.
  const all = runsInScope(runs.data ?? [], { scope, selected }, runs.source === "legacy").filter((r) => matchesQuery(r, view.query))
  const facetCount = Object.fromEntries(RUN_FACETS.map((f) => [f.key, all.filter((r) => matches(r, f.key)).length])) as Record<RunFilter, number>
  const attention = overview.data?.needs_attention?.length ?? 0

  const ctx: SectionCtx = {
    scope, selected, workspaces, currentWorkspaceId: workspaceId, demo,
    focusRun: focus.run, focusPath: focus.path, backUpNow, newPlanSignal,
    focusPlan: view.plan, runFilter: view.status, inDrill: true, recoveryView: view.recovery, runQuery: view.query,
    go: (section, opts) => {
      setFocus({ run: opts?.run ?? null, path: opts?.path ?? null })
      update({ section })
    },
  }

  const newPlan = () => {
    setNewPlanSignal((x) => x + 1)
    update({ section: "schedules", plan: "new" })
  }

  const setQuery = (next: Partial<RunQuery>) => update({ query: { ...view.query, ...next } })
  const needle = view.query.q.trim().toLowerCase()
  const shownPlans = (plans.data ?? []).filter((p) => !needle || p.name.toLowerCase().includes(needle))
  const filterCount = [view.query.kind, view.query.proof, view.query.period].filter(Boolean).length

  // The panel's toolbar narrows runs (and the plans list, by name) on top of
  // the Runs facets: what was kept, how far it is proven, and when.
  const toolbar = (
    <>
      <SidebarSearch value={view.query.q} onValueChange={(q) => setQuery({ q })} placeholder="Search runs, plans…" />
      <SidebarFilterPopover label="Filter runs" activeCount={filterCount} onClear={() => setQuery({ kind: "", proof: "", period: "" })} panelClassName="min-w-[240px]">
        <SidebarFacet label="Kind" resetLabel="Any kind" resetActive={!view.query.kind} onReset={() => setQuery({ kind: "" })} first>
          {KINDS.map((k) => (
            <SidebarFacetOption key={k.key} active={view.query.kind === k.key} onToggle={() => setQuery({ kind: view.query.kind === k.key ? "" : k.key })}>{k.label}</SidebarFacetOption>
          ))}
        </SidebarFacet>
        <SidebarFacet label="Proof" resetLabel="Any proof" resetActive={!view.query.proof} onReset={() => setQuery({ proof: "" })}>
          {PROOFS.map((k) => (
            <SidebarFacetOption key={k.key} active={view.query.proof === k.key} onToggle={() => setQuery({ proof: view.query.proof === k.key ? "" : k.key })}>{k.label}</SidebarFacetOption>
          ))}
        </SidebarFacet>
        <SidebarFacet label="When" resetLabel="Any time" resetActive={!view.query.period} onReset={() => setQuery({ period: "" })}>
          {PERIODS.map((k) => (
            <SidebarFacetOption key={k.key} active={view.query.period === k.key} onToggle={() => setQuery({ period: view.query.period === k.key ? "" : k.key })}>{k.label}</SidebarFacetOption>
          ))}
        </SidebarFacet>
      </SidebarFilterPopover>
    </>
  )

  const instanceOnly = INSTANCE_ONLY.has(view.section)
  const wsScope: Scope = { all: urlState.ws === null, ids: selected }
  const inert = instanceOnly ? "Instance setting · applies to every backup plan" : scope === "instance" ? "The whole instance is picked above" : undefined

  const nav = (
    <>
      <div data-slot="backups-scope">
        <DrillNavSection label="Scope" collapsible={false}>
          <div className={cn(instanceOnly && "pointer-events-none opacity-45")} aria-disabled={instanceOnly ? true : undefined}>
            <DrillNavItem pressed={scope === "instance"} selected={scope === "instance"} onSelect={() => commit("instance", selected)}
              icon={<Globe className="h-3.5 w-3.5" />} label="Whole instance" sub="Workspaces, users, instance settings" />
            <DrillNavItem pressed={scope === "workspaces"} selected={scope === "workspaces"} onSelect={() => commit("workspaces", selected)}
              icon={<Users className="h-3.5 w-3.5" />} label="Selected workspaces" meta={`${selected.size}/${workspaces.length}`} />
          </div>
        </DrillNavSection>
        <WorkspaceScopeSection workspaces={workspaces} scope={wsScope} currentId={workspaceId} inert={inert}
          onChange={(next) => commit("workspaces", next.all ? new Set(workspaces.map((w) => w.id)) : next.ids)} />
      </div>
      <DrillNavSection label="Status" collapsible={false}>
        <DrillNavItem selected={view.section === "overview"} onSelect={() => update({ section: "overview" })}
          icon={<Home className="h-3.5 w-3.5" />} label="Overview"
          meta={attention ? <span className="text-destructive">{attention}</span> : undefined} />
      </DrillNavSection>
      <DrillNavSection label="Runs" count={facetCount.all}>
        {RUN_FACETS.map((f, i) => (
          <DrillNavItem key={f.key} index={i} pressed={view.section === "history" && view.status === f.key} selected={view.section === "history" && view.status === f.key}
            muted={f.key !== "all" && facetCount[f.key] === 0}
            onSelect={() => update({ section: "history", status: f.key })}
            icon={<span className={cn("h-1.5 w-1.5 rounded-full", f.dot)} aria-hidden />} label={f.label} meta={facetCount[f.key]} />
        ))}
      </DrillNavSection>
      <DrillNavSection label="Plans" count={shownPlans.length}>
        {shownPlans.map((p, i) => (
          <DrillNavItem key={p.id} index={i} selected={view.section === "schedules" && view.plan === p.id} muted={!p.enabled}
            onSelect={() => update({ section: "schedules", plan: p.id })}
            icon={<CalendarClock className="h-3.5 w-3.5" />} label={p.name} sub={describePlanWhen(p)} meta={p.enabled ? "on" : "off"} />
        ))}
        <DrillNavItem selected={view.section === "schedules" && view.plan === "new"} onSelect={newPlan}
          icon={<Plus className="h-3.5 w-3.5" />} label={<span className="text-primary-hover">New plan</span>} />
      </DrillNavSection>
      <DrillNavSection label="Recovery">
        <DrillNavItem selected={view.section === "recovery" && view.recovery === "new"} onSelect={() => update({ section: "recovery", recovery: "new" })}
          icon={<RotateCcw className="h-3.5 w-3.5" />} label="New restore" />
        <DrillNavItem selected={view.section === "recovery" && view.recovery === "history"} onSelect={() => update({ section: "recovery", recovery: "history" })}
          icon={<ListChecks className="h-3.5 w-3.5" />} label="Restore history" />
        <DrillNavItem selected={view.section === "recovery" && view.recovery === "drills"} onSelect={() => update({ section: "recovery", recovery: "drills" })}
          icon={<ShieldCheck className="h-3.5 w-3.5" />} label="Drills" />
      </DrillNavSection>
      <DrillNavSection label="Settings" count={2}>
        <DrillNavItem selected={view.section === "storage"} onSelect={() => update({ section: "storage" })}
          icon={<HardDrive className="h-3.5 w-3.5" />} label="Storage" title="Instance setting · applies to every backup plan"
          meta={<span className="rounded bg-primary/10 px-1 text-[10px] text-primary-hover">Instance</span>} />
        <DrillNavItem selected={view.section === "keys"} onSelect={() => update({ section: "keys" })}
          icon={<KeyRound className="h-3.5 w-3.5" />} label="Keys & alerts" title="Instance setting · applies to every backup plan"
          meta={<span className="rounded bg-primary/10 px-1 text-[10px] text-primary-hover">Instance</span>} />
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <select aria-label="Section" value={view.section} onChange={(e) => update({ section: e.target.value as BackupsSection })}
      className="h-9 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control coarse:h-[2.75rem]">
      {BACKUP_SECTIONS.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
    </select>
  )

  const wide = view.section === "history" || view.section === "recovery"

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
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={newPlan}><Plus />New plan</Button>
          <Button size="sm" className="h-7 text-xs" onClick={() => backUpNow()}><History />Back up now</Button>
        </>
      }
    >
      <div data-slot="backups-page" data-section={view.section} className={cn("mx-auto space-y-4 p-4 md:p-6", wide ? "max-w-5xl" : "max-w-3xl")}>
        {demo && <p className="text-xs text-muted-foreground">Demo data · nothing is sent</p>}
        {view.section === "overview" && <BackupsOverview ctx={ctx} />}
        {view.section === "history" && <BackupsHistory ctx={ctx} />}
        {view.section === "schedules" && <BackupsSchedules ctx={ctx} />}
        {view.section === "storage" && <BackupsStorage ctx={ctx} />}
        {view.section === "recovery" && <BackupsRecovery ctx={ctx} />}
        {view.section === "keys" && <BackupsKeys ctx={ctx} />}
      </div>
    </DrillPage>
  )
}
