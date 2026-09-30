"use client"

import * as React from "react"
import { Building2, ChevronRight, Grid3x3, Plus, Shield, UserPlus, Users } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarSearch } from "@/components/layout/sidebar-kit"
import { SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  FACETS, byAttentionThenName, displayName, facetMatches, membersOf, personMatches, personStatus, workspaceMatches,
  type Facet,
} from "./people-model"
import { AccessMatrix, AttentionStrip, PeopleTable, WorkspacesTable } from "./people-views"
import { PersonProfile } from "./person-profile"
import { WorkspaceProfile } from "./workspace-profile"
import { AddPersonDialog, NewWorkspaceDialog } from "./people-dialogs"
import { usePeople } from "./use-people"

type View = "people" | "workspaces" | "access"
const VIEWS: { key: View; label: string; icon: typeof Users }[] = [
  { key: "people", label: "People", icon: Users },
  { key: "workspaces", label: "Workspaces", icon: Building2 },
  { key: "access", label: "Access", icon: Grid3x3 },
]

interface UrlState { view: View; facet: Facet; person: string; ws: string; q: string }

/** ?view= ?facet= ?person= ?ws= ?q= keep the page shareable and reload-safe. */
function readUrl(): UrlState {
  const empty: UrlState = { view: "people", facet: "all", person: "", ws: "", q: "" }
  if (typeof window === "undefined") return empty
  const p = new URLSearchParams(window.location.search)
  const v = p.get("view")
  const f = p.get("facet")
  return {
    view: v === "workspaces" || v === "access" ? v : "people",
    facet: FACETS.some((x) => x.key === f) ? (f as Facet) : "all",
    person: p.get("person") ?? "",
    ws: p.get("ws") ?? "",
    q: p.get("q") ?? "",
  }
}
function writeUrl(s: UrlState) {
  const url = new URL(window.location.href)
  const set = (k: string, v: string, keep: boolean) => (keep ? url.searchParams.set(k, v) : url.searchParams.delete(k))
  set("view", s.view, s.view !== "people")
  set("facet", s.facet, s.facet !== "all")
  set("person", s.person, Boolean(s.person))
  set("ws", s.ws, Boolean(s.ws))
  set("q", s.q, Boolean(s.q))
  window.history.replaceState(window.history.state, "", url.toString())
}

/**
 * Admin › People & workspaces. One page for who can sign in and where they
 * work, built like Settings › Crew links: "← Admin", the page's own panel
 * (search, People / Workspaces / Access, the people facets, the workspaces),
 * and the content in the middle — a table, or one person's or one
 * workspace's profile. It replaces the separate Workspaces and Users tabs.
 */
