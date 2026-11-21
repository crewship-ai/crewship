"use client"

import * as React from "react"
import { Ban, Building2, ChevronDown, Download, Grid3x3, House, Pause, ShieldCheck, Shield, Trash2, TriangleAlert, UserPlus, Users, X, ArrowLeft } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { PageBanner } from "@/components/layout/app-banners"
import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { Skeleton } from "@/components/ui/skeleton"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"
import { SettingsCard, selectionBar, selectionBarAction, selectionBarButton } from "@/components/features/settings/shared"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  STATUS_FACETS, busiestWorkspaces, facetMatches, handGroups, handPeopleCount, isEmptyWorkspace, isPersonal, membersOf,
  peopleCsv, personMatches, personalOwnerName, roleIn, sortPeople, sortWorkspaces, workspaceMatches, workspacesCsv,
  type Facet, type PeopleOrder, type Person, type View, type WorkspaceOrder,
} from "./people-model"
import { AccessMatrix, HandView, PeopleTable, PersonalWorkspacesTable, WorkspacesTable, useMatrixMax } from "./people-views"
import { PersonProfile } from "./person-profile"
import { WorkspaceProfile } from "./workspace-profile"
import { AddPersonDialog, AddToWorkspaceDialog, NewWorkspaceDialog, OwnershipConflictDialog, SetupLinkDialog, SuspendConfirm } from "./people-dialogs"
import { ChooseWorkspacesDialog } from "./people-pickers"
import { parseUrl, writeUrl, type UrlState } from "./people-url"
import { usePeople, type SetupLink } from "./use-people"
import { toast } from "@/lib/toast"

const readUrl = (): UrlState => parseUrl(typeof window === "undefined" ? "" : window.location.search)

const FACET_LABEL: Record<Exclude<Facet, "all">, string> = Object.fromEntries(STATUS_FACETS.map((f) => [f.key, f.label])) as never

