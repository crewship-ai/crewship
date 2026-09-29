import React from "react"
import Link from "next/link"
import { Activity, AlertTriangle, Boxes, Building2, DollarSign, Info, Mail, Plus, Search, Users } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { UserAvatar } from "@/components/ui/user-avatar"
import { CreateWorkspaceDialog } from "@/components/layout/workspace-switcher"
import type { AdminOrg, AdminScope, AdminUser, LicenseInfo } from "../types"
import { DetailDrawer, DrawerSection, Facts, FilterChip, Kpi, RolePill, WeekLine, WorkspaceTile, ago } from "./admin-kit"

interface WorkspacesTabProps {
  orgs: AdminOrg[]
  /** For the member faces on each row and the drawer's member list. */
  users?: AdminUser[]
  scope?: AdminScope
  license?: LicenseInfo | null
  /** Re-read the admin data after a create, so the list the operator is
   *  looking at catches up on its own. A created workspace that does not
   *  appear reads as a failed create. */
  onRefresh: () => void
}

type Filter = "all" | "busy" | "quiet" | "limit"
type Sort = "name" | "members" | "crews" | "runs" | "cost" | "activity"

const BUSY = 20
const runs = (o: AdminOrg) => o.runs_7d ?? (o.runs_by_day ?? []).reduce((s, v) => s + v, 0)
const usd = (v: number) => (v > 0 && v < 0.01 ? "<$0.01" : `$${v.toFixed(2)}`)
const LANG: Record<string, string> = { cs: "Čeština", en: "English", de: "Deutsch", sk: "Slovenčina" }

/**
 * Admin › Workspaces: every workspace the caller may see — all of them for
 * the instance owner, the current one for everyone else — with who is in it,
 * how much it runs, what it costs and how close it is to the licence. A row
 * opens the workspace in a drawer.
 */
