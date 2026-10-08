"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { WeekLine, WorkspaceTile, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  displayName, isLastOwner, ownersOf, personStatus, roleIn, STATUS_LABEL, type Person, type Workspace,
} from "./people-model"
import { PersonLabel, RoleSelect, StatusChip, roleLabel } from "./people-parts"
import type { usePeople } from "./use-people"
import { StatusPill } from "@/components/ui/status-pill"

type Actions = ReturnType<typeof usePeople>["actions"]

const th = "whitespace-nowrap px-3 py-2 text-left text-[11px] font-medium text-muted-foreground"
const td = "px-3 py-2.5 align-middle"

/** People who need a hand, one card each, above the table. */
export function AttentionStrip({ people, onOpen, actions, busy }: {
  people: Person[]
  onOpen: (id: string) => void
  actions: Actions
  busy: string | null
}) {
  const needing = people.filter((p) => personStatus(p) !== "active").slice(0, 6)
  if (needing.length === 0) return null
  return (
    <ul className="grid gap-2 sm:grid-cols-2" aria-label="Needs attention" data-slot="people-attention">
      {needing.map((p) => {
        const s = personStatus(p)
        return (
          <li key={p.id} className="flex items-center gap-3 rounded-card border border-border bg-card px-3 py-2.5">
            <button type="button" onClick={() => onOpen(p.id)} className="min-w-0 flex-1 text-left">
              <PersonLabel person={p} sub={
                s === "locked" ? `Locked after ${p.failed_login_count ?? 0} failed sign-ins`
                  : s === "setup" ? "Has not chosen a password yet"
                    : `Suspended${p.suspended_reason ? ` · ${p.suspended_reason}` : ""}`
              } />
            </button>
            {s === "locked" && <Button size="xs" variant="outline" disabled={busy === `u:${p.id}`} onClick={() => void actions.unlock(p.id, `${displayName(p)} can sign in again`)}>Unlock</Button>}
            {s !== "locked" && <Button size="xs" variant="ghost" onClick={() => onOpen(p.id)}>Open</Button>}
          </li>
        )
      })}
    </ul>
  )
}

export function PeopleTable({ people, onOpen }: { people: Person[]; onOpen: (id: string) => void }) {
  return (
    <section aria-label="People" className="overflow-hidden rounded-card border border-border bg-card">
      <div className="overflow-x-auto">
        <table className="w-full text-[12.5px]">
          <thead className="border-b border-border">
            <tr><th className={th}>Person</th><th className={th}>Workspaces</th><th className={th}>Last active</th><th className={th}>Status</th><th className={cn(th, "text-right")}>Sessions</th></tr>
          </thead>
          <tbody>
            {people.map((p) => (
              <tr key={p.id} tabIndex={0} onClick={() => onOpen(p.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(p.id) }}
                className="cursor-pointer border-b border-border last:border-b-0 hover:bg-accent focus-visible:bg-accent focus-visible:outline-none" data-person={p.id}>
                <td className={td}><PersonLabel person={p} /></td>
                <td className={td}>
                  <span className="flex flex-wrap gap-1">
                    {p.memberships.length === 0 && <span className="text-muted-foreground">No workspace</span>}
                    {p.memberships.map((m) => (
                      <span key={m.workspace_id} className="inline-flex items-center gap-1 rounded-md border border-border py-0.5 pl-0.5 pr-1.5 text-[11px]">
                        <WorkspaceTile id={m.workspace_id} name={m.name} size="xs" />{m.name}<span className="text-muted-foreground">{roleLabel(m.role)}</span>
                      </span>
                    ))}
                  </span>
                </td>
                <td className={cn(td, "whitespace-nowrap text-muted-foreground")}>{ago(p.last_active_at)}</td>
                <td className={td}><StatusChip person={p} /></td>
                <td className={cn(td, "text-right font-mono tabular-nums")}>{p.active_sessions ?? 0}</td>
              </tr>
            ))}
            {people.length === 0 && <tr><td colSpan={5} className="px-3 py-6 text-center text-muted-foreground">No one matches.</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
  )
}

export function WorkspacesTable({ workspaces, people, onOpen }: { workspaces: Workspace[]; people: Person[]; onOpen: (id: string) => void }) {
  return (
    <section aria-label="Workspaces" className="overflow-hidden rounded-card border border-border bg-card">
      <div className="overflow-x-auto">
        <table className="w-full text-[12.5px]">
          <thead className="border-b border-border">
            <tr><th className={th}>Workspace</th><th className={th}>Owner</th><th className={cn(th, "text-right")}>People</th><th className={cn(th, "text-right")}>Crews</th><th className={th}>Runs · 7 d</th><th className={th}>Last activity</th></tr>
          </thead>
          <tbody>
            {workspaces.map((w) => {
              const owner = ownersOf(people, w.id)[0]
              return (
                <tr key={w.id} tabIndex={0} onClick={() => onOpen(w.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(w.id) }}
                  className="cursor-pointer border-b border-border last:border-b-0 hover:bg-accent focus-visible:bg-accent focus-visible:outline-none">
                  <td className={td}>
                    <span className="flex items-center gap-2.5"><WorkspaceTile id={w.id} name={w.name} logoUrl={w.logo_url} />
                      <span><span className="block">{w.name}</span><span className="block font-mono text-[11px] text-muted-foreground">{w.slug}</span></span>
                    </span>
                  </td>
                  <td className={td}>{owner ? <PersonLabel person={owner} /> : <StatusPill tone="warn" label="No owner" />}</td>
                  <td className={cn(td, "text-right font-mono tabular-nums")}>{w._count_members}</td>
                  <td className={cn(td, "text-right font-mono tabular-nums")}>{w._count_crews}</td>
                  <td className={td}><span className="inline-flex items-center gap-2"><WeekLine values={w.runs_by_day?.length ? w.runs_by_day : [0]} muted={!w.runs_7d} /><span className="font-mono text-[11px] text-muted-foreground">{w.runs_7d ?? 0}</span></span></td>
                  <td className={cn(td, "whitespace-nowrap text-muted-foreground")}>{ago(w.last_activity_at)}</td>
                </tr>
              )
            })}
            {workspaces.length === 0 && <tr><td colSpan={6} className="px-3 py-6 text-center text-muted-foreground">No workspace matches.</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
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
        <table className="w-full text-[12.5px]">
          <thead className="border-b border-border">
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
              <tr key={p.id} className="border-b border-border last:border-b-0">
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
                          className={cn("min-w-20 rounded-md border border-dashed border-transparent px-2 py-1 text-[11.5px] hover:border-control-border disabled:cursor-default disabled:hover:border-transparent",
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
