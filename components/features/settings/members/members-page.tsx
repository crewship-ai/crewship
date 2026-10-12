"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { motion, useReducedMotion } from "motion/react"
import { Grid3x3, HelpCircle, Settings as SettingsIcon, UserRound, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { isAdminTier, isManagerTier } from "@/lib/permissions/tiers"
import { ALL_CAPABILITIES, CAPABILITY_LABELS } from "@/lib/capabilities"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarSearch } from "@/components/layout/sidebar-kit"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { BulkPresetAction, HIGH_STAKES_CAPABILITIES, useMemberCapabilities } from "@/components/admin/member-capabilities"
import { SettingsRow } from "@/components/features/settings/shared"

import {
  DEFAULT_MEMBER_FILTERS,
  ROLES,
  ROLE_LABEL,
  ROLE_SUMMARIES,
  activeFilterCount,
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
import { MembersList } from "./members-list"
import { MemberDetail } from "./member-detail"
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
  const countWith = (f: Partial<MemberFilters>) => sorted.filter((m) => memberMatches(m, { ...filters, ...f }, caps)).length

  // Bring a deep-linked person into view once the roster has arrived. On a
  // roster that fits the screen this is a no-op; on one that does not, a
  // selected row below the fold is the same as no deep link at all.
  React.useEffect(() => {
    if (!arrivedWith || roster.loading) return
    const row = Array.from(document.querySelectorAll<HTMLElement>("[data-member]")).find((el) => el.dataset.member === arrivedWith)
    row?.scrollIntoView?.({ block: "nearest" })
  }, [arrivedWith, roster.loading])

  const onRemove = async (m: Member) => {
    const ok = await roster.remove(m)
    if (ok && state.member === m.user.id) select(null)
    return ok
  }

  const toolbar = (
    <SidebarSearch value={filters.q} onValueChange={(q) => setFilters({ q })} placeholder="Search name or email…" aria-label="Search members" />
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

  const nav = (
    <>
      {viewSwitch(false)}
      <DrillNavSection label="Role" count={members.length}>
        <DrillNavItem index={0} selected={!filters.role} pressed={!filters.role} onSelect={() => setFilters({ role: "" })}
          icon={<Users className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />} label="All people" meta={countWith({ role: "" })} />
        {ROLES.map((r, i) => {
          const n = countWith({ role: r })
          return (
            <DrillNavItem key={r} index={i + 1} selected={filters.role === r} pressed={filters.role === r} muted={!n}
              onSelect={() => setFilters({ role: filters.role === r ? "" : r })}
              icon={<span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground" aria-hidden />}
              label={ROLE_LABEL[r]} meta={n} />
          )
        })}
      </DrillNavSection>
      {/* Effective holders: role grants nothing beyond chat, so the stored set
          is what a person can do (components/admin/member-capabilities). */}
      {capsReadable && (
        <DrillNavSection label="Has capability">
          {ALL_CAPABILITIES.map((cap, i) => {
            const n = countWith({ cap })
            const risky = HIGH_STAKES_CAPABILITIES.has(cap)
            return (
              <DrillNavItem key={cap} index={i} selected={filters.cap === cap} pressed={filters.cap === cap} muted={!n && !risky}
                onSelect={() => setFilters({ cap: filters.cap === cap ? "" : cap })}
                title={CAPABILITY_LABELS[cap].description}
                icon={<span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", risky ? "bg-destructive" : "bg-primary")} aria-hidden />}
                label={<span className={cn(risky && "text-destructive")}>{CAPABILITY_LABELS[cap].en}</span>}
                meta={capsLoading ? "…" : n} />
            )
          })}
        </DrillNavSection>
      )}
    </>
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

  const n = members.length
  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Members"
      icon={Users}
      description={`${n} ${n === 1 ? "person" : "people"} · who can do what in this workspace`}
      toolbar={toolbar}
      nav={nav}
      mobileNav={viewSwitch(true) || undefined}
      filterCount={activeFilterCount(filters, capsReadable)}
      actions={actions}
    >
      {wsLoading || roster.loading ? (
        <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-10 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
      ) : (
        <div className="space-y-4 p-4 md:p-6">
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
            {chips.length > 0 && (
              <button type="button" onClick={() => setFilters(DEFAULT_MEMBER_FILTERS)} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear all</button>
            )}
            <span className="text-xs text-muted-foreground">
              {shown.length !== n && <><span className="font-mono tabular-nums text-foreground">{shown.length}</span> of {n} shown</>}
            </span>
            <span className="ml-auto"><RoleLegend /></span>
          </div>

          {roster.error && (
            <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
              {roster.error}. <button type="button" className="underline" onClick={roster.refresh}>Retry</button>
            </p>
          )}

          {view === "matrix" ? (
            <MembersMatrix members={shown} workspaceId={workspaceId ?? ""} currentUserId={currentUserId} capsByUser={capsByUser}
              canEdit={isAdmin} onSelect={(m) => select(m)} />
          ) : (
            <div className="lg:grid lg:grid-cols-[minmax(0,1fr)_340px] lg:items-start lg:gap-4">
              {/* Below lg the detail takes the list's place (it has a way
                  back); from lg it is a column beside the list. CSS rather
                  than a Sheet, so the person's panel is the same component —
                  and the same focus order — at every width. */}
              <div className={cn(selected && "hidden lg:block")}>
                <MembersList members={shown} selectedUserId={selected?.user.id ?? ""} currentUserId={currentUserId}
                  capsByUser={capsByUser} showCaps={capsReadable} onSelect={select} />
              </div>
              <div className={cn("rounded-card border border-border bg-card p-4 lg:sticky lg:top-0", !selected && "hidden lg:block")}>
                {selected ? (
                  <MemberDetail key={selected.id} member={selected} workspaceId={workspaceId ?? ""} currentUserId={currentUserId}
                    callerRole={role} isAdmin={isAdmin} granted={capsByUser?.[selected.user.id] ?? []} capsLoading={capsLoading}
                    onChangeRole={roster.changeRole} onRemove={onRemove} onBack={() => select(null)} />
                ) : (
                  <p className="py-8 text-center text-xs text-muted-foreground">Pick a person to see their role and what they may do.</p>
                )}
              </div>
            </div>
          )}

          {/* The roster stays visible to everyone; only the mutating controls
              are tier-gated. Say so once, quietly — this is a normal state
              for MANAGER/MEMBER/VIEWER, not an error. */}
          {!isAdmin && (
            <p className="px-1 text-label text-muted-foreground">
              {isManager ? "Only admins can invite or remove members." : "Only managers and admins can make changes here."}
            </p>
          )}
        </div>
      )}
    </DrillPage>
  )
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

/**
 * The role legend, as help rather than furniture: five roles, identical in
 * every workspace, consulted when choosing one. The per-person half — what
 * THIS person's role grants — is stated in their detail panel.
 */
function RoleLegend() {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" className="h-7 gap-1.5 px-2 text-label text-muted-foreground" aria-label="What do the roles mean?">
          <HelpCircle className="size-3.5" />
          <span className="hidden sm:inline">Roles</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[22rem] p-0">
        <div className="border-b border-border/60 px-3 py-2">
          <div className="text-xs font-medium">Roles &amp; permissions</div>
        </div>
        {ROLE_SUMMARIES.map((item, idx) => (
          <SettingsRow key={item.role} label={item.role} border={idx < ROLE_SUMMARIES.length - 1}>
            <span className="text-right text-label text-muted-foreground">{item.summary}</span>
          </SettingsRow>
        ))}
      </PopoverContent>
    </Popover>
  )
}
