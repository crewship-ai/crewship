"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { motion, useReducedMotion } from "motion/react"
import { Grid3x3, Settings as SettingsIcon, UserRound, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { isAdminTier, isManagerTier } from "@/lib/permissions/tiers"
import { ALL_CAPABILITIES, CAPABILITY_LABELS, Capability } from "@/lib/capabilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { Skeleton } from "@/components/ui/skeleton"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { BulkPresetAction, HIGH_STAKES_CAPABILITIES, useMemberCapabilities } from "@/components/admin/member-capabilities"

import {
  DEFAULT_MEMBER_FILTERS,
  ROLES,
  ROLE_LABEL,
  activeFilterCount,
  extraCapabilities,
  byRoleThenName,
  memberMatches,
  membersUrlQuery,
  parseMembersUrl,
  type Member,
  type MemberFilters,
  type MembersUrlState,
  type MembersView,
} from "./members-model"
import { useMembers } from "./use-members"
import { MemberDetail, ROLE_DOT } from "./member-detail"
import { MembersMatrix } from "./members-matrix"

const VIEWS: { key: MembersView; label: string; icon: typeof Users }[] = [
  { key: "people", label: "People", icon: UserRound },
  { key: "matrix", label: "Matrix", icon: Grid3x3 },
]

/**
 * Settings › Members as a nested page (DrillPage), like Crew links and the
 * Audit log. It used to be a card in the Settings column: one collapsible row
 * per person, which answered "what can THIS person do?" well and "who can
 * reveal secrets?" not at all — that took expanding every row.
 *
 * Now the side panel filters (search, role, capability) and every value says
 * how many people it would show; the People view lists them with what they
 * hold beyond their role, and a person opens in a detail panel beside the
 * list; the Matrix view lays people against capabilities for the audit
 * question.
 *
 * Status (active / invited) is deliberately absent: Add member provisions the
 * account immediately (invite-member-dialog), so the roster has no pending
 * state to filter by.
 *
 * State lives in the URL (members-model's parseMembersUrl): ?member=<userId>
 * is what the ⌘K palette and the old /settings?tab=members&member= links
 * resolve to.
 */
export function MembersPage() {
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const { session } = useAuth()
  const currentUserId = session?.user?.id
  const reduce = useReducedMotion()
  const isMobile = useIsMobile()
  const roster = useMembers(workspaceId)
  const { members } = roster

  // isAdmin gates invite, remove, bulk edit AND the per-member capabilities —
  // all map to `roleManage` routes. Role change is MANAGER+ and is gated per
  // person inside the detail panel (members-model roleChoices).
  const isAdmin = isAdminTier(role)
  const isManager = isManagerTier(role)
  const capsReadable = isAdmin && Boolean(currentUserId)
  // One round-trip for the whole roster: the chips, the counts and the matrix
  // all read it, and 500 people must not mean 500 requests.
  const { data: capsByUser, isLoading: capsLoading } = useMemberCapabilities(
    workspaceId ?? "",
    capsReadable && members.length > 0,
  )
  const caps = capsReadable ? capsByUser : undefined

  const [state, setState, arrivedWith] = useMembersUrl()
  const { filters } = state
  // The matrix is capabilities against people; without the admin read there
  // is nothing to put in it, so a shared ?view=matrix link opens People.
  const view: MembersView = capsReadable ? state.view : "people"
  const update = (next: Partial<MembersUrlState>) => setState({ ...state, ...next })
  const setFilters = (next: Partial<MemberFilters>) => update({ filters: { ...filters, ...next } })
  const select = (m: Member | null) => update({ member: m?.user.id ?? "", view: m ? "people" : view })

  const sorted = React.useMemo(() => [...members].sort(byRoleThenName), [members])
  const shown = sorted.filter((m) => memberMatches(m, filters, caps))
  // An explicitly opened person stays open even when a filter hides them in
  // the list — the panel is about them, not about the filter.
  const selected = members.find((m) => m.user.id === state.member) ?? null
  // Like Crew links: someone is always open, so the page is never an empty
  // frame waiting for a click — the deep-linked person, else the first shown.
  const current = selected ?? shown[0] ?? null
  const countWith = (f: Partial<MemberFilters>) => sorted.filter((m) => memberMatches(m, { ...filters, ...f }, caps)).length

  // Bring a deep-linked person into view once the roster has arrived. On a
  // roster that fits the screen this is a no-op; on one that does not, a
  // selected row below the fold is the same as no deep link at all.
  React.useEffect(() => {
    if (!arrivedWith || roster.loading) return
    // The open person's row in the side panel (DrillNavItem marks it
    // aria-current), brought into view inside the panel's own scroll.
    document.querySelector<HTMLElement>('nav [aria-current="true"]')?.scrollIntoView?.({ block: "nearest" })
  }, [arrivedWith, roster.loading])

  const onRemove = async (m: Member) => {
    const ok = await roster.remove(m)
    if (ok && state.member === m.user.id) select(null)
    return ok
  }

  const popoverCount = Number(Boolean(filters.cap && capsReadable))
  const toolbar = (
    <>
      <SidebarSearch value={filters.q} onValueChange={(q) => setFilters({ q })} placeholder="Search name or email…" aria-label="Search members" />
      {/* Effective holders: role grants nothing beyond chat, so the stored set
          is what a person can do (components/admin/member-capabilities). */}
      {capsReadable && (
        <SidebarFilterPopover label="Filter by capability" activeCount={popoverCount} onClear={() => setFilters({ cap: "" })} panelClassName="min-w-[240px]">
          <SidebarFacet label="Has capability" resetLabel="Anyone" resetActive={!filters.cap} onReset={() => setFilters({ cap: "" })} first>
            {ALL_CAPABILITIES.map((cap) => {
              const risky = HIGH_STAKES_CAPABILITIES.has(cap)
              return (
                <SidebarFacetOption key={cap} active={filters.cap === cap} onToggle={() => setFilters({ cap: filters.cap === cap ? "" : cap })}>
                  <span className={cn(risky && "text-destructive")}>{CAPABILITY_LABELS[cap].en}</span>
                  <span className="ml-auto font-mono text-micro text-muted-foreground">{capsLoading ? "…" : countWith({ cap })}</span>
                </SidebarFacetOption>
              )
            })}
          </SidebarFacet>
        </SidebarFilterPopover>
      )}
    </>
  )

  const viewSwitch = (phone: boolean) => capsReadable && (
    <div className={cn("grid grid-cols-2 gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5", !phone && "mx-2 mt-1")} role="group" aria-label="View">
      {VIEWS.map((v) => (
        <button key={v.key} type="button" aria-pressed={view === v.key} onClick={() => update({ view: v.key })} data-drill-close
          className={cn("flex items-center justify-center gap-1 rounded-md font-medium transition-colors",
            phone ? "h-8 text-xs" : "h-7 text-label",
            view === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
          <v.icon className="h-3.5 w-3.5" />{v.label}
        </button>
      ))}
    </div>
  )

  // What a row says under the name: the grants beyond the role, in words.
  const subOf = (m: Member) => {
    if (!caps) return m.user.full_name?.trim() ? m.user.email : undefined
    const extras = extraCapabilities(caps[m.user.id])
    if (m.role === "OWNER") return "Everything"
    if (extras.length === 0) return "Role defaults"
    const risky = extras.filter((c) => HIGH_STAKES_CAPABILITIES.has(c)).length
    return `${extras.length} extra${risky ? ` · ${risky} high-stakes` : ""}`
  }
  const personItem = (m: Member, i: number) => (
    <DrillNavItem key={m.id} index={i} selected={view === "people" && current?.user.id === m.user.id} onSelect={() => select(m)}
      title={`${personLabel(m.user.full_name, m.user.email)} · ${m.user.email}`}
      icon={<UserAvatar name={m.user.full_name} email={m.user.email} src={m.user.avatar_url} className="h-7 w-7" textClassName="text-[10px]" />}
      label={<>{personLabel(m.user.full_name, m.user.email)}{m.user.id === currentUserId && <span className="ml-1.5 text-micro font-normal text-muted-foreground">you</span>}</>}
      sub={subOf(m)} />
  )
  const groups = ROLES.map((r) => ({ role: r, people: shown.filter((m) => m.role === r) })).filter((g) => g.people.length > 0)

  const nav = (
    <>
      {viewSwitch(false)}
      <DrillNavSection label="Roles" count={members.length}>
        <DrillNavItem index={0} selected={!filters.role} pressed={!filters.role} onSelect={() => setFilters({ role: "" })}
          icon={<span className="h-1.5 w-1.5 shrink-0 rounded-full bg-border" aria-hidden />} label="All people" meta={countWith({ role: "" })} />
        {ROLES.map((r, i) => {
          const n = countWith({ role: r })
          return (
            <DrillNavItem key={r} index={i + 1} selected={filters.role === r} pressed={filters.role === r} muted={!n}
              onSelect={() => setFilters({ role: filters.role === r ? "" : r })}
              icon={<span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", ROLE_DOT[r])} aria-hidden />}
              label={ROLE_LABEL[r]} meta={n} />
          )
        })}
      </DrillNavSection>
      {groups.length === 0 && <p className="px-3 py-2 text-xs text-muted-foreground">Nobody matches these filters.</p>}
      {groups.map((g, gi) => (
        <DrillNavSection key={g.role} label={`${ROLE_LABEL[g.role]}s`} count={g.people.length} defaultOpen={gi < 3 || g.people.length <= 12}>
          {g.people.map(personItem)}
        </DrillNavSection>
      ))}
    </>
  )

  // Phone: no side panel on screen, so the view and the person are picked
  // here, the way Crew links picks a crew.
  const mobileNav = (
    <div className="flex flex-col gap-2">
      {viewSwitch(true)}
      {view === "people" && current && (
        <select aria-label="Person" value={current.user.id} onChange={(e) => { const m = members.find((x) => x.user.id === e.target.value); if (m) select(m) }}
          className="h-10 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control">
          {groups.map((g) => (
            <optgroup key={g.role} label={`${ROLE_LABEL[g.role]}s`}>
              {g.people.map((m) => <option key={m.id} value={m.user.id}>{personLabel(m.user.full_name, m.user.email)}</option>)}
            </optgroup>
          ))}
        </select>
      )}
    </div>
  )

  // The filters in words, each removable, above whichever view is open.
  const chips: { key: string; label: string; clear: Partial<MemberFilters> }[] = []
  if (filters.q.trim()) chips.push({ key: "q", label: `“${filters.q.trim()}”`, clear: { q: "" } })
  if (filters.role) chips.push({ key: "role", label: ROLE_LABEL[filters.role], clear: { role: "" } })
  if (filters.cap && capsReadable) chips.push({ key: "cap", label: `Has: ${CAPABILITY_LABELS[filters.cap as keyof typeof CAPABILITY_LABELS]?.en ?? filters.cap}`, clear: { cap: "" } })

  const actions = (
    <>
      {isAdmin && currentUserId && (
        <BulkPresetAction members={members} workspaceId={workspaceId ?? ""} currentUserId={currentUserId} capsByUser={capsByUser} />
      )}
      {isAdmin && workspaceId && <InviteMemberDialog workspaceId={workspaceId} onInvited={roster.refresh} />}
    </>
  )

  // The audit numbers, before anyone is opened — the same summary line Crew
  // links leads with.
  const admins = members.filter((m) => m.role === "ADMIN" || m.role === "OWNER").length
  const revealers = caps ? members.filter((m) => (caps[m.user.id] ?? []).includes(Capability.CredentialReveal)).length : null
  const highStakes = caps ? members.filter((m) => m.role !== "OWNER" && extraCapabilities(caps[m.user.id]).some((c) => HIGH_STAKES_CAPABILITIES.has(c))).length : null

  const n = members.length
  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Members"
      icon={Users}
      description="Who is in this workspace, and what each person may do"
      toolbar={toolbar}
      nav={nav}
      mobileNav={mobileNav}
      filterCount={activeFilterCount(filters, capsReadable)}
      actions={isMobile ? undefined : actions}
    >
      {wsLoading || roster.loading ? (
        <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-10 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
      ) : (
        <div className="space-y-4 p-4 md:p-6">
          {isMobile && isAdmin && <div className="flex flex-wrap gap-2" data-slot="members-actions">{actions}</div>}
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
            <Stat n={n} one="person" many="people" />
            <Stat n={admins} one="owner or admin" many="owners and admins" />
            {revealers != null && <span className={cn(revealers > 2 && "text-warn")}><span className="font-mono tabular-nums text-foreground">{revealers}</span> can reveal secrets</span>}
            {highStakes != null && <Stat n={highStakes} one="holds a high-stakes grant" many="hold high-stakes grants" />}
            {shown.length !== n && <span><span className="font-mono tabular-nums text-foreground">{shown.length}</span> of {n} shown</span>}
            {!isAdmin && <span className="ml-auto">{isManager ? "Only admins can invite or remove members." : "Read-only — managers and admins make changes."}</span>}
          </div>

          {roster.error && (
            <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
              {roster.error}. <button type="button" className="underline" onClick={roster.refresh}>Retry</button>
            </p>
          )}

          {view === "matrix" ? (
            <MembersMatrix members={shown} workspaceId={workspaceId ?? ""} currentUserId={currentUserId} capsByUser={capsByUser}
              canEdit={isAdmin} onSelect={(m) => select(m)} />
          ) : current ? (
            <motion.div key={current.id} initial={reduce ? false : { opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.22, ease: [0.2, 0.7, 0.2, 1] }}>
              <MemberDetail member={current} workspaceId={workspaceId ?? ""} currentUserId={currentUserId}
                callerRole={role} isAdmin={isAdmin} granted={capsByUser?.[current.user.id] ?? []} capsLoading={capsLoading}
                onChangeRole={roster.changeRole} onRemove={onRemove} />
            </motion.div>
          ) : (
            <p className="rounded-card border border-border bg-card px-4 py-8 text-center text-xs text-muted-foreground">Nobody matches these filters.</p>
          )}
        </div>
      )}
    </DrillPage>
  )
}

function Stat({ n, one, many }: { n: number; one: string; many: string }) {
  return <span><span className="font-mono tabular-nums text-foreground">{n}</span> {n === 1 ? one : many}</span>
}

/**
 * The page's state, read from the URL and written back to it.
 *
 * Read from useSearchParams, not window.location: during a client-side
 * navigation the App Router renders the new route before window.location
 * updates (settings-layout has the long version). Re-read when the query
 * string CHANGES, so a second ⌘K pick on a mounted page selects the new
 * person; written with history.replaceState, which does not navigate, and
 * remembered so our own write is never mistaken for a navigation.
 *
 * The third value is the person the page ARRIVED with, for the one-time
 * scroll into view.
 */
function useMembersUrl(): [MembersUrlState, (s: MembersUrlState) => void, string] {
  const search = useSearchParams().toString()
  const [state, setLocal] = React.useState(() => parseMembersUrl(search))
  const [arrivedWith, setArrivedWith] = React.useState(state.member)
  const applied = React.useRef(search)

  React.useEffect(() => {
    if (applied.current === search) return
    applied.current = search
    const next = parseMembersUrl(search)
    setLocal(next)
    setArrivedWith(next.member)
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

  return [state, set, arrivedWith]
}
