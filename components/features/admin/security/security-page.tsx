"use client"

import * as React from "react"
import {
  Activity as ActivityIcon, Building2, Clock, Gavel, Home, ListChecks, RefreshCw, Shield, Sparkles, type LucideIcon,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { INSTANCE_SCOPE } from "@/lib/admin-api"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarFilterButton, SidebarSearch } from "@/components/layout/sidebar-kit"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { FINDING_ACTIONS } from "@/app/(dashboard)/admin/tabs/overview-tab"
import { KeeperJudgeCard } from "@/components/features/admin/keeper-judge-card"
import { KeeperProfileCard } from "@/components/features/admin/keeper-profile-card"
import { JudgeModelsCard } from "@/components/features/admin/judge-models-card"
import { SecurityOverview } from "./security-overview"
import { SecurityActivity } from "./security-activity"
import { SecurityWorkspaces } from "./security-workspaces"
import { WorkspaceSecurityProfile } from "./workspace-profile"
import { InstancePage } from "./instance-page"
import { useSecurity } from "./use-security"
import { useInstanceKeeper, type InstanceGovRow } from "./use-instance-keeper"
import { DefaultsForm } from "./bulk-governance"
import { INSTANCE_ITEMS, STREAMS, findings, secondApprover, watchdogFinding, type DecisionFilter, type InstanceItem, type Stream } from "./security-model"

const ITEM_ICON: Record<InstanceItem, LucideIcon> = { judge: Gavel, rules: ListChecks, background: Clock, defaults: Sparkles }

type View = "overview" | "workspaces" | "activity"
const VIEWS: { key: View; label: string; icon: LucideIcon }[] = [
  { key: "overview", label: "Overview", icon: Home },
  { key: "workspaces", label: "Workspaces", icon: Building2 },
  { key: "activity", label: "Activity", icon: ActivityIcon },
]

type WatchFacet = "" | "on" | "off"
type JudgeFacet = "" | "instance" | "own"
type SecondFacet = "" | "required" | "tier"

interface UrlState {
  view: View
  /** An instance setting open in the middle. */
  item: InstanceItem | null
  /** A workspace's page open in the middle (slug). */
  ws: string
  stream: Stream | "all"
  /** Activity of some workspaces only (slugs, comma-joined); "" is every workspace. */
  in: string
  q: string
  /** The Workspaces view's facets. */
  watch: WatchFacet
  judge: JudgeFacet
  second: SecondFacet
}

const INSTANCE_KEYS = new Set<string>(INSTANCE_ITEMS.map((i) => i.key))
/** Sections the page had before it had views; links to them still land. */
const PER_WORKSPACE = new Set(["matrix", "watchdog", "alerts", "leases", "workspace-judge"])
const oneOf = <T extends string>(v: string | null, ok: readonly T[], none: T): T => (ok.includes(v as T) ? (v as T) : none)

/** ?view= ?section= ?ws= ?stream= ?in= ?q= (and the facets) keep the page shareable and reload-safe. */
function parseParams(p: URLSearchParams): UrlState {
  const v = p.get("view")
  const s = p.get("section") ?? ""
  const st = p.get("stream")
  const ws = p.get("ws") ?? ""
  return {
    view: v === "workspaces" || v === "activity" ? v : s === "activity" ? "activity" : PER_WORKSPACE.has(s) ? "workspaces" : "overview",
    item: INSTANCE_KEYS.has(s) ? (s as InstanceItem) : null,
    // One workspace opens its page; an old list of ticked ones opens none.
    ws: ws.includes(",") || ws === "none" ? "" : ws,
    stream: STREAMS.some((x) => x.key === st) ? (st as Stream) : "all",
    in: p.get("in") ?? "",
    q: p.get("q") ?? "",
    watch: oneOf(p.get("watch"), ["on", "off"], ""),
    judge: oneOf(p.get("judge"), ["instance", "own"], ""),
    second: oneOf(p.get("second"), ["required", "tier"], ""),
  }
}
const readUrl = (): UrlState => parseParams(typeof window === "undefined" ? new URLSearchParams() : new URLSearchParams(window.location.search))

function writeUrl(s: UrlState) {
  const url = new URL(window.location.href)
  const set = (k: string, v: string, keep: boolean) => (keep ? url.searchParams.set(k, v) : url.searchParams.delete(k))
  set("view", s.view, s.view !== "overview")
  set("section", s.item ?? "", Boolean(s.item))
  set("ws", s.ws, Boolean(s.ws))
  set("stream", s.stream, s.view === "activity" && s.stream !== "all")
  set("in", s.in, s.view === "activity" && Boolean(s.in))
  set("q", s.q, Boolean(s.q))
  set("watch", s.watch, s.view === "workspaces" && Boolean(s.watch))
  set("judge", s.judge, s.view === "workspaces" && Boolean(s.judge))
  set("second", s.second, s.view === "workspaces" && Boolean(s.second))
  window.history.replaceState(window.history.state, "", url.toString())
}

