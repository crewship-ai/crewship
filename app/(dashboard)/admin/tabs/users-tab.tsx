"use client"

import React from "react"
import { toast } from "sonner"
import { Activity, Clock, Lock, LogOut, Mail, Monitor, Search, Smartphone, Terminal, Unlock, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import { useSessionSafe } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { UserAvatar } from "@/components/ui/user-avatar"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { UserDataActions } from "@/components/features/admin/user-data-actions"
import type { AdminScope, AdminUser, AdminUserSessions } from "../types"
import { DetailDrawer, DrawerSection, Facts, FilterChip, Kpi, ROLES, RolePill, WorkspaceTile, ago, daysSince, roleLabel } from "./admin-kit"

interface UsersTabProps {
  users: AdminUser[]
  /** The workspace a new member would be added to, and the scope every
   *  per-person action runs in. Null while it is still resolving — there is
   *  nothing to invite INTO or act ON yet, and a control that can only fail
   *  is worse than no control. */
  workspaceId: string | null
  scope?: AdminScope
  onRefresh: () => void
}

type Status = "all" | "online" | "idle" | "locked"
const ONLINE_MIN = 15
const isOnline = (u: AdminUser) => daysSince(u.last_active_at) * 1440 < ONLINE_MIN
const isIdle = (u: AdminUser) => u.last_active_at !== undefined && daysSince(u.last_active_at) >= 30
const isLocked = (u: AdminUser) => !!u.locked_until && Date.parse(u.locked_until) > Date.now()

/**
 * Admin › Users: every account the caller may see, the workspaces each
 * belongs to, when it was last here, and whether it is locked out. A row opens
 * the person in a drawer: profile and unlock, role per workspace, signed-in
 * devices and CLI tokens, and their data (export, erase).
 */
export const UsersTab = React.memo(function UsersTab({ users, workspaceId, scope, onRefresh }: UsersTabProps) {
  // Tolerant on purpose: the tab renders in tests and previews with no
  // AuthProvider, and "which row is you" is a label, not a permission.
  const me = useSessionSafe().data?.user?.id
  const [query, setQuery] = React.useState("")
  const [status, setStatus] = React.useState<Status>("all")
  const [role, setRole] = React.useState<string>("all")
  const [openId, setOpenId] = React.useState<string | null>(null)
  const [tab, setTab] = React.useState("overview")

  const has = users.some((u) => u.last_active_at !== undefined)
  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase()
    return users.filter((u) => {
      if (q && !u.email.toLowerCase().includes(q) && !u.id.toLowerCase().includes(q) && !(u.full_name?.toLowerCase().includes(q) ?? false)) return false
      if (role !== "all" && u.role !== role) return false
      if (status === "online") return isOnline(u)
      if (status === "idle") return isIdle(u)
      if (status === "locked") return isLocked(u)
      return true
    })
  }, [query, users, role, status])

  const count = (f: (u: AdminUser) => boolean) => users.filter(f).length
  const open = users.find((u) => u.id === openId) ?? null
  const openUser = (id: string) => { setOpenId(id); setTab("overview") }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-end gap-2">
        {/* The same dialog Settings → Members opens, provisioning included. */}
        {workspaceId && <InviteMemberDialog workspaceId={workspaceId} onInvited={onRefresh} />}
      </div>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Kpi icon={Users} tint="var(--primary)" label="People" value={users.length} sub={`${count((u) => u.role === "OWNER" || u.role === "ADMIN")} owners and admins`} />
        <Kpi icon={Activity} tint="var(--success)" label="Online now" value={has ? count(isOnline) : "—"} sub={`Active in the last ${ONLINE_MIN} min`} />
        <Kpi icon={Monitor} tint="var(--info)" label="Signed in" value={has ? users.reduce((s, u) => s + (u.active_sessions ?? 0), 0) : "—"} sub={has ? `devices · ${users.reduce((s, u) => s + (u.cli_tokens ?? 0), 0)} CLI tokens` : undefined} />
        <Kpi icon={Clock} tint="var(--purple)" label="Idle 30+ days" value={has ? count(isIdle) : "—"} sub="No sign-in for a month" />
        <Kpi icon={Lock} tint="var(--destructive)" label="Locked" value={has ? count(isLocked) : "—"} sub="Too many failed sign-ins" />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <label className="flex h-8 min-w-[240px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-muted-foreground focus-within:border-primary/40">
          <Search className="h-3.5 w-3.5 shrink-0" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search by name, email or user id" aria-label="Search users"
            className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground-soft" />
          {query && <span className="shrink-0 font-mono text-[10px] tabular-nums">{filtered.length}/{users.length}</span>}
        </label>
        <FilterChip pressed={status === "all"} onClick={() => setStatus("all")} count={users.length}>Everyone</FilterChip>
        {has && <FilterChip pressed={status === "online"} onClick={() => setStatus("online")} count={count(isOnline)} dot="bg-success">Online</FilterChip>}
        {has && <FilterChip pressed={status === "idle"} onClick={() => setStatus("idle")} count={count(isIdle)}>Idle 30+ days</FilterChip>}
        {has && <FilterChip pressed={status === "locked"} onClick={() => setStatus("locked")} count={count(isLocked)} dot="bg-destructive">Locked</FilterChip>}
        <span className="mx-1 h-5 w-px bg-border" aria-hidden />
        <FilterChip pressed={role === "all"} onClick={() => setRole("all")}>All roles</FilterChip>
        {ROLES.filter((r) => count((u) => u.role === r) > 0).map((r) => (
          <FilterChip key={r} pressed={role === r} onClick={() => setRole(r)} count={count((u) => u.role === r)}>{roleLabel(r)}</FilterChip>
        ))}
      </div>

      {scope === "workspace" && (
        <p className="text-[11.5px] text-muted-foreground" data-slot="admin-scope-note">
          Members of this workspace. The instance owner (CREWSHIP_OWNER_EMAIL) sees every account on the instance.
        </p>
      )}

      <div className="overflow-x-auto rounded-card border border-border bg-card" data-slot="admin-users">
        <table className="w-full border-collapse text-xs">
          <thead className="border-b border-border">
            <tr>
              {["Person", "Role", "Workspaces", "Last active", "Signed in", "Joined"].map((h) => (
                <th key={h} scope="col" className="whitespace-nowrap bg-surface-subtle px-3 py-2 text-left font-mono text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {users.length === 0 ? (
              <tr><td colSpan={6} className="px-4 py-10 text-center text-muted-foreground">No users</td></tr>
            ) : filtered.length === 0 ? (
              <tr><td colSpan={6} className="px-4 py-10 text-center text-muted-foreground">No matching users</td></tr>
            ) : filtered.map((u, i) => {
              const memberships = u.memberships ?? (u.workspace ? [{ workspace_id: u.workspace.id, name: u.workspace.name, slug: "", role: u.role ?? "", joined_at: u.created_at }] : [])
              const online = isOnline(u)
              return (
                <tr key={u.id} onClick={() => openUser(u.id)} aria-selected={openId === u.id}
                  className="cursor-pointer border-b border-border/60 transition-colors last:border-b-0 hover:bg-[var(--row-hover-bg)] aria-selected:bg-[var(--selection-bg)] motion-safe:animate-in motion-safe:fade-in-0"
                  style={{ animationDelay: `${Math.min(i, 12) * 20}ms` }}>
                  <td className="px-3 py-2">
                    <button type="button" aria-label={u.full_name ?? u.email} onClick={(e) => { e.stopPropagation(); openUser(u.id) }} className="flex min-w-0 items-center gap-2.5 text-left">
                      <span className="relative shrink-0">
                        {/* The same face the top bar draws. */}
                        <UserAvatar name={u.full_name} email={u.email} src={u.avatar_url} className="h-7 w-7" textClassName="text-[10px]" />
                        {online && <span className="absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full border-2 border-card bg-success" aria-label="Online" />}
                      </span>
                      <span className="min-w-0">
                        <span className="flex items-center gap-1.5 font-medium">
                          <span className="truncate">{u.full_name ?? "—"}</span>
                          {u.id === me && <span className="rounded bg-primary/10 px-1 font-mono text-[9.5px] font-semibold tracking-wide text-primary-hover">YOU</span>}
                        </span>
                        <span className="block truncate text-[11px] text-muted-foreground">{u.email}</span>
                      </span>
                    </button>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2">
                    <span className="inline-flex items-center gap-1.5"><RolePill role={u.role} />{isLocked(u) && <span className="rounded-full bg-destructive/15 px-1.5 font-mono text-[10px] font-semibold text-destructive">Locked</span>}</span>
                  </td>
                  <td className="px-3 py-2">
                    <span className="flex gap-1">{memberships.slice(0, 5).map((m) => <WorkspaceTile key={m.workspace_id} id={m.workspace_id} name={m.name} size="xs" />)}
                      {memberships.length > 5 && <span className="font-mono text-[10.5px] text-muted-foreground">+{memberships.length - 5}</span>}</span>
                  </td>
                  <td className={cn("whitespace-nowrap px-3 py-2", isIdle(u) ? "text-muted-foreground-soft" : "text-muted-foreground")}>
                    {u.last_active_at === undefined ? "—" : <span className="inline-flex items-center gap-1.5">{online && <span className="h-1.5 w-1.5 rounded-full bg-success" aria-hidden />}{ago(u.last_active_at)}</span>}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-[11px] text-muted-foreground">
                    {u.active_sessions === undefined ? "—" : (
                      <span className="inline-flex items-center gap-2">
                        <span className="inline-flex items-center gap-1" title="Browsers and apps"><Monitor className="h-3 w-3" />{u.active_sessions}</span>
                        {(u.cli_tokens ?? 0) > 0 && <span className="inline-flex items-center gap-1" title="CLI tokens"><Terminal className="h-3 w-3" />{u.cli_tokens}</span>}
                      </span>
                    )}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-[11px] text-muted-foreground">{new Date(u.created_at).toLocaleDateString()}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {workspaceId && <PendingInvitations workspaceId={workspaceId} />}

      {open && (
        <DetailDrawer
          open={!!open}
          onOpenChange={(v) => { if (!v) setOpenId(null) }}
          title={open.full_name ?? open.email}
          tab={tab}
          onTab={setTab}
          tabs={[
            { key: "overview", label: "Overview" },
            { key: "workspaces", label: `Workspaces · ${open.memberships?.length ?? (open.workspace ? 1 : 0)}` },
            { key: "sessions", label: open.active_sessions !== undefined ? `Sessions · ${(open.active_sessions ?? 0) + (open.cli_tokens ?? 0)}` : "Sessions" },
            { key: "data", label: "Data" },
          ]}
          header={
            <>
              <UserAvatar name={open.full_name} email={open.email} src={open.avatar_url} className="h-10 w-10" textClassName="text-sm" />
              <div className="min-w-0 flex-1">
                <h3 className="truncate text-[15px] font-semibold">{open.full_name ?? open.email}{open.id === me && <span className="ml-1.5 align-middle rounded bg-primary/10 px-1 font-mono text-[9.5px] font-semibold text-primary-hover">YOU</span>}</h3>
                <p className="truncate text-[12px] text-muted-foreground">{open.email}</p>
              </div>
            </>
          }
        >
          {tab === "overview" && <UserOverview user={open} workspaceId={workspaceId} onChanged={onRefresh} />}
          {tab === "workspaces" && <UserWorkspaces user={open} onChanged={onRefresh} />}
          {tab === "sessions" && workspaceId && <UserSessions user={open} workspaceId={workspaceId} isMe={open.id === me} onChanged={onRefresh} />}
          {tab === "data" && (
            <section aria-label={`Data actions for ${open.email}`}>
              {workspaceId ? (
                <UserDataActions userId={open.id} email={open.email} workspaceId={workspaceId} onErased={() => { setOpenId(null); onRefresh() }} />
              ) : (
                <p className="text-[11px] text-muted-foreground">Resolving the workspace…</p>
              )}
            </section>
          )}
        </DetailDrawer>
      )}
    </div>
  )
})

const adminUserURL = (userId: string, workspaceId: string, tail: string) =>
  `/api/v1/admin/users/${encodeURIComponent(userId)}/${tail}?workspace_id=${encodeURIComponent(workspaceId)}`

function UserOverview({ user, workspaceId, onChanged }: { user: AdminUser; workspaceId: string | null; onChanged: () => void }) {
  const [busy, setBusy] = React.useState(false)
  const locked = isLocked(user)
  const unlock = async () => {
    if (!workspaceId) return
    setBusy(true)
    try {
      const res = await apiFetch(adminUserURL(user.id, workspaceId, "unlock"), { method: "POST" })
      if (!res.ok) { toast.error(await readApiError(res, "Could not unlock the account")); return }
      toast.success(`${user.full_name ?? user.email} can sign in again`)
      onChanged()
    } catch {
      toast.error("Could not unlock the account")
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      {locked && (
        <div className="grid gap-2 rounded-lg border border-destructive/40 bg-destructive/5 p-3" data-slot="admin-user-locked">
          <p className="flex items-center gap-2 text-[12.5px] font-medium text-destructive"><Lock className="h-3.5 w-3.5" />Account locked</p>
          <p className="text-[12px] text-muted-foreground">
            {user.failed_login_count ?? 0} failed sign-ins in a row. It unlocks by itself at {new Date(user.locked_until!).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}.
          </p>
          <Button variant="outline" size="sm" className="h-7 w-fit text-xs" onClick={unlock} disabled={busy || !workspaceId}><Unlock className="mr-1.5 h-3 w-3" />Unlock now</Button>
        </div>
      )}
      <DrawerSection label="Profile">
        <Facts rows={[
          ["Email", user.email],
          ["Role here", <RolePill key="r" role={user.role} />],
          ["Account created", new Date(user.created_at).toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" })],
          ["Last active", user.last_active_at === undefined ? "—" : ago(user.last_active_at)],
          ["Email verified", user.email_verified === undefined ? "—" : user.email_verified ? "Yes" : <span key="v" className="text-warn">No</span>],
          ["Failed sign-ins", <span key="f" className="font-mono">{user.failed_login_count ?? 0}</span>],
        ]} />
      </DrawerSection>
    </>
  )
}

function UserWorkspaces({ user, onChanged }: { user: AdminUser; onChanged: () => void }) {
  const [busy, setBusy] = React.useState<string | null>(null)
  const memberships = user.memberships ?? []
  const act = async (wsId: string, memberId: string, init: RequestInit, done: string) => {
    setBusy(memberId)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${wsId}/members/${memberId}?workspace_id=${wsId}`, init)
      if (!res.ok) { toast.error(await readApiError(res, "That change was refused")); return }
      toast.success(done)
      onChanged()
    } catch {
      toast.error("That change did not go through")
    } finally {
      setBusy(null)
    }
  }
  if (memberships.length === 0) return <p className="text-[12px] text-muted-foreground">Not a member of any workspace.</p>
  return (
    <DrawerSection label="Member of">
      <ul className="overflow-hidden rounded-lg border border-border">
        {memberships.map((m) => (
          <li key={m.workspace_id} className="flex items-center gap-2.5 border-b border-border/60 px-3 py-2 last:border-b-0">
            <WorkspaceTile id={m.workspace_id} name={m.name} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[12.5px]">{m.name}</span>
              <span className="block truncate text-[11px] text-muted-foreground">member since {new Date(m.joined_at).toLocaleDateString()}</span>
            </span>
            {m.member_id ? (
              <>
                <select aria-label={`Role in ${m.name}`} value={m.role} disabled={busy === m.member_id}
                  onChange={(e) => act(m.workspace_id, m.member_id!, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ role: e.target.value }) }, `Role changed to ${roleLabel(e.target.value)}`)}
                  className="h-7 rounded-md border border-border bg-card px-1.5 text-[12px]">
                  {ROLES.map((r) => <option key={r} value={r}>{roleLabel(r)}</option>)}
                </select>
                <button type="button" aria-label={`Remove from ${m.name}`} disabled={busy === m.member_id}
                  onClick={() => { if (window.confirm(`Remove ${user.full_name ?? user.email} from ${m.name}?`)) void act(m.workspace_id, m.member_id!, { method: "DELETE" }, "Removed from the workspace") }}
                  className="grid h-7 w-7 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground">
                  <X className="h-3.5 w-3.5" />
                </button>
              </>
            ) : <RolePill role={m.role} />}
          </li>
        ))}
      </ul>
    </DrawerSection>
  )
}

function UserSessions({ user, workspaceId, isMe, onChanged }: { user: AdminUser; workspaceId: string; isMe: boolean; onChanged: () => void }) {
  const [data, setData] = React.useState<AdminUserSessions | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [busy, setBusy] = React.useState<string | null>(null)
  const load = React.useCallback(async () => {
    try {
      const res = await apiFetch(adminUserURL(user.id, workspaceId, "sessions"))
      if (!res.ok) { setError(await readApiError(res, "Sessions could not be read")); return }
      setData(await res.json())
      setError(null)
    } catch {
      setError("Sessions could not be read")
    }
  }, [user.id, workspaceId])
  React.useEffect(() => { void load() }, [load])

  const post = async (tail: string, key: string, done: string) => {
    setBusy(key)
    try {
      const res = await apiFetch(adminUserURL(user.id, workspaceId, tail), { method: "POST" })
      if (!res.ok) { toast.error(await readApiError(res, "That did not go through")); return }
      toast.success(done)
      await load()
      onChanged()
    } catch {
      toast.error("That did not go through")
    } finally {
      setBusy(null)
    }
  }

  /** Sign out several sessions of one device; one request each, then reload. */
  const revokeMany = async (key: string, ids: string[]) => {
    setBusy(key)
    let failed = 0
    for (const id of ids) {
      try {
        const res = await apiFetch(adminUserURL(user.id, workspaceId, `sessions/${encodeURIComponent(id)}/revoke`), { method: "POST" })
        if (!res.ok) failed++
      } catch {
        failed++
      }
    }
    if (failed) toast.error(`${failed} of ${ids.length} sessions could not be signed out`)
    else toast.success(ids.length > 1 ? `Signed out of ${ids.length} sessions` : "Signed out of that device")
    await load()
    onChanged()
    setBusy(null)
  }

  if (error) return <p className="text-[12px] text-destructive">{error}</p>
  if (!data) return <div className="grid gap-2"><Skeleton className="h-10 w-full" /><Skeleton className="h-10 w-full" /></div>
  const mobile = (ua: string | null) => !!ua && /iphone|android|mobile|ipad/i.test(ua)
  // The same browser on the same address signs in again and again (every
  // expired cookie is a new session); a list of 200 identical rows hides the
  // one device that matters. Group them, newest first.
  const currentId = data.sessions.find((x) => x.current)?.id
  const groups = Object.values(
    data.sessions.reduce<Record<string, { key: string; ua: string | null; ip: string | null; ids: string[]; last: string; current: boolean }>>((acc, x) => {
      const key = `${describeAgent(x.user_agent)}|${x.ip ?? ""}`
      const g = (acc[key] ??= { key, ua: x.user_agent, ip: x.ip, ids: [], last: x.last_used_at, current: false })
      g.ids.push(x.id)
      if (x.last_used_at > g.last) g.last = x.last_used_at
      if (x.current) g.current = true
      return acc
    }, {}),
  ).sort((a, b) => Number(b.current) - Number(a.current) || b.last.localeCompare(a.last))
  return (
    <>
      <DrawerSection label="Signed-in devices">
        {groups.length === 0 ? <p className="text-[12px] text-muted-foreground">Not signed in anywhere.</p> : (
          <ul className="overflow-hidden rounded-lg border border-border">
            {groups.map((g) => {
              const Icon = mobile(g.ua) ? Smartphone : Monitor
              const revocable = g.ids.filter((id) => id !== currentId)
              return (
                <li key={g.key} className="flex items-center gap-2.5 border-b border-border/60 px-3 py-2 last:border-b-0" data-slot="admin-session-group">
                  <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[12.5px]" title={g.ua ?? ""}>
                      {describeAgent(g.ua)}
                      {g.ids.length > 1 && <span className="ml-1.5 font-mono text-[11px] text-muted-foreground">× {g.ids.length}</span>}
                      {g.current && <span className="ml-1.5 text-[11px] text-success">this session</span>}
                    </span>
                    <span className="block truncate text-[11px] text-muted-foreground">last used {ago(g.last)}{g.ip ? ` · ${g.ip}` : ""}</span>
                  </span>
                  {revocable.length > 0 && (
                    <Button variant="outline" size="sm" className="h-7 text-xs" disabled={busy === g.key}
                      onClick={() => revokeMany(g.key, revocable)}>
                      {revocable.length > 1 ? `Sign out ${revocable.length}` : "Sign out"}
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </DrawerSection>
      <DrawerSection label="CLI tokens">
        {data.cli_tokens.length === 0 ? <p className="text-[12px] text-muted-foreground">No CLI tokens.</p> : (
          <ul className="overflow-hidden rounded-lg border border-border">
            {data.cli_tokens.map((t) => (
              <li key={t.id} className="flex items-center gap-2.5 border-b border-border/60 px-3 py-2 last:border-b-0">
                <Terminal className="h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[12.5px]">{t.name}</span>
                  <span className="block truncate text-[11px] text-muted-foreground">
                    {Array.isArray(t.scopes) ? t.scopes.join(", ") : t.scopes || "all scopes"} · used {ago(t.last_used_at)}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </DrawerSection>
      {data.sessions.some((s) => !s.current) && !isMe && (
        <Button variant="outline" size="sm" className="h-8 w-fit text-xs text-destructive" disabled={busy === "all"}
          onClick={() => { if (window.confirm(`Sign ${user.full_name ?? user.email} out of every device? Their password stays the same.`)) void post("sessions/revoke-all", "all", "Signed out everywhere") }}>
          <LogOut className="mr-1.5 h-3 w-3" />Sign out everywhere
        </Button>
      )}
    </>
  )
}

/** "Chrome · macOS" from a user-agent string; the raw string is on hover. */
export function describeAgent(ua: string | null): string {
  if (!ua) return "Unknown device"
  if (/crewship-cli|go-http-client/i.test(ua)) return "Crewship CLI"
  const browser = /edg\//i.test(ua) ? "Edge" : /firefox\//i.test(ua) ? "Firefox" : /chrome\//i.test(ua) ? "Chrome" : /safari\//i.test(ua) ? "Safari" : "Browser"
  const os = /iphone|ipad/i.test(ua) ? "iOS" : /android/i.test(ua) ? "Android" : /mac os x/i.test(ua) ? "macOS" : /windows/i.test(ua) ? "Windows" : /linux/i.test(ua) ? "Linux" : ""
  return os ? `${browser} · ${os}` : browser
}

interface Invitation { id: string; email: string; role: string; expires_at: string; created_at: string }

/** Invitations sent from this workspace and not yet accepted. */
function PendingInvitations({ workspaceId }: { workspaceId: string }) {
  const [items, setItems] = React.useState<Invitation[] | null>(null)
  React.useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const r = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/invitations?workspace_id=${encodeURIComponent(workspaceId)}`)
        const rows = r?.ok ? await r.json() : []
        if (!cancelled) setItems(Array.isArray(rows) ? rows : [])
      } catch {
        if (!cancelled) setItems([])
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId])
  if (!items || items.length === 0) return null
  return (
    <section aria-label="Pending invitations" className="overflow-hidden rounded-card border border-border bg-card">
      <div className="flex items-center gap-3 border-b border-border px-4 py-3">
        <span className="icon-tile grid h-8 w-8 place-items-center rounded-lg" style={{ "--ic": "var(--purple)" } as React.CSSProperties} aria-hidden><Mail className="h-4 w-4" /></span>
        <div><h3 className="text-sm font-semibold">Pending invitations</h3><p className="text-[12px] text-muted-foreground">Sent from this workspace, not accepted yet</p></div>
      </div>
      <ul>
        {items.map((inv) => {
          const expired = Date.parse(inv.expires_at) < Date.now()
          return (
            <li key={inv.id} className="flex items-center gap-3 border-b border-border/60 px-4 py-2.5 last:border-b-0">
              <Mail className="h-4 w-4 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[12.5px]">{inv.email}</span>
                <span className="block text-[11px] text-muted-foreground">sent {ago(inv.created_at)}</span>
              </span>
              <RolePill role={inv.role} />
              {expired && <span className="rounded-full bg-warn/15 px-1.5 font-mono text-[10px] text-warn">Expired</span>}
            </li>
          )
        })}
      </ul>
    </section>
  )
}