export const WorkspacesTab = React.memo(function WorkspacesTab({ orgs, users = [], scope, license, onRefresh }: WorkspacesTabProps) {
  const [createOpen, setCreateOpen] = React.useState(false)
  const [query, setQuery] = React.useState("")
  const [filter, setFilter] = React.useState<Filter>("all")
  const [sort, setSort] = React.useState<Sort>("runs")
  const [openId, setOpenId] = React.useState<string | null>(null)
  const [tab, setTab] = React.useState("overview")

  const maxCrews = license?.max_crews ?? 0
  const maxMembers = license?.max_members ?? 0
  const nearLimit = (o: AdminOrg) => (maxCrews > 0 && o._count_crews / maxCrews >= 0.7) || (maxMembers > 0 && o._count_members / maxMembers >= 0.8)
  const membersOf = React.useCallback(
    (id: string) => users.filter((u) => (u.memberships?.some((m) => m.workspace_id === id) ?? u.workspace?.id === id)),
    [users],
  )

  const counts = {
    all: orgs.length,
    busy: orgs.filter((o) => runs(o) > BUSY).length,
    quiet: orgs.filter((o) => runs(o) <= BUSY).length,
    limit: orgs.filter(nearLimit).length,
  }
  const rows = React.useMemo(() => {
    const q = query.trim().toLowerCase()
    const list = orgs.filter((o) => {
      if (q && !o.name.toLowerCase().includes(q) && !o.slug.toLowerCase().includes(q)) return false
      if (filter === "busy") return runs(o) > BUSY
      if (filter === "quiet") return runs(o) <= BUSY
      if (filter === "limit") return nearLimit(o)
      return true
    })
    const key: Record<Sort, (o: AdminOrg) => number | string> = {
      name: (o) => o.name.toLowerCase(), members: (o) => -o._count_members, crews: (o) => -o._count_crews,
      runs: (o) => -runs(o), cost: (o) => -(o.cost_30d_usd ?? 0), activity: (o) => -(Date.parse(o.last_activity_at ?? "") || 0),
    }
    return [...list].sort((a, b) => {
      const x = key[sort](a), y = key[sort](b)
      return typeof x === "string" ? x.localeCompare(y as string) : (x as number) - (y as number) || a.name.localeCompare(b.name)
    })
  }, [orgs, query, filter, sort]) // eslint-disable-line react-hooks/exhaustive-deps

  const total = (f: (o: AdminOrg) => number) => orgs.reduce((s, o) => s + f(o), 0)
  const busiest = [...orgs].sort((a, b) => runs(b) - runs(a))[0]
  const open = orgs.find((o) => o.id === openId) ?? null
  const hasActivity = orgs.some((o) => o.runs_by_day !== undefined)

  const Th = ({ k, children, className }: { k?: Sort; children: React.ReactNode; className?: string }) => (
    <th scope="col" className={cn("whitespace-nowrap bg-surface-subtle px-3 py-2 text-left font-mono text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground", className)}
      aria-sort={k && sort === k ? "descending" : undefined}>
      {k ? <button type="button" onClick={() => setSort(k)} className="uppercase hover:text-foreground">{children}{sort === k ? " ↓" : ""}</button> : children}
    </th>
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-end gap-2">
        {/* The SAME dialog the workspace switcher opens: two create forms for
            one object drift, and the slug rule is the kind of thing that drifts
            silently. */}
        <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => setCreateOpen(true)}>
          <Plus className="mr-1.5 h-3 w-3" />
          Create workspace
        </Button>
      </div>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Kpi icon={Building2} tint="var(--purple)" label="Workspaces" value={orgs.length} sub={scope === "workspace" ? "The one you are in" : "On this instance"} />
        <Kpi icon={Users} tint="var(--primary)" label="Members" value={total((o) => o._count_members)} sub={scope === "instance" ? `${users.length} people` : undefined} />
        <Kpi icon={Boxes} tint="var(--info)" label="Crews" value={total((o) => o._count_crews)} sub={`${total((o) => o._count_agents)} agents`} />
        <Kpi icon={Activity} tint="var(--success)" label="Runs · 7 days" value={hasActivity ? total(runs) : "—"} sub={busiest && runs(busiest) > 0 ? `Busiest: ${busiest.name}` : undefined} />
        <Kpi icon={DollarSign} tint="var(--warn)" label="Spend · 30 days" value={orgs.some((o) => o.cost_30d_usd !== undefined) ? usd(total((o) => o.cost_30d_usd ?? 0)) : "—"} sub="LLM tokens" />
      </div>

      {scope === "workspace" && (
        <p className="flex items-start gap-2 rounded-card border border-border bg-card px-4 py-2.5 text-[12px] text-muted-foreground" data-slot="admin-scope-note">
          <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          You see the workspace you are in. The instance owner — the account named by CREWSHIP_OWNER_EMAIL on the server — sees every workspace here.
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <label className="flex h-8 min-w-[240px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-muted-foreground focus-within:border-primary/40">
          <Search className="h-3.5 w-3.5 shrink-0" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search workspaces" aria-label="Search workspaces"
            className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground-soft" />
        </label>
        <FilterChip pressed={filter === "all"} onClick={() => setFilter("all")} count={counts.all}>All</FilterChip>
        <FilterChip pressed={filter === "busy"} onClick={() => setFilter("busy")} count={counts.busy}>Busy</FilterChip>
        <FilterChip pressed={filter === "quiet"} onClick={() => setFilter("quiet")} count={counts.quiet}>Quiet</FilterChip>
        {license && <FilterChip pressed={filter === "limit"} onClick={() => setFilter("limit")} count={counts.limit} dot="bg-warn">Near licence limit</FilterChip>}
      </div>

      <div className="overflow-x-auto rounded-card border border-border bg-card" data-slot="admin-workspaces">
        <table className="w-full border-collapse text-xs">
          <thead className="border-b border-border">
            <tr>
              <Th k="name">Workspace</Th><Th k="members">Members</Th><Th k="crews">Crews · agents</Th>
              {license && <Th>Licence</Th>}
              <Th k="runs">Runs · 7 days</Th><Th k="cost" className="text-right">Spend · 30d</Th><Th k="activity">Last activity</Th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr><td colSpan={7} className="px-4 py-10 text-center text-muted-foreground">{orgs.length === 0 ? "No workspaces" : "No workspace matches."}</td></tr>
            ) : rows.map((o, i) => {
              const people = membersOf(o.id)
              const crewPct = maxCrews ? o._count_crews / maxCrews : 0
              return (
                <tr key={o.id} onClick={() => { setOpenId(o.id); setTab("overview") }} aria-selected={openId === o.id}
                  className="cursor-pointer border-b border-border/60 transition-colors last:border-b-0 hover:bg-[var(--row-hover-bg)] aria-selected:bg-[var(--selection-bg)] motion-safe:animate-in motion-safe:fade-in-0"
                  style={{ animationDelay: `${Math.min(i, 12) * 20}ms` }}>
                  <td className="px-3 py-2.5">
                    <button type="button" className="flex min-w-0 items-center gap-2.5 text-left" aria-label={`Open ${o.name}`} onClick={(e) => { e.stopPropagation(); setOpenId(o.id); setTab("overview") }}>
                      <WorkspaceTile id={o.id} name={o.name} />
                      <span className="min-w-0">
                        <span className="flex items-center gap-1.5 font-medium">
                          <span className="truncate">{o.name}</span>
                          {o.current && <span className="rounded bg-primary/10 px-1 font-mono text-[9.5px] font-semibold tracking-wide text-primary-hover">CURRENT</span>}
                        </span>
                        <span className="block truncate font-mono text-[11px] text-muted-foreground">{o.slug} · since {new Date(o.created_at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}</span>
                      </span>
                    </button>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2.5">
                    <span className="inline-flex items-center gap-2">
                      <span className="flex -space-x-1.5">
                        {people.slice(0, 4).map((u) => <UserAvatar key={u.id} name={u.full_name} email={u.email} src={u.avatar_url} className="h-5 w-5 ring-2 ring-card" textClassName="text-[8px]" />)}
                      </span>
                      <span className="font-mono tabular-nums text-muted-foreground">{o._count_members}</span>
                    </span>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2.5 font-mono tabular-nums">{o._count_crews} <span className="text-muted-foreground-soft">·</span> {o._count_agents}</td>
                  {license && (
                    <td className="whitespace-nowrap px-3 py-2.5">
                      <span className="inline-flex items-center gap-2" title={`${o._count_crews} of ${maxCrews} crews`}>
                        <span className="block h-1.5 w-14 overflow-hidden rounded-full bg-muted">
                          <span className={cn("block h-full rounded-full", crewPct > 1 ? "bg-destructive" : crewPct >= 0.7 ? "bg-warn" : "bg-primary")} style={{ width: `${Math.min(100, crewPct * 100)}%` }} />
                        </span>
                        <span className="font-mono text-[11px] text-muted-foreground">{o._count_crews}/{maxCrews}</span>
                      </span>
                    </td>
                  )}
                  <td className="whitespace-nowrap px-3 py-2.5">
                    {o.runs_by_day ? (
                      <span className="inline-flex items-center gap-2"><WeekLine values={o.runs_by_day} muted={runs(o) <= BUSY} /><span className="font-mono tabular-nums">{runs(o)}</span></span>
                    ) : <span className="text-muted-foreground">—</span>}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2.5 text-right font-mono tabular-nums">{o.cost_30d_usd === undefined ? "—" : usd(o.cost_30d_usd)}</td>
                  <td className="whitespace-nowrap px-3 py-2.5 text-muted-foreground">
                    <span className="inline-flex items-center gap-2">
                      {o.last_activity_at !== undefined ? ago(o.last_activity_at) : new Date(o.created_at).toLocaleDateString()}
                      {o.allow_privileged_credentials && <AlertTriangle className="h-3.5 w-3.5 text-warn" aria-label="Privileged credentials allowed" />}
                      {(o.pending_invitations ?? 0) > 0 && (
                        <span className="rounded-full bg-warn/15 px-1.5 font-mono text-[10px] text-warn">{o.pending_invitations} invite{o.pending_invitations === 1 ? "" : "s"}</span>
                      )}
                    </span>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {open && (
        <DetailDrawer
          open={!!open}
          onOpenChange={(v) => { if (!v) setOpenId(null) }}
          title={open.name}
          tab={tab}
          onTab={setTab}
          tabs={[{ key: "overview", label: "Overview" }, { key: "members", label: `Members · ${open._count_members}` }]}
          header={
            <>
              <WorkspaceTile id={open.id} name={open.name} size="lg" />
              <div className="min-w-0 flex-1">
                <h3 className="truncate text-[15px] font-semibold">{open.name}</h3>
                <p className="font-mono text-[11.5px] text-muted-foreground">{open.slug}</p>
              </div>
              {open.current && <Link href="/settings?tab=workspace" className="shrink-0 rounded-md border border-border px-2 py-1 text-[11px] font-medium hover:bg-accent">Settings</Link>}
            </>
          }
        >
          {tab === "overview" ? (
            <>
              <div className="grid grid-cols-2 gap-2">
                <Kpi icon={Activity} tint="var(--success)" label="Runs · 7d" value={open.runs_by_day ? runs(open) : "—"} sub={open.runs_by_day ? <WeekLine values={open.runs_by_day} /> : undefined} />
                <Kpi icon={DollarSign} tint="var(--warn)" label="Spend · 30d" value={open.cost_30d_usd === undefined ? "—" : usd(open.cost_30d_usd)} />
                <Kpi icon={Boxes} tint="var(--primary)" label="Crews" value={maxCrews ? `${open._count_crews}/${maxCrews}` : open._count_crews} sub={`${open._count_agents} agents`} />
                <Kpi icon={Users} tint="var(--purple)" label="Members" value={maxMembers ? `${open._count_members}/${maxMembers}` : open._count_members}
                  sub={maxMembers ? (open._count_members > maxMembers ? "Over the licensed seats" : `${maxMembers - open._count_members} seats left`) : undefined} />
              </div>
              <DrawerSection label="Details">
                <Facts rows={[
                  ["Slug", <span key="s" className="font-mono">{open.slug}</span>],
                  ["Created", new Date(open.created_at).toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" })],
                  ["Language", open.preferred_language ? LANG[open.preferred_language] ?? open.preferred_language : "Default"],
                  ["Run retention", open.run_retention_days ? `${open.run_retention_days} days` : "Keep all"],
                  ["Privileged credentials", open.allow_privileged_credentials ? <span key="p" className="text-warn">Allowed</span> : "Off"],
                  ["Pending invitations", <span key="i" className="inline-flex items-center gap-1.5"><Mail className="h-3 w-3" />{open.pending_invitations ?? 0}</span>],
                  ["Last activity", ago(open.last_activity_at)],
                ]} />
              </DrawerSection>
            </>
          ) : (
            <DrawerSection label="Members">
              {membersOf(open.id).length === 0 ? (
                <p className="text-[12px] text-muted-foreground">No members listed for this workspace.</p>
              ) : (
                <ul className="overflow-hidden rounded-lg border border-border">
                  {membersOf(open.id).map((u) => {
                    const m = u.memberships?.find((x) => x.workspace_id === open.id)
                    return (
                      <li key={u.id} className="flex items-center gap-2.5 border-b border-border/60 px-3 py-2 last:border-b-0">
                        <UserAvatar name={u.full_name} email={u.email} src={u.avatar_url} className="h-7 w-7" textClassName="text-[10px]" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-[12.5px]">{u.full_name ?? u.email}</span>
                          <span className="block truncate text-[11px] text-muted-foreground">{u.email}</span>
                        </span>
                        <RolePill role={m?.role ?? u.role} />
                      </li>
                    )
                  })}
                </ul>
              )}
              <p className="mt-2 text-[11px] text-muted-foreground">Change roles on the Users tab.</p>
            </DrawerSection>
          )}
        </DetailDrawer>
      )}

      <CreateWorkspaceDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        // The switcher's version switches you INTO the new workspace; from the
        // admin console you are auditing the instance, not moving house, so the
        // list refreshes and the operator stays where they were.
        onCreated={() => onRefresh()}
      />
    </div>
  )
})
