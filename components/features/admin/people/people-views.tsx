"use client"

import * as React from "react"
import { House, Lock } from "lucide-react"

import { cn } from "@/lib/utils"
import { useShowMore } from "@/hooks/use-show-more"
import { useIsMobile } from "@/hooks/use-mobile"
import { WorkspaceTile, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  displayName, isEmptyWorkspace, isGuest, isLastOwner, isPersonal, ownersOf, personalOwnerName, roleIn, daysUntil,
  type HandGroups, type Person, type Workspace,
} from "./people-model"
import { PersonLabel, RoleSelect, StatusChip, roleLabel } from "./people-parts"
import type { usePeople } from "./use-people"
import { Button } from "@/components/ui/button"
import { ShowMore } from "@/components/ui/show-more"
import { StatusPill } from "@/components/ui/status-pill"
import { SettingsCard, settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"
import { Building2, KeyRound, TriangleAlert, UserRound } from "lucide-react"

type Actions = ReturnType<typeof usePeople>["actions"]

const th = settingsTh
const td = settingsTd

/**
 * Tables become stacked rows at phone width: the header goes, a row is a
 * block, a cell is a line. No horizontal scroll at 390 px.
 */
const stack = {
  table: "max-md:block",
  head: "max-md:hidden",
  body: "max-md:block",
  row: "max-md:flex max-md:flex-wrap max-md:items-center max-md:gap-x-3 max-md:gap-y-1 max-md:border-b max-md:border-border max-md:px-4 max-md:py-3 max-md:last:border-b-0",
  cell: "max-md:border-b-0 max-md:p-0",
  /** Only on a desk: the column a phone does not need. */
  deskOnly: "max-md:hidden",
}

/** A person's place in one workspace, as a chip: the workspace's tile and
 *  name, or — for their own personal account — a quiet "Personal account". */
export function MembershipChip({ m, personal, logoUrl }: { m: Person["memberships"][number]; personal: boolean; logoUrl?: string | null }) {
  if (personal) {
    return (
      <span className="inline-flex items-center gap-1 rounded-md border border-dashed border-border px-1.5 py-0.5 text-label text-muted-foreground" title={`${m.name} · personal account`}>
        <House className="h-3 w-3" aria-hidden />Personal account<span className="text-muted-foreground-soft">{roleLabel(m.role)}</span>
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-md border border-border py-0.5 pl-0.5 pr-1.5 text-label">
      <WorkspaceTile id={m.workspace_id} name={m.name} size="xs" logoUrl={logoUrl} />{m.name}<span className="text-muted-foreground">{roleLabel(m.role)}</span>
    </span>
  )
}

const EMPTY = "px-4 py-6 text-center text-muted-foreground"

export function PeopleTable({ people, workspaces, onOpen, selected, onSelect, selectable, resetKey }: {
  people: Person[]
  workspaces: Workspace[]
  onOpen: (id: string) => void
  selected?: Set<string>
  onSelect?: (ids: string[], on: boolean) => void
  /** Who may be ticked (not the caller, not an instance owner from env). */
  selectable?: (p: Person) => boolean
  /** A new filter or order starts from the first rows. */
  resetKey?: unknown
}) {
  const personal = new Set(workspaces.filter(isPersonal).map((w) => w.id))
  const logos = new Map(workspaces.map((w) => [w.id, w.logo_url]))
  const page = useShowMore(people, { resetKey })
  const canTick = Boolean(selected && onSelect)
  const tickable = page.shown.filter((p) => selectable?.(p) ?? true)
  const allOn = tickable.length > 0 && tickable.every((p) => selected?.has(p.id))
  return (
    <div>
      <table className={cn(settingsTable, stack.table)}>
        <thead className={stack.head}>
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
        <tbody className={stack.body}>
          {page.shown.map((p) => {
            const mine = [...p.memberships].sort((a, b) => Number(personal.has(a.workspace_id)) - Number(personal.has(b.workspace_id)))
            return (
              <tr key={p.id} tabIndex={0} onClick={() => onOpen(p.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(p.id) }}
                className={cn(settingsTableRowLink, stack.row)} data-person={p.id}>
                {canTick && (
                  <td className={cn(td, stack.cell, "max-md:flex max-md:size-11 max-md:items-center max-md:justify-center")} onClick={(e) => e.stopPropagation()}>
                    <input type="checkbox" aria-label={`Select ${displayName(p)}`} checked={selected!.has(p.id)} disabled={!(selectable?.(p) ?? true)}
                      onChange={(e) => onSelect!([p.id], e.target.checked)} className="size-3.5 accent-[var(--primary)] disabled:opacity-30" />
                  </td>
                )}
                <td className={cn(td, stack.cell, "max-md:min-w-0 max-md:flex-1")}><PersonLabel person={p} /></td>
                <td className={cn(td, stack.cell, "max-md:order-last max-md:w-full")}>
                  <span className="flex flex-wrap items-center gap-1">
                    {mine.length === 0 && <span className="text-muted-foreground">No workspace</span>}
                    {mine.slice(0, 2).map((m) => (
                      <MembershipChip key={m.workspace_id} m={m} personal={personal.has(m.workspace_id)} logoUrl={logos.get(m.workspace_id)} />
                    ))}
                    {mine.length > 2 && <span className="text-label text-muted-foreground" title={mine.slice(2).map((m) => m.name).join(", ")}>+{mine.length - 2}</span>}
                  </span>
                </td>
                <td className={cn(td, stack.cell, stack.deskOnly, "whitespace-nowrap text-muted-foreground")}>{ago(p.last_active_at)}</td>
                <td className={cn(td, stack.cell)}>
                  <span className="inline-flex flex-wrap items-center gap-1">
                    <StatusChip person={p} />
                    {p.instance_admin && <StatusPill tone="purple" label="Instance admin" />}
                  </span>
                </td>
                <td className={cn(td, stack.cell, stack.deskOnly, "text-right tabular-nums")}>{p.active_sessions || "—"}</td>
              </tr>
            )
          })}
          {people.length === 0 && <tr className="max-md:block"><td colSpan={canTick ? 6 : 5} className={EMPTY}>No one matches.</td></tr>}
        </tbody>
      </table>
      <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} paged={page.paged} onMore={page.showMore} noun="people" />
    </div>
  )
}

/**
 * The personal accounts: one row per person who has one, what is in it,
 * and where else they work. Only empty ones can be ticked for removal.
 */
export function PersonalWorkspacesTable({ workspaces, people, onOpen, selected, onSelect, logos, resetKey }: {
  workspaces: Workspace[]
  people: Person[]
  onOpen: (id: string) => void
  selected: Set<string>
  onSelect: (ids: string[], on: boolean) => void
  /** Every workspace's logo, for the chips of where else they work. */
  logos?: Map<string, string | null | undefined>
  resetKey?: unknown
}) {
  const page = useShowMore(workspaces, { resetKey })
  const empty = page.shown.filter(isEmptyWorkspace)
  const allOn = empty.length > 0 && empty.every((w) => selected.has(w.id))
  return (
    <div>
      <table className={cn(settingsTable, stack.table)} aria-label="Personal accounts">
        <thead className={stack.head}>
          <tr>
            <th className={cn(th, "w-9")}>
              <input type="checkbox" aria-label="Select every empty one" checked={allOn} disabled={empty.length === 0}
                onChange={(e) => onSelect(empty.map((w) => w.id), e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
            </th>
            <th className={th}>Person</th><th className={cn(th, "text-right")}>Crews</th><th className={cn(th, "text-right")}>Agents</th><th className={cn(th, "text-right")}>Runs · 7 d</th><th className={th}>Also works in</th>
          </tr>
        </thead>
        <tbody className={stack.body}>
          {page.shown.map((w) => {
            const owner = people.find((p) => p.memberships.some((m) => m.workspace_id === w.id && m.role === "OWNER"))
            const elsewhere = owner?.memberships.filter((m) => m.workspace_id !== w.id && !workspaces.some((x) => x.id === m.workspace_id)) ?? []
            const canDelete = isEmptyWorkspace(w)
            return (
              <tr key={w.id} tabIndex={0} onClick={() => onOpen(w.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(w.id) }}
                className={cn(settingsTableRowLink, stack.row)} data-workspace={w.id}>
                <td className={cn(td, stack.cell, "max-md:flex max-md:size-11 max-md:items-center max-md:justify-center")} onClick={(e) => e.stopPropagation()}>
                  {canDelete ? (
                    <input type="checkbox" aria-label={`Select ${personalOwnerName(w, people)}'s personal account`} checked={selected.has(w.id)}
                      onChange={(e) => onSelect([w.id], e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
                  ) : (
                    <span title="Has crews, agents or runs; open it to decide" className="text-muted-foreground-soft">
                      <input type="checkbox" disabled aria-label={`Select ${personalOwnerName(w, people)}'s personal account`} className="size-3.5 opacity-30" />
                    </span>
                  )}
                </td>
                <td className={cn(td, stack.cell, "max-md:min-w-0 max-md:flex-1")}>{owner ? <PersonLabel person={owner} sub={w.name} /> : <span>{w.name}</span>}</td>
                <td className={cn(td, stack.cell, stack.deskOnly, "text-right tabular-nums")}>{w._count_crews}</td>
                <td className={cn(td, stack.cell, stack.deskOnly, "text-right tabular-nums")}>{w._count_agents}</td>
                <td className={cn(td, stack.cell, stack.deskOnly, "text-right tabular-nums")}>{w.runs_7d ?? 0}</td>
                <td className={cn(td, stack.cell, "max-md:order-last max-md:w-full")}>
                  <span className="flex flex-wrap gap-1">
                    {elsewhere.length === 0 ? <span className="text-muted-foreground">Nowhere else</span>
                      : elsewhere.map((m) => <MembershipChip key={m.workspace_id} m={m} personal={false} logoUrl={logos?.get(m.workspace_id)} />)}
                  </span>
                </td>
              </tr>
            )
          })}
          {workspaces.length === 0 && <tr className="max-md:block"><td colSpan={6} className={EMPTY}>No personal account matches.</td></tr>}
        </tbody>
      </table>
      <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} paged={page.paged} onMore={page.showMore} noun="personal accounts" />
    </div>
  )
}

export function WorkspacesTable({ workspaces, people, onOpen, resetKey }: { workspaces: Workspace[]; people: Person[]; onOpen: (id: string) => void; resetKey?: unknown }) {
  const page = useShowMore(workspaces, { resetKey })
  return (
    <div>
      <table className={cn(settingsTable, stack.table)}>
        <thead className={stack.head}>
          <tr><th className={th}>Workspace</th><th className={th}>Owner</th><th className={cn(th, "text-right")}>People</th><th className={cn(th, "text-right")}>Crews</th><th className={cn(th, "text-right")}>Runs · 7 d</th><th className={th}>Last activity</th></tr>
        </thead>
        <tbody className={stack.body}>
          {page.shown.map((w) => {
            const owner = ownersOf(people, w.id)[0]
            return (
              <tr key={w.id} tabIndex={0} onClick={() => onOpen(w.id)} onKeyDown={(e) => { if (e.key === "Enter") onOpen(w.id) }}
                className={cn(settingsTableRowLink, stack.row)}>
                <td className={cn(td, stack.cell, "max-md:min-w-0 max-md:flex-1")}>
                  <span className="flex items-center gap-2.5"><WorkspaceTile id={w.id} name={w.name} logoUrl={w.logo_url} />
                    <span><span className="block">{w.name}</span><span className="block font-mono text-micro text-muted-foreground">{w.slug}</span></span>
                  </span>
                </td>
                <td className={cn(td, stack.cell)}>{owner ? displayName(owner) : <StatusPill tone="warn" label="No owner" />}</td>
                <td className={cn(td, stack.cell, stack.deskOnly, "text-right tabular-nums")}>{w._count_members}</td>
                <td className={cn(td, stack.cell, stack.deskOnly, "text-right tabular-nums")}>{w._count_crews}</td>
                <td className={cn(td, stack.cell, stack.deskOnly, "text-right tabular-nums")}>{w.runs_7d ?? 0}</td>
                <td className={cn(td, stack.cell, "whitespace-nowrap text-muted-foreground max-md:order-last max-md:w-full")}>
                  <span className="md:hidden">{w._count_members} people · {w._count_crews} crews · </span>{ago(w.last_activity_at)}
                </td>
              </tr>
            )
          })}
          {workspaces.length === 0 && <tr className="max-md:block"><td colSpan={6} className={EMPTY}>No workspace matches.</td></tr>}
        </tbody>
      </table>
      <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} paged={page.paged} onMore={page.showMore} noun="workspaces" />
    </div>
  )
}

/**
 * Who is where: people as paged rows, a few workspaces as columns (chosen in
 * "Choose workspaces"), every role at a glance and each cell changeable. The
 * rows are ordered by the caller — people in the chosen workspaces first.
 */
export function AccessMatrix({ people, allPeople, columns, actions, busy, resetKey, onOpenPerson }: {
  people: Person[]
  allPeople: Person[]
  columns: Workspace[]
  actions: Actions
  busy: string | null
  resetKey?: unknown
  onOpenPerson?: (id: string) => void
}) {
  const [editing, setEditing] = React.useState<string | null>(null)
  const page = useShowMore(people, { resetKey })
  return (
    <div>
      <table className={cn(settingsTable, "table-fixed")}>
        <thead>
          <tr>
            <th className={cn(th, "w-[38%] max-md:w-[40%]")}>Person</th>
            {columns.map((w) => (
              <th key={w.id} className={cn(th, "text-center max-md:px-1")}>
                <span className="inline-flex max-w-full items-center gap-1.5"><WorkspaceTile id={w.id} name={w.name} size="xs" logoUrl={w.logo_url} /><span className="truncate">{w.name}</span></span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {page.shown.map((p) => (
            <tr key={p.id}>
              <td className={cn(td, onOpenPerson && "cursor-pointer")} onClick={() => onOpenPerson?.(p.id)}><PersonLabel person={p} /></td>
              {columns.map((w) => {
                const role = roleIn(p, w.id)
                const key = `${p.id}:${w.id}`
                const locked = role === "OWNER" && isLastOwner(allPeople, w.id, p.id)
                return (
                  <td key={w.id} className={cn(td, "text-center max-md:px-1")}>
                    {editing === key ? (
                      <RoleSelect label={`${displayName(p)} in ${w.name}`} value={role} allowNone guest={isGuest(p)} className="w-28 max-md:w-full" disabled={busy === `m:${key}`}
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
                        className={cn("min-h-8 min-w-16 rounded-md border border-dashed border-transparent px-2 py-1 text-label hover:border-control-border disabled:cursor-default disabled:hover:border-transparent max-md:min-w-0 coarse:min-h-[44px]",
                          role ? "text-foreground" : "text-muted-foreground-soft")}>
                        {locked && <Lock className="mr-1 inline size-3" aria-hidden />}
                        {role ? roleLabel(role) : "—"}
                      </button>
                    )}
                  </td>
                )
              })}
            </tr>
          ))}
          {people.length === 0 && <tr><td colSpan={columns.length + 1} className={EMPTY}>No one matches.</td></tr>}
        </tbody>
      </table>
      <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} paged={page.paged} onMore={page.showMore} noun="people" />
    </div>
  )
}

/** The most columns the grid draws: two on a phone, six on a desk. */
export function useMatrixMax(): number {
  return useIsMobile() ? 2 : 6
}

// ── Needs a hand ──────────────────────────────────────────────────────────

const CAP = 6

/** One finding: who, a note, and what to do about it. */
function FindRow({ person, note, children }: { person: Person; note?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div data-slot="hand-row" className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border px-4 py-2.5 last:border-b-0">
      <div className="min-w-0 flex-[1_1_14rem]"><PersonLabel person={person} /></div>
      {note && <span className="text-label text-muted-foreground max-md:hidden">{note}</span>}
      <div className="ml-auto flex flex-wrap items-center gap-1.5">{children}</div>
    </div>
  )
}

function Capped({ shown, total, onShowAll, children }: { shown: number; total: number; onShowAll: () => void; children: React.ReactNode }) {
  return (
    <>
      {children}
      {total > shown && (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-4 py-2 text-label text-muted-foreground">
          <span className="tabular-nums">{shown} of {total} shown</span>
          <Button size="sm" variant="outline" onClick={onShowAll}>Show all</Button>
        </div>
      )}
    </>
  )
}

/**
 * Needs a hand: the people who cannot get in, those who get in to nothing,
 * and personal accounts nobody uses. Each card shows six rows at most, with a
 * link to the full list.
 */
export function HandView({ groups, actions, busy, onOpen, onFilter, onAccounts, onAddToWorkspace, onRemoveEmpty, onNewLink }: {
  groups: HandGroups
  actions: Actions
  busy: string | null
  onOpen: (id: string) => void
  onFilter: (facet: "locked" | "setup" | "noaccess") => void
  onAccounts: () => void
  onAddToWorkspace: (person: Person) => void
  onRemoveEmpty: () => void
  onNewLink: (person: Person) => void
}) {
  const { locked, setup, nowhere, emptyAccounts } = groups
  const nothing = !locked.length && !setup.length && !nowhere.length && !emptyAccounts.length
  return (
    <>
      {nothing && <p className="rounded-card border border-border bg-card px-4 py-6 text-center text-control text-muted-foreground">Nobody needs a hand.</p>}
      {locked.length > 0 && (
        <SettingsCard icon={TriangleAlert} tint="var(--destructive)" title={`Locked out · ${locked.length}`} description="Too many failed sign-ins. Unlock, or let the lock run out by itself.">
          <Capped shown={Math.min(CAP, locked.length)} total={locked.length} onShowAll={() => onFilter("locked")}>
            {locked.slice(0, CAP).map((p) => (
              <FindRow key={p.id} person={p} note={`${p.failed_login_count ?? 0} failed sign-ins`}>
                <Button size="sm" variant="outline" disabled={busy === `u:${p.id}`} onClick={() => void actions.unlock(p.id, `${displayName(p)} can sign in again`)}>Unlock</Button>
                <Button size="sm" variant="ghost" onClick={() => onOpen(p.id)}>Open</Button>
              </FindRow>
            ))}
          </Capped>
        </SettingsCard>
      )}
      {setup.length > 0 && (
        <SettingsCard icon={KeyRound} tint="var(--primary)" title={`Have not chosen a password · ${setup.length}`} description="They got a setup link and never used it. Links are valid 7 days and shown once.">
          <Capped shown={Math.min(CAP, setup.length)} total={setup.length} onShowAll={() => onFilter("setup")}>
            {setup.slice(0, CAP).map((p) => {
              const days = daysUntil(p.setup_link_expires_at)
              return (
                <FindRow key={p.id} person={p} note={`link expires ${days <= 1 ? "within a day" : `in ${days} days`}`}>
                  <Button size="sm" variant="outline" disabled={busy === `l:${p.id}`} onClick={() => onNewLink(p)}>New setup link</Button>
                  <Button size="sm" variant="ghost" disabled={busy === `l:${p.id}`} onClick={() => void actions.revokeLink(p.id, "Setup link voided")}>Void</Button>
                </FindRow>
              )
            })}
          </Capped>
        </SettingsCard>
      )}
      {nowhere.length > 0 && (
        <SettingsCard icon={Building2} tint="var(--warn)" title={`In no workspace · ${nowhere.length}`} description="They can sign in and see nothing.">
          <Capped shown={Math.min(CAP, nowhere.length)} total={nowhere.length} onShowAll={() => onFilter("noaccess")}>
            {nowhere.slice(0, CAP).map((p) => (
              <FindRow key={p.id} person={p} note={`last active ${ago(p.last_active_at)}`}>
                <Button size="sm" variant="outline" onClick={() => onAddToWorkspace(p)}>Add to a workspace</Button>
                <Button size="sm" variant="ghost" onClick={() => onOpen(p.id)}>Open</Button>
              </FindRow>
            ))}
          </Capped>
        </SettingsCard>
      )}
      {emptyAccounts.length > 0 && (
        <SettingsCard icon={UserRound} tint="var(--muted-foreground)" title={`Empty personal accounts · ${emptyAccounts.length}`} description="Made at sign-up and never used: no crews, no agents, no runs. The person keeps their sign-in.">
          <div data-slot="hand-row" className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
            <div className="min-w-0 flex-[1_1_14rem]">
              <b className="block text-control font-medium">{emptyAccounts.length} {emptyAccounts.length === 1 ? "account" : "accounts"} can be removed safely</b>
              <span className="block text-label text-muted-foreground">Review them one by one under Personal accounts.</span>
            </div>
            <div className="ml-auto flex flex-wrap items-center gap-1.5">
              <Button size="sm" variant="outline" onClick={onAccounts}>Review</Button>
              <Button size="sm" variant="outline" className="border-destructive/40 text-destructive" onClick={onRemoveEmpty}>Remove {emptyAccounts.length}</Button>
            </div>
          </div>
        </SettingsCard>
      )}
    </>
  )
}


