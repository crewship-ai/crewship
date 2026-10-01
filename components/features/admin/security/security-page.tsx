"use client"

import * as React from "react"
import { Bell, BookOpen, Brain, Building2, Clock, Eye, Gavel, Grid3x3, Home, KeyRound, ListChecks, RefreshCw, Shield, Sparkles, Timer, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { INSTANCE_SCOPE } from "@/lib/admin-api"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { KeeperJudgeCard } from "@/components/features/admin/keeper-judge-card"
import { KeeperProfileCard } from "@/components/features/admin/keeper-profile-card"
import { KeeperGovernancePanel } from "@/components/features/admin/keeper-governance-panel"
import { JudgeModelsCard } from "@/components/features/admin/judge-models-card"
import { WorkspaceScopeSection, readScope, writeScope, type Scope, type ScopeWorkspace } from "@/components/features/admin/workspace-scope"
import { SecurityOverview } from "./security-overview"
import { SecurityActivity } from "./security-activity"
import { useSecurity } from "./use-security"
import { useInstanceKeeper, type InstanceGovRow } from "./use-instance-keeper"
import { BulkGovernanceForm, DefaultsForm, type BulkSection } from "./bulk-governance"
import { WhatsOnWhere } from "./whats-on-where"
import { SCOPE_HINT, SCOPE_LABEL, SETTINGS, STREAMS, isSection, type DecisionFilter, type Section, type SettingsSection, type Stream } from "./security-model"

const STREAM_ICON: Record<Stream, LucideIcon> = {
  requests: KeyRound, skill_review: Sparkles, behavior: Eye, memory_health: Brain, negative_learning: BookOpen,
}
const SETTINGS_ICON: Record<SettingsSection, LucideIcon> = {
  judge: Gavel, rules: ListChecks, defaults: Sparkles, "workspace-judge": Building2, background: Clock, watchdog: Shield, alerts: Bell, leases: Timer,
}
const PANEL_SECTION: Record<BulkSection, "judge" | "watchdog" | "alerts" | "leases"> = {
  "workspace-judge": "judge", watchdog: "watchdog", alerts: "alerts", leases: "leases",
}
const isBulk = (s: Section): s is BulkSection => s === "workspace-judge" || s === "watchdog" || s === "alerts" || s === "leases"

interface UrlState { section: Section; stream: Stream | "all" }

/** ?section= and ?stream= keep the page shareable and reload-safe; the
 *  workspace ticks live in ?ws= (workspace-scope.tsx). */
function readUrl(): UrlState {
  if (typeof window === "undefined") return { section: "overview", stream: "all" }
  const p = new URLSearchParams(window.location.search)
  const s = p.get("section")
  const st = p.get("stream")
  return {
    section: isSection(s) ? s : "overview",
    stream: STREAMS.some((x) => x.key === st) ? (st as Stream) : "all",
  }
}
function writeUrl(s: UrlState) {
  const url = new URL(window.location.href)
  if (s.section === "overview") url.searchParams.delete("section"); else url.searchParams.set("section", s.section)
  if (s.section === "activity" && s.stream !== "all") url.searchParams.set("stream", s.stream); else url.searchParams.delete("stream")
  window.history.replaceState(window.history.state, "", url.toString())
}

/**
 * Admin › Security. One nested page for the Keeper of the whole instance:
 * "← Admin", its own panel, one thing at a time in the middle.
 *
 * Admin never depends on the workspace its user sits in. The panel's
 * Workspaces section starts on all of them: the activity, the overview and
 * "What's on where" cover what is ticked, and a per-workspace setting edits
 * the ticked workspace — or, with several ticked, overwrites all of them after
 * a dialog lists what changes where. Instance-wide settings grey the list out.
 *
 * The instance settings cards are the existing ones, unchanged.
 */
export function SecurityPage() {
  const { workspaceId, loading: workspaceLoading } = useWorkspace()
  const data = useSecurity(workspaceId, workspaceLoading)
  // The instance-wide cards need no workspace: an instance admin who belongs
  // to none uses them with INSTANCE_SCOPE; null still means "not known yet".
  const cardWs = workspaceId ?? (workspaceLoading ? null : INSTANCE_SCOPE)
  const [state, setState] = React.useState(readUrl)
  const { section, stream } = state
  const update = (next: Partial<UrlState>) => setState((prev) => {
    const merged = { ...prev, ...next }
    writeUrl(merged)
    return merged
  })

  const [scope, setScope] = React.useState<Scope | null>(null)
  const [decision, setDecision] = React.useState<DecisionFilter>("all")
  // Activity filters on the server, over the whole history (review R6). The
  // overview reads the log unfiltered.
  const filter = React.useMemo(() => {
    if (section !== "activity") return {}
    return {
      types: stream === "all" ? undefined : stream === "requests" ? ["access", "execute"] : [stream],
      decision: decision === "all" ? undefined : decision,
    }
  }, [section, stream, decision])
  // null asks for every workspace: "all" includes one created meanwhile.
  const inst = useInstanceKeeper(scope === null || scope.all ? null : [...scope.ids], data.liveTick, filter)

  const workspaces: ScopeWorkspace[] = React.useMemo(() => {
    const counts = new Map((inst.requests?.by_workspace ?? []).map((b) => [b.workspace_id, b.count]))
    return (inst.gov?.workspaces ?? []).map((w) => ({ id: w.workspace_id, name: w.workspace_name, slug: w.workspace_slug, count: counts.get(w.workspace_id) ?? 0 }))
  }, [inst.gov, inst.requests])

  // The ticks start from the URL once the list of workspaces is known.
  React.useEffect(() => {
    if (scope !== null || workspaces.length === 0) return
    setScope(readScope(workspaces))
  }, [workspaces, scope])
  const changeScope = (next: Scope) => {
    setScope(next)
    writeScope(workspaces, next)
  }
  const pickOne = (id: string) => changeScope({ all: false, ids: new Set([id]) })

  // Until the URL is read, the page covers every workspace, as it will then.
  // In "all" every workspace counts, including one that appeared since.
  const allTicked = scope === null || scope.all
  const sel = React.useMemo(() => allTicked ? new Set(workspaces.map((w) => w.id)) : scope!.ids, [allTicked, scope, workspaces])
  const entries = inst.requests?.items ?? []
  // Per kind from the server, over every workspace ticked and all history.
  const byType = inst.requests?.by_type ?? {}
  const counts: Record<Stream, number> = {
    requests: (byType.access ?? 0) + (byType.execute ?? 0) + (byType[""] ?? 0),
    skill_review: byType.skill_review ?? 0, behavior: byType.behavior ?? 0,
    memory_health: byType.memory_health ?? 0, negative_learning: byType.negative_learning ?? 0,
  }
  const activityTotal = Object.values(byType).reduce((a, b) => a + b, 0)
  const settings = SETTINGS.find((s) => s.key === section)
  const rows = (inst.gov?.workspaces ?? []).filter((w) => sel.has(w.workspace_id))

  // "All workspaces" always means the bulk path, which also sets the defaults
  // for new workspaces — with one workspace on the server as with many
  // (review R9). A workspace ticked on its own gets its full editor, even the
  // only one (contact, judge key and watch rules are per workspace).
  const bulkRows = allTicked ? rows.length > 0 : rows.length > 1
  const singleRow = !allTicked && rows.length === 1 ? rows[0] : null
  const instanceProp = React.useMemo(
    () => (singleRow ? { row: singleRow, onSaved: () => void inst.reloadGov() } : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the row object is the identity that matters
    [singleRow],
  )

  const scopeMode = isBulk(section) ? "edit" : "view"
  const scopeInert = settings?.scope === "instance" ? "Instance setting · applies to every workspace" : undefined

  const nav = (
    <>
      <WorkspaceScopeSection workspaces={workspaces} scope={scope ?? { all: true, ids: sel }} onChange={changeScope} currentId={workspaceId} mode={scopeMode} inert={scopeInert} />
      <DrillNavSection label="Status" collapsible={false}>
        <DrillNavItem selected={section === "overview"} onSelect={() => update({ section: "overview" })}
          icon={<Home className="h-3.5 w-3.5" />} label="Overview" />
        <DrillNavItem selected={section === "matrix"} onSelect={() => update({ section: "matrix" })}
          icon={<Grid3x3 className="h-3.5 w-3.5" />} label="What's on where" />
      </DrillNavSection>
      <DrillNavSection label="Activity" count={activityTotal}>
        {STREAMS.map((s, i) => {
          const Icon = STREAM_ICON[s.key]
          const on = section === "activity" && stream === s.key
          return (
            <DrillNavItem key={s.key} index={i} selected={on} muted={!counts[s.key]}
              onSelect={() => update({ section: "activity", stream: s.key })}
              icon={<Icon className="h-3.5 w-3.5" />} label={s.label} meta={counts[s.key]} />
          )
        })}
      </DrillNavSection>
      <DrillNavSection label="Settings" count={SETTINGS.length}>
        {SETTINGS.map((s, i) => {
          const Icon = SETTINGS_ICON[s.key]
          return (
            <DrillNavItem key={s.key} index={i} selected={section === s.key} onSelect={() => update({ section: s.key })}
              icon={<Icon className="h-3.5 w-3.5" />} label={s.label} title={SCOPE_HINT[s.scope]}
              meta={<span className={cn("rounded px-1 text-[10px]", s.scope === "instance" ? "bg-primary/10 text-primary-hover" : "bg-muted")}>{s.scope === "instance" ? "Instance" : "Per workspace"}</span>} />
          )
        })}
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <div className="flex flex-col gap-2">
      <select aria-label="Section" value={section === "activity" ? `activity:${stream}` : section}
        onChange={(e) => {
          const v = e.target.value
          if (v.startsWith("activity:")) update({ section: "activity", stream: v.slice(9) as Stream | "all" })
          else update({ section: v as Section })
        }}
        className="h-9 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control coarse:h-[2.75rem]">
        <option value="overview">Overview</option>
        <option value="matrix">What&apos;s on where</option>
        <option value="activity:all">Activity · all</option>
        {STREAMS.map((s) => <option key={s.key} value={`activity:${s.key}`}>Activity · {s.label}</option>)}
        {SETTINGS.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
      </select>
      <select aria-label="Workspaces" value={allTicked ? "all" : sel.size === 1 ? [...sel][0] : "some"}
        onChange={(e) => e.target.value === "all" ? changeScope({ all: true, ids: new Set(workspaces.map((w) => w.id)) }) : pickOne(e.target.value)}
        className="h-9 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control coarse:h-[2.75rem]">
        <option value="all">All workspaces</option>
        {!allTicked && sel.size > 1 && <option value="some">{sel.size} workspaces</option>}
        {workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
      </select>
    </div>
  )

  let body: React.ReactNode
  if (!inst.gov && inst.loading) {
    body = <div className="space-y-3"><Skeleton className="h-8 rounded-lg" /><Skeleton className="h-[280px] rounded-card" /></div>
  } else if (inst.govError && !inst.gov) {
    body = <p className="rounded-lg border border-border px-4 py-3 text-[13px] text-destructive">{inst.govError}</p>
  } else if (section === "overview") {
    body = <SecurityOverview status={data.status} posture={data.posture} postureError={data.postureError} entries={entries}
      workspaceId={workspaceId ?? ""} counts={inst.requests ? { total: inst.requests.total, ...inst.requests.counts } : undefined}
      health={inst.health.filter((h) => sel.has(h.workspace_id))} healthError={inst.healthError} activityError={inst.requestsError}
      selectedCount={sel.size}
      onOpenActivity={() => update({ section: "activity", stream: "all" })} />
  } else if (section === "matrix") {
    body = (
      <>
        <SettingsSummary slot="security-scope">
          <SummaryItem>Every per-workspace setting, one row per workspace. Open a cell to change it there.</SummaryItem>
        </SettingsSummary>
        <WhatsOnWhere rows={inst.gov?.workspaces ?? []} selected={sel}
          defaults={inst.gov ? ({ ...inst.gov.defaults, workspace_id: "defaults", workspace_name: "New workspaces", workspace_slug: "defaults" } as InstanceGovRow) : undefined}
          onOpenDefaults={() => update({ section: "defaults" })}
          onOpen={(s, id) => { pickOne(id); update({ section: s }) }} />
      </>
    )
  } else if (section === "activity") {
    body = sel.size === 0
      ? <p className="text-[13px] text-muted-foreground">Tick a workspace in the panel.</p>
      : <SecurityActivity entries={entries} stream={stream} onStream={(s) => update({ stream: s })} live={data.live} error={inst.requestsError}
          server={{
            decision, onDecision: setDecision, total: inst.requests?.total ?? 0, loading: inst.requestsLoading,
            counts: inst.requests?.counts ?? { allow: 0, deny: 0, escalate: 0, pending: 0 }, onLoadMore: () => void inst.loadMore(),
          }} />
  } else if (settings) {
    const bulk = isBulk(section)
    body = (
      <>
        <SettingsSummary slot="security-scope">
          <SummaryItem>
            <span className={cn("mr-2 rounded-full px-2 font-mono text-[10.5px]", settings.scope === "instance" ? "bg-primary/10 text-primary-hover" : "bg-muted text-muted-foreground")}>
              {SCOPE_LABEL[settings.scope]}
            </span>
            {bulk ? (singleRow ? `Editing ${singleRow.workspace_name}` : bulkRows ? (allTicked ? `All ${rows.length} existing workspace${rows.length === 1 ? "" : "s"}` : `${rows.length} workspaces selected`) : SCOPE_HINT[settings.scope]) : SCOPE_HINT[settings.scope]}
          </SummaryItem>
          <SummaryItem>{settings.about}</SummaryItem>
        </SettingsSummary>
        {section === "judge" && cardWs && <KeeperJudgeCard workspaceId={cardWs} />}
        {section === "rules" && cardWs && <KeeperProfileCard workspaceId={cardWs} />}
        {section === "background" && cardWs && <JudgeModelsCard workspaceId={cardWs} />}
        {section === "defaults" && inst.gov && <DefaultsForm current={inst.gov.defaults} onSaved={() => void inst.reloadGov()} />}
        {bulk && rows.length === 0 && <p className="text-[13px] text-muted-foreground">Tick one or more workspaces in the panel.</p>}
        {bulk && instanceProp && (
          <KeeperGovernancePanel key={instanceProp.row.workspace_id} workspaceId={instanceProp.row.workspace_id}
            serverEnabled={data.status?.enabled ?? false} section={PANEL_SECTION[section]} instance={instanceProp} />
        )}
        {bulk && bulkRows && (
          <BulkGovernanceForm section={section} rows={rows} all={allTicked} onSaved={() => void inst.reloadGov()} onEditOne={pickOne} />
        )}
      </>
    )
  }

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      title="Security"
      icon={Shield}
      description="Who may read a secret, and how every workspace is watched"
      nav={nav}
      mobileNav={mobileNav}
      actions={
        <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => { void data.reload(); void inst.reload() }} disabled={data.loading || inst.loading}>
          <RefreshCw className={cn((data.loading || inst.loading) && "animate-spin")} />Refresh
        </Button>
      }
    >
      <div className={cn("mx-auto space-y-4 p-4 md:p-6", section === "activity" || section === "matrix" ? "max-w-5xl" : "max-w-3xl")}>
        {body}
      </div>
    </DrillPage>
  )
}
