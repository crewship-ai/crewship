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
import { SettingsCard, SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
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
import { toast } from "sonner"

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
  const shownWorkspaces = workspaces.filter((w) => workspaceMatches(w, q, people)).sort((a, b) => a.name.localeCompare(b.name))
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
    if (done) toast.success(`Deleted ${done} personal ${done === 1 ? "workspace" : "workspaces"}`)
  }
  const attention = people.filter((p) => personStatus(p) !== "active").length
  const admins = people.filter((p) => p.instance_admin).length

  const nav = (
    <>
      <div className="mx-2 mt-1 grid grid-cols-3 gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label="View">
        {VIEWS.map((v) => (
          <button key={v.key} type="button" aria-pressed={view === v.key && !person && !ws} onClick={() => showView(v.key)} data-drill-close
            className={cn("flex h-7 items-center justify-center gap-1 rounded-md text-label font-medium transition-colors",
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
              icon={<f.icon className={cn("h-3.5 w-3.5 shrink-0", f.tone ?? "text-muted-foreground")} aria-hidden />}
              label={f.label} sub={f.hint} meta={n} />
          )
        })}
      </DrillNavSection>
      <DrillNavSection label="Team workspaces" count={teamShown.length}>
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
      {personalAll.length > 0 && (
        // Folded unless searching: there can be one per person, and a search
        // ("ivana") must still find hers.
        <DrillNavSection key={q.trim() ? "personal-open" : "personal"} label="Personal" count={q.trim() ? `${personalShown.length} of ${personalAll.length}` : personalAll.length} defaultOpen={Boolean(q.trim()) || Boolean(ws && isPersonal(ws))}>
          <DrillNavItem index={0} selected={view === "workspaces" && !ws} onSelect={() => showView("workspaces")}
            icon={<House className="h-3.5 w-3.5 text-muted-foreground" />} label="All personal workspaces" sub="Created at sign-up, one person each" meta={personalAll.length} />
          {personalShown.map((w, i) => (
            <DrillNavItem key={w.id} index={i + 1} selected={ws?.id === w.id} onSelect={() => openWorkspace(w.id)} title={`${w.name} · ${w.slug}`}
              icon={<WorkspaceTile id={w.id} name={personalOwnerName(w, people)} size="xs" logoUrl={w.logo_url} />}
              label={personalOwnerName(w, people)} sub={isEmptyWorkspace(w) ? "Empty" : `${w._count_crews} crews · ${w._count_agents} agents`} muted={isEmptyWorkspace(w)} />
          ))}
        </DrillNavSection>
      )}
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
    <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-label text-muted-foreground">
      <button type="button" className="hover:text-foreground" onClick={() => showView(ws ? "workspaces" : "people")}>{ws ? "Workspaces" : "People"}</button>
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
    body = <>{crumb(ws.name)}<WorkspaceProfile ws={ws} people={people} busy={busy} actions={actions} onDeleted={() => showView("workspaces")} /></>
  } else if (view === "workspaces") {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem n={workspaces.length - personalAll.length}>team workspaces</SummaryItem>
          <SummaryItem n={personalAll.length}>personal</SummaryItem>
          <SummaryItem n={people.length}>people</SummaryItem>
          <SummaryItem n={workspaces.reduce((a, w) => a + (w._count_agents ?? 0), 0)}>agents</SummaryItem>
        </SettingsSummary>
        <SettingsCard icon={Building2} title="Team workspaces" description="Where people work together"
          actions={<Button size="sm" variant="outline" className="h-7 px-2.5 text-xs" onClick={() => setDialog("workspace")}><Plus />New workspace</Button>}>
          <WorkspacesTable workspaces={teamShown} people={people} onOpen={openWorkspace} />
        </SettingsCard>
        <SettingsCard icon={House} tint="var(--muted-foreground)" title="Personal workspaces"
          description="Created automatically when someone signs up without an invitation · one person each"
          actions={pickedWs.size > 0 ? (
            <Button size="sm" variant="outline" className="h-7 border-destructive/40 px-2.5 text-xs text-destructive" onClick={() => setConfirmDelete(true)}>
              <Trash2 />Delete {pickedWs.size} empty
            </Button>
          ) : undefined}>
          <PersonalWorkspacesTable workspaces={personalShown} people={people} onOpen={openWorkspace} selected={pickedWs} onSelect={toggle(setPickedWs)} />
          <p className="border-t border-border px-4 py-2.5 text-label text-muted-foreground">
            Only empty ones can be removed here — no crews, no agents, no runs. Their owners keep their accounts and every other workspace. Open one to make it a team workspace instead.
          </p>
        </SettingsCard>
        <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete {pickedWs.size} empty personal {pickedWs.size === 1 ? "workspace" : "workspaces"}?</AlertDialogTitle>
              <AlertDialogDescription>
                {workspaces.filter((w) => pickedWs.has(w.id)).map((w) => personalOwnerName(w, people)).join(", ")}. Nothing is in them; their owners keep their accounts. This cannot be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction variant="destructive" onClick={(e) => { e.preventDefault(); void deletePicked() }}>Delete</AlertDialogAction>
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
        <AccessMatrix people={shownPeople} allPeople={people} workspaces={shownWorkspaces} actions={actions} busy={busy} />
      </>
    )
  } else {
    body = (
      <>
        <SettingsSummary>
          <SummaryItem n={shownPeople.length}>of {people.length} people</SummaryItem>
          <SummaryItem n={workspaces.length - personalAll.length}>team workspaces</SummaryItem>
          <SummaryItem n={personalAll.length}>personal</SummaryItem>
          {attention > 0 && <SummaryItem n={attention} tone="warn">need attention</SummaryItem>}
          <SummaryItem n={admins}>{admins === 1 ? "instance admin" : "instance admins"}</SummaryItem>
        </SettingsSummary>
        {facet === "all" && !q && (
          <MembersAttention findings={peopleFindings({
            people, workspaces, actions, onOpen: openPerson,
            onFacet: (f) => update({ view: "people", facet: f, person: "", ws: "" }),
            onPersonal: () => showView("workspaces"),
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
 * role in a team workspace, or suspend them. Each person is one call; the
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
      className="fixed bottom-6 left-1/2 z-40 flex -translate-x-1/2 items-center gap-2 rounded-xl border border-border bg-popover px-3 py-2 shadow-lg">
      <span className="px-1 text-xs"><span className="font-mono font-semibold tabular-nums">{people.length}</span> selected</span>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant="outline" size="sm" className="h-8 gap-1 px-3 text-xs" disabled={workspaces.length === 0}><Building2 />Add to workspace<ChevronDown className="h-3 w-3" /></Button>
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
      <Button variant="outline" size="sm" className="h-8 gap-1 px-3 text-xs" onClick={() => setSuspending(true)}><Ban />Suspend</Button>
      <Button variant="ghost" size="icon-sm" className="h-8 w-8" aria-label="Clear selection" onClick={onClear}><X className="h-3.5 w-3.5" /></Button>
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
