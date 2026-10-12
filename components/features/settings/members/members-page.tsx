"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import { ChevronDown, ChevronRight, Download, Settings as SettingsIcon, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { isAdminTier, isManagerTier } from "@/lib/permissions/tiers"
import { ALL_CAPABILITIES, CAPABILITY_BUNDLES, CAPABILITY_LABELS, Capability, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"
import { SettingsCard } from "@/components/features/settings/shared"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { HIGH_STAKES_CAPABILITIES, useMemberCapabilities, useToggleCapability } from "@/components/admin/member-capabilities"

import {
  ACCESS_PROFILES,
  ASSIGNABLE_ROLES,
  CAPABILITY_SHORT,
  DEFAULT_MEMBER_FILTERS,
  MEMBER_FLAGS,
  PROFILE_LABEL,
  ROLES,
  ROLE_LABEL,
  accessProfile,
  activeFilterCount,
  byRoleThenName,
  exceptionCount,
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
import { AccessMatrix, ProfileText, type MatrixGroup } from "./access-matrix"
import { MemberDetail } from "./member-detail"
import { Dot, ROLE_DOT, RolePill } from "./member-bits"

type GroupBy = "role" | "profile"

interface Confirm {
  title: string
  body: React.ReactNode
  action: string
  danger?: boolean
  run: () => Promise<unknown> | void
}

/**
 * Settings › Members: who is here, what each person manages, and what each
 * person may do — for everyone at once.
 *
 * The page separates the two things the old one blurred. The ROLE is what a
 * person manages (people, crews, settings); the ACCESS PROFILE is what they
 * may do, described as the nearest of the server's three presets plus its
 * exceptions ("Power user +Reveal"). The main view is a matrix: people grouped
 * by role (or by profile), one dot per capability in three groups, exceptions
 * ringed in orange. Assigning is mostly picking a profile — one call — and a
 * dot is the exception, not the rule.
 *
 *   - one person: the profile button in their row, or a dot;
 *   - many people: tick the rows, then Apply profile / Grant / Revoke / Role;
 *   - one person in full: click the name — a side panel (full screen on a
 *     phone) with role, profile, every capability, resource access, removal.
 *
 * Granting anything high-stakes, replacing someone's exceptions and every
 * bulk change ask first. The filters live in the URL (?role= ?profile= ?flag=
 * ?cap= ?q=); ?member= is what ⌘K and old /settings?tab=members links resolve
 * to, and opens that person.
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
  const { data: capsByUser, isLoading: capsLoading } = useMemberCapabilities(ws, capsReadable && members.length > 0)
  const caps = React.useMemo(() => (capsReadable ? capsByUser ?? {} : undefined), [capsReadable, capsByUser])
  const toggle = useToggleCapability(ws)
  const access = useAccessWrites(ws, roster.refresh)
  const busy = toggle.isPending || access.busy

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
  }, [shown.length, filters.q, filters.role, filters.profile, filters.flag, filters.cap])

  const groups: MatrixGroup[] = React.useMemo(() => {
    if (groupBy === "profile" && caps) {
      const owners = shown.filter((m) => m.role === "OWNER")
      const rest = shown.filter((m) => m.role !== "OWNER")
      const exact = (key: string) => rest.filter((m) => { const p = accessProfile(caps[m.user.id]); return p.key === key && exceptionCount(p) === 0 })
      return [
        { key: "owner", label: "Owners", people: owners },
        ...ACCESS_PROFILES.map((p) => ({ key: p.key, label: p.label, people: exact(p.key) })),
        { key: "custom", label: "With exceptions", people: rest.filter((m) => exceptionCount(accessProfile(caps[m.user.id])) > 0) },
      ].filter((g) => g.people.length > 0)
    }
    return ROLES.map((r) => ({ key: r, label: `${ROLE_LABEL[r]}s`, people: shown.filter((m) => m.role === r) })).filter((g) => g.people.length > 0)
  }, [groupBy, caps, shown])

  // ── Writes, each asking first when it should ─────────────────────────

  const onCell = (m: Member, cap: CapabilityValue, next: boolean) => {
    const label = CAPABILITY_LABELS[cap].en
    const apply = () => toggle.mutate({ userId: m.user.id, cap, next })
    if (next && HIGH_STAKES_CAPABILITIES.has(cap)) {
      setConfirm({
        title: `Grant ${label}?`,
        body: <><span className="font-medium text-foreground">{nameOf(m)}</span> will be able to: {CAPABILITY_LABELS[cap].description}</>,
        action: `Grant ${label}`,
        danger: true,
        run: apply,
      })
      return
    }
    apply()
  }

  const onProfile = (m: Member, preset: CapabilityBundle) => {
    const p = accessProfile(caps?.[m.user.id])
    const gains = CAPABILITY_BUNDLES[preset].filter((c) => HIGH_STAKES_CAPABILITIES.has(c) && !(caps?.[m.user.id] ?? []).includes(c))
    const diff = exceptionCount(p)
    const run = () => access.applyProfile([m], preset)
    if (diff === 0 && gains.length === 0) { void run(); return }
    setConfirm({
      title: `Set ${nameOf(m)} to ${PROFILE_LABEL[preset]}?`,
      body: (
        <>
          {diff > 0 && <>Their {diff === 1 ? "exception" : `${diff} exceptions`} ({[...p.added.map((c) => `+${CAPABILITY_SHORT[c]}`), ...p.removed.map((c) => `−${CAPABILITY_SHORT[c]}`)].join(" ")}) will be replaced. </>}
          {gains.length > 0 && <>This grants high-stakes access: {gains.map((c) => CAPABILITY_LABELS[c].en).join(", ")}.</>}
        </>
      ),
      action: `Set to ${PROFILE_LABEL[preset]}`,
      danger: gains.length > 0,
      run,
    })
  }

  const clearSelection = () => setSelected(new Set())
  const bulk = {
    profile: (preset: CapabilityBundle) => setConfirm({
      title: `Set ${selectedPeople.length} people to ${PROFILE_LABEL[preset]}?`,
      body: <>Every exception they have is replaced by the profile: {ACCESS_PROFILES.find((p) => p.key === preset)?.summary}</>,
      action: "Apply profile",
      danger: CAPABILITY_BUNDLES[preset].some((c) => HIGH_STAKES_CAPABILITIES.has(c)),
      run: async () => { await access.applyProfile(selectedPeople, preset); clearSelection() },
    }),
    cap: (cap: CapabilityValue, next: boolean) => setConfirm({
      title: `${next ? "Grant" : "Revoke"} ${CAPABILITY_LABELS[cap].en} ${next ? "to" : "from"} ${selectedPeople.length} people?`,
      body: next ? CAPABILITY_LABELS[cap].description : "Anyone who has it loses it now; the rest are unchanged.",
      action: next ? "Grant" : "Revoke",
      danger: next && HIGH_STAKES_CAPABILITIES.has(cap),
      run: async () => {
        await (next ? access.grant : access.revoke)(selectedPeople, cap, CAPABILITY_LABELS[cap].en)
        clearSelection()
      },
    }),
    role: (r: string) => {
      const eligible = selectedPeople.filter((m) => m.role !== r && roleChoices(role, m, m.user.id === currentUserId).includes(r))
      setConfirm({
        title: `Make ${eligible.length} ${eligible.length === 1 ? "person" : "people"} ${ROLE_LABEL[r]}?`,
        body: <>
          {eligible.length < selectedPeople.length && <>{selectedPeople.length - eligible.length} of the selected already have this role or are above yours, and stay as they are. </>}
          Their capabilities do not change.
        </>,
        action: "Change role",
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
  const nav = (
    <>
      <DrillNavSection label="Role" count={members.length}>
        <DrillNavItem index={0} selected={!filters.role} pressed={!filters.role} onSelect={() => setFilters({ role: "" })}
          icon={dot("bg-border")} label="All people" meta={countWith({ role: "" })} />
        {ROLES.map((r, i) => {
          const n = countWith({ role: r })
          return (
            <DrillNavItem key={r} index={i + 1} selected={filters.role === r} pressed={filters.role === r} muted={!n}
              onSelect={() => setFilters({ role: filters.role === r ? "" : r })} icon={dot(ROLE_DOT[r])} label={ROLE_LABEL[r]} meta={n} />
          )
        })}
      </DrillNavSection>
      {capsReadable && (
        <DrillNavSection label="Access profile">
          {[...ACCESS_PROFILES.map((p) => ({ key: p.key as string, label: p.label })), { key: "custom", label: "With exceptions" }].map((p, i) => {
            const n = countWith({ profile: p.key })
            return (
              <DrillNavItem key={p.key} index={i} selected={filters.profile === p.key} pressed={filters.profile === p.key} muted={!n}
                onSelect={() => setFilters({ profile: filters.profile === p.key ? "" : p.key })}
                icon={p.key === "custom" ? dot("bg-warn") : dot("bg-primary")} label={p.label} meta={capsLoading ? "…" : n} />
            )
          })}
        </DrillNavSection>
      )}
      {capsReadable && (
        <DrillNavSection label="Needs a look">
          {MEMBER_FLAGS.map((f, i) => {
            const n = countWith({ flag: f.key })
            return (
              <DrillNavItem key={f.key} index={i} selected={filters.flag === f.key} pressed={filters.flag === f.key} muted={!n}
                onSelect={() => setFilters({ flag: filters.flag === f.key ? "" : f.key })}
                icon={dot(f.key === "exceptions" ? "bg-warn" : "bg-destructive")} label={f.label} sub={f.hint} meta={capsLoading ? "…" : n} />
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
  if (filters.profile && capsReadable) chips.push({ key: "profile", label: filters.profile === "custom" ? "With exceptions" : PROFILE_LABEL[filters.profile], clear: { profile: "" } })
  if (filters.flag && capsReadable) chips.push({ key: "flag", label: MEMBER_FLAGS.find((f) => f.key === filters.flag)?.label ?? filters.flag, clear: { flag: "" } })
  if (filters.cap && capsReadable) chips.push({ key: "cap", label: `Has: ${CAPABILITY_LABELS[filters.cap as CapabilityValue]?.en ?? filters.cap}`, clear: { cap: "" } })

  const nonOwners = members.filter((m) => m.role !== "OWNER")
  const withExceptions = caps ? nonOwners.filter((m) => exceptionCount(accessProfile(caps[m.user.id])) > 0).length : null
  const revealers = caps ? members.filter((m) => (caps[m.user.id] ?? []).includes(Capability.CredentialReveal)).length : null

  const addMember = isAdmin && workspaceId ? <InviteMemberDialog workspaceId={workspaceId} onInvited={roster.refresh} /> : null
  const cardActions = (
    <>
      {capsReadable && !isMobile && (
        <div role="group" aria-label="Group by" className="inline-flex gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
          {(["role", "profile"] as const).map((g) => (
            <button key={g} type="button" aria-pressed={groupBy === g} onClick={() => setGroupBy(g)}
              className={cn("h-6 rounded-md px-2.5 text-xs font-medium transition-colors", groupBy === g ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
              By {g}
            </button>
          ))}
        </div>
      )}
      <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={exportCsv} disabled={shown.length === 0}>
        <Download className="mr-1.5 h-3 w-3" />Export CSV
      </Button>
    </>
  )

  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Members"
      icon={Users}
      description="Who is in this workspace, what each person manages and what they may do"
      toolbar={toolbar}
      nav={nav}
      filterCount={activeFilterCount(filters, capsReadable)}
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

          <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-muted-foreground" data-slot="members-summary">
            <Stat n={members.length} one="person" many="people" />
            {withExceptions != null && <Stat n={nonOwners.length - withExceptions} one="on a standard profile" many="on a standard profile" />}
            {withExceptions != null && (
              <button type="button" onClick={() => setFilters({ profile: filters.profile === "custom" ? "" : "custom" })} className="hover:text-foreground">
                <span className={cn("font-mono tabular-nums", withExceptions ? "text-warn" : "text-foreground")}>{withExceptions}</span> with exceptions
              </button>
            )}
            {revealers != null && (
              <button type="button" onClick={() => setFilters({ flag: filters.flag === "reveal" ? "" : "reveal" })} className="hover:text-foreground">
                <span className="font-mono tabular-nums text-destructive">{revealers}</span> can reveal secrets
              </button>
            )}
            {shown.length !== members.length && <span><span className="font-mono tabular-nums text-foreground">{shown.length}</span> of {members.length} shown</span>}
            {!isAdmin && <span className="ml-auto">{isManager ? "Only admins change what people may do." : "Read-only — managers and admins make changes."}</span>}
          </div>

          {roster.error && (
            <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
              {roster.error}. <button type="button" className="underline" onClick={roster.refresh}>Retry</button>
            </p>
          )}

          <SettingsCard icon={Users} title={capsReadable ? "Access" : "People"}
            description={capsReadable ? "The role says what they manage · the profile says what they may do" : `${members.length} people, by role`}
            actions={cardActions}>
            {shown.length === 0 ? (
              <p className="px-4 py-8 text-center text-xs text-muted-foreground">Nobody matches these filters.</p>
            ) : isMobile ? (
              <PeopleList groups={groups} caps={caps} currentUserId={currentUserId} onOpen={open} />
            ) : (
              <AccessMatrix groups={groups} caps={caps} currentUserId={currentUserId} canEdit={isAdmin} busy={busy}
                selected={selected}
                onSelect={(id, on) => setSelected((s) => { const n = new Set(s); if (on) n.add(id); else n.delete(id); return n })}
                onSelectMany={(ids, on) => setSelected(on ? new Set(ids) : new Set())}
                onOpen={open} onCell={onCell} onProfile={onProfile} />
            )}
          </SettingsCard>
          {capsReadable && !isMobile && shown.length > 0 && <Legend />}
        </div>
      )}

      <AnimatePresence>
        {selected.size > 0 && !isMobile && (
          <BulkBar count={selected.size} busy={busy} canChangeRole={isManager} callerRole={role}
            onProfile={bulk.profile} onCap={bulk.cap} onRole={bulk.role} onClear={clearSelection} reduce={Boolean(reduce)} />
        )}
      </AnimatePresence>

      <Sheet open={opened !== null} onOpenChange={(o) => { if (!o) open(null) }}>
        <SheetContent side="right" className="w-full gap-0 overflow-y-auto p-5 sm:max-w-[440px]" data-slot="member-sheet">
          <SheetTitle className="sr-only">{opened ? nameOf(opened) : "Member"}</SheetTitle>
          <SheetDescription className="sr-only">Role, access profile and capabilities</SheetDescription>
          {opened && (
            <MemberDetail member={opened} workspaceId={ws} currentUserId={currentUserId} callerRole={role} isAdmin={isAdmin}
              granted={capsByUser?.[opened.user.id] ?? []} capsLoading={capsLoading} busy={busy}
              onChangeRole={roster.changeRole} onRemove={onRemove} onCell={onCell} onProfile={onProfile} />
          )}
        </SheetContent>
      </Sheet>

      <AlertDialog open={confirm !== null} onOpenChange={(o) => { if (!o) setConfirm(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">{confirm?.title}</AlertDialogTitle>
            <AlertDialogDescription className="text-xs">{confirm?.body}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction className={cn("h-7 text-xs", confirm?.danger && "bg-destructive text-destructive-foreground hover:bg-destructive/90")}
              onClick={() => { const c = confirm; setConfirm(null); void c?.run() }}>
              {confirm?.action}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </DrillPage>
  )
}

function Stat({ n, one, many }: { n: number; one: string; many: string }) {
  return <span><span className="font-mono tabular-nums text-foreground">{n}</span> {n === 1 ? one : many}</span>
}

function Legend() {
  return (
    <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-label text-muted-foreground" data-slot="members-legend">
      <span className="inline-flex items-center gap-2"><Dot state="profile" />held, from the profile</span>
      <span className="inline-flex items-center gap-2"><Dot state="added" />exception — added</span>
      <span className="inline-flex items-center gap-2"><Dot state="removed" />exception — taken away</span>
      <span className="inline-flex items-center gap-2"><Dot state="profile" danger />high-stakes</span>
      <span className="inline-flex items-center gap-2"><Dot state="off" />not held</span>
      <span className="ml-auto">Owners and your own row cannot be changed.</span>
    </div>
  )
}

/** The phone's roster: grouped like the matrix, one tappable row per person
 *  with their profile under the name and the role on the right. */
function PeopleList({ groups, caps, currentUserId, onOpen }: {
  groups: MatrixGroup[]
  caps: Record<string, string[]> | undefined
  currentUserId?: string
  onOpen: (m: Member) => void
}) {
  return (
    <div data-slot="members-list">
      {groups.map((g) => (
        <section key={g.key} aria-label={g.label}>
          <h3 className="border-b border-border/40 bg-card px-4 py-1.5 font-mono text-micro font-medium uppercase tracking-[0.08em] text-muted-foreground">
            {g.label}<span className="ml-2 font-normal normal-case tracking-normal text-muted-foreground-soft">{g.people.length}</span>
          </h3>
          {g.people.map((m) => {
            const label = personLabel(m.user.full_name, m.user.email)
            return (
              <button key={m.id} type="button" onClick={() => onOpen(m)} title={`${label} · ${m.user.email}`}
                className="flex min-h-[52px] w-full items-center gap-3 border-b border-border/40 px-4 py-2 text-left active:bg-[var(--row-hover-bg)]">
                <UserAvatar name={m.user.full_name} email={m.user.email} src={m.user.avatar_url} className="h-8 w-8" textClassName="text-[10px]" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-control">{label}{m.user.id === currentUserId && <span className="ml-1.5 text-micro text-muted-foreground">you</span>}</span>
                  <span className="block truncate text-label text-muted-foreground">
                    {caps ? (m.role === "OWNER" ? "Owner · fixed" : <ProfileText profile={accessProfile(caps[m.user.id])} />) : m.user.email}
                  </span>
                </span>
                <RolePill role={m.role} />
                <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
              </button>
            )
          })}
        </section>
      ))}
    </div>
  )
}

/** What to do with the ticked rows: a bar floating over the bottom of the
 *  page, like a mail client's selection bar. */
function BulkBar({ count, busy, canChangeRole, callerRole, onProfile, onCap, onRole, onClear, reduce }: {
  count: number
  busy: boolean
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
        <Button variant="outline" size="sm" className="h-8 gap-1 px-3 text-xs">{label}<ChevronDown className="h-3 w-3" /></Button>
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
      className="fixed bottom-6 left-1/2 z-40 flex -translate-x-1/2 items-center gap-2 rounded-xl border border-primary/30 bg-popover px-3 py-2 shadow-lg">
      <span className="px-1 text-xs"><span className="font-mono font-semibold tabular-nums">{count}</span> selected</span>
      {menu("Apply profile", ACCESS_PROFILES.map((p) => ({ key: p.key, label: p.label, run: () => onProfile(p.key) })), "Access profile")}
      {menu("Grant", caps.map((c) => ({ key: c, label: CAPABILITY_LABELS[c].en, danger: HIGH_STAKES_CAPABILITIES.has(c), run: () => onCap(c, true) })), "Grant to all selected")}
      {menu("Revoke", caps.map((c) => ({ key: c, label: CAPABILITY_LABELS[c].en, run: () => onCap(c, false) })), "Revoke from all selected")}
      {canChangeRole && roles.length > 0 && menu("Role", roles.map((r) => ({ key: r, label: ROLE_LABEL[r], run: () => onRole(r) })), "Change role")}
      <Button variant="ghost" size="sm" className="h-8 px-2.5 text-xs text-muted-foreground" onClick={onClear}>Clear</Button>
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
