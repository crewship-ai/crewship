"use client"

import * as React from "react"
import {
  Activity as ActivityIcon, BookOpen, Brain, Building2, ChevronDown, ChevronRight, Clock, Eye, Gavel, Home, KeyRound, ListChecks,
  RefreshCw, Shield, Sparkles, X, type LucideIcon,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { INSTANCE_SCOPE } from "@/lib/admin-api"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarSearch } from "@/components/layout/sidebar-kit"
import {
  SettingsCard, SettingsSummary, SummaryItem, nativeSelect, selectionBar, selectionBarAction, selectionBarButton,
} from "@/components/features/settings/shared"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { KeeperJudgeCard } from "@/components/features/admin/keeper-judge-card"
import { KeeperProfileCard } from "@/components/features/admin/keeper-profile-card"
import { KeeperGovernancePanel } from "@/components/features/admin/keeper-governance-panel"
import { JudgeModelsCard } from "@/components/features/admin/judge-models-card"
import { SecurityOverview } from "./security-overview"
import { SecurityActivity } from "./security-activity"
import { useSecurity } from "./use-security"
import { useInstanceKeeper, type InstanceGovRow } from "./use-instance-keeper"
import { BulkGovernanceForm, DefaultsForm, type BulkSection } from "./bulk-governance"
import { WhatsOnWhere, judgeLabel, leaseLabel, watchSummary } from "./whats-on-where"
import { SETTINGS, STREAMS, type DecisionFilter, type Stream } from "./security-model"

const STREAM_ICON: Record<Stream, LucideIcon> = {
  requests: KeyRound, skill_review: Sparkles, behavior: Eye, memory_health: Brain, negative_learning: BookOpen,
}

/** What reaches every workspace on the server: one editor each, in the panel. */
type InstanceItem = "judge" | "rules" | "background" | "defaults"
const INSTANCE_ITEMS: { key: InstanceItem; label: string; hint: string; icon: LucideIcon }[] = [
  { key: "judge", label: "Credential judge", hint: "Allows, denies or escalates a secret", icon: Gavel },
  { key: "rules", label: "Decision rules", hint: "When a person confirms", icon: ListChecks },
  { key: "background", label: "Background checks", hint: "Models behind the reviews", icon: Clock },
  { key: "defaults", label: "New workspaces", hint: "What a new one starts with", icon: Sparkles },
]
const ABOUT: Record<InstanceItem, string> = Object.fromEntries(
  SETTINGS.filter((s) => s.scope === "instance").map((s) => [s.key, s.about]),
) as Record<InstanceItem, string>

type View = "overview" | "workspaces" | "activity"
const VIEWS: { key: View; label: string; icon: LucideIcon }[] = [
  { key: "overview", label: "Overview", icon: Home },
  { key: "workspaces", label: "Workspaces", icon: Building2 },
  { key: "activity", label: "Activity", icon: ActivityIcon },
]

/** The selection bar's buttons: each opens the form for that part of the watch. */
const BULK: { key: BulkSection; label: string; title: string }[] = [
  { key: "watchdog", label: "Watchdog", title: "Watchdog" },
  { key: "alerts", label: "Alerts", title: "Alerts & approvals" },
  { key: "leases", label: "Lease", title: "Credential leases" },
  { key: "workspace-judge", label: "Judge", title: "Judge" },
]

interface UrlState {
  view: View
  /** An instance setting open in the middle. */
  item: InstanceItem | null
  /** A workspace's profile open in the middle (slug). */
  ws: string
  stream: Stream | "all"
  /** Activity of one workspace only (slug); "" is every workspace. */
  in: string
  q: string
}

const INSTANCE_KEYS = new Set<string>(INSTANCE_ITEMS.map((i) => i.key))
/** Sections the page had before it had views; links to them still land. */
const PER_WORKSPACE = new Set(["matrix", "watchdog", "alerts", "leases", "workspace-judge"])