export function PeoplePage() {
  const { workspaceId, loading: workspaceLoading } = useWorkspace()
  const { session } = useAuth()
  const data = usePeople(workspaceId, workspaceLoading)
  const { people, workspaces, loading, error, busy, actions, reload, scope } = data
  const [state, setState] = React.useState(readUrl)
  const [dialog, setDialog] = React.useState<"person" | "workspace" | null>(null)
  const { view, facet, q } = state

  const update = (next: Partial<UrlState>) => setState((prev) => {
    const merged = { ...prev, ...next }
    writeUrl(merged)
    return merged
  })
  const openPerson = (id: string) => update({ person: id, ws: "" })
  const openWorkspace = (id: string) => update({ ws: id, person: "" })
  const showView = (v: View) => update({ view: v, person: "", ws: "" })

  const person = people.find((p) => p.id === state.person)
  const ws = workspaces.find((w) => w.id === state.ws)
  const shownPeople = people.filter((p) => facetMatches(p, facet) && personMatches(p, q)).sort((a, b) => byAttentionThenName(a, b))
  const shownWorkspaces = workspaces.filter((w) => workspaceMatches(w, q)).sort((a, b) => a.name.localeCompare(b.name))
  const attention = people.filter((p) => personStatus(p) !== "active").length
  const admins = people.filter((p) => p.instance_admin).length

  const nav = (
    <>
      <div className="mx-2 mt-1 grid grid-cols-3 gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label="View">
        {VIEWS.map((v) => (
          <button key={v.key} type="button" aria-pressed={view === v.key && !person && !ws} onClick={() => showView(v.key)} data-drill-close
            className={cn("flex h-7 items-center justify-center gap-1 rounded-md text-[11.5px] font-medium transition-colors",
              view === v.key && !person && !ws ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
            <v.icon className="h-3.5 w-3.5" />{v.label}
          </button>
        ))}
      </div>
      <DrillNavSection label="People" count={people.length}>
        {FACETS.map((f, i) => {
          const n = people.filter((p) => facetMatches(p, f.key) && personMatches(p, q)).length
          const on = view === "people" && facet === f.key && !person && !ws
          return (
            <DrillNavItem key={f.key} index={i} selected={on} pressed={on} muted={!n}
              onSelect={() => update({ view: "people", facet: f.key, person: "", ws: "" })}
              icon={<span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", f.dot ?? "bg-border")} aria-hidden />}
              label={f.label} meta={n} />
          )
        })}
      </DrillNavSection>
      <DrillNavSection label="Workspaces" count={shownWorkspaces.length}>
        {shownWorkspaces.map((w, i) => (
          <DrillNavItem key={w.id} index={i} selected={ws?.id === w.id} onSelect={() => openWorkspace(w.id)} title={`${w.name} · ${w.slug}`}
            icon={<WorkspaceTile id={w.id} name={w.name} />}
            label={w.name}
            sub={`${membersOf(people, w.id).length} people · ${w._count_crews} crews`}
            meta={w.current ? <span className="text-[10px]">you</span> : undefined} />
        ))}
        <DrillNavItem index={shownWorkspaces.length} onSelect={() => setDialog("workspace")}
          icon={<Plus className="h-3.5 w-3.5 text-primary-hover" />} label={<span className="text-primary-hover">New workspace</span>} />
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-surface-subtle p-0.5" role="tablist" aria-label="View">
      {VIEWS.map((v) => (
        <button key={v.key} type="button" role="tab" aria-selected={view === v.key} onClick={() => showView(v.key)}
          className={cn("flex h-8 items-center justify-center gap-1.5 rounded-md text-xs font-medium", view === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground")}>
          <v.icon className="h-3.5 w-3.5" />{v.label}
        </button>
      ))}
    </div>
  )

  const crumb = (label: string) => (
    <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-[12.5px] text-muted-foreground">
      <button type="button" className="hover:text-foreground" onClick={() => showView(ws ? "workspaces" : "people")}>{ws ? "Workspaces" : "People"}</button>
      <ChevronRight className="size-3" /><span className="text-foreground">{label}</span>
    </nav>
  )

  let body: React.ReactNode
  if (loading) {
    body = <div className="space-y-3"><Skeleton className="h-8 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
  } else if (error) {
    body = <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">{error} <button type="button" className="underline" onClick={() => void reload()}>Retry</button></p>
  } else if (person && workspaceId) {
    body = <>{crumb(displayName(person))}<PersonProfile person={person} people={people} workspaces={workspaces} workspaceId={workspaceId} meId={session?.user.id} busy={busy} actions={actions} onChanged={() => void reload()} /></>
  } else if (ws) {
    body = <>{crumb(ws.name)}<WorkspaceProfile ws={ws} people={people} busy={busy} actions={actions} onDeleted={() => showView("workspaces")} /></>
  } else if (view === "workspaces") {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem n={workspaces.length}>workspaces</SummaryItem>
          <SummaryItem n={people.length}>people</SummaryItem>
          <SummaryItem n={workspaces.reduce((a, w) => a + (w._count_agents ?? 0), 0)}>agents</SummaryItem>
        </SettingsSummary>
        <WorkspacesTable workspaces={shownWorkspaces} people={people} onOpen={openWorkspace} />
      </>
    )
  } else if (view === "access") {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem>Click a cell to change a role, or to give or take away access.</SummaryItem>
          <SummaryItem>An only owner stays; hand the workspace over first.</SummaryItem>
        </SettingsSummary>
        <AccessMatrix people={shownPeople} allPeople={people} workspaces={shownWorkspaces} actions={actions} busy={busy} />
      </>
    )
  } else {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem n={shownPeople.length}>of {people.length} people</SummaryItem>
          <SummaryItem n={workspaces.length}>workspaces</SummaryItem>
          {attention > 0 && <SummaryItem n={attention} tone="warn">need attention</SummaryItem>}
          <SummaryItem n={admins}>{admins === 1 ? "instance admin" : "instance admins"}</SummaryItem>
        </SettingsSummary>
        {facet === "all" && !q && <AttentionStrip people={people} onOpen={openPerson} actions={actions} busy={busy} />}
        <PeopleTable people={shownPeople} onOpen={openPerson} />
      </>
    )
  }

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      title="People & workspaces"
      icon={Users}
      description="Who can sign in, and where they work"
      toolbar={<SidebarSearch value={q} onValueChange={(v) => update({ q: v })} placeholder="Search people, workspaces…" />}
      nav={nav}
      mobileNav={mobileNav}
      filterCount={Number(Boolean(q.trim())) + Number(facet !== "all")}
      actions={
        <>
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setDialog("workspace")}><Building2 />New workspace</Button>
          <Button size="sm" className="h-7 text-xs" onClick={() => setDialog("person")}><UserPlus />Add person</Button>
        </>
      }
    >
      <div className={cn("mx-auto space-y-4 p-4 md:p-6", person || ws ? "max-w-3xl" : "max-w-5xl")}>
        {scope === "workspace" && !loading && (
          <p className="rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
            You see your own workspace only. Instance administrators see and change every workspace.
          </p>
        )}
        {body}
      </div>
      <AddPersonDialog open={dialog === "person"} onOpenChange={(o) => setDialog(o ? "person" : null)} workspaces={workspaces} actions={actions} busy={busy} onCreated={openPerson} />
      <NewWorkspaceDialog open={dialog === "workspace"} onOpenChange={(o) => setDialog(o ? "workspace" : null)} people={people} actions={actions} busy={busy} onCreated={openWorkspace} />
    </DrillPage>
  )
}
