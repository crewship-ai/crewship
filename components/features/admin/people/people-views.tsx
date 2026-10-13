"use client"

import * as React from "react"
import { House } from "lucide-react"

import { cn } from "@/lib/utils"
import { WeekLine, WorkspaceTile, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  daysUntil, displayName, isEmptyWorkspace, isLastOwner, isPersonal, ownersOf, personStatus, personalOwnerName, roleIn, STATUS_LABEL, type Person, type Workspace,
} from "./people-model"
import { PersonLabel, RoleSelect, StatusChip, roleLabel } from "./people-parts"
import type { usePeople } from "./use-people"
import type { Finding } from "@/components/features/settings/members/members-attention"
import { StatusPill } from "@/components/ui/status-pill"
import { settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"

type Actions = ReturnType<typeof usePeople>["actions"]

const th = settingsTh
const td = settingsTd

/**
 * What on the instance needs an admin, most pressing first, each with the
 * verb that deals with it: a locked account (Unlock), an unfinished sign-up
 * (Open — the profile issues a new link), someone who belongs nowhere, and
 * personal accounts nobody uses. Drawn by Members' "Needs attention" card.
 */
export function peopleFindings({ people, workspaces, actions, onOpen, onFacet, onPersonal }: {
  people: Person[]
  workspaces: Workspace[]
  actions: Actions
  onOpen: (id: string) => void
  onFacet: (facet: "setup" | "locked" | "noaccess") => void
  onPersonal: () => void
}): Finding[] {
  const out: Finding[] = []
  const locked = people.filter((p) => personStatus(p) === "locked")
  for (const p of locked.slice(0, 2)) {
    out.push({ key: `locked:${p.id}`, severity: "high", title: `${displayName(p)} is locked out.`,
      body: `${p.failed_login_count ?? 0} failed sign-ins in a row. Unlock them once you know it was them.`,
      verb: "Unlock", run: () => void actions.unlock(p.id, `${displayName(p)} can sign in again`) })
  }
  if (locked.length > 2) out.push({ key: "locked", severity: "high", title: `${locked.length - 2} more accounts are locked.`, body: "Too many failed sign-ins.", verb: "Show", run: () => onFacet("locked") })
  const setup = people.filter((p) => personStatus(p) === "setup")
  if (setup.length === 1) {
    const p = setup[0]
    const days = daysUntil(p.setup_link_expires_at)
    out.push({ key: `setup:${p.id}`, severity: "medium", title: `${displayName(p)} has not chosen a password yet.`,
      body: `Their setup link runs out ${days <= 1 ? "within a day" : `in ${days} days`}. Open them to issue a new one or void it.`,
      verb: "Open", run: () => onOpen(p.id) })
  } else if (setup.length > 1) {
    out.push({ key: "setup", severity: "medium", title: `${setup.length} people have not chosen a password yet.`, body: "Their setup links are still waiting.", verb: `Show ${setup.length}`, run: () => onFacet("setup") })
  }
  const nowhere = people.filter((p) => p.memberships.length === 0 && personStatus(p) !== "suspended")
  if (nowhere.length === 1) {
    out.push({ key: `noaccess:${nowhere[0].id}`, severity: "medium", title: `${displayName(nowhere[0])} belongs to no workspace.`,
      body: "They can sign in but see nothing. Add them to a workspace, or suspend the account.", verb: "Open", run: () => onOpen(nowhere[0].id) })
  } else if (nowhere.length > 1) {
    out.push({ key: "noaccess", severity: "medium", title: `${nowhere.length} people belong to no workspace.`, body: "They can sign in but see nothing.", verb: `Show ${nowhere.length}`, run: () => onFacet("noaccess") })
  }
  const idle = workspaces.filter((w) => isPersonal(w) && isEmptyWorkspace(w))
  if (idle.length > 0) {
    out.push({ key: "personal", severity: "low", title: `${idle.length} personal ${idle.length === 1 ? "account is" : "accounts are"} empty.`,
      body: "Each was made when someone signed up without an invitation: no crews, no runs. Review them, and remove the ones nobody uses.", verb: `Review ${idle.length}`, run: onPersonal })
  }
  return out
}

/** A person's place in one workspace, as a chip: the workspace's tile and
 *  name, or — for their own personal account — a quiet "Personal account". */
export function MembershipChip({ m, personal }: { m: Person["memberships"][number]; personal: boolean }) {
  if (personal) {
    return (
      <span className="inline-flex items-center gap-1 rounded-md border border-dashed border-border px-1.5 py-0.5 text-label text-muted-foreground" title={`${m.name} · personal account`}>
        <House className="h-3 w-3" aria-hidden />Personal account<span className="text-muted-foreground-soft">{roleLabel(m.role)}</span>
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-md border border-border py-0.5 pl-0.5 pr-1.5 text-label">
      <WorkspaceTile id={m.workspace_id} name={m.name} size="xs" />{m.name}<span className="text-muted-foreground">{roleLabel(m.role)}</span>
    </span>
  )
}

export function PeopleTable({ people, workspaces, onOpen, selected, onSelect, selectable }: {
  people: Person[]
  workspaces: Workspace[]
  onOpen: (id: string) => void
  selected?: Set<string>
  onSelect?: (ids: string[], on: boolean) => void
  /** Who may be ticked (not the caller, not an instance owner from env). */
  selectable?: (p: Person) => boolean
}) {
  const personal = new Set(workspaces.filter(isPersonal).map((w) => w.id))
  const canTick = Boolean(selected && onSelect)
  const tickable = people.filter((p) => selectable?.(p) ?? true)
  const allOn = tickable.length > 0 && tickable.every((p) => selected?.has(p.id))
  return (
    <div>
      <div className="overflow-x-auto">
        <table className={settingsTable}>
          <thead>
            <tr>
              {canTick && (
                <th className={cn(th, "w-9")}>
                  <input type="checkbox" aria-label="Select everyone shown" checked={allOn} disabled={tickable.length === 0}
                    onChange={(e) => onSelect!(tickable.map((p) => p.id), e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
                </th>
              )}
              <th className={th}>Person</th><th className={th}>Workspaces</th><th className={th}>Last active</th><th className={th}>Status</th><th className={cn(th, "text-right")}>Sessions</th>
            </tr>
          </thead>
          <tbody>
            {people.map((p) => (
              <tr key={p.id} tabIndex={0} onClick={() => onOpen(p.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(p.id) }}
                className={settingsTableRowLink} data-person={p.id}>
                {canTick && (
                  <td className={td} onClick={(e) => e.stopPropagation()}>
                    <input type="checkbox" aria-label={`Select ${displayName(p)}`} checked={selected!.has(p.id)} disabled={!(selectable?.(p) ?? true)}
                      onChange={(e) => onSelect!([p.id], e.target.checked)} className="size-3.5 accent-[var(--primary)] disabled:opacity-30" />
                  </td>
                )}
                <td className={td}><PersonLabel person={p} /></td>
                <td className={td}>
                  <span className="flex flex-wrap gap-1">
                    {p.memberships.length === 0 && <span className="text-muted-foreground">No workspace</span>}
                    {[...p.memberships].sort((a, b) => Number(personal.has(a.workspace_id)) - Number(personal.has(b.workspace_id))).map((m) => (
                      <MembershipChip key={m.workspace_id} m={m} personal={personal.has(m.workspace_id)} />
                    ))}
                  </span>
                </td>
                <td className={cn(td, "whitespace-nowrap text-muted-foreground")}>{ago(p.last_active_at)}</td>
                <td className={td}><StatusChip person={p} /></td>
                <td className={cn(td, "text-right font-mono tabular-nums")}>{p.active_sessions ?? 0}</td>
              </tr>
            ))}
            {people.length === 0 && <tr><td colSpan={canTick ? 6 : 5} className="px-3 py-6 text-center text-muted-foreground">No one matches.</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/**
 * The personal accounts: one row per person who has one, what is in it,
 * and where else they work. Only empty ones can be ticked for removal.
 */
export function PersonalWorkspacesTable({ workspaces, people, onOpen, selected, onSelect }: {
  workspaces: Workspace[]
  people: Person[]
  onOpen: (id: string) => void
  selected: Set<string>
  onSelect: (ids: string[], on: boolean) => void
}) {
  const empty = workspaces.filter(isEmptyWorkspace)
  const allOn = empty.length > 0 && empty.every((w) => selected.has(w.id))
  return (
    <div className="overflow-x-auto">
      <table className={settingsTable} aria-label="Personal accounts">
        <thead>
          <tr>
            <th className={cn(th, "w-9")}>
              <input type="checkbox" aria-label="Select every empty one" checked={allOn} disabled={empty.length === 0}
                onChange={(e) => onSelect(empty.map((w) => w.id), e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
            </th>
            <th className={th}>Person</th><th className={cn(th, "text-right")}>Crews</th><th className={cn(th, "text-right")}>Agents</th><th className={cn(th, "text-right")}>Runs · 7 d</th><th className={th}>Also works in</th>
          </tr>
        </thead>
        <tbody>
          {workspaces.map((w) => {
            const owner = people.find((p) => p.memberships.some((m) => m.workspace_id === w.id && m.role === "OWNER"))
            const elsewhere = owner?.memberships.filter((m) => m.workspace_id !== w.id && !workspaces.some((x) => x.id === m.workspace_id)) ?? []
            const canDelete = isEmptyWorkspace(w)
            return (
              <tr key={w.id} tabIndex={0} onClick={() => onOpen(w.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(w.id) }} className={settingsTableRowLink} data-workspace={w.id}>
                <td className={td} onClick={(e) => e.stopPropagation()}>
                  <input type="checkbox" aria-label={`Select ${personalOwnerName(w, people)}'s personal account`} checked={selected.has(w.id)} disabled={!canDelete}
                    title={canDelete ? undefined : "Has crews, agents or runs; open it to decide"}
                    onChange={(e) => onSelect([w.id], e.target.checked)} className="size-3.5 accent-[var(--primary)] disabled:opacity-30" />
                </td>
                <td className={td}>{owner ? <PersonLabel person={owner} sub={w.name} /> : <span>{w.name}</span>}</td>
                <td className={cn(td, "text-right font-mono tabular-nums")}>{w._count_crews}</td>
                <td className={cn(td, "text-right font-mono tabular-nums")}>{w._count_agents}</td>
                <td className={cn(td, "text-right font-mono tabular-nums")}>{w.runs_7d ?? 0}</td>
                <td className={td}>
                  <span className="flex flex-wrap gap-1">
                    {elsewhere.length === 0 ? <span className="text-muted-foreground">Nowhere else</span>
                      : elsewhere.map((m) => <MembershipChip key={m.workspace_id} m={m} personal={false} />)}
                  </span>
                </td>
              </tr>
            )
          })}
          {workspaces.length === 0 && <tr><td colSpan={6} className="px-3 py-6 text-center text-muted-foreground">No personal account matches.</td></tr>}
        </tbody>
      </table>
    </div>
  )
}

export function WorkspacesTable({ workspaces, people, onOpen }: { workspaces: Workspace[]; people: Person[]; onOpen: (id: string) => void }) {
  return (
    <div>
      <div className="overflow-x-auto">
        <table className={settingsTable}>
          <thead>
            <tr><th className={th}>Workspace</th><th className={th}>Owner</th><th className={cn(th, "text-right")}>People</th><th className={cn(th, "text-right")}>Crews</th><th className={th}>Runs · 7 d</th><th className={th}>Last activity</th></tr>
          </thead>
          <tbody>
            {workspaces.map((w) => {
              const owner = ownersOf(people, w.id)[0]
              return (
                <tr key={w.id} tabIndex={0} onClick={() => onOpen(w.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(w.id) }}
                  className={settingsTableRowLink}>
                  <td className={td}>
                    <span className="flex items-center gap-2.5"><WorkspaceTile id={w.id} name={w.name} logoUrl={w.logo_url} />
                      <span><span className="block">{w.name}</span><span className="block font-mono text-micro text-muted-foreground">{w.slug}</span></span>
                    </span>
                  </td>
                  <td className={td}>{owner ? <PersonLabel person={owner} /> : <StatusPill tone="warn" label="No owner" />}</td>
                  <td className={cn(td, "text-right font-mono tabular-nums")}>{w._count_members}</td>
                  <td className={cn(td, "text-right font-mono tabular-nums")}>{w._count_crews}</td>
                  <td className={td}><span className="inline-flex items-center gap-2"><WeekLine values={w.runs_by_day?.length ? w.runs_by_day : [0]} muted={!w.runs_7d} /><span className="font-mono text-micro text-muted-foreground">{w.runs_7d ?? 0}</span></span></td>
                  <td className={cn(td, "whitespace-nowrap text-muted-foreground")}>{ago(w.last_activity_at)}</td>
                </tr>
              )
            })}
            {workspaces.length === 0 && <tr><td colSpan={6} className="px-3 py-6 text-center text-muted-foreground">No workspace matches.</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/** People × workspaces: every role at a glance, each cell changeable. */
export function AccessMatrix({ people, allPeople, workspaces, actions, busy }: {
  people: Person[]
  allPeople: Person[]
  workspaces: Workspace[]
  actions: Actions
  busy: string | null
}) {
  const [editing, setEditing] = React.useState<string | null>(null)
  return (
    <section aria-label="Access" className="overflow-hidden rounded-card border border-border bg-card">
      <div className="overflow-x-auto">
        <table className={settingsTable}>
          <thead>
            <tr>
              <th className={th}>Person</th>
              {workspaces.map((w) => (
                <th key={w.id} className={cn(th, "text-center")}>
                  <span className="inline-flex items-center gap-1.5"><WorkspaceTile id={w.id} name={w.name} size="xs" />{w.name}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {people.map((p) => (
              <tr key={p.id}>
                <td className={td}><PersonLabel person={p} /></td>
                {workspaces.map((w) => {
                  const role = roleIn(p, w.id)
                  const key = `${p.id}:${w.id}`
                  const locked = role === "OWNER" && isLastOwner(allPeople, w.id, p.id)
                  return (
                    <td key={w.id} className={cn(td, "text-center")}>
                      {editing === key ? (
                        <RoleSelect label={`${displayName(p)} in ${w.name}`} value={role} allowNone className="w-32" disabled={busy === `m:${key}`}
                          onChange={async (r) => {
                            setEditing(null)
                            if (r === role) return
                            if (r) await actions.setRole(p.id, w.id, r, `${displayName(p)}: ${roleLabel(r)} in ${w.name}`)
                            else await actions.removeAccess(p.id, w.id, `${displayName(p)} removed from ${w.name}`)
                          }} />
                      ) : (
                        <button type="button" disabled={locked} onClick={() => setEditing(key)}
                          aria-label={`${displayName(p)} in ${w.name}: ${role ? roleLabel(role) : "no access"}`}
                          title={locked ? "The only owner; hand the workspace over first" : `Change ${displayName(p)} in ${w.name}`}
                          className={cn("min-w-20 rounded-md border border-dashed border-transparent px-2 py-1 text-label hover:border-control-border disabled:cursor-default disabled:hover:border-transparent",
                            role ? "text-foreground" : "text-muted-foreground-soft")}>
                          {role ? roleLabel(role) : "—"}
                        </button>
                      )}
                    </td>
                  )
                })}
              </tr>
            ))}
            {people.length === 0 && <tr><td colSpan={workspaces.length + 1} className="px-3 py-6 text-center text-muted-foreground">No one matches.</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
  )
}

export { STATUS_LABEL }