/** ?view= ?section= ?ws= ?stream= ?in= ?q= keep the page shareable and reload-safe. */
function readUrl(): UrlState {
  const empty: UrlState = { view: "overview", item: null, ws: "", stream: "all", in: "", q: "" }
  if (typeof window === "undefined") return empty
  const p = new URLSearchParams(window.location.search)
  const v = p.get("view")
  const s = p.get("section") ?? ""
  const st = p.get("stream")
  const ws = p.get("ws") ?? ""
  return {
    view: v === "workspaces" || v === "activity" ? v : s === "activity" ? "activity" : PER_WORKSPACE.has(s) ? "workspaces" : "overview",
    item: INSTANCE_KEYS.has(s) ? (s as InstanceItem) : null,
    // One workspace opens its profile; an old list of ticked ones opens none.
    ws: ws.includes(",") || ws === "none" ? "" : ws,
    stream: STREAMS.some((x) => x.key === st) ? (st as Stream) : "all",
    in: p.get("in") ?? "",
    q: p.get("q") ?? "",
  }
}
function writeUrl(s: UrlState) {
  const url = new URL(window.location.href)
  const set = (k: string, v: string, keep: boolean) => (keep ? url.searchParams.set(k, v) : url.searchParams.delete(k))
  set("view", s.view, s.view !== "overview")
  set("section", s.item ?? "", Boolean(s.item))
  set("ws", s.ws, Boolean(s.ws))
  set("stream", s.stream, s.view === "activity" && s.stream !== "all")
  set("in", s.in, s.view === "activity" && Boolean(s.in))
  set("q", s.q, Boolean(s.q))
  window.history.replaceState(window.history.state, "", url.toString())
}

/**
 * Admin › Security, built like Admin › People & workspaces: "← Admin", a
 * panel with three views (Overview · Workspaces · Activity), the instance-wide
 * settings, the activity streams and every workspace; the middle shows one
 * thing at a time.
 *
 * What is set per workspace — watchdog, alerts, leases, its own judge — is
 * not a section per setting: the Workspaces view is one table of how each
 * workspace is watched, ticked rows change together from the selection bar,
 * and a row opens that workspace's profile with all of its settings. What is
 * set for the whole instance stays in the panel, one editor each.
 */