/**
 * Admin › Security: "← Admin", a panel with three views (Overview, Workspaces,
 * Activity), the four settings that reach every workspace, and the few
 * workspaces whose watchdog is off. The middle shows one thing at a time.
 *
 * What is set per workspace — watchdog, alerts, access time, its own judge —
 * is not a section per setting: the Workspaces view is one table of how each
 * workspace is watched, ticked rows change together from the selection bar,
 * and a row opens that workspace's page. What is set for the whole instance
 * has a page each, with "← Overview" and a "Whole instance" mark.
 */
export function SecurityPage() {
  const { workspaceId, loading: workspaceLoading, workspace } = useWorkspace()
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
  /** A link that stays on this page: the view, section and facets it names, nothing carried over. */
  const navigate = (fn: (q: URLSearchParams) => void) => {
    const p = new URLSearchParams()
    fn(p)
    update({ ...parseParams(p), q: "" })
  }

  const [decision, setDecision] = React.useState<DecisionFilter>("all")
  const pickedSlugs = React.useMemo(() => state.in.split(",").filter(Boolean), [state.in])
  // Activity filters on the server, over the whole history (review R6). The
  // overview reads the log unfiltered.
  const onActivity = view === "activity" && !item && !state.ws
  const filter = React.useMemo(() => {
    if (!onActivity) return {}
    return {
      types: stream === "all" ? undefined : stream === "requests" ? ["access", "execute"] : [stream],
      decision: decision === "all" ? undefined : decision,
    }
  }, [onActivity, stream, decision])

  // The governance list names the workspaces; the activity of some is asked
  // for by id. Until the list is here, every workspace.
  const [govRows, setGovRows] = React.useState<InstanceGovRow[]>([])
  const activityIds = React.useMemo(() => {
    if (!onActivity || pickedSlugs.length === 0 || govRows.length === 0) return null
    return govRows.filter((r) => pickedSlugs.includes(r.workspace_slug) || pickedSlugs.includes(r.workspace_id)).map((r) => r.workspace_id)
  }, [onActivity, pickedSlugs, govRows])
  const inst = useInstanceKeeper(activityIds, data.liveTick, filter)
  const rows = React.useMemo(() => inst.gov?.workspaces ?? [], [inst.gov])
  React.useEffect(() => { setGovRows(rows) }, [rows])

  const counts = React.useMemo(() => new Map((inst.requests?.by_workspace ?? []).map((b) => [b.workspace_id, b.count])), [inst.requests])
  const ws = rows.find((r) => r.workspace_slug === state.ws || r.workspace_id === state.ws)
  const openWs = (id: string) => {
    const r = rows.find((x) => x.workspace_id === id)
    if (r) update({ ws: r.workspace_slug, item: null })
  }

  const needle = q.trim().toLowerCase()
  const matchesQ = (r: InstanceGovRow) => !needle || r.workspace_name.toLowerCase().includes(needle) || r.workspace_slug.includes(needle)
  const matchesFacets = (r: InstanceGovRow, skip?: "watch" | "judge" | "second") =>
    (skip === "watch" || !state.watch || (state.watch === "on") === r.enabled) &&
    (skip === "judge" || !state.judge || (state.judge === "own") === Boolean(r.gov_model_provider)) &&
    (skip === "second" || !state.second || (state.second === "required") === secondApprover(r).required)
  const shown = React.useMemo(() => rows.filter((r) => matchesQ(r) && matchesFacets(r)), [rows, needle, state.watch, state.judge, state.second]) // eslint-disable-line react-hooks/exhaustive-deps -- the matchers read exactly these
  const off = rows.filter((r) => !r.enabled)
  const offShown = off.filter(matchesQ)
  const defaultsRow = inst.gov?.defaults

  const [picked, setPicked] = React.useState<Set<string>>(() => new Set())
  const toggle = (ids: string[], on: boolean) => setPicked((prev) => { const n = new Set(prev); for (const id of ids) { if (on) n.add(id); else n.delete(id) } return n })

  // Per kind from the server, over the workspaces asked for and all history.
  const byType = inst.requests?.by_type ?? {}
  const streamCount: Record<Stream, number> = {
    requests: (byType.access ?? 0) + (byType.execute ?? 0) + (byType[""] ?? 0),
    skill_review: byType.skill_review ?? 0, behavior: byType.behavior ?? 0,
    memory_health: byType.memory_health ?? 0, negative_learning: byType.negative_learning ?? 0,
  }
  const activityTotal = Object.values(byType).reduce((a, b) => a + b, 0)
  const entries = inst.requests?.items ?? []

  const attention = findings(data.posture, data.status, FINDING_ACTIONS).length + (watchdogFinding(rows) ? 1 : 0)
  const judgeSub = !data.status ? "…" : !data.status.enabled ? "Keeper off" : `${data.status.model || "no model"}${data.status.ollama_online ? " · answering" : " · not answering"}`
  const middleWide = !item && !ws

  const sessionName = workspaceId ? rows.find((r) => r.workspace_id === workspaceId)?.workspace_name ?? workspace?.name : undefined
  const session = workspaceId && sessionName ? { id: workspaceId, name: sessionName } : null

  const facets = useSidebarFacets()
  const facetCount = Number(Boolean(state.watch)) + Number(Boolean(state.judge)) + Number(Boolean(state.second))
  const onWorkspaces = view === "workspaces" && middleWide
  const facetRows = rows.filter(matchesQ)
  const countFor = (skip: "watch" | "judge" | "second", f: (r: InstanceGovRow) => boolean) => facetRows.filter((r) => matchesFacets(r, skip) && f(r)).length
  const toggleFacet = <K extends "watch" | "judge" | "second">(k: K, v: UrlState[K]) => update({ [k]: state[k] === v ? "" : v } as Partial<UrlState>)
  const facetBlock = onWorkspaces ? (
    <SidebarFacetBlock {...facets.blockProps} label="Filter workspaces" activeCount={facetCount} onClear={() => update({ watch: "", judge: "", second: "" })} className="mt-2">
      <SidebarInlineFacet label="Watchdog" selected={state.watch || null} onToggle={(v) => toggleFacet("watch", v as WatchFacet)} onClear={() => update({ watch: "" })}
        values={[{ value: "on", label: "On", count: countFor("watch", (r) => r.enabled) }, { value: "off", label: "Off", count: countFor("watch", (r) => !r.enabled) }]} />
      <SidebarInlineFacet label="Judge" selected={state.judge || null} onToggle={(v) => toggleFacet("judge", v as JudgeFacet)} onClear={() => update({ judge: "" })}
        values={[{ value: "instance", label: "Instance judge", count: countFor("judge", (r) => !r.gov_model_provider) }, { value: "own", label: "Its own", count: countFor("judge", (r) => Boolean(r.gov_model_provider)) }]} />
      <SidebarInlineFacet label="Second approver" selected={state.second || null} onToggle={(v) => toggleFacet("second", v as SecondFacet)} onClear={() => update({ second: "" })}
        values={[{ value: "required", label: "Required", count: countFor("second", (r) => secondApprover(r).required) }, { value: "tier", label: "Only by level", count: countFor("second", (r) => !secondApprover(r).required) }]} />
    </SidebarFacetBlock>
  ) : undefined

  const toolbar = (
    <>
      <SidebarSearch value={q} onValueChange={(v) => update(v && view !== "workspaces" ? { q: v, view: "workspaces", item: null, ws: "" } : { q: v })} placeholder="Search workspaces…" />
      {onWorkspaces && <SidebarFilterButton activeCount={facetCount} {...facets.triggerProps}>Filter</SidebarFilterButton>}
    </>
  )

  const nav = (
    <>
      <DrillNavSection label="Views" collapsible={false}>
        {VIEWS.map((v, i) => {
          const n = v.key === "overview" ? attention : v.key === "workspaces" ? rows.length : activityTotal
          return (
            <DrillNavItem key={v.key} index={i} selected={view === v.key && middleWide} onSelect={() => showView(v.key)}
              icon={<v.icon className="h-3.5 w-3.5 text-muted-foreground" />} label={v.label}
              meta={v.key === "overview" ? (n > 0 ? <span className="rounded-full bg-chip-warn-bg px-1.5 text-chip-warn-fg" aria-label={`${n} to look at`}>{n}</span> : undefined) : n} />
          )
        })}
      </DrillNavSection>
      <DrillNavSection label="Instance settings" count={INSTANCE_ITEMS.length}>
        {INSTANCE_ITEMS.map((s, i) => {
          const Icon = ITEM_ICON[s.key]
          return (
            <DrillNavItem key={s.key} index={i} selected={item === s.key} onSelect={() => openItem(s.key)}
              icon={<Icon className="h-3.5 w-3.5 text-muted-foreground" />} label={s.label}
              sub={s.key === "judge" ? judgeSub : s.hint} title="Applies to every workspace on this server" />
          )
        })}
      </DrillNavSection>
      {off.length > 0 && (
        <DrillNavSection label="Watchdog off" count={off.length}>
          {offShown.slice(0, 5).map((r, i) => (
            <DrillNavItem key={r.workspace_id} index={i} selected={ws?.workspace_id === r.workspace_id} onSelect={() => openWs(r.workspace_id)}
              title={`${r.workspace_name} · ${r.workspace_slug}`}
              icon={<WorkspaceTile id={r.workspace_id} name={r.workspace_name} logoUrl={r.workspace_logo_url} size="xs" />} label={r.workspace_name}
              meta={<span className="inline-block h-1.5 w-1.5 rounded-full bg-warn" aria-label="Watchdog off" />} />
          ))}
          <DrillNavItem index={5} selected={false} muted onSelect={() => update({ view: "workspaces", item: null, ws: "", watch: "off" })}
            label={`All ${rows.length} workspaces…`} />
        </DrillNavSection>
      )}
    </>
  )

  const mobileNav = (
    <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-surface-subtle p-0.5" role="tablist" aria-label="View">
      {VIEWS.map((v) => (
        <button key={v.key} type="button" role="tab" aria-selected={view === v.key && middleWide} onClick={() => showView(v.key)}
          className={cn("flex h-8 items-center justify-center gap-1.5 rounded-md text-xs font-medium coarse:min-h-[44px]", view === v.key && middleWide ? "bg-card text-foreground shadow-xs" : "text-muted-foreground")}>
          <v.icon className="h-3.5 w-3.5" />{v.label}
        </button>
      ))}
    </div>
  )

  let body: React.ReactNode
  if (!inst.gov && inst.loading) {
    body = <div className="space-y-3"><Skeleton className="h-8 rounded-lg" /><Skeleton className="h-[280px] rounded-card" /></div>
  } else if (inst.govError && !inst.gov) {
    body = <p className="rounded-lg border border-border px-4 py-3 text-control text-destructive">{inst.govError}</p>
  } else if (item) {
    const back = () => showView("overview")
    body = (
      <InstancePage item={item} icon={ITEM_ICON[item]} onBack={back}>
        {item === "judge" && cardWs && <KeeperJudgeCard workspaceId={cardWs} />}
        {item === "rules" && cardWs && <KeeperProfileCard workspaceId={cardWs} />}
        {item === "background" && cardWs && <JudgeModelsCard workspaceId={cardWs} />}
        {item === "defaults" && defaultsRow && <DefaultsForm current={defaultsRow} onSaved={() => void inst.reloadGov()} />}
      </InstancePage>
    )
  } else if (ws) {
    body = (
      <WorkspaceSecurityProfile row={ws} requests={counts.get(ws.workspace_id) ?? 0} serverEnabled={data.status?.enabled ?? false}
        onBack={() => showView("workspaces")} onActivity={() => update({ view: "activity", ws: "", in: ws.workspace_slug })}
        onSaved={() => void inst.reloadGov()} />
    )
  } else if (view === "workspaces") {
    body = (
      <SecurityWorkspaces rows={shown} all={rows} total={rows.length} off={off.length} defaults={Boolean(defaultsRow)}
        filterKey={`${needle}|${state.watch}|${state.judge}|${state.second}`}
        onOpen={openWs} onOpenDefaults={() => openItem("defaults")} picked={picked} onPick={toggle} onSaved={() => void inst.reloadGov()} />
    )
  } else if (view === "activity") {
    body = (
      <SecurityActivity entries={entries} stream={stream} onStream={(s) => update({ stream: s })} streamCount={streamCount} streamTotal={activityTotal}
        live={data.live} error={inst.requestsError} decision={decision} onDecision={setDecision}
        total={inst.requests?.total ?? 0} loading={inst.requestsLoading} onMore={() => void inst.loadMore()}
        workspaces={rows} picked={pickedSlugs} onPick={(slugs) => update({ in: slugs.join(",") })} session={session}
        onChanged={() => void inst.reload()} />
    )
  } else {
    body = (
      <SecurityOverview status={data.status} posture={data.posture} postureError={data.postureError} entries={entries} rows={rows}
        workspaceId={workspaceId ?? ""} counts={inst.requests ? { total: inst.requests.total, ...inst.requests.counts } : undefined}
        health={inst.health} healthError={inst.healthError} activityError={inst.requestsError}
        go={{
          params: navigate,
          workspace: openWs,
          activity: () => showView("activity"),
        }}
        onChanged={() => void inst.reload()} />
    )
  }

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      panelId="drill-security"
      title="Security"
      icon={Shield}
      description="Who may read a secret, and how every workspace is watched"
      toolbar={toolbar}
      facets={facetBlock}
      nav={nav}
      mobileNav={mobileNav}
      actions={
        <Button size="sm" variant="outline" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="Refresh" onClick={() => { void data.reload(); void inst.reload() }} disabled={data.loading || inst.loading}>
          <RefreshCw className={cn((data.loading || inst.loading) && "animate-spin")} /><span className="max-md:hidden">Refresh</span>
        </Button>
      }
    >
      <div className={cn("mx-auto flex flex-col gap-4 p-4 md:p-6", item || ws ? "max-w-3xl" : "max-w-5xl")}>
        {body}
      </div>
    </DrillPage>
  )
}
