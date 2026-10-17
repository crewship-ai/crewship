"use client"

import * as React from "react"
import { Ban, Building2, ChevronDown, ChevronRight, Grid3x3, House, Plus, Shield, Trash2, UserPlus, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarSearch } from "@/components/layout/sidebar-kit"
import { SettingsCard, SettingsSummary, SummaryItem, selectionBar, selectionBarAction, selectionBarButton } from "@/components/features/settings/shared"
import { MembersAttention } from "@/components/features/settings/members/members-attention"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  FACETS, byAttentionThenName, displayName, facetMatches, isEmptyWorkspace, isPersonal, membersOf, personMatches, personStatus,
  personalOwnerName, workspaceMatches, type Facet, type Person, type Role,
} from "./people-model"
import { AccessMatrix, PeopleTable, PersonalWorkspacesTable, WorkspacesTable, peopleFindings } from "./people-views"
import { RoleSegmented } from "./people-parts"
import { PersonProfile } from "./person-profile"
import { WorkspaceProfile } from "./workspace-profile"
import { AddPersonDialog, NewWorkspaceDialog } from "./people-dialogs"
import { usePeople } from "./use-people"
import { toast } from "@/lib/toast"

/** "accounts" is the personal accounts, under People: it has no tab of its own. */
type View = "people" | "accounts" | "workspaces" | "access"
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
    view: v === "workspaces" || v === "access" || v === "accounts" ? v : "people",
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
  const shownWorkspaces = workspaces.filter((w) => workspaceMatches(w, q, people)).sort((a, b) => a.name.localeCompare(b.name))
  // Workspaces are the places people share; a personal account is one
  // person's own, made at sign-up, and is listed under People.
  const teamShown = shownWorkspaces.filter((w) => !isPersonal(w))
  const personalShown = shownWorkspaces.filter(isPersonal).sort((a, b) => personalOwnerName(a, people).localeCompare(personalOwnerName(b, people)))
  const personalAll = workspaces.filter(isPersonal)
  const [picked, setPicked] = React.useState<Set<string>>(() => new Set())
  const [pickedWs, setPickedWs] = React.useState<Set<string>>(() => new Set())
  const toggle = (set: React.Dispatch<React.SetStateAction<Set<string>>>) => (ids: string[], on: boolean) =>
    set((prev) => { const n = new Set(prev); for (const id of ids) { if (on) n.add(id); else n.delete(id) } return n })
  const [confirmDelete, setConfirmDelete] = React.useState(false)
  const deletePicked = async () => {
    const targets = workspaces.filter((w) => pickedWs.has(w.id) && isPersonal(w) && isEmptyWorkspace(w))
    let done = 0
    for (const w of targets) if (await actions.deleteWorkspace(w.id, w.slug, "")) done++
    setPickedWs(new Set())
    setConfirmDelete(false)
    if (done) toast.success(`Removed ${done} empty personal ${done === 1 ? "account" : "accounts"}`)
  }
  const attention = people.filter((p) => personStatus(p) !== "active").length
  const admins = people.filter((p) => p.instance_admin).length

  const nav = (
    <>
      <div className="mx-2 mt-1 grid grid-cols-3 gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label="View">
        {VIEWS.map((v) => (
          <button key={v.key} type="button" aria-pressed={view === v.key && !person && !ws} onClick={() => showView(v.key)} data-drill-close
            className={cn("flex h-7 items-center justify-center gap-1 rounded-md text-label font-medium transition-colors",
              (view === v.key || (v.key === "people" && view === "accounts")) && !person && !ws ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
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
              icon={<f.icon className={cn("h-3.5 w-3.5 shrink-0", f.tone ?? "text-muted-foreground")} aria-hidden />}
              label={f.label} sub={f.hint} meta={n} />
          )
        })}
        {personalAll.length > 0 && (
          // One per person who signed up on their own; a search ("ivana")
          // counts hers, and the list it opens is filtered the same way.
          <DrillNavItem index={FACETS.length} selected={view === "accounts" && !person && !ws} pressed={view === "accounts" && !person && !ws}
            onSelect={() => showView("accounts")} muted={!personalShown.length}
            icon={<House className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />}
            label="Personal accounts" sub="Made at sign-up, one person each"
            meta={q.trim() ? `${personalShown.length}/${personalAll.length}` : personalAll.length} />
        )}
      </DrillNavSection>
      <DrillNavSection label="Workspaces" count={teamShown.length}>
        {teamShown.map((w, i) => (
          <DrillNavItem key={w.id} index={i} selected={ws?.id === w.id} onSelect={() => openWorkspace(w.id)} title={`${w.name} · ${w.slug}`}
            icon={<WorkspaceTile id={w.id} name={w.name} logoUrl={w.logo_url} />}
            label={w.name}
            sub={`${membersOf(people, w.id).length} people · ${w._count_crews} crews`}
            meta={w.current ? <span className="text-micro">you</span> : undefined} />
        ))}
        <DrillNavItem index={teamShown.length} onSelect={() => setDialog("workspace")}
          icon={<Plus className="h-3.5 w-3.5 text-primary-hover" />} label={<span className="text-primary-hover">New workspace</span>} />
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-surface-subtle p-0.5" role="tablist" aria-label="View">
      {VIEWS.map((v) => (
        <button key={v.key} type="button" role="tab" aria-selected={view === v.key || (v.key === "people" && view === "accounts")} onClick={() => showView(v.key)}
          className={cn("flex h-8 items-center justify-center gap-1.5 rounded-md text-xs font-medium", view === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground")}>
          <v.icon className="h-3.5 w-3.5" />{v.label}
        </button>
      ))}
    </div>
  )

  const crumb = (label: string) => (
    <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-label text-muted-foreground">
      <button type="button" className="hover:text-foreground" onClick={() => showView(ws ? (isPersonal(ws) ? "accounts" : "workspaces") : "people")}>
        {ws ? (isPersonal(ws) ? "Personal accounts" : "Workspaces") : "People"}
      </button>
      <ChevronRight className="size-3" /><span className="text-foreground">{label}</span>
    </nav>
  )

  let body: React.ReactNode
  if (loading) {
    body = <div className="space-y-3"><Skeleton className="h-8 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
  } else if (error) {
    body = <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">{error} <button type="button" className="underline" onClick={() => void reload()}>Retry</button></p>
  } else if (person) {
    // An instance admin may belong to no workspace: the profile opens all the
    // same, and what is per workspace picks one of the person's (below).
    body = <>{crumb(displayName(person))}<PersonProfile person={person} people={people} workspaces={workspaces} workspaceId={workspaceId ?? ""} meId={session?.user.id} busy={busy} actions={actions} onChanged={() => void reload()} /></>
  } else if (ws) {
    const personal = isPersonal(ws)
    body = <>{crumb(personal ? personalOwnerName(ws, people) : ws.name)}<WorkspaceProfile ws={ws} people={people} busy={busy} actions={actions} onDeleted={() => showView(personal ? "accounts" : "workspaces")} /></>
  } else if (view === "workspaces") {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem n={teamShown.length}>workspaces</SummaryItem>
          <SummaryItem n={people.length}>people</SummaryItem>
          <SummaryItem n={workspaces.filter((w) => !isPersonal(w)).reduce((a, w) => a + (w._count_agents ?? 0), 0)}>agents</SummaryItem>
        </SettingsSummary>
        <SettingsCard icon={Building2} title="Workspaces" description="Where people work together"
          actions={<Button size="sm" variant="outline" className="h-7 px-2.5 text-xs" onClick={() => setDialog("workspace")}><Plus />New workspace</Button>}>
          <WorkspacesTable workspaces={teamShown} people={people} onOpen={openWorkspace} />
        </SettingsCard>
        {personalAll.length > 0 && (
          <p className="flex items-center gap-1.5 text-label text-muted-foreground">
            <House className="h-3 w-3" aria-hidden />
            {personalAll.length} personal {personalAll.length === 1 ? "account is" : "accounts are"} listed under People.
            <button type="button" className="text-primary-hover hover:underline" onClick={() => showView("accounts")}>Show</button>
          </p>
        )}
      </>
    )
  } else if (view === "accounts") {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem n={personalAll.length}>personal accounts</SummaryItem>
          <SummaryItem n={personalAll.filter(isEmptyWorkspace).length}>empty</SummaryItem>
        </SettingsSummary>
        <SettingsCard icon={House} tint="var(--muted-foreground)" title="Personal accounts"
          description="Made when someone signs up without an invitation · one person each, apart from the workspaces"
          actions={pickedWs.size > 0 ? (
            <Button size="sm" variant="outline" className="h-7 border-destructive/40 px-2.5 text-xs text-destructive" onClick={() => setConfirmDelete(true)}>
              <Trash2 />Remove {pickedWs.size} empty
            </Button>
          ) : undefined}>
          <PersonalWorkspacesTable workspaces={personalShown} people={people} onOpen={openWorkspace} selected={pickedWs} onSelect={toggle(setPickedWs)}
            logos={new Map(workspaces.map((w) => [w.id, w.logo_url]))} />
          <p className="border-t border-border px-4 py-2.5 text-label text-muted-foreground">
            Only empty ones can be removed — no crews, no agents, no runs. The person keeps signing in and keeps every workspace they belong to. Open one to turn it into a workspace instead.
          </p>
        </SettingsCard>
        <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Remove {pickedWs.size} empty personal {pickedWs.size === 1 ? "account" : "accounts"}?</AlertDialogTitle>
              <AlertDialogDescription>
                {workspaces.filter((w) => pickedWs.has(w.id)).map((w) => personalOwnerName(w, people)).join(", ")}. Nothing is in them. The people keep signing in and keep every workspace they belong to. This cannot be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction variant="destructive" onClick={(e) => { e.preventDefault(); void deletePicked() }}>Remove</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </>
    )
  } else if (view === "access") {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem>Click a cell to change a role, or to give or take away access.</SummaryItem>
          <SummaryItem>An only owner stays; hand the workspace over first.</SummaryItem>
        </SettingsSummary>
        <AccessMatrix people={shownPeople} allPeople={people} workspaces={teamShown} actions={actions} busy={busy} />
      </>
    )
  } else {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem n={shownPeople.length}>of {people.length} people</SummaryItem>
          <SummaryItem n={workspaces.length - personalAll.length}>workspaces</SummaryItem>
          {personalAll.length > 0 && (
            <SummaryItem n={personalAll.length}>
              <button type="button" className="hover:text-foreground hover:underline" onClick={() => showView("accounts")}>personal accounts</button>
            </SummaryItem>
          )}
          {attention > 0 && <SummaryItem n={attention} tone="warn">need attention</SummaryItem>}
          <SummaryItem n={admins}>{admins === 1 ? "instance admin" : "instance admins"}</SummaryItem>
        </SettingsSummary>
        {facet === "all" && !q && (
          <MembersAttention findings={peopleFindings({
            people, workspaces, actions, onOpen: openPerson,
            onFacet: (f) => update({ view: "people", facet: f, person: "", ws: "" }),
            onPersonal: () => showView("accounts"),
          })} />
        )}
        <SettingsCard icon={Users} title="People" description="Everyone who can sign in to this instance · who needs a hand first">
          <PeopleTable people={shownPeople} workspaces={workspaces} onOpen={openPerson}
            selected={picked} onSelect={toggle(setPicked)} selectable={(p) => p.id !== session?.user.id && p.instance_admin_source !== "env"} />
        </SettingsCard>
        {picked.size > 0 && (
          <BulkPeopleBar people={people.filter((p) => picked.has(p.id))} workspaces={workspaces.filter((w) => !isPersonal(w))} actions={actions} busy={busy}
            onClear={() => setPicked(new Set())} />
        )}
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
          {/* Icons only on a phone, so the sub-bar keeps room for Filters. */}
          <Button size="sm" variant="outline" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="New workspace" onClick={() => setDialog("workspace")}>
            <Building2 /><span className="max-md:hidden">New workspace</span>
          </Button>
          <Button size="sm" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="Add person" onClick={() => setDialog("person")}>
            <UserPlus /><span className="max-md:hidden">Add person</span>
          </Button>
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

/**
 * The ticked people, and what can be done to all of them at once: give them a
 * role in a workspace, or suspend them. Each person is one call; the
 * toast counts what went through.
 */
function BulkPeopleBar({ people, workspaces, actions, busy, onClear }: {
  people: Person[]
  workspaces: ReturnType<typeof usePeople>["workspaces"]
  actions: ReturnType<typeof usePeople>["actions"]
  busy: string | null
  onClear: () => void
}) {
  const [wsId, setWsId] = React.useState(workspaces[0]?.id ?? "")
  const [role, setRole] = React.useState<Role | null>("MEMBER")
  const [open, setOpen] = React.useState(false)
  const [suspending, setSuspending] = React.useState(false)
  const target = workspaces.find((w) => w.id === wsId)
  const addAll = async () => {
    if (!target || !role) return
    let ok = 0
    for (const p of people) if (await actions.setRole(p.id, target.id, role, "")) ok++
    toast.success(`${ok} of ${people.length} added to ${target.name}`)
    setOpen(false)
    onClear()
  }
  const suspendAll = async () => {
    let ok = 0
    for (const p of people) if (await actions.suspend(p.id, "Suspended from People & workspaces", "")) ok++
    toast.success(`Suspended ${ok} of ${people.length}`)
    setSuspending(false)
    onClear()
  }
  return (
    <div role="toolbar" aria-label="Change the selected people" data-slot="people-bulk"
      className={cn(selectionBar, "fixed bottom-6 left-1/2 -translate-x-1/2 whitespace-nowrap max-sm:bottom-[calc(var(--mobile-tab-bar-h,0px)+0.75rem)] max-sm:gap-1.5 max-sm:px-2")}>
      <span className="px-1 text-xs"><span className="font-mono font-semibold tabular-nums">{people.length}</span> selected</span>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button size="sm" className={selectionBarAction} disabled={workspaces.length === 0}><Building2 />Add to workspace<ChevronDown className="h-3 w-3" /></Button>
        </PopoverTrigger>
        <PopoverContent side="top" align="start" className="w-auto space-y-3 p-3">
          <label className="grid gap-1 text-label text-muted-foreground">Workspace
            <select value={wsId} onChange={(e) => setWsId(e.target.value)} className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 text-control text-foreground">
              {workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
            </select>
          </label>
          <RoleSegmented label="Role" value={role} onChange={setRole} />
          <div className="flex justify-end"><Button size="sm" className="h-7 text-xs" disabled={!target || !role || busy !== null} onClick={() => void addAll()}>Add {people.length}</Button></div>
        </PopoverContent>
      </Popover>
      <Button variant="outline" size="sm" className={selectionBarButton} onClick={() => setSuspending(true)}><Ban />Suspend</Button>
      <Button variant="ghost" size="icon-sm" className={cn(selectionBarButton, "w-8 px-0")} aria-label="Clear selection" onClick={onClear}><X className="h-3.5 w-3.5" /></Button>
      <AlertDialog open={suspending} onOpenChange={setSuspending}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Suspend {people.length} {people.length === 1 ? "person" : "people"}?</AlertDialogTitle>
            <AlertDialogDescription>{people.map(displayName).join(", ")} can no longer sign in; their sessions end now. Their access is kept for when you reactivate them.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={(e) => { e.preventDefault(); void suspendAll() }}>Suspend</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
