"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import { Download, Mail, Terminal, TriangleAlert, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useShowMore } from "@/hooks/use-show-more"
import { isAdminTier, isManagerTier } from "@/lib/permissions/tiers"
import { ALL_CAPABILITIES, CAPABILITY_BUNDLES, Capability, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { PageBanner } from "@/components/layout/app-banners"
import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { ShowMore } from "@/components/ui/show-more"
import { Skeleton } from "@/components/ui/skeleton"
import { personLabel } from "@/components/ui/user-avatar"
import { Peek } from "@/components/ui/peek"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { DrillNavItem, DrillNavSection } from "@/components/layout/drill-page"
import { SettingsDrillPage } from "@/components/features/settings/settings-drill-page"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"
import { SettingsCard } from "@/components/features/settings/shared"
import { PageError } from "@/components/layout/page-state"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { HIGH_STAKES_CAPABILITIES, useMemberCapabilities } from "@/components/admin/member-capabilities"
import { PageSaveBar, usePageSave } from "@/components/ui/page-save-bar"

import {
  AREAS,
  DEFAULT_MEMBER_FILTERS,
  CAPABILITY_NAME,
  MEMBER_FLAGS,
  PROFILE_LABEL,
  ROLES,
  ROLE_LABEL,
  activeFilterCount,
  byRoleThenName,
  memberMatches,
  membersUrlQuery,
  parseMembersUrl,
  accessProfile,
  profileText,
  roleChoices,
  type Member,
  type MemberFilters,
  type MembersUrlState,
} from "./members-model"
import { useMembers } from "./use-members"
import { useAccessWrites } from "./use-access"
import { MemberDetail } from "./member-detail"
import { ConfirmWhen, ROLE_ICON } from "./member-bits"
import { PeopleList, PeopleTable, joinedDate, type PeopleGroup } from "./people-table"
import { InvitationRows } from "./invitations-card"
import { MembersLook, lookCount, lookGroups, lookSummary, type LookKind } from "./members-look"
import { BulkAbilitiesDialog, BulkBar, type AbilityChange } from "./bulk-bar"
import { invitationStatus, useInvitations, useRevealPolicy } from "./use-invitations"

type GroupBy = "role" | "name"

interface Confirm {
  title: string
  body: React.ReactNode
  action: string
  danger?: boolean
  /** When the change lands: written on confirm, or drafted until Save. */
  when: "now" | "save"
  run: () => Promise<unknown> | void
}

/**
 * Settings › Members: who is here, what each person manages, what they may do
 * and where — for everyone at once.
 *
 * Three words, each one question about a person. ROLE is what they manage
 * (people, crews, settings). ABILITIES are what they may do themselves: nine
 * grants, read as one line — the nearest preset and what differs ("Power user
 * + create skills"). REACH is where it applies: the whole workspace, or only
 * what is shared with a Guest.
 *
 * Three views, named in the panel: Everyone (the roster, one card, grouped by
 * role), Needs a look (one card per kind of finding) and Invited. The panel
 * lists the views and the five roles; abilities, single abilities and reach
 * are facets of the Filter, so it never grows with the workspace.
 *
 *   - one person: click the row — a side panel (full screen on a phone) with
 *     three cards, Role, Abilities and Reach, and the way out;
 *   - many people: tick the rows, then Role or Abilities.
 *
 * Granting anything high-stakes and every role change ask first; ability and
 * reach edits are drafts until Save. The filters live in the URL (?role=
 * ?area= ?cap= ?reach= ?flag= ?status= ?q=); ?member= is what ⌘K and old
 * /settings?tab=members links resolve to, and opens that person.
 */
export function MembersPage() {
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const { session } = useAuth()
  const currentUserId = session?.user?.id
  const reduce = useReducedMotion()
  const isMobile = useIsMobile()
  const roster = useMembers(workspaceId)
  const { members } = roster
  const ws = workspaceId ?? ""

  const isAdmin = isAdminTier(role)
  const isManager = isManagerTier(role)
  const capsReadable = isAdmin && Boolean(currentUserId)
  const capsQuery = useMemberCapabilities(ws, capsReadable && members.length > 0)
  const { data: capsByUser, isLoading: capsLoading } = capsQuery
  // Undefined until the server has said what each person holds: an unread or
  // failed read is never an empty set, because a write sends the whole set
  // and would take away whatever was not shown.
  const serverCaps = React.useMemo(() => (capsReadable && capsByUser ? capsByUser : undefined), [capsReadable, capsByUser])
  const capsFailed = capsReadable && capsQuery.isError
  const access = useAccessWrites(ws, roster.refresh)

  // Ability edits are a draft until Save, like every other editor in
  // Settings (components/ui/page-save-bar): a switch, a preset or a bulk
  // change edits `drafts` — user id → the whole set they would hold — and the
  // page's one floating bar commits them all, or Discard drops them. What the
  // roster, the counts and the filters show is the set WITH the draft.
  //
  // A draft belongs to the workspace it was made in. Switching workspaces asks
  // first (the page's Save guard); should one switch anyway, the drafts of the
  // old workspace are not shown here and cannot be saved into the new one.
  const [draftBook, setDraftBook] = React.useState<{ ws: string; users: Record<string, string[]> }>({ ws, users: {} })
  const drafts = React.useMemo(() => (draftBook.ws === ws ? draftBook.users : {}), [draftBook, ws])
  const setDrafts = (next: (d: Record<string, string[]>) => Record<string, string[]>) =>
    setDraftBook((b) => ({ ws, users: next(b.ws === ws ? b.users : {}) }))
  const caps = React.useMemo(() => (serverCaps ? { ...serverCaps, ...drafts } : undefined), [serverCaps, drafts])
  const pendingCount = Object.entries(drafts).reduce((n, [id, set]) => n + capabilityDiff(serverCaps?.[id], set), 0)
  const stage = (people: Member[], next: (current: string[]) => string[]) => serverCaps && setDrafts((d) => {
    const out = { ...d }
    for (const m of people) {
      // A new member may precede their entry in the capability cache. A
      // missing entry is unknown, so never build a replacement set from it.
      if (!serverCaps[m.user.id]) continue
      const set = canonical(next(out[m.user.id] ?? serverCaps?.[m.user.id] ?? []))
      if (capabilityDiff(serverCaps?.[m.user.id], set) === 0) delete out[m.user.id]
      else out[m.user.id] = set
    }
    return out
  })
  const saveDrafts = async () => {
    const entries = Object.entries(drafts)
    const done = await access.setCapabilities(entries.map(([id, set]) => [members.find((m) => m.user.id === id), set] as const).filter((e): e is readonly [Member, string[]] => Boolean(e[0])))
    setDrafts((d) => { const out = { ...d }; for (const id of done) delete out[id]; return out })
    if (done.length < entries.length) throw new Error(`${entries.length - done.length} of ${entries.length} people could not be changed`)
  }
  const busy = access.busy

  const [state, setState] = useMembersUrl()
  const { filters } = state
  const setFilters = (next: Partial<MemberFilters>) => setState({ ...state, filters: { ...filters, ...next } })
  // The open person is local state, not the URL. ?member= (⌘K, old
  // /settings?tab=members links) is read on arrival and then dropped: the
  // sheet keeps its own history entry for the back button, and writing the URL
  // under it meant its unwind restored ?member= and reopened the person.
  const [openedId, setOpenedId] = React.useState("")
  React.useEffect(() => {
    if (!state.member) return
    setOpenedId(state.member)
    setState({ ...state, member: "" })
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.member])
  const open = (m: Member | null) => setOpenedId(m?.user.id ?? "")
  const [groupBy, setGroupBy] = React.useState<GroupBy>("role")
  const [selected, setSelected] = React.useState<Set<string>>(() => new Set())
  const [confirm, setConfirm] = React.useState<Confirm | null>(null)
  const [bulkAbilities, setBulkAbilities] = React.useState(false)

  const sorted = React.useMemo(() => [...members].sort(byRoleThenName), [members])
  const shown = sorted.filter((m) => memberMatches(m, filters, caps))
  const countWith = (f: Partial<MemberFilters>) => sorted.filter((m) => memberMatches(m, { ...filters, ...f }, caps)).length
  const opened = members.find((m) => m.user.id === openedId) ?? null
  const selectedPeople = members.filter((m) => selected.has(m.user.id))
  const nameOf = (m: Member) => personLabel(m.user.full_name, m.user.email)

  // A selection only means something for people still on screen.
  React.useEffect(() => {
    setSelected((prev) => {
      const visible = new Set(shown.map((m) => m.user.id))
      const next = new Set([...prev].filter((id) => visible.has(id)))
      return next.size === prev.size ? prev : next
    })
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown.length, filters.q, filters.role, filters.area, filters.flag, filters.status, filters.cap, filters.reach])

  const invitationsQuery = useInvitations(workspaceId, isManager)
  const invitations = React.useMemo(() => invitationsQuery.data ?? [], [invitationsQuery.data])
  // A revoked invitation was withdrawn: it stays listed (with its status) but
  // is not "invited" any more.
  const invitedCount = invitations.filter((i) => invitationStatus(i) !== "revoked").length
  const revealEnabled = useRevealPolicy(workspaceId, isManager).data ?? null

  // Which view the page shows. A view a role may not open falls back to the
  // roster instead of an empty card.
  const view: "everyone" | "look" | "invited" =
    filters.status === "invited" && isManager ? "invited" : filters.status === "look" && capsReadable ? "look" : "everyone"

  const order = React.useMemo(() => (groupBy === "name"
    ? [...shown].sort((a, b) => nameOf(a).localeCompare(nameOf(b)))
    : ROLES.flatMap((r) => shown.filter((m) => m.role === r))),
  [groupBy, shown])
  // 25 at a time, from the top again whenever the filters or the grouping change.
  const paged = useShowMore(order, { resetKey: JSON.stringify([filters, groupBy]) })
  const groups: PeopleGroup[] = React.useMemo(() => {
    if (groupBy === "name") return paged.shown.length ? [{ key: "all", label: "", people: paged.shown, total: order.length }] : []
    return ROLES.map((r) => ({
      key: r, label: `${ROLE_LABEL[r]}s`,
      people: paged.shown.filter((m) => m.role === r),
      // The header counts the whole group, not the page.
      total: order.filter((m) => m.role === r).length,
    })).filter((g) => g.people.length > 0)
  }, [groupBy, paged.shown, order])

  // ── Writes, each asking first when it should ─────────────────────────

  // Asking first is kept for one thing: putting a high-stakes grant into the
  // draft. Everything else is undone by Discard.
  const askHighStakes = (title: string, body: React.ReactNode, action: string, gains: CapabilityValue[], run: () => void) => {
    if (gains.length === 0) { run(); return }
    setConfirm({ title, body, action, danger: true, when: "save", run })
  }

  const onCell = (m: Member, cap: CapabilityValue, next: boolean) => {
    const label = CAPABILITY_NAME[cap]
    askHighStakes(`Give ${label}?`,
      <><span className="font-medium text-foreground">{nameOf(m)}</span> will be able to: {label.toLowerCase()}.</>,
      `Give ${label}`, next && HIGH_STAKES_CAPABILITIES.has(cap) ? [cap] : [],
      () => stage([m], (cur) => (next ? [...cur, cap] : cur.filter((c) => c !== cap))))
  }

  const highStakesGains = (people: Member[], add: readonly string[]) =>
    [...new Set(add.filter((c) => HIGH_STAKES_CAPABILITIES.has(c as CapabilityValue) && people.some((m) => !(caps?.[m.user.id] ?? []).includes(c))))] as CapabilityValue[]

  const gainText = (gains: CapabilityValue[]) => gains.map((c) => CAPABILITY_NAME[c]).join(", ")

  const onProfile = (m: Member, preset: CapabilityBundle) => {
    const gains = highStakesGains([m], CAPABILITY_BUNDLES[preset])
    askHighStakes(`Set ${nameOf(m)} to ${PROFILE_LABEL[preset]}?`,
      <>This gives high-stakes abilities: {gainText(gains)}.</>,
      `Set to ${PROFILE_LABEL[preset]}`, gains, () => stage([m], () => [...CAPABILITY_BUNDLES[preset]]))
  }

  /** Take high-stakes abilities away (the panel's "Turn it off"): a draft, no
   *  question — taking access away is what Discard undoes. */
  const onRevoke = (m: Member, revoke: CapabilityValue[]) => stage([m], (cur) => cur.filter((c) => !revoke.includes(c as CapabilityValue)))

  const clearSelection = () => setSelected(new Set())
  const bulk = {
    /** The merged Abilities dialog: a preset for everyone, and/or single
     *  abilities given or taken away. Same draft, same high-stakes question. */
    abilities: (change: AbilityChange) => {
      const people = selectedPeople
      const next = (cur: string[]) => {
        const base = change.preset ? [...CAPABILITY_BUNDLES[change.preset]] : cur
        return [...base, ...change.give].filter((c) => !change.take.includes(c as CapabilityValue))
      }
      const gains = [...new Set(people.flatMap((m) => {
        const cur = caps?.[m.user.id] ?? []
        return next(cur).filter((c) => HIGH_STAKES_CAPABILITIES.has(c as CapabilityValue) && !cur.includes(c))
      }))] as CapabilityValue[]
      askHighStakes(`Change abilities for ${people.length} ${people.length === 1 ? "person" : "people"}?`,
        <>This gives high-stakes abilities: {gainText(gains)}.</>,
        "Apply", gains, () => { stage(people, next); clearSelection() })
    },
    role: (r: string) => {
      const eligible = selectedPeople.filter((m) => m.role !== r && roleChoices(role, m, m.user.id === currentUserId).includes(r))
      setConfirm({
        title: `Make ${eligible.length} ${eligible.length === 1 ? "person" : "people"} ${ROLE_LABEL[r]}?`,
        body: <>
          {eligible.length < selectedPeople.length && <>{selectedPeople.length - eligible.length} of the selected already have this role{r === "ADMIN" && selectedPeople.some((m) => m.access_mode === "restricted") ? ", are Guests (who cannot be administrators)" : ""} or are above yours, and stay as they are. </>}
          Their abilities do not change. You can only give roles below your own.
        </>,
        action: "Change role",
        when: "now",
        run: async () => { await access.changeRole(eligible, r); clearSelection() },
      })
    },
  }

  const exportCsv = () => {
    const cols = ALL_CAPABILITIES.filter((c) => c !== Capability.Chat)
    const cell = (v: string) => `"${v.replace(/"/g, '""')}"`
    const lines = [
      ["Name", "Email", "Role", ...(caps ? ["Reach", "Abilities", ...cols.map((c) => CAPABILITY_NAME[c])] : []), "Joined"].map(cell).join(","),
      ...shown.map((m) => [
        m.user.full_name ?? "", m.user.email, ROLE_LABEL[m.role] ?? m.role,
        ...(caps ? [m.access_mode === "restricted" ? "Guest" : m.access_mode ? "Whole workspace" : "", m.role === "OWNER" ? "Everything" : profileText(accessProfile(caps[m.user.id])), ...cols.map((c) => ((caps[m.user.id] ?? []).includes(c) ? "yes" : ""))] : []),
        m.created_at.slice(0, 10),
      ].map(cell).join(",")),
    ].join("\n")
    const url = URL.createObjectURL(new Blob([lines], { type: "text/csv" }))
    const a = document.createElement("a")
    a.href = url
    a.download = `members-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  // ── Side panel ───────────────────────────────────────────────────────

  const pick = (next: Partial<MemberFilters>) => setFilters({ ...next, status: "" })
  const toggle = <K extends "area" | "cap" | "reach">(key: K, value: MemberFilters[K]) => pick({ [key]: filters[key] === value ? "" : value } as Partial<MemberFilters>)
  const activeFilters = activeFilterCount(filters, capsReadable) - Number(Boolean(filters.status))
  const toolbar = (
    <>
      <SidebarSearch value={filters.q} onValueChange={(q) => setFilters({ q })} placeholder="Search name or email…" aria-label="Search members" />
      {capsReadable && (
        <SidebarFilterPopover label="Filter" activeCount={Number(Boolean(filters.cap)) + Number(Boolean(filters.area)) + Number(Boolean(filters.reach))}
          onClear={() => pick({ cap: "", area: "", reach: "" })} panelClassName="min-w-[260px]">
          <SidebarFacet label="Abilities" resetLabel="Any" resetActive={!filters.area} onReset={() => pick({ area: "" })} first>
            {[...AREAS.map((a) => ({ key: a.key as string, label: a.label })), { key: "chat", label: "Chat only" }].map((a) => (
              <SidebarFacetOption key={a.key} active={filters.area === a.key} onToggle={() => toggle("area", a.key)}>
                {a.label}
                <span className="ml-auto text-micro tabular-nums text-muted-foreground">{capsLoading ? "…" : countWith({ area: a.key })}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Single ability" resetLabel="Any" resetActive={!filters.cap} onReset={() => pick({ cap: "" })}>
            {ALL_CAPABILITIES.filter((c) => c !== Capability.Chat).map((cap) => (
              <SidebarFacetOption key={cap} active={filters.cap === cap} onToggle={() => toggle("cap", cap)}>
                <span className={cn(HIGH_STAKES_CAPABILITIES.has(cap) && "text-destructive")}>{CAPABILITY_NAME[cap]}</span>
                <span className="ml-auto text-micro tabular-nums text-muted-foreground">{capsLoading ? "…" : countWith({ cap })}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Reach" resetLabel="Any" resetActive={!filters.reach} onReset={() => pick({ reach: "" })}>
            {([["whole", "Whole workspace"], ["guest", "Guest"]] as const).map(([key, label]) => (
              <SidebarFacetOption key={key} active={filters.reach === key} onToggle={() => toggle("reach", key)}>
                {label}
                <span className="ml-auto text-micro tabular-nums text-muted-foreground">{countWith({ reach: key })}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
        </SidebarFilterPopover>
      )}
    </>
  )

  const ico = (Icon: React.ElementType, cls = "text-muted-foreground") => <Icon className={cn("h-3.5 w-3.5 shrink-0", cls)} aria-hidden />
  const findings = capsReadable && caps && !capsLoading ? lookGroups({ members, caps: caps as Record<string, string[]>, invitations, revealEnabled }) : null
  const lookTotal = findings ? lookCount(findings) : 0
  const nav = (
    <>
      <DrillNavSection collapsible={false} label="People">
        <DrillNavItem index={0} selected={view === "everyone" && !filters.role} onSelect={() => setFilters({ ...DEFAULT_MEMBER_FILTERS, q: filters.q })}
          icon={ico(Users)} label="Everyone" meta={members.length} />
        {capsReadable && (
          <DrillNavItem index={1} selected={view === "look"} pressed={view === "look"} muted={!lookTotal}
            onSelect={() => setFilters({ status: view === "look" ? "" : "look", flag: "" })}
            icon={ico(TriangleAlert)} label="Needs a look" meta={findings ? lookTotal || "" : "…"} />
        )}
        {isManager && (
          <DrillNavItem index={2} selected={view === "invited"} pressed={view === "invited"} muted={!invitedCount}
            onSelect={() => setFilters({ status: view === "invited" ? "" : "invited", flag: "" })}
            icon={ico(Mail)} label="Invited" meta={invitationsQuery.isLoading ? "…" : invitedCount} />
        )}
      </DrillNavSection>
      <DrillNavSection collapsible={false} label="Role">
        {ROLES.map((r, i) => {
          const n = countWith({ role: r })
          return (
            <DrillNavItem key={r} index={i} selected={view === "everyone" && filters.role === r} pressed={filters.role === r} muted={!n}
              onSelect={() => pick({ role: filters.role === r ? "" : r })} icon={ico(ROLE_ICON[r])} label={ROLE_LABEL[r]} meta={n} />
          )
        })}
      </DrillNavSection>
    </>
  )

  // ── Main ─────────────────────────────────────────────────────────────

  const chips: { key: string; label: string; clear: Partial<MemberFilters> }[] = []
  if (filters.q.trim()) chips.push({ key: "q", label: `“${filters.q.trim()}”`, clear: { q: "" } })
  if (filters.role) chips.push({ key: "role", label: `Role: ${ROLE_LABEL[filters.role]}`, clear: { role: "" } })
  if (filters.area && capsReadable) chips.push({ key: "area", label: `Abilities: ${filters.area === "chat" ? "Chat only" : AREAS.find((a) => a.key === filters.area)?.label ?? filters.area}`, clear: { area: "" } })
  if (filters.cap && capsReadable) chips.push({ key: "cap", label: `Ability: ${CAPABILITY_NAME[filters.cap] ?? filters.cap}`, clear: { cap: "" } })
  if (filters.reach && capsReadable) chips.push({ key: "reach", label: `Reach: ${filters.reach === "guest" ? "Guest" : "Whole workspace"}`, clear: { reach: "" } })
  if (filters.flag && capsReadable) chips.push({ key: "flag", label: MEMBER_FLAGS.find((f) => f.key === filters.flag)?.label ?? filters.flag, clear: { flag: "" } })

  // J / K in the Peek walk the people in the order the roster shows them.
  const at = opened ? order.findIndex((m) => m.user.id === opened.user.id) : -1
  const prev = at > 0 ? order[at - 1] : null
  const next = at >= 0 && at < order.length - 1 ? order[at + 1] : null

  const showAll = (kind: LookKind) => (kind === "invites"
    ? setFilters({ ...DEFAULT_MEMBER_FILTERS, status: "invited" })
    : setFilters({ ...DEFAULT_MEMBER_FILTERS, flag: kind === "reveal" ? "reveal" : "risky-low-role" }))
  const revealCandidates = caps ? sorted.filter((m) => m.role !== "OWNER" && m.user.id !== currentUserId && !(caps[m.user.id] ?? []).includes(Capability.CredentialReveal)) : []

  const addMember = isAdmin && workspaceId ? <InviteMemberDialog workspaceId={workspaceId} callerRole={role} onInvited={roster.refresh} /> : null

  const headline = view === "look" ? "Access worth a second pair of eyes. Nothing here is broken."
    : view === "invited" ? "People who were sent a link and have not joined yet"
    : `${members.length} ${members.length === 1 ? "person" : "people"}${isManager && !invitationsQuery.isLoading && invitedCount > 0 ? ` · ${invitedCount} invited` : ""}`

  const mobileTabs: { key: string; label: string; n?: number | string; on: boolean; run: () => void }[] = [
    { key: "all", label: "Everyone", n: members.length, on: view === "everyone", run: () => setFilters({ status: "", flag: "" }) },
    ...(capsReadable ? [{ key: "look", label: "Needs a look", n: findings ? lookTotal : "…", on: view === "look", run: () => setFilters({ status: "look", flag: "" }) }] : []),
    ...(isManager ? [{ key: "invited", label: "Invited", n: invitedCount, on: view === "invited", run: () => setFilters({ status: "invited", flag: "" }) }] : []),
  ]
  const mobileNav = (
    <div className="-mx-1 flex gap-1 overflow-x-auto px-1" role="tablist" aria-label="Show">
      {mobileTabs.map((t) => (
        <button key={t.key} type="button" role="tab" aria-selected={t.on} onClick={t.run}
          className={cn("inline-flex h-9 coarse:h-[44px] shrink-0 items-center gap-1.5 rounded-md px-3 text-xs font-medium", t.on ? "bg-[var(--selection-bg)] text-foreground" : "text-muted-foreground")}>
          {t.label}{t.n != null && <span className="text-micro tabular-nums text-muted-foreground">{t.n}</span>}
        </button>
      ))}
    </div>
  )

  return (
    <SettingsDrillPage
      panelId="drill-members"
      title="Members"
      icon={Users}
      description={wsLoading || roster.loading ? "" : headline}
      toolbar={toolbar}
      nav={nav}
      mobileNav={mobileNav}
      actions={isMobile ? undefined : addMember}
    >
      {wsLoading || roster.loading ? (
        <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-6 w-80 rounded-md" /><Skeleton className="h-[420px] rounded-card" /></div>
      ) : (
        <div className={cn("space-y-4 p-4 md:p-6", selected.size > 0 && "pb-24")}>
          {isMobile && addMember && <div className="flex flex-wrap gap-2" data-slot="members-actions">{addMember}</div>}
          {!isAdmin && (
            <p className="text-xs text-muted-foreground" data-slot="members-note">
              {isManager ? "Only admins change what people may do." : "Read-only — managers and admins make changes."}
            </p>
          )}

          {roster.error && (
            <PageError className="max-w-none px-0 py-0" title="Couldn’t load the members" description={`${roster.error}.`} onRetry={roster.refresh} />
          )}

          {capsFailed && (
            <PageBanner
              tone="bad"
              icon={TriangleAlert}
              data-slot="members-caps-error"
              action={<Button variant="outline" size="sm" onClick={() => void capsQuery.refetch()}>Retry</Button>}
            >
              Could not read what people may do, so it cannot be changed until it loads.
            </PageBanner>
          )}

          {view === "invited" && (
            <SettingsCard icon={Mail} title="Invitations" description="A link is valid for 7 days. A revoked or expired one cannot be accepted — invite again.">
              {invitationsQuery.isLoading ? <div className="p-4"><Skeleton className="h-10 w-full" /></div> : (
                <InvitedList invitations={invitations} workspaceId={ws} manage={isAdmin} callerRole={role} onChanged={() => void invitationsQuery.refetch()} />
              )}
            </SettingsCard>
          )}

          {view === "look" && findings && (
            <MembersLook groups={findings} currentUserId={currentUserId} callerRole={role} workspaceId={ws} revealEnabled={revealEnabled}
              holders={findings.find((g) => g.kind === "reveal")?.people.length ?? 0} grantCandidates={revealCandidates} canGrantReveal={isAdmin}
              onTurnOff={onRevoke} onOpen={open} onShowAll={showAll}
              onGrantReveal={(m) => onCell(m, Capability.CredentialReveal, true)} onInvitationsChanged={() => void invitationsQuery.refetch()} />
          )}
          {view === "look" && !findings && <Skeleton className="h-40 rounded-card" />}

          {view === "everyone" && (
            <>
              {findings && findings.length > 0 && activeFilters === 0 && (
                <PageBanner tone="warn" icon={TriangleAlert} data-slot="members-look-banner"
                  action={<Button variant="outline" size="sm" className="coarse:h-[44px]" onClick={() => setFilters({ status: "look", flag: "" })}>Review</Button>}>
                  <b>{lookTotal} {lookTotal === 1 ? "thing needs" : "things need"} a look</b>
                  <span className="block font-normal">{lookSummary(findings)}</span>
                </PageBanner>
              )}
              <SettingsCard icon={Users} title="People" hideHeader>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border/60 px-4 py-2.5" data-slot="members-toolbar">
                  <b className="text-control font-semibold tabular-nums">
                    {shown.length === members.length ? `${members.length} ${members.length === 1 ? "person" : "people"}` : `${shown.length} of ${members.length} people`}
                  </b>
                  {chips.map((c) => (
                    <motion.span key={c.key} initial={reduce ? false : { scale: 0.85, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={{ duration: 0.18 }}
                      className="inline-flex h-6 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-micro font-medium text-primary-hover">
                      {c.label}
                      <button type="button" onClick={() => setFilters(c.clear)} aria-label={`Remove filter ${c.label}`} className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20 relative coarse:after:absolute coarse:after:-inset-4 coarse:after:content-['']">
                        <X className="h-3 w-3" />
                      </button>
                    </motion.span>
                  ))}
                  {chips.length > 1 && (
                    <button type="button" onClick={() => setFilters({ ...DEFAULT_MEMBER_FILTERS, status: filters.status })} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear all</button>
                  )}
                  <span className="ml-auto flex items-center gap-2">
                    {!isMobile && (
                      <Segmented ariaLabel="Group by" value={groupBy} onChange={setGroupBy} options={[{ value: "role", label: "By role" }, { value: "name", label: "A–Z" }]} />
                    )}
                    <Button variant="outline" size="sm" className="px-2.5 text-xs coarse:h-[44px]" onClick={exportCsv} disabled={shown.length === 0}>
                      <Download className="mr-1.5 h-3 w-3" />Export CSV
                    </Button>
                  </span>
                </div>
                {shown.length === 0 ? (
                  <p className="px-4 py-8 text-center text-xs text-muted-foreground">Nobody matches these filters.</p>
                ) : isMobile ? (
                  <PeopleList groups={groups} caps={caps} currentUserId={currentUserId} onOpen={open} />
                ) : (
                  <PeopleTable groups={groups} caps={caps} serverCaps={serverCaps} currentUserId={currentUserId} canSelect={isAdmin}
                    selected={selected} openedId={openedId}
                    onSelect={(id, on) => setSelected((s) => { const n = new Set(s); if (on) n.add(id); else n.delete(id); return n })}
                    onSelectMany={(ids, on) => setSelected(on ? new Set(ids) : new Set())}
                    onOpen={open} />
                )}
                <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} noun="people" paged={paged.paged} />
              </SettingsCard>
            </>
          )}
        </div>
      )}

      <RegisterDraft count={pendingCount} saving={access.busy} save={saveDrafts} discard={() => setDrafts(() => ({}))} />

      <AnimatePresence>
        {selected.size > 0 && !isMobile && (
          <BulkBar count={selected.size} busy={busy} lifted={pendingCount > 0} canChangeRole={isManager} callerRole={role}
            onAbilities={() => setBulkAbilities(true)} onRole={bulk.role} onClear={clearSelection} reduce={Boolean(reduce)} />
        )}
      </AnimatePresence>
      <BulkAbilitiesDialog open={bulkAbilities} onOpenChange={setBulkAbilities} count={selected.size} onApply={bulk.abilities} />

      <Peek
        open={opened !== null}
        onOpenChange={(o) => { if (!o) open(null) }}
        title={opened ? <>{nameOf(opened)}{opened.user.id === currentUserId && <span className="ml-1.5 text-xs font-normal text-muted-foreground">you</span>}</> : "Member"}
        subtitle={opened ? <>{opened.user.full_name?.trim() ? `${opened.user.email} · ` : ""}joined {joinedDate(opened.created_at)}</> : undefined}
        onPrev={prev ? () => open(prev) : undefined}
        onNext={next ? () => open(next) : undefined}
        footer={opened ? (
          <div className="flex flex-col gap-2">
            {/* The page's own save bar sits under the Peek's overlay; the
                same bar, from the same context, docks here. */}
            <div className="flex justify-center empty:hidden [&>*]:pointer-events-auto">
              <PageSaveBar className="static left-auto bottom-auto translate-x-0 max-sm:static max-sm:rounded-xl max-sm:border-x max-sm:border-b" />
            </div>
            <p className="text-label text-muted-foreground" data-slot="member-when">A role change applies at once. Abilities and reach wait for Save.</p>
            <p className="flex min-w-0 items-center gap-1.5 text-label text-muted-foreground" data-slot="member-cli">
              <Terminal className="h-3 w-3 shrink-0" aria-hidden />
              <code className="truncate font-mono text-micro">crewship workspace member capabilities list {opened.user.id}</code>
            </p>
          </div>
        ) : undefined}
      >
        {opened && (
          <MemberDetail member={opened} workspaceId={ws} currentUserId={currentUserId} callerRole={role} isAdmin={isAdmin}
            granted={caps?.[opened.user.id] ?? []} serverGranted={serverCaps?.[opened.user.id] ?? []} capsLoading={capsLoading || !serverCaps?.[opened.user.id]} busy={busy}
            onChangeRole={roster.changeRole} tryRemove={roster.tryRemove} handOverPage={roster.handOverPage}
            onRemoved={(m) => { if (openedId === m.user.id) open(null) }}
            onCell={onCell} onProfile={onProfile} onRevoke={onRevoke} revealEnabled={revealEnabled} onAccessSaved={roster.refresh} />
        )}
      </Peek>

      <AlertDialog open={confirm !== null} onOpenChange={(o) => { if (!o) setConfirm(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">{confirm?.title}</AlertDialogTitle>
            <AlertDialogDescription className="text-xs">{confirm?.body}</AlertDialogDescription>
            {confirm && <ConfirmWhen when={confirm.when} />}
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction className={cn("text-xs", confirm?.danger && "bg-destructive text-destructive-foreground hover:bg-destructive/90")}
              onClick={() => { const c = confirm; setConfirm(null); void c?.run() }}>
              {confirm?.action}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsDrillPage>
  )
}

/** The Invited view's list, paged like every list of hundreds. */
function InvitedList({ invitations, ...rest }: { invitations: React.ComponentProps<typeof InvitationRows>["invitations"] } & Omit<React.ComponentProps<typeof InvitationRows>, "invitations">) {
  const paged = useShowMore(invitations)
  if (invitations.length === 0) return <p className="px-4 py-6 text-center text-label text-muted-foreground">No invitations.</p>
  return (
    <>
      <InvitationRows invitations={paged.shown} {...rest} />
      <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} noun="invitations" paged={paged.paged} />
    </>
  )
}

/** Hands the capability draft to the page's save bar. It renders inside
 *  DrillPage, which is where the bar's provider lives. */
function RegisterDraft({ count, saving, save, discard }: { count: number; saving: boolean; save: () => Promise<void>; discard: () => void }) {
  usePageSave({ label: "Members", count, saving, save, discard })
  return null
}

/**
 * The page's state, read from the URL and written back to it.
 *
 * Read from useSearchParams, not window.location: during a client-side
 * navigation the App Router renders the new route before window.location
 * updates (settings-layout has the long version). Re-read when the query
 * string CHANGES, so a second ⌘K pick on a mounted page opens the new
 * person; written with history.replaceState, which does not navigate, and
 * remembered so our own write is never mistaken for a navigation.
 */
function useMembersUrl(): [MembersUrlState, (s: MembersUrlState) => void] {
  const search = useSearchParams().toString()
  const [state, setLocal] = React.useState(() => parseMembersUrl(search))
  const applied = React.useRef(search)

  React.useEffect(() => {
    if (applied.current === search) return
    applied.current = search
    setLocal(parseMembersUrl(search))
  }, [search])

  const set = React.useCallback((next: MembersUrlState) => {
    setLocal(next)
    const query = membersUrlQuery(next)
    applied.current = query
    if (typeof window === "undefined") return
    const url = new URL(window.location.href)
    url.search = query
    window.history.replaceState(window.history.state, "", url.toString())
  }, [])

  return [state, set]
}

/** Every capability the server stores, chat included (a `set` without chat
 *  is refused), sorted so two sets compare as strings. */
function canonical(set: string[]): string[] {
  return [...new Set([Capability.Chat as string, ...set])].sort()
}

/** How many capabilities differ between what the server holds and a draft. */
function capabilityDiff(server: readonly string[] | undefined, draft: readonly string[]): number {
  const a = new Set((server ?? []).filter((c) => c !== Capability.Chat))
  const b = new Set(draft.filter((c) => c !== Capability.Chat))
  let n = 0
  for (const c of a) if (!b.has(c)) n++
  for (const c of b) if (!a.has(c)) n++
  return n
}