/** Downloads the text as a file; the rows are already in the browser. */
function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/csv" }))
  const a = document.createElement("a")
  a.href = url
  a.download = `${name}-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

/** The row over a card's table: what it lists, how to order it, Export. */
function CardToolbar({ title, sub, children }: { title: React.ReactNode; sub?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <div data-slot="card-toolbar" className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border px-4 py-3">
      <div className="min-w-0">
        <h3 className="text-control font-semibold">{title}</h3>
        {sub && <p className="text-label text-muted-foreground">{sub}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-2">{children}</div>
    </div>
  )
}

/**
 * Admin › People & workspaces. One page for who can sign in and where they
 * work, built like Settings › Crew links and the other two Admin pages: "←
 * Admin", the page's own panel (Views, a short list of places with "All N…",
 * the Status and Workspace filters), and the content in the middle — a table,
 * or one person's or one workspace's page. Every list is held whole in the
 * browser and paged with "Show more".
 */
/** "1 person", "12 people". */
function plural(n: number, one: string, many: string): string {
  return `${n.toLocaleString("en")} ${n === 1 ? one : many}`
}

export function PeoplePage() {
  const { workspaceId, loading: workspaceLoading } = useWorkspace()
  const { session } = useAuth()
  const data = usePeople(workspaceId, workspaceLoading)
  const { people, workspaces, loading, error, busy, actions, reload, scope, conflict, clearConflict } = data
  const [state, setState] = React.useState(readUrl)
  const [dialog, setDialog] = React.useState<"person" | "workspace" | null>(null)
  const [picked, setPicked] = React.useState<Set<string>>(() => new Set())
  const [pickedWs, setPickedWs] = React.useState<Set<string>>(() => new Set())
  const [addTo, setAddTo] = React.useState<Person[] | null>(null)
  const [suspending, setSuspending] = React.useState(false)
  const [removeEmpty, setRemoveEmpty] = React.useState<string[] | null>(null)
  const [link, setLink] = React.useState<(SetupLink & { userId?: string }) | null>(null)
  const [cols, setCols] = React.useState<string[] | null>(null)
  const [choosing, setChoosing] = React.useState<"columns" | "workspace" | null>(null)
  const matrixMax = useMatrixMax()
  const { view, facet, q } = state
  const meId = session?.user.id

  // The address bar is written from the handler, not inside a state updater.
  const stateRef = React.useRef(state)
  const update = (next: Partial<UrlState>) => {
    const merged = { ...stateRef.current, ...next }
    stateRef.current = merged
    setState(merged)
    window.history.replaceState(window.history.state, "", writeUrl(merged, window.location.href))
    // What was ticked belongs to the list it was ticked in.
    if ("view" in next || "facet" in next || "q" in next || "in" in next) { setPicked(new Set()); setPickedWs(new Set()) }
  }
  const openPerson = (id: string) => update({ person: id, ws: "", from: state.person ? state.from : view })
  const openWorkspace = (id: string) => update({ ws: id, person: "" })
  const showView = (v: View, extra: Partial<UrlState> = {}) => update({ view: v, person: "", ws: "", facet: "all", in: "", ...extra })

  const person = people.find((p) => p.id === state.person)
  const ws = workspaces.find((w) => w.id === state.ws)
  const teams = workspaces.filter((w) => !isPersonal(w))
  const personalAll = workspaces.filter(isPersonal)
  const groups = handGroups(people, workspaces)
  const toggle = (set: React.Dispatch<React.SetStateAction<Set<string>>>) => (ids: string[], on: boolean) =>
    set((prev) => { const n = new Set(prev); for (const id of ids) { if (on) n.add(id); else n.delete(id) } return n })

  // ── The lists ───────────────────────────────────────────────────────────
  const inWs = state.in && workspaces.some((w) => w.id === state.in) ? state.in : ""
  const base = view === "admins" ? people.filter((p) => p.instance_admin) : view === "suspended" ? people.filter((p) => !!p.suspended_at) : people
  const filtered = base.filter((p) => facetMatches(p, facet) && personMatches(p, q) && (!inWs || roleIn(p, inWs) !== null))
  const shownPeople = sortPeople(filtered, state.sort)
  const shownTeams = sortWorkspaces(teams.filter((w) => workspaceMatches(w, q, people)), state.wsort)
  const personalShown = personalAll.filter((w) => workspaceMatches(w, q, people))
    .sort((a, b) => personalOwnerName(a, people).localeCompare(personalOwnerName(b, people)))
  const filterCount = Number(facet !== "all") + Number(Boolean(inWs))
  const filterKey = `${view}|${facet}|${q}|${inWs}|${state.sort}|${state.wsort}`

  // The access grid: the busiest workspaces unless some were chosen.
  const colCap = Math.min(matrixMax, 5)
  const columns = (cols ?? busiestWorkspaces(teams, colCap).map((w) => w.id)).map((id) => teams.find((w) => w.id === id)).filter((w): w is NonNullable<typeof w> => !!w).slice(0, matrixMax)
  const colIds = new Set(columns.map((w) => w.id))
  const matrixPeople = [...shownPeople].sort((a, b) => Number(!a.memberships.some((m) => colIds.has(m.workspace_id))) - Number(!b.memberships.some((m) => colIds.has(m.workspace_id))))

  const removeEmptyNow = async (ids: string[]) => {
    const targets = workspaces.filter((w) => ids.includes(w.id) && isPersonal(w) && isEmptyWorkspace(w))
    let done = 0
    for (const w of targets) if (await actions.deleteWorkspace(w.id, w.slug, "")) done++
    // Every one refused: the confirmation stays open (each refusal toasted).
    if (targets.length > 0 && done === 0) throw new Error("nothing removed")
    setPickedWs(new Set())
    if (done) toast.success(`Removed ${done} empty personal ${done === 1 ? "account" : "accounts"}`)
  }
  const suspendPicked = async (reason: string) => {
    const targets = people.filter((p) => picked.has(p.id))
    let ok = 0
    for (const p of targets) if (await actions.suspend(p.id, reason, "")) ok++
    // Every one refused: the confirmation stays open (each refusal toasted).
    if (ok === 0) throw new Error("nobody suspended")
    toast.success(`Suspended ${ok} of ${targets.length}`)
    setPicked(new Set())
  }
  const newLink = async (p: Person) => {
    const l = await actions.issueLink(p.id, p.email)
    if (l) setLink({ ...l, userId: p.id })
  }

  // ── Side panel ──────────────────────────────────────────────────────────
  const here = !person && !ws
  const count = (list: Person[]) => list.filter((p) => personMatches(p, q)).length
  const hand = handPeopleCount(groups) + groups.emptyAccounts.length
  const busiest = busiestWorkspaces(teams.filter((w) => workspaceMatches(w, q, people)))
  const view_ = (v: View, icon: React.ReactNode, label: string, meta: React.ReactNode, i: number) => (
    <DrillNavItem key={v} index={i} selected={here && view === v} onSelect={() => showView(v)} icon={icon} label={label} meta={meta} muted={meta === 0} />
  )
  const ic = (Icon: React.ElementType) => <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />

  const nav = (
    <>
      <DrillNavSection collapsible={false} label="People">
        {view_("people", ic(Users), "Everyone", count(people), 0)}
        {view_("hand", ic(TriangleAlert), "Needs a hand", hand, 1)}
        {view_("admins", ic(ShieldCheck), "Instance admins", count(people.filter((p) => p.instance_admin)), 2)}
        {view_("suspended", ic(Pause), "Suspended", count(people.filter((p) => p.suspended_at)), 3)}
      </DrillNavSection>
      <DrillNavSection collapsible={false} label="Workspaces">
        {view_("workspaces", ic(Building2), "All workspaces", teams.length, 0)}
        {view_("accounts", ic(House), "Personal accounts", q.trim() ? `${personalShown.length}/${personalAll.length}` : personalAll.length, 1)}
        {view_("access", ic(Grid3x3), "Who is where", "", 2)}
      </DrillNavSection>
      <DrillNavSection collapsible={false} label="Busiest this week">
        {busiest.map((w, i) => (
          <DrillNavItem key={w.id} index={i} selected={ws?.id === w.id} onSelect={() => openWorkspace(w.id)} title={`${w.name} · ${w.slug}`}
            icon={<WorkspaceTile id={w.id} name={w.name} size="xs" logoUrl={w.logo_url} />} label={w.name} meta={membersOf(people, w.id).length} />
        ))}
        {teams.length > busiest.length && (
          <DrillNavItem index={busiest.length} onSelect={() => showView("workspaces")}
            label={<span className="text-primary-hover">All {teams.length} workspaces…</span>} />
        )}
      </DrillNavSection>
    </>
  )

  // Status and Workspace narrow the people lists, so they live in Filter.
  const topFor = [...teams].sort((a, b) => membersOf(people, b.id).length - membersOf(people, a.id).length).slice(0, 6)
  const facetWs = inWs && !topFor.some((w) => w.id === inWs) ? [...topFor, teams.find((w) => w.id === inWs)!].filter(Boolean) : topFor
  const toolbar = (
    <>
      <SidebarSearch value={q} onValueChange={(v) => update({ q: v })} placeholder="Search people, workspaces…" aria-label="Search people and workspaces" />
      {(view === "people" || view === "access") && here && (
        <SidebarFilterPopover label="Filter people" activeCount={filterCount} onClear={() => update({ facet: "all", in: "" })} panelClassName="min-w-[240px]">
          <SidebarFacet label="Status" resetLabel="Any status" resetActive={facet === "all"} onReset={() => update({ facet: "all" })} first>
            {STATUS_FACETS.map((f) => (
              <SidebarFacetOption key={f.key} active={facet === f.key} onToggle={() => update({ facet: facet === f.key ? "all" : f.key })}>
                {f.label}
                <span className="ml-auto text-micro tabular-nums text-muted-foreground">{people.filter((p) => facetMatches(p, f.key) && personMatches(p, q)).length}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Workspace" resetLabel="Any workspace" resetActive={!inWs} onReset={() => update({ in: "" })}>
            {facetWs.map((w) => (
              <SidebarFacetOption key={w.id} active={inWs === w.id} onToggle={() => update({ in: inWs === w.id ? "" : w.id })}>
                <span className="truncate">{w.name}</span>
                <span className="ml-auto text-micro tabular-nums text-muted-foreground">{membersOf(people, w.id).length}</span>
              </SidebarFacetOption>
            ))}
            {teams.length > facetWs.length && (
              <SidebarFacetOption active={false} onToggle={() => setChoosing("workspace")}>
                <span className="text-primary-hover">All {teams.length}…</span>
              </SidebarFacetOption>
            )}
          </SidebarFacet>
        </SidebarFilterPopover>
      )}
    </>
  )

  // ── Content ─────────────────────────────────────────────────────────────
  const backTo = (label: string, to: View) => (
    <Button type="button" variant="ghost" size="sm" className="-ml-2 w-fit text-muted-foreground" onClick={() => update({ person: "", ws: "", view: to })}>
      <ArrowLeft />{label}
    </Button>
  )
  const filterNote = [facet !== "all" ? FACET_LABEL[facet] : "", inWs ? `in ${workspaces.find((w) => w.id === inWs)?.name ?? "a workspace"}` : "", q.trim() ? `“${q.trim()}”` : ""].filter(Boolean).join(" · ")
  const clearFilters = (
    <Button size="sm" variant="ghost" onClick={() => update({ facet: "all", in: "", q: "" })}>Clear filters</Button>
  )

  const teamAgents = teams.reduce((a, w) => a + (w._count_agents ?? 0), 0)
  const description: string = person || ws ? "" : {
    people: `${plural(people.length, "person", "people")} · ${plural(teams.length, "workspace", "workspaces")} · ${plural(personalAll.length, "personal account", "personal accounts")}`,
    hand: "People who cannot get in, and accounts nobody uses. Most pressing first.",
    admins: "People who run this server: other people, every workspace, limits and backups.",
    suspended: "Accounts that cannot sign in. Their data and memberships are kept.",
    workspaces: `${plural(teams.length, "workspace", "workspaces")} · ${plural(people.length, "person", "people")} · ${plural(teamAgents, "agent", "agents")}`,
    accounts: "Made when someone signs up without an invitation. One person each, apart from the workspaces.",
    access: "Click a cell to change a role, or to give or take away access. An only owner stays until the workspace is handed over.",
  }[view]

  let body: React.ReactNode
  if (loading) {
    body = <div className="space-y-3"><Skeleton className="h-8 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
  } else if (error) {
    body = <PageBanner tone="bad" icon={TriangleAlert} action={<Button variant="outline" size="sm" onClick={() => void reload()}>Retry</Button>}>{error}</PageBanner>
  } else if (person) {
    // An instance admin may belong to no workspace: the page opens all the
    // same, and what is per workspace picks one of the person's (below).
    body = (
      <>
        {backTo("People", state.from)}
        <PersonProfile key={person.id} person={person} people={people} workspaces={workspaces} workspaceId={workspaceId ?? ""} meId={meId} busy={busy} actions={actions}
          onChanged={() => void reload()} onAddToWorkspace={(p) => setAddTo([p])} onShowLink={(l) => setLink({ ...l, userId: person.id })} showAudit={scope !== "workspace"} />
      </>
    )
  } else if (ws) {
    const personal = isPersonal(ws)
    body = (
      <>
        {backTo(personal ? "Personal accounts" : "Workspaces", personal ? "accounts" : "workspaces")}
        <WorkspaceProfile key={ws.id} ws={ws} people={people} busy={busy} actions={actions} onOpenPerson={openPerson}
          onShowLink={(l) => setLink(l)} showAudit={scope !== "workspace"}
          onDeleted={() => showView(personal ? "accounts" : "workspaces")} />
      </>
    )
  } else if (view === "hand") {
    body = (
      <HandView groups={groups} actions={actions} busy={busy} onOpen={openPerson}
        onFilter={(f) => showView("people", { facet: f })} onAccounts={() => showView("accounts")}
        onAddToWorkspace={(p) => setAddTo([p])} onRemoveEmpty={() => setRemoveEmpty(groups.emptyAccounts.map((w) => w.id))} onNewLink={(p) => void newLink(p)} />
    )
  } else if (view === "workspaces") {
    body = (
      <SettingsCard title="Workspaces" hideHeader>
        <CardToolbar title={`${shownTeams.length} ${shownTeams.length === 1 ? "workspace" : "workspaces"}`} sub={q.trim() ? `of ${teams.length} · “${q.trim()}”` : "Where people work together"}>
          <Segmented<WorkspaceOrder> ariaLabel="Order workspaces" value={state.wsort} onChange={(wsort) => update({ wsort })}
            options={[{ value: "busy", label: "Busiest" }, { value: "az", label: "A–Z" }, { value: "new", label: "Newest" }]} />
          <Button size="sm" variant="ghost" onClick={() => download("workspaces", workspacesCsv(shownTeams, people))}><Download />Export CSV</Button>
        </CardToolbar>
        <WorkspacesTable workspaces={shownTeams} people={people} onOpen={openWorkspace} resetKey={filterKey} />
      </SettingsCard>
    )
  } else if (view === "accounts") {
    const empties = personalShown.filter(isEmptyWorkspace)
    body = (
      <SettingsCard title="Personal accounts" hideHeader>
        <CardToolbar title={`${personalShown.length} personal ${personalShown.length === 1 ? "account" : "accounts"}`} sub={`${empties.length} empty`}>
          {pickedWs.size > 0 ? (
            <Button size="sm" variant="outline" className="border-destructive/40 text-destructive" onClick={() => setRemoveEmpty([...pickedWs])}>
              <Trash2 />Remove {pickedWs.size} empty
            </Button>
          ) : (
            <Button size="sm" variant="outline" disabled={empties.length === 0} onClick={() => setPickedWs(new Set(empties.map((w) => w.id)))}>Select all empty</Button>
          )}
        </CardToolbar>
        <PersonalWorkspacesTable workspaces={personalShown} people={people} onOpen={openWorkspace} selected={pickedWs} onSelect={toggle(setPickedWs)}
          logos={new Map(workspaces.map((w) => [w.id, w.logo_url]))} resetKey={filterKey} />
        <p className="border-t border-border px-4 py-2.5 text-label text-muted-foreground">
          Only empty ones can be removed — no crews, no agents, no runs. The person keeps signing in and keeps every workspace they belong to. Open one to turn it into a workspace instead.
        </p>
      </SettingsCard>
    )
  } else if (view === "access") {
    body = (
      <SettingsCard title="Who is where" hideHeader>
        <CardToolbar title={`${matrixPeople.length} people × ${columns.length} of ${teams.length} workspaces`} sub={filterNote || undefined}>
          {filterCount > 0 && clearFilters}
          <Button size="sm" variant="outline" onClick={() => setChoosing("columns")}><Building2 />Choose workspaces</Button>
        </CardToolbar>
        <AccessMatrix people={matrixPeople} allPeople={people} columns={columns} actions={actions} busy={busy} resetKey={filterKey} onOpenPerson={openPerson} />
      </SettingsCard>
    )
  } else {
    const noun = view === "admins" ? "instance admins" : view === "suspended" ? "suspended" : "people"
    const title = view === "people"
      ? (shownPeople.length === base.length ? plural(base.length, "person", "people") : `${shownPeople.length} of ${base.length} people`)
      : `${shownPeople.length} ${noun}`
    const needy = handPeopleCount(groups)
    body = (
      <>
        {view === "people" && facet === "all" && !q.trim() && !inWs && needy > 0 && (
          <PageBanner tone="warn" icon={TriangleAlert} action={<Button size="sm" variant="outline" onClick={() => showView("hand")}>Review</Button>}>
            <b>{needy} {needy === 1 ? "person needs" : "people need"} a hand</b>
            <br />
            <span className="text-muted-foreground">{groups.locked.length} locked out · {groups.setup.length} without a password · {groups.nowhere.length} in no workspace</span>
          </PageBanner>
        )}
        <SettingsCard title="People" hideHeader>
          <CardToolbar title={title} sub={filterNote || undefined}>
            {filterCount > 0 && clearFilters}
            <Segmented<PeopleOrder> ariaLabel="Order people" value={state.sort} onChange={(sort) => update({ sort })}
              options={[{ value: "hand", label: "Needs a hand first" }, { value: "az", label: "A–Z" }, { value: "recent", label: "Last active" }]} />
            <Button size="sm" variant="ghost" onClick={() => download("people", peopleCsv(shownPeople))}><Download />Export CSV</Button>
          </CardToolbar>
          <PeopleTable people={shownPeople} workspaces={workspaces} onOpen={openPerson} resetKey={filterKey}
            selected={picked} onSelect={toggle(setPicked)} selectable={(p) => p.id !== meId && p.instance_admin_source !== "env"} />
        </SettingsCard>
        {picked.size > 0 && (
          <div role="toolbar" aria-label="Change the selected people" data-slot="people-bulk"
            className={cn(selectionBar, "fixed bottom-6 left-1/2 -translate-x-1/2 whitespace-nowrap max-md:bottom-[calc(var(--mobile-tab-bar-h,0px)+0.75rem)] max-md:gap-1.5 max-md:px-2")}>
            <span className="px-1 text-xs"><span className="font-semibold tabular-nums">{picked.size}</span> selected</span>
            <Button size="sm" className={selectionBarAction} disabled={teams.length === 0}
              onClick={() => setAddTo(people.filter((p) => picked.has(p.id)))}><Building2 />Add to workspace<ChevronDown className="h-3 w-3" /></Button>
            <Button variant="outline" size="sm" className={selectionBarButton} onClick={() => setSuspending(true)}><Ban />Suspend</Button>
            <Button variant="ghost" size="icon-sm" className={cn(selectionBarButton, "w-8 px-0")} aria-label="Clear selection" onClick={() => setPicked(new Set())}><X className="h-3.5 w-3.5" /></Button>
          </div>
        )}
      </>
    )
  }

  const removing = removeEmpty ? workspaces.filter((w) => removeEmpty.includes(w.id)) : []
  const suspendTargets = people.filter((p) => picked.has(p.id))

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      panelId="drill-people"
      title="People & workspaces"
      icon={Users}
      description={description || undefined}
      head={here}
      toolbar={toolbar}
      nav={nav}
      actions={
        <>
          {/* Icons only on a phone, so the actions fit the sub-bar. */}
          <Button size="sm" variant="outline" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="New workspace" onClick={() => setDialog("workspace")}>
            <Building2 /><span className="max-md:hidden">New workspace</span>
          </Button>
          <Button size="sm" className="h-7 text-xs max-md:w-8 max-md:px-0" aria-label="Add person" onClick={() => setDialog("person")}>
            <UserPlus /><span className="max-md:hidden">Add person</span>
          </Button>
        </>
      }
    >
      <div className={cn("mx-auto space-y-4 p-4 md:p-6", person || ws ? "max-w-3xl" : "max-w-5xl", here && "pt-4 md:pt-4")}>
        {scope === "workspace" && !loading && (
          <p className="rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
            You see your own workspace only. Instance administrators see and change every workspace.
          </p>
        )}
        {body}
      </div>
      <AddPersonDialog open={dialog === "person"} onOpenChange={(o) => setDialog(o ? "person" : null)} workspaces={workspaces} actions={actions} busy={busy} onCreated={openPerson} />
      <NewWorkspaceDialog open={dialog === "workspace"} onOpenChange={(o) => setDialog(o ? "workspace" : null)} people={people} actions={actions} busy={busy} onCreated={openWorkspace} />
      <AddToWorkspaceDialog open={addTo !== null} onOpenChange={(o) => { if (!o) setAddTo(null) }} people={addTo ?? []} workspaces={teams} actions={actions} busy={busy}
        onDone={() => setPicked(new Set())} />
      <SuspendConfirm open={suspending} onOpenChange={setSuspending} people={suspendTargets} onSuspend={suspendPicked} />
      <SetupLinkDialog link={link} onClose={() => setLink(null)} onOpenProfile={openPerson} />
      <OwnershipConflictDialog conflict={conflict} people={people} workspaces={workspaces} onClose={clearConflict} />
      <ChooseWorkspacesDialog open={choosing === "columns"} onOpenChange={(o) => { if (!o) setChoosing(null) }} workspaces={teams} people={people}
        selected={columns.map((w) => w.id)} max={matrixMax} title="Workspaces to compare" description={`Up to ${matrixMax} columns at a time.`}
        onPick={(ids) => setCols(ids)} />
      <ChooseWorkspacesDialog open={choosing === "workspace"} onOpenChange={(o) => { if (!o) setChoosing(null) }} workspaces={teams} people={people}
        selected={inWs ? [inWs] : []} max={1} title="Workspace" description="Show the people in one workspace." onPick={(ids) => update({ in: ids[0] ?? "" })} />
      <ConfirmDialog open={removeEmpty !== null} onOpenChange={(o) => { if (!o) setRemoveEmpty(null) }} destructive
        title={`Remove ${removing.length} empty personal ${removing.length === 1 ? "account" : "accounts"}?`}
        description={`${removing.slice(0, 6).map((w) => personalOwnerName(w, people)).join(", ")}${removing.length > 6 ? ` and ${removing.length - 6} more` : ""}. This cannot be undone.`}
        consequences={[
          { tone: "lost", text: "The personal accounts picked. Only accounts with no crews, agents or data are removed." },
          { tone: "kept", text: "The people keep signing in and keep every workspace they belong to." },
        ]}
        confirmLabel="Remove"
        onConfirm={() => removeEmptyNow(removeEmpty ?? [])} />
    </DrillPage>
  )
}