export function SecurityPage() {
  const { workspaceId, loading: workspaceLoading } = useWorkspace()
  const data = useSecurity(workspaceId, workspaceLoading)
  // The instance-wide cards need no workspace: an instance admin who belongs
  // to none uses them with INSTANCE_SCOPE; null still means "not known yet".
  const cardWs = workspaceId ?? (workspaceLoading ? null : INSTANCE_SCOPE)
  const [state, setState] = React.useState(readUrl)
  const { view, item, stream, q } = state
  const update = (next: Partial<UrlState>) => setState((prev) => {
    const merged = { ...prev, ...next }
    writeUrl(merged)
    return merged
  })
  const showView = (v: View) => update({ view: v, item: null, ws: "" })
  const openItem = (k: InstanceItem) => update({ item: k, ws: "" })

  const [decision, setDecision] = React.useState<DecisionFilter>("all")
  // Activity filters on the server, over the whole history (review R6). The
  // overview reads the log unfiltered.
  const filter = React.useMemo(() => {
    if (view !== "activity" || item || state.ws) return {}
    return {
      types: stream === "all" ? undefined : stream === "requests" ? ["access", "execute"] : [stream],
      decision: decision === "all" ? undefined : decision,
    }
  }, [view, item, state.ws, stream, decision])

  // The governance list names the workspaces; the activity of one is asked
  // for by id. Until the list is here, every workspace.
  const [govIds, setGovIds] = React.useState<Map<string, string>>(new Map())
  const activityWs = view === "activity" && state.in ? govIds.get(state.in) ?? null : null
  const inst = useInstanceKeeper(activityWs ? [activityWs] : null, data.liveTick, filter)
  const rows = React.useMemo(() => inst.gov?.workspaces ?? [], [inst.gov])
  React.useEffect(() => { setGovIds(new Map(rows.flatMap((r) => [[r.workspace_slug, r.workspace_id], [r.workspace_id, r.workspace_id]]))) }, [rows])

  const counts = React.useMemo(() => new Map((inst.requests?.by_workspace ?? []).map((b) => [b.workspace_id, b.count])), [inst.requests])
  const ws = rows.find((r) => r.workspace_slug === state.ws || r.workspace_id === state.ws)
  const openWs = (id: string) => {
    const r = rows.find((x) => x.workspace_id === id)
    if (r) update({ ws: r.workspace_slug, item: null })
  }
  const needle = q.trim().toLowerCase()
  const shown = rows.filter((r) => !needle || r.workspace_name.toLowerCase().includes(needle) || r.workspace_slug.includes(needle))
  const defaultsRow = inst.gov ? ({ ...inst.gov.defaults, workspace_id: "defaults", workspace_name: "New workspaces", workspace_slug: "defaults" } as InstanceGovRow) : undefined

  // Per kind from the server, over the workspaces asked for and all history.
  const byType = inst.requests?.by_type ?? {}
  const streamCount: Record<Stream, number> = {
    requests: (byType.access ?? 0) + (byType.execute ?? 0) + (byType[""] ?? 0),
    skill_review: byType.skill_review ?? 0, behavior: byType.behavior ?? 0,
    memory_health: byType.memory_health ?? 0, negative_learning: byType.negative_learning ?? 0,
  }
  const activityTotal = Object.values(byType).reduce((a, b) => a + b, 0)
  const entries = inst.requests?.items ?? []

  const [picked, setPicked] = React.useState<Set<string>>(() => new Set())
  const toggle = (ids: string[], on: boolean) => setPicked((prev) => { const n = new Set(prev); for (const id of ids) { if (on) n.add(id); else n.delete(id) } return n })
  const [bulk, setBulk] = React.useState<BulkSection | null>(null)
  const pickedRows = rows.filter((r) => picked.has(r.workspace_id))

  const watchOn = rows.filter((r) => r.enabled).length
  const ownJudge = rows.filter((r) => r.gov_model_provider).length
  const judgeSub = !data.status ? "…" : !data.status.enabled ? "Keeper off" : `${data.status.model || "no model"}${data.status.ollama_online ? "" : " · not answering"}`
  const middleWide = !item && !ws

  const nav = (
    <>
      <div className="mx-2 mt-1 grid grid-cols-3 gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label="View">
        {VIEWS.map((v) => {
          const on = view === v.key && middleWide
          return (
            <button key={v.key} type="button" aria-pressed={on} onClick={() => showView(v.key)} data-drill-close
              className={cn("flex h-7 min-w-0 items-center justify-center gap-1 rounded-md px-1 text-label font-medium transition-colors",
                on ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
              {/* Words only: three icons left "Workspaces" no room in the panel. */}
              <span className="truncate">{v.label}</span>
            </button>
          )
        })}
      </div>
      <DrillNavSection label="Instance settings" count={INSTANCE_ITEMS.length}>
        {INSTANCE_ITEMS.map((s, i) => (
          <DrillNavItem key={s.key} index={i} selected={item === s.key} onSelect={() => openItem(s.key)}
            icon={<s.icon className="h-3.5 w-3.5 text-muted-foreground" />} label={s.label}
            sub={s.key === "judge" ? judgeSub : s.hint} title="Applies to every workspace on this server" />
        ))}
      </DrillNavSection>
      <DrillNavSection label="Activity" count={activityTotal}>
        {STREAMS.map((s, i) => {
          const Icon = STREAM_ICON[s.key]
          const on = view === "activity" && stream === s.key && middleWide
          return (
            <DrillNavItem key={s.key} index={i} selected={on} muted={!streamCount[s.key]}
              onSelect={() => update({ view: "activity", stream: s.key, item: null, ws: "" })}
              icon={<Icon className="h-3.5 w-3.5" />} label={s.label} meta={streamCount[s.key]} />
          )
        })}
      </DrillNavSection>
      <DrillNavSection label="Workspaces" count={needle ? `${shown.length} of ${rows.length}` : rows.length}>
        {shown.map((r, i) => (
          <DrillNavItem key={r.workspace_id} index={i} selected={ws?.workspace_id === r.workspace_id} onSelect={() => openWs(r.workspace_id)}
            title={`${r.workspace_name} · ${r.workspace_slug}`}
            icon={<WorkspaceTile id={r.workspace_id} name={r.workspace_name} />} label={r.workspace_name}
            sub={watchSummary(r)}
            meta={<span className={cn("inline-block h-1.5 w-1.5 rounded-full", r.enabled ? "bg-success" : "bg-warn")} aria-label={r.enabled ? "Watchdog on" : "Watchdog off"} />} />
        ))}
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-surface-subtle p-0.5" role="tablist" aria-label="View">
      {VIEWS.map((v) => (
        <button key={v.key} type="button" role="tab" aria-selected={view === v.key && middleWide} onClick={() => showView(v.key)}
          className={cn("flex h-8 items-center justify-center gap-1.5 rounded-md text-xs font-medium", view === v.key && middleWide ? "bg-card text-foreground shadow-xs" : "text-muted-foreground")}>
          <v.icon className="h-3.5 w-3.5" />{v.label}
        </button>
      ))}
    </div>
  )

  const crumb = (back: View, backLabel: string, label: string) => (
    <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-label text-muted-foreground">
      <button type="button" className="hover:text-foreground" onClick={() => showView(back)}>{backLabel}</button>
      <ChevronRight className="size-3" /><span className="text-foreground">{label}</span>
    </nav>
  )

  let body: React.ReactNode
  if (!inst.gov && inst.loading) {
    body = <div className="space-y-3"><Skeleton className="h-8 rounded-lg" /><Skeleton className="h-[280px] rounded-card" /></div>
  } else if (inst.govError && !inst.gov) {
    body = <p className="rounded-lg border border-border px-4 py-3 text-control text-destructive">{inst.govError}</p>
  } else if (item) {
    const meta = INSTANCE_ITEMS.find((i) => i.key === item)!
    body = (
      <>
        {crumb("overview", "Instance settings", meta.label)}
        <SettingsSummary slot="security-scope">
          <SummaryItem><span className="mr-2 rounded-full bg-primary/10 px-2 font-mono text-micro text-primary-hover">Instance</span>
            {item === "defaults" ? "Copied into a workspace when it is created; existing workspaces do not change" : "Applies to every workspace on this server"}</SummaryItem>
          <SummaryItem>{ABOUT[item]}</SummaryItem>
        </SettingsSummary>
        {item === "judge" && cardWs && <KeeperJudgeCard workspaceId={cardWs} />}
        {item === "rules" && cardWs && <KeeperProfileCard workspaceId={cardWs} />}
        {item === "background" && cardWs && <JudgeModelsCard workspaceId={cardWs} />}
        {item === "defaults" && inst.gov && <DefaultsForm current={inst.gov.defaults} onSaved={() => void inst.reloadGov()} />}
      </>
    )
  } else if (ws) {
    body = (
      <>
        {crumb("workspaces", "Workspaces", ws.workspace_name)}
        <WorkspaceSecurityProfile row={ws} requests={counts.get(ws.workspace_id) ?? 0} serverEnabled={data.status?.enabled ?? false}
          onSaved={() => void inst.reloadGov()} />
      </>
    )
  } else if (view === "workspaces") {
    body = (
      <>
        <SettingsSummary slot="security-scope">
          <SummaryItem n={rows.length}>{rows.length === 1 ? "workspace" : "workspaces"}</SummaryItem>
          <SummaryItem n={watchOn} tone={watchOn ? "success" : undefined}>watchdog on</SummaryItem>
          <SummaryItem n={rows.length - watchOn} tone={rows.length - watchOn ? "warn" : undefined}>watchdog off</SummaryItem>
          <SummaryItem n={ownJudge}>with their own judge</SummaryItem>
        </SettingsSummary>
        <SettingsCard icon={Building2} title="Workspaces" description="How each one is watched · open one for all its settings, tick several to change them together">
          <WhatsOnWhere rows={shown} onOpen={openWs} defaults={needle ? undefined : defaultsRow} onOpenDefaults={() => openItem("defaults")}
            selected={picked} onSelect={toggle} />
        </SettingsCard>
        {pickedRows.length > 0 && (
          <div role="toolbar" aria-label="Change the selected workspaces" data-slot="security-bulk"
            className={cn(selectionBar, "fixed bottom-6 left-1/2 -translate-x-1/2 whitespace-nowrap max-sm:bottom-[calc(var(--mobile-tab-bar-h,0px)+0.75rem)] max-sm:gap-1.5 max-sm:px-2")}>
            <span className="px-1"><span className="font-mono font-semibold tabular-nums">{pickedRows.length}</span> selected</span>
            {BULK.map((b, i) => (
              <Button key={b.key} type="button" size="sm" variant={i === 0 ? "default" : "outline"} className={cn(i === 0 ? selectionBarAction : selectionBarButton, "max-sm:px-2")}
                onClick={() => setBulk(b.key)}>
                {b.label}<ChevronDown className="h-3 w-3" />
              </Button>
            ))}
            <Button type="button" variant="ghost" size="icon-sm" className={cn(selectionBarButton, "w-8 px-0")} aria-label="Clear selection" onClick={() => setPicked(new Set())}>
              <X className="h-3.5 w-3.5" />
            </Button>
          </div>
        )}
        <Dialog open={bulk !== null} onOpenChange={(o) => { if (!o) setBulk(null) }}>
          <DialogContent className="sm:max-w-lg">
            <DialogHeader>
              <DialogTitle>{BULK.find((b) => b.key === bulk)?.title} for {pickedRows.length} {pickedRows.length === 1 ? "workspace" : "workspaces"}</DialogTitle>
              <DialogDescription>{pickedRows.map((r) => r.workspace_name).join(", ")}</DialogDescription>
            </DialogHeader>
            {bulk && (
              <BulkGovernanceForm section={bulk} rows={pickedRows} all={false} inDialog={{ onCancel: () => setBulk(null) }}
                onSaved={() => { setBulk(null); setPicked(new Set()); void inst.reloadGov() }} />
            )}
          </DialogContent>
        </Dialog>
      </>
    )
  } else if (view === "activity") {
    body = (
      <>
        <div className="flex flex-wrap items-center gap-2">
          <select aria-label="Workspace" value={state.in} onChange={(e) => update({ in: e.target.value })} className={cn(nativeSelect, "w-auto")}>
            <option value="">All workspaces</option>
            {rows.map((r) => <option key={r.workspace_id} value={r.workspace_slug}>{r.workspace_name}</option>)}
          </select>
        </div>
        <SecurityActivity entries={entries} stream={stream} onStream={(s) => update({ stream: s })} live={data.live} error={inst.requestsError}
          server={{
            decision, onDecision: setDecision, total: inst.requests?.total ?? 0, loading: inst.requestsLoading,
            counts: inst.requests?.counts ?? { allow: 0, deny: 0, escalate: 0, pending: 0 }, onLoadMore: () => void inst.loadMore(),
          }} />
      </>
    )
  } else {
    body = (
      <SecurityOverview status={data.status} posture={data.posture} postureError={data.postureError} entries={entries}
        workspaceId={workspaceId ?? ""} counts={inst.requests ? { total: inst.requests.total, ...inst.requests.counts } : undefined}
        health={inst.health} healthError={inst.healthError} activityError={inst.requestsError}
        selectedCount={rows.length}
        onOpenActivity={() => showView("activity")}
        workspaces={rows.length > 0 && (
          <SettingsCard icon={Building2} title="What's on where" description="How each workspace is watched · open one for all its settings"
            actions={<Button type="button" size="sm" variant="outline" className="h-7 px-2.5 text-xs" onClick={() => showView("workspaces")}>Change several<ChevronRight /></Button>}>
            <WhatsOnWhere rows={rows} onOpen={openWs} defaults={defaultsRow} onOpenDefaults={() => openItem("defaults")} label="What's on where" />
          </SettingsCard>
        )} />
    )
  }

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      title="Security"
      icon={Shield}
      description="Who may read a secret, and how every workspace is watched"
      toolbar={<SidebarSearch value={q} onValueChange={(v) => update({ q: v })} placeholder="Search workspaces…" />}
      nav={nav}
      mobileNav={mobileNav}
      filterCount={Number(Boolean(needle))}
      actions={
        <Button size="sm" variant="outline" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="Refresh" onClick={() => { void data.reload(); void inst.reload() }} disabled={data.loading || inst.loading}>
          <RefreshCw className={cn((data.loading || inst.loading) && "animate-spin")} /><span className="max-md:hidden">Refresh</span>
        </Button>
      }
    >
      <div className={cn("mx-auto space-y-4 p-4 md:p-6", item || ws ? "max-w-3xl" : "max-w-5xl")}>
        {body}
      </div>
    </DrillPage>
  )
}

/**
 * One workspace's security on one page: how it is watched, in a line of chips,
 * then every setting that belongs to it — watchdog, findings and approvals,
 * leases, its own judge — with the existing editors through the instance
 * route, so an instance admin reaches a workspace they are not a member of.
 */
function WorkspaceSecurityProfile({ row, requests, serverEnabled, onSaved }: {
  row: InstanceGovRow
  requests: number
  serverEnabled: boolean
  onSaved: () => void
}) {
  // The editors read the row once and reload only for a new row (after a
  // save): a callback that changes every render must not reset their drafts.
  const saved = React.useRef(onSaved)
  saved.current = onSaved
  const instance = React.useMemo(() => ({ row, onSaved: () => saved.current() }), [row])
  const chip = (text: React.ReactNode, tone?: "warn" | "success") => (
    <span className="inline-flex items-center gap-1.5 rounded-full border border-border px-2 py-0.5 text-label text-muted-foreground">
      {tone && <span className={cn("h-1.5 w-1.5 rounded-full", tone === "warn" ? "bg-warn" : "bg-success")} aria-hidden />}{text}
    </span>
  )
  return (
    <>
      <section className="flex items-start gap-3 rounded-card border border-border bg-card p-4" data-slot="workspace-security-header">
        <WorkspaceTile id={row.workspace_id} name={row.workspace_name} size="lg" />
        <div className="min-w-0 flex-1">
          <h2 className="text-lg font-semibold tracking-[-0.01em]">{row.workspace_name}</h2>
          <p className="font-mono text-micro text-muted-foreground">{row.workspace_slug}{row.configured ? "" : " · built-in settings, never changed"}</p>
          <div className="mt-2.5 flex flex-wrap gap-1.5">
            {chip(watchSummary(row), row.enabled ? "success" : "warn")}
            {chip(judgeLabel(row))}
            {chip(`Lease ${leaseLabel(row.auto_lease_seconds).toLowerCase()}`)}
            {chip(`${requests} ${requests === 1 ? "request" : "requests"}`)}
          </div>
        </div>
      </section>
      <KeeperGovernancePanel key={`${row.workspace_id}-policy`} workspaceId={row.workspace_id} serverEnabled={serverEnabled} section="policy" instance={instance} />
      <KeeperGovernancePanel key={`${row.workspace_id}-judge`} workspaceId={row.workspace_id} serverEnabled={serverEnabled} section="judge" instance={instance} />
    </>
  )
}
