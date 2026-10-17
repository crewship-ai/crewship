"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import { ChevronDown, Download, Mail, MessageSquare, Terminal, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { isAdminTier, isManagerTier } from "@/lib/permissions/tiers"
import { ALL_CAPABILITIES, CAPABILITY_BUNDLES, CAPABILITY_LABELS, Capability, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { personLabel } from "@/components/ui/user-avatar"
import { Peek } from "@/components/ui/peek"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { DrillNavItem, DrillNavSection } from "@/components/layout/drill-page"
import { SettingsDrillPage } from "@/components/features/settings/settings-drill-page"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"
import { SettingsCard, SettingsSummary, SummaryItem, selectionBar, selectionBarButton } from "@/components/features/settings/shared"
import { PageError } from "@/components/layout/page-state"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { HIGH_STAKES_CAPABILITIES, useMemberCapabilities } from "@/components/admin/member-capabilities"
import { PageSaveBar, usePageSave } from "@/components/ui/page-save-bar"

import {
  ACCESS_PROFILES,
  AREAS,
  ASSIGNABLE_ROLES,
  DEFAULT_MEMBER_FILTERS,
  MEMBER_FLAGS,
  PROFILE_LABEL,
  ROLES,
  ROLE_LABEL,
  aboveRole,
  accessProfile,
  byRoleThenName,
  memberMatches,
  membersUrlQuery,
  parseMembersUrl,
  profileText,
  roleChoices,
  type Member,
  type MemberFilters,
  type MembersUrlState,
} from "./members-model"
import { useMembers } from "./use-members"
import { useAccessWrites } from "./use-access"
import { MemberDetail } from "./member-detail"
import { AREA_ICON, ConfirmWhen, ROLE_ICON } from "./member-bits"
import { InvitationList, PeopleList, PeopleTable, joinedDate, type PeopleGroup } from "./people-table"
import { MembersAttention, RevealCard, membersFindings } from "./members-attention"
import { useInvitations, useRevealPolicy } from "./use-invitations"

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
 * Settings › Members: who is here, what each person manages, and what each
 * person may do — for everyone at once.
 *
 * The ROLE is what a person manages (people, crews, settings); what they may
 * DO is nine grants, read here as three areas — Routines, Content, Secrets —
 * so a row says "Routines · Content 3/4 · Secrets · reveal" instead of nine
 * dots. Top to bottom the page answers what needs a person (who reads secrets
 * in plaintext, who holds more than their role suggests, invitations running
 * out), then the roster grouped by role.
 *
 *   - one person: click the row — a side panel (full screen on a phone) with
 *     the role, a preset, every ability by area, crews and removal;
 *   - many people: tick the rows, then Set access / Grant / Revoke / Role.
 *
 * Granting anything high-stakes and every role change ask first; capability
 * edits are drafts until Save. The filters live in the URL (?role= ?area=
 * ?flag= ?status= ?cap= ?q=); ?member= is what ⌘K and old
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

  // Capability edits are a draft until Save, like every other editor in
  // Settings (components/ui/page-save-bar): a dot, a profile or a bulk action
  // changes `drafts` — user id → the whole set they would hold — and the
  // page's one floating bar commits them all, or Discard drops them. What the
  // matrix, the counts and the filters show is the set WITH the draft.
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
  }, [shown.length, filters.q, filters.role, filters.area, filters.flag, filters.status, filters.cap])

  const invitationsQuery = useInvitations(workspaceId, isManager)
  const invitations = invitationsQuery.data ?? []
  const revealEnabled = useRevealPolicy(workspaceId, isManager).data ?? null

  const groups: PeopleGroup[] = React.useMemo(() => {
    if (groupBy === "name") {
      const byName = [...shown].sort((a, b) => nameOf(a).localeCompare(nameOf(b)))
      return byName.length ? [{ key: "all", label: "A–Z", people: byName }] : []
    }
    return ROLES.map((r) => ({ key: r, label: `${ROLE_LABEL[r]}s`, people: shown.filter((m) => m.role === r) })).filter((g) => g.people.length > 0)
  }, [groupBy, shown])

  // ── Writes, each asking first when it should ─────────────────────────

  // Asking first is kept for one thing: putting a high-stakes grant into the
  // draft. Everything else is undone by Discard.
  const askHighStakes = (title: string, body: React.ReactNode, action: string, gains: CapabilityValue[], run: () => void) => {
    if (gains.length === 0) { run(); return }
    setConfirm({ title, body, action, danger: true, when: "save", run })
  }

  const onCell = (m: Member, cap: CapabilityValue, next: boolean) => {
    const label = CAPABILITY_LABELS[cap].en
    askHighStakes(`Grant ${label}?`,
      <><span className="font-medium text-foreground">{nameOf(m)}</span> will be able to: {CAPABILITY_LABELS[cap].description}</>,
      `Grant ${label}`, next && HIGH_STAKES_CAPABILITIES.has(cap) ? [cap] : [],
      () => stage([m], (cur) => (next ? [...cur, cap] : cur.filter((c) => c !== cap))))
  }

  const highStakesGains = (people: Member[], add: readonly string[]) =>
    [...new Set(add.filter((c) => HIGH_STAKES_CAPABILITIES.has(c as CapabilityValue) && people.some((m) => !(caps?.[m.user.id] ?? []).includes(c))))] as CapabilityValue[]

  const onProfile = (m: Member, preset: CapabilityBundle) => {
    const gains = highStakesGains([m], CAPABILITY_BUNDLES[preset])
    askHighStakes(`Set ${nameOf(m)} to ${PROFILE_LABEL[preset]}?`,
      <>This grants high-stakes access: {gains.map((c) => CAPABILITY_LABELS[c].en).join(", ")}.</>,
      `Set to ${PROFILE_LABEL[preset]}`, gains, () => stage([m], () => [...CAPABILITY_BUNDLES[preset]]))
  }

  /** Take high-stakes grants away (the panel's "Turn … off"): a draft, no
   *  question — taking access away is what Discard undoes. */
  const onRevoke = (m: Member, revoke: CapabilityValue[]) => stage([m], (cur) => cur.filter((c) => !revoke.includes(c as CapabilityValue)))

  const clearSelection = () => setSelected(new Set())
  const bulk = {
    profile: (preset: CapabilityBundle) => {
      const people = selectedPeople
      const gains = highStakesGains(people, CAPABILITY_BUNDLES[preset])
      askHighStakes(`Set ${people.length} people to ${PROFILE_LABEL[preset]}?`,
        <>This grants high-stakes access: {gains.map((c) => CAPABILITY_LABELS[c].en).join(", ")}.</>,
        "Apply profile", gains, () => { stage(people, () => [...CAPABILITY_BUNDLES[preset]]); clearSelection() })
    },
    cap: (cap: CapabilityValue, next: boolean) => {
      const people = selectedPeople
      askHighStakes(`Grant ${CAPABILITY_LABELS[cap].en} to ${people.length} people?`,
        <>{CAPABILITY_LABELS[cap].description}</>,
        "Grant", next && HIGH_STAKES_CAPABILITIES.has(cap) ? [cap] : [],
        () => { stage(people, (cur) => (next ? [...cur, cap] : cur.filter((c) => c !== cap))); clearSelection() })
    },
    role: (r: string) => {
      const eligible = selectedPeople.filter((m) => m.role !== r && roleChoices(role, m, m.user.id === currentUserId).includes(r))
      setConfirm({
        title: `Make ${eligible.length} ${eligible.length === 1 ? "person" : "people"} ${ROLE_LABEL[r]}?`,
        body: <>
          {eligible.length < selectedPeople.length && <>{selectedPeople.length - eligible.length} of the selected already have this role or are above yours, and stay as they are. </>}
          Their capabilities do not change.
        </>,
        action: "Change role",
        when: "now",
        run: async () => { await access.changeRole(eligible, r); clearSelection() },
      })
    },
  }

  const onRemove = async (m: Member) => {
    const ok = await roster.remove(m)
    if (ok && openedId === m.user.id) open(null)
    return ok
  }

  const exportCsv = () => {
    const cols = ALL_CAPABILITIES.filter((c) => c !== Capability.Chat)
    const cell = (v: string) => `"${v.replace(/"/g, '""')}"`
    const lines = [
      ["Name", "Email", "Role", ...(caps ? ["Access profile", ...cols.map((c) => CAPABILITY_LABELS[c].en)] : []), "Joined"].map(cell).join(","),
      ...shown.map((m) => [
        m.user.full_name ?? "", m.user.email, ROLE_LABEL[m.role] ?? m.role,
        ...(caps ? [m.role === "OWNER" ? "Owner" : profileText(accessProfile(caps[m.user.id])), ...cols.map((c) => ((caps[m.user.id] ?? []).includes(c) ? "yes" : ""))] : []),
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

  const toolbar = (
    <>
      <SidebarSearch value={filters.q} onValueChange={(q) => setFilters({ q })} placeholder="Search name or email…" aria-label="Search members" />
      {capsReadable && (
        <SidebarFilterPopover label="Filter by capability" activeCount={Number(Boolean(filters.cap))} onClear={() => setFilters({ cap: "" })} panelClassName="min-w-[240px]">
          <SidebarFacet label="Has capability" resetLabel="Anyone" resetActive={!filters.cap} onReset={() => setFilters({ cap: "" })} first>
            {ALL_CAPABILITIES.filter((c) => c !== Capability.Chat).map((cap) => (
              <SidebarFacetOption key={cap} active={filters.cap === cap} onToggle={() => setFilters({ cap: filters.cap === cap ? "" : cap })}>
                <span className={cn(HIGH_STAKES_CAPABILITIES.has(cap) && "text-destructive")}>{CAPABILITY_LABELS[cap].en}</span>
                <span className="ml-auto font-mono text-micro text-muted-foreground">{capsLoading ? "…" : countWith({ cap })}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
        </SidebarFilterPopover>
      )}
    </>
  )

  const dot = (cls: string) => <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", cls)} aria-hidden />
  const ico = (Icon: React.ElementType, cls = "text-muted-foreground") => <Icon className={cn("h-3.5 w-3.5 shrink-0", cls)} aria-hidden />
  const pick = (next: Partial<MemberFilters>) => setFilters({ ...next, status: "" })
  const nav = (
    <>
      <DrillNavSection label="People" count={members.length}>
        <DrillNavItem index={0} selected={!filters.status && !filters.role && !filters.area && !filters.flag} onSelect={() => setFilters({ ...DEFAULT_MEMBER_FILTERS, q: filters.q })}
          icon={ico(Users)} label="Everyone" meta={members.length} />
        {isManager && (
          <DrillNavItem index={1} selected={filters.status === "invited"} pressed={filters.status === "invited"} muted={!invitations.length}
            onSelect={() => setFilters({ status: filters.status === "invited" ? "" : "invited" })}
            icon={ico(Mail)} label="Invited" meta={invitationsQuery.isLoading ? "…" : invitations.length} />
        )}
      </DrillNavSection>
      <DrillNavSection label="Role">
        {ROLES.map((r, i) => {
          const n = countWith({ role: r })
          return (
            <DrillNavItem key={r} index={i} selected={filters.role === r && !filters.status} pressed={filters.role === r} muted={!n}
              onSelect={() => pick({ role: filters.role === r ? "" : r })} icon={ico(ROLE_ICON[r])} label={ROLE_LABEL[r]} meta={n} />
          )
        })}
      </DrillNavSection>
      {capsReadable && (
        <DrillNavSection label="Can do">
          {[...AREAS.map((a) => ({ key: a.key as string, label: a.label, sub: a.caps.map((c) => c.short.toLowerCase()).join(" · "), icon: ico(AREA_ICON[a.key], "text-primary-hover") })),
            { key: "chat", label: "Chat only", sub: undefined, icon: ico(MessageSquare) }].map((a, i) => {
            const n = countWith({ area: a.key })
            return (
              <DrillNavItem key={a.key} index={i} selected={filters.area === a.key && !filters.status} pressed={filters.area === a.key} muted={!n}
                onSelect={() => pick({ area: filters.area === a.key ? "" : a.key })} icon={a.icon} label={a.label} sub={a.sub} meta={capsLoading ? "…" : n} />
            )
          })}
        </DrillNavSection>
      )}
      {capsReadable && (
        <DrillNavSection label="Needs a look">
          {MEMBER_FLAGS.map((f, i) => {
            const n = countWith({ flag: f.key })
            return (
              <DrillNavItem key={f.key} index={i} selected={filters.flag === f.key && !filters.status} pressed={filters.flag === f.key} muted={!n}
                onSelect={() => pick({ flag: filters.flag === f.key ? "" : f.key })}
                icon={dot(f.key === "reveal" ? "bg-destructive" : "bg-warn")} label={f.label} sub={f.hint} meta={capsLoading ? "…" : n} />
            )
          })}
        </DrillNavSection>
      )}
    </>
  )

  // ── Main ─────────────────────────────────────────────────────────────

  const chips: { key: string; label: string; clear: Partial<MemberFilters> }[] = []
  if (filters.q.trim()) chips.push({ key: "q", label: `“${filters.q.trim()}”`, clear: { q: "" } })
  if (filters.role) chips.push({ key: "role", label: ROLE_LABEL[filters.role], clear: { role: "" } })
  if (filters.status) chips.push({ key: "status", label: "Invited", clear: { status: "" } })
  if (filters.area && capsReadable) chips.push({ key: "area", label: filters.area === "chat" ? "Chat only" : `Can do: ${AREAS.find((a) => a.key === filters.area)?.label ?? filters.area}`, clear: { area: "" } })
  if (filters.flag && capsReadable) chips.push({ key: "flag", label: MEMBER_FLAGS.find((f) => f.key === filters.flag)?.label ?? filters.flag, clear: { flag: "" } })
  if (filters.cap && capsReadable) chips.push({ key: "cap", label: `Has: ${CAPABILITY_LABELS[filters.cap as CapabilityValue]?.en ?? filters.cap}`, clear: { cap: "" } })

  const revealers = caps ? members.filter((m) => (caps[m.user.id] ?? []).includes(Capability.CredentialReveal)).length : null
  const aboveCount = caps ? members.filter((m) => aboveRole(m, caps[m.user.id]).length > 0).length : null
  const chatOnlyCount = caps ? countWith({ ...DEFAULT_MEMBER_FILTERS, area: "chat" }) : null
  const findings = capsReadable && caps && !capsLoading ? membersFindings({
    members, caps: caps as Record<string, string[]>, invitations, revealEnabled,
    onFlag: (flag) => pick({ ...DEFAULT_MEMBER_FILTERS, flag }),
    onOpen: open,
    onInvited: () => setFilters({ status: "invited" }),
  }) : null

  // J / K in the Peek walk the people in the order the roster shows them.
  const order = groups.flatMap((g) => g.people)
  const at = opened ? order.findIndex((m) => m.user.id === opened.user.id) : -1
  const prev = at > 0 ? order[at - 1] : null
  const next = at >= 0 && at < order.length - 1 ? order[at + 1] : null

  const addMember = isAdmin && workspaceId ? <InviteMemberDialog workspaceId={workspaceId} onInvited={roster.refresh} /> : null
  const cardActions = (
    <>
      {!isMobile && (
        <div role="group" aria-label="Group by" className="inline-flex gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
          {([["role", "By role"], ["name", "A–Z"]] as const).map(([g, l]) => (
            <button key={g} type="button" aria-pressed={groupBy === g} onClick={() => setGroupBy(g)}
              className={cn("h-6 rounded-md px-2.5 text-xs transition-colors", groupBy === g ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}>
              {l}
            </button>
          ))}
        </div>
      )}
      <Button variant="outline" size="sm" className="px-2.5 text-xs" onClick={exportCsv} disabled={shown.length === 0}>
        <Download className="mr-1.5 h-3 w-3" />Export CSV
      </Button>
    </>
  )

  const tabs: { key: string; label: string; n?: number | null; on: boolean; run: () => void; tone?: string }[] = [
    { key: "all", label: "Everyone", n: members.length, on: !filters.status && !filters.flag, run: () => setFilters({ status: "", flag: "" }) },
    ...(isManager ? [{ key: "invited", label: "Invited", n: invitations.length, on: filters.status === "invited", run: () => setFilters({ status: "invited", flag: "" }) }] : []),
    ...(capsReadable && revealers ? [{ key: "reveal", label: "Can reveal", n: revealers, tone: "bg-destructive", on: filters.flag === "reveal" && !filters.status, run: () => pick({ flag: "reveal" }) }] : []),
    ...(capsReadable && aboveCount ? [{ key: "above", label: "Above role", n: aboveCount, tone: "bg-warn", on: filters.flag === "risky-low-role" && !filters.status, run: () => pick({ flag: "risky-low-role" }) }] : []),
  ]
  const mobileNav = (
    <div className="-mx-1 flex gap-1 overflow-x-auto px-1" role="tablist" aria-label="Show">
      {tabs.map((t) => (
        <button key={t.key} type="button" role="tab" aria-selected={t.on} onClick={t.run}
          className={cn("inline-flex h-9 shrink-0 items-center gap-1.5 rounded-md px-3 text-xs font-medium", t.on ? "bg-[var(--selection-bg)] text-foreground" : "text-muted-foreground")}>
          {t.tone && <span className={cn("h-1.5 w-1.5 rounded-full", t.tone)} aria-hidden />}
          {t.label}{t.n != null && <span className="font-mono text-micro text-muted-foreground">{t.n}</span>}
        </button>
      ))}
    </div>
  )

  return (
    <SettingsDrillPage
      panelId="drill-members"
      title="Members"
      icon={Users}
      description="Who is in this workspace, what each person manages and what they may do."
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
          {chips.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              {chips.map((c) => (
                <motion.span key={c.key} initial={reduce ? false : { scale: 0.85, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={{ duration: 0.18 }}
                  className="inline-flex h-6 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-micro font-medium text-primary-hover">
                  {c.label}
                  <button type="button" onClick={() => setFilters(c.clear)} aria-label={`Remove filter ${c.label}`} className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20">
                    <X className="h-3 w-3" />
                  </button>
                </motion.span>
              ))}
              <button type="button" onClick={() => setFilters(DEFAULT_MEMBER_FILTERS)} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear all</button>
            </div>
          )}

          <SettingsSummary slot="members-summary">
            <SummaryItem n={members.length}>{members.length === 1 ? "person" : "people"}</SummaryItem>
            {isManager && invitations.length > 0 && <SummaryItem n={invitations.length}>invited</SummaryItem>}
            {chatOnlyCount != null && <SummaryItem n={chatOnlyCount}>chat only</SummaryItem>}
            {revealers != null && (
              <button type="button" onClick={() => pick({ flag: filters.flag === "reveal" ? "" : "reveal" })} className="hover:text-foreground">
                <SummaryItem n={revealers} tone={revealers ? "danger" : undefined}>can reveal secrets</SummaryItem>
              </button>
            )}
            {aboveCount != null && aboveCount > 0 && (
              <button type="button" onClick={() => pick({ flag: filters.flag === "risky-low-role" ? "" : "risky-low-role" })} className="hover:text-foreground">
                <SummaryItem n={aboveCount} tone="warn">above their role</SummaryItem>
              </button>
            )}
            {!filters.status && shown.length !== members.length && <SummaryItem n={shown.length}>of {members.length} shown</SummaryItem>}
            {!isAdmin && <span className="ml-auto">{isManager ? "Only admins change what people may do." : "Read-only — managers and admins make changes."}</span>}
          </SettingsSummary>

          {roster.error && (
            <PageError className="max-w-none px-0 py-0" title="Couldn’t load the members" description={`${roster.error}.`} onRetry={roster.refresh} />
          )}

          {capsFailed && (
            <p role="alert" data-slot="members-caps-error" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
              Could not read what people may do, so it cannot be changed until it loads. <button type="button" className="underline" onClick={() => void capsQuery.refetch()}>Retry</button>
            </p>
          )}

          {findings && !filters.status && <MembersAttention findings={findings} />}

          {capsReadable && caps && filters.flag === "reveal" && !filters.status && (
            <RevealCard revealEnabled={revealEnabled} holders={revealers ?? 0} canGrant={isAdmin}
              candidates={sorted.filter((m) => m.role !== "OWNER" && m.user.id !== currentUserId && !(caps[m.user.id] ?? []).includes(Capability.CredentialReveal))}
              onGrant={(m) => onCell(m, Capability.CredentialReveal, true)} />
          )}

          {filters.status === "invited" ? (
            <SettingsCard icon={Mail} title="Invitations" description="Sent, not accepted yet. An expired one cannot be accepted — invite again.">
              {invitationsQuery.isLoading ? <div className="p-4"><Skeleton className="h-10 w-full" /></div> : <InvitationList invitations={invitations} workspaceId={ws} manage={isAdmin} callerRole={role} onChanged={() => void invitationsQuery.refetch()} />}
            </SettingsCard>
          ) : (
            <SettingsCard icon={Users} title="People"
              description={capsReadable ? "The role says what they manage · the chips say what they may do" : `${members.length} people, by role`}
              actions={cardActions}>
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
            </SettingsCard>
          )}
        </div>
      )}

      <RegisterDraft count={pendingCount} saving={access.busy} save={saveDrafts} discard={() => setDrafts(() => ({}))} />

      <AnimatePresence>
        {selected.size > 0 && !isMobile && (
          <BulkBar count={selected.size} busy={busy} lifted={pendingCount > 0} canChangeRole={isManager} callerRole={role}
            onProfile={bulk.profile} onCap={bulk.cap} onRole={bulk.role} onClear={clearSelection} reduce={Boolean(reduce)} />
        )}
      </AnimatePresence>

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
            <p className="flex min-w-0 items-center gap-1.5 text-label text-muted-foreground" data-slot="member-cli">
              <Terminal className="h-3 w-3 shrink-0" aria-hidden />
              <code className="truncate font-mono text-micro">crewship workspace member capabilities list {opened.user.id}</code>
            </p>
          </div>
        ) : undefined}
      >
        {opened && (
          <MemberDetail member={opened} workspaceId={ws} currentUserId={currentUserId} callerRole={role} isAdmin={isAdmin} inPeek
            granted={caps?.[opened.user.id] ?? []} serverGranted={serverCaps?.[opened.user.id] ?? []} capsLoading={capsLoading || !serverCaps?.[opened.user.id]} busy={busy}
            onChangeRole={roster.changeRole} onRemove={onRemove} onCell={onCell} onProfile={onProfile} onRevoke={onRevoke} revealEnabled={revealEnabled} onAccessSaved={roster.refresh} />
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

/** Hands the capability draft to the page's save bar. It renders inside
 *  DrillPage, which is where the bar's provider lives. */
function RegisterDraft({ count, saving, save, discard }: { count: number; saving: boolean; save: () => Promise<void>; discard: () => void }) {
  usePageSave({ label: "Members", count, saving, save, discard })
  return null
}

/** What to do with the ticked rows: a bar floating over the bottom of the
 *  page, like a mail client's selection bar. */
function BulkBar({ count, busy, lifted, canChangeRole, callerRole, onProfile, onCap, onRole, onClear, reduce }: {
  count: number
  busy: boolean
  /** The save bar is showing: sit above it, not on it. */
  lifted: boolean
  canChangeRole: boolean
  callerRole?: string | null
  onProfile: (p: CapabilityBundle) => void
  onCap: (cap: CapabilityValue, next: boolean) => void
  onRole: (r: string) => void
  onClear: () => void
  reduce: boolean
}) {
  const caps = ALL_CAPABILITIES.filter((c) => c !== Capability.Chat)
  const menu = (label: string, items: { key: string; label: string; danger?: boolean; run: () => void }[], heading: string) => (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={busy}>
        <Button variant="outline" size="sm" className={selectionBarButton}>{label}<ChevronDown className="h-3 w-3" /></Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-56">
        <DropdownMenuLabel className="font-mono text-micro uppercase tracking-[0.08em] text-primary-hover">{heading}</DropdownMenuLabel>
        {items.map((i) => (
          <DropdownMenuItem key={i.key} onSelect={i.run} className={cn("text-xs", i.danger && "text-destructive")}>{i.label}</DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
  const callerRank = ASSIGNABLE_ROLES.indexOf(callerRole as (typeof ASSIGNABLE_ROLES)[number])
  const roles = callerRole === "OWNER" ? [...ASSIGNABLE_ROLES] : ASSIGNABLE_ROLES.filter((_, i) => callerRank < 0 ? false : i < callerRank)
  return (
    <motion.div role="toolbar" aria-label="Change the selected people" data-slot="members-bulk"
      initial={reduce ? false : { y: 24, opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={reduce ? undefined : { y: 24, opacity: 0 }} transition={{ duration: 0.18 }}
      className={cn(selectionBar, "fixed left-1/2 -translate-x-1/2 transition-[bottom]", lifted ? "bottom-20" : "bottom-6")}>
      <span className="px-1 text-xs"><span className="font-mono font-semibold tabular-nums">{count}</span> selected</span>
      {menu("Set access", ACCESS_PROFILES.map((p) => ({ key: p.key, label: p.label, run: () => onProfile(p.key) })), "Preset")}
      {menu("Grant", caps.map((c) => ({ key: c, label: CAPABILITY_LABELS[c].en, danger: HIGH_STAKES_CAPABILITIES.has(c), run: () => onCap(c, true) })), "Grant to all selected")}
      {menu("Revoke", caps.map((c) => ({ key: c, label: CAPABILITY_LABELS[c].en, run: () => onCap(c, false) })), "Revoke from all selected")}
      {canChangeRole && roles.length > 0 && menu("Role", roles.map((r) => ({ key: r, label: ROLE_LABEL[r], run: () => onRole(r) })), "Change role")}
      <Button variant="ghost" size="sm" className={cn(selectionBarButton, "border-transparent px-2.5")} onClick={onClear}>Clear</Button>
    </motion.div>
  )
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
