"use client"

import * as React from "react"
import Link from "next/link"
import { AlertTriangle, ChevronRight, Mail, Plus, Settings as SettingsIcon, Users, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { SettingsCard, SettingsDangerCard, SettingsRow } from "@/components/features/settings/shared"
import { WeekLine, WorkspaceTile, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  daysUntil, displayName, isLastOwner, membersOf, ownersOf, personStatus, roleIn, type Person, type Role, type Workspace,
} from "./people-model"
import { InlineConfirm, PersonLabel, RoleSelect, SetupLinkBox, StatusChip, roleLabel } from "./people-parts"
import type { SetupLink, usePeople } from "./use-people"

type Actions = ReturnType<typeof usePeople>["actions"]

/**
 * One workspace, the way an instance admin looks after it: who is in it and
 * as what, who still has to finish signing up, and — last — handing it over
 * or deleting it. Its own settings stay in Settings, one link away.
 */
export function WorkspaceProfile({ ws, people, busy, actions, onDeleted }: {
  ws: Workspace
  people: Person[]
  busy: string | null
  actions: Actions
  onDeleted: () => void
}) {
  const members = membersOf(people, ws.id).sort((a, b) => displayName(a).localeCompare(displayName(b)))
  const owners = ownersOf(people, ws.id)
  const pending = members.filter((p) => personStatus(p) === "setup")
  const candidates = people.filter((p) => !roleIn(p, ws.id))
  const [adding, setAdding] = React.useState(false)
  const [who, setWho] = React.useState("")
  const [addRole, setAddRole] = React.useState<Role>("MEMBER")
  const [link, setLink] = React.useState<SetupLink | null>(null)
  const [removing, setRemoving] = React.useState<string | null>(null)
  const [newOwner, setNewOwner] = React.useState("")
  const [confirm, setConfirm] = React.useState<"transfer" | "delete" | null>(null)
  const nonOwners = members.filter((p) => roleIn(p, ws.id) !== "OWNER")
  const runs = ws.runs_by_day ?? []

  React.useEffect(() => { setAdding(false); setLink(null); setRemoving(null); setConfirm(null); setNewOwner("") }, [ws.id])

  const add = async () => {
    const value = who.trim()
    if (!value) return
    const existing = people.find((p) => p.email.toLowerCase() === value.toLowerCase() || displayName(p).toLowerCase() === value.toLowerCase())
    if (existing) {
      const ok = await actions.setRole(existing.id, ws.id, addRole, `${displayName(existing)} joined ${ws.name} as ${roleLabel(addRole)}`)
      if (ok) { setAdding(false); setWho("") }
      return
    }
    if (!value.includes("@")) return
    const created = await actions.createPerson(value, "", [{ workspace_id: ws.id, role: addRole }])
    if (created) { setLink(created.link); setAdding(false); setWho("") }
  }

  return (
    <div className="space-y-4" data-slot="workspace-profile">
      <section aria-label={ws.name} className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-wrap items-start gap-4 p-4">
          <WorkspaceTile id={ws.id} name={ws.name} size="lg" logoUrl={ws.logo_url} />
          <div className="min-w-0 flex-1">
            <h2 className="flex flex-wrap items-baseline gap-2 text-lg font-semibold tracking-[-0.01em]">
              {ws.name}<span className="font-mono text-[12px] font-normal text-muted-foreground">{ws.slug}</span>
            </h2>
            <p className="mt-0.5 text-[12.5px] text-muted-foreground">
              Owner {owners.length ? owners.map(displayName).join(", ") : "—"} · created {new Date(ws.created_at).toLocaleDateString()} · active {ago(ws.last_activity_at)}
            </p>
            <div className="mt-3 flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted-foreground">
              <Chip n={members.length} label="people" />
              <Chip n={ws._count_agents} label="agents" />
              <Chip n={ws._count_crews} label="crews" />
              <span className="inline-flex items-center gap-1.5 rounded-full border border-border px-2 py-0.5">
                <WeekLine values={runs.length ? runs : [0]} muted={!ws.runs_7d} /><b className="font-medium text-foreground">{ws.runs_7d ?? 0}</b>runs · 7 d
              </span>
              <Chip n={`$${(ws.cost_30d_usd ?? 0).toFixed(2)}`} label="· 30 d" />
            </div>
          </div>
          {ws.current && (
            <Button asChild size="sm" variant="outline"><Link href="/settings?tab=general"><SettingsIcon />Settings</Link></Button>
          )}
        </div>
      </section>

      <SettingsCard icon={Users} tint="var(--primary)" title="Members" description="Everyone who can open this workspace"
        actions={<span className="font-mono text-[11px] text-muted-foreground">{members.length}</span>}>
        {members.map((p) => {
          const role = roleIn(p, ws.id)!
          const lastOwner = role === "OWNER" && isLastOwner(people, ws.id, p.id)
          const key = `m:${p.id}:${ws.id}`
          return (
            <div key={p.id} className="border-b border-border last:border-b-0">
              <div className="flex items-center gap-3 px-4 py-2.5">
                <span className="min-w-0 flex-1"><PersonLabel person={p} sub={personStatus(p) === "active" ? p.email : <StatusChip person={p} />} /></span>
                <span className="flex shrink-0 items-center justify-end gap-1.5 sm:w-64">
                  {lastOwner ? (
                    <span className="inline-flex h-8 w-36 items-center rounded-md border border-border px-2 text-control text-muted-foreground" title="Hand the workspace over below">Owner</span>
                  ) : (
                    <RoleSelect label={`Role of ${displayName(p)}`} value={role} disabled={busy === key}
                      onChange={(r) => r && void actions.setRole(p.id, ws.id, r, `${displayName(p)} is now ${roleLabel(r)} in ${ws.name}`)} />
                  )}
                  <Button size="icon-sm" variant="ghost" className="h-7 w-7" disabled={lastOwner || busy === key}
                    aria-label={`Remove ${displayName(p)}`} onClick={() => setRemoving(p.id)}>
                    <X className="size-3.5" />
                  </Button>
                </span>
              </div>
              {removing === p.id && (
                <InlineConfirm busy={busy === key} message={`${displayName(p)} loses access to ${ws.name}. Pages they own move to someone who stays.`}
                  confirmLabel="Remove" onCancel={() => setRemoving(null)}
                  onConfirm={async () => { await actions.removeAccess(p.id, ws.id, `${displayName(p)} removed from ${ws.name}`); setRemoving(null) }} />
              )}
            </div>
          )
        })}
        {members.length === 0 && <p className="border-b border-border px-4 py-3 text-[12px] text-muted-foreground">No one yet.</p>}
        {link && <SetupLinkBox url={link.url} expiresAt={link.expires_at} onDone={() => setLink(null)} />}
        {adding ? (
          <div className="grid gap-1.5 border-t border-border px-4 py-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Input list={`cands-${ws.id}`} value={who} onChange={(e) => setWho(e.target.value)} placeholder="Name or email" aria-label="Person to add"
                autoFocus className="h-8 min-w-0 flex-1" onKeyDown={(e) => { if (e.key === "Enter") void add() }} />
              <datalist id={`cands-${ws.id}`}>
                {candidates.map((p) => <option key={p.id} value={p.email}>{displayName(p)}</option>)}
              </datalist>
              <RoleSelect label="Role" value={addRole} onChange={(r) => r && setAddRole(r)} />
              <Button size="sm" variant="ghost" onClick={() => setAdding(false)}>Cancel</Button>
              <Button size="sm" onClick={add} disabled={!who.trim() || busy === "create-person"}>Add</Button>
            </div>
            <p className="text-[11px] text-muted-foreground">Someone new gets an account and a setup link you pass on. No email is sent.</p>
          </div>
        ) : (
          <button type="button" onClick={() => setAdding(true)}
            className="flex w-full items-center gap-1.5 border-t border-border px-4 py-2.5 text-left text-[12.5px] text-primary-hover hover:bg-accent">
            <Plus className="size-3.5" />Add a person to {ws.name}
          </button>
        )}
      </SettingsCard>

      {pending.length > 0 && (
        <SettingsCard icon={Mail} tint="var(--purple)" title="Waiting for setup" description="Accounts nobody has chosen a password for yet">
          {pending.map((p) => (
            <SettingsRow key={p.id} label={displayName(p)} description={`Link expires in ${daysUntil(p.setup_link_expires_at)} days`}>
              <Button size="sm" variant="outline" disabled={busy === `l:${p.id}`} onClick={async () => setLink(await actions.issueLink(p.id, p.email))}>New link</Button>
              <Button size="sm" variant="ghost" disabled={busy === `l:${p.id}`} onClick={() => void actions.revokeLink(p.id, "Setup link voided")}>Void</Button>
            </SettingsRow>
          ))}
        </SettingsCard>
      )}

      <SettingsDangerCard icon={AlertTriangle} title="Danger zone" description="Who owns this workspace, and whether it exists">
        <SettingsRow label="Transfer ownership" description="The new owner must be a member; the current owner stays on as Admin">
          <select aria-label="New owner" value={newOwner} onChange={(e) => setNewOwner(e.target.value)} disabled={nonOwners.length === 0}
            className="h-8 w-40 rounded-md border border-control-border bg-surface-subtle px-2 text-control disabled:opacity-50">
            <option value="">{nonOwners.length ? "Choose…" : "No other member"}</option>
            {nonOwners.map((p) => <option key={p.id} value={p.id}>{displayName(p)}</option>)}
          </select>
          <Button size="sm" variant="outline" className="border-destructive/40 text-destructive" disabled={!newOwner} onClick={() => setConfirm("transfer")}>Transfer</Button>
        </SettingsRow>
        {confirm === "transfer" && newOwner && (
          <InlineConfirm busy={busy === `t:${ws.id}`}
            message={`${displayName(people.find((p) => p.id === newOwner)!)} becomes the owner of ${ws.name}.`}
            confirmLabel="Transfer" onCancel={() => setConfirm(null)}
            onConfirm={async () => {
              const to = people.find((p) => p.id === newOwner)!
              await actions.transfer(ws.id, newOwner, `${displayName(to)} now owns ${ws.name}`)
              setConfirm(null); setNewOwner("")
            }} />
        )}
        <SettingsRow label="Delete workspace" description={`Removes ${ws._count_crews} crews, ${ws._count_agents} agents and their history`}>
          <Button size="sm" variant="outline" className="border-destructive/40 text-destructive" onClick={() => setConfirm("delete")}>Delete…</Button>
        </SettingsRow>
        {confirm === "delete" && (
          <InlineConfirm typeToConfirm={ws.slug} busy={busy === `d:${ws.id}`}
            message={`${ws.name} and everything in it is deleted.`} confirmLabel={`Delete ${ws.name}`}
            onCancel={() => setConfirm(null)}
            onConfirm={async () => { const ok = await actions.deleteWorkspace(ws.id, ws.slug, `${ws.name} deleted`); if (ok) onDeleted() }} />
        )}
      </SettingsDangerCard>

      <Link href={`/admin/people?view=access`} className="inline-flex items-center gap-1 text-[12px] text-primary-hover hover:underline">
        Everyone's access at a glance <ChevronRight className="size-3" />
      </Link>
    </div>
  )
}

function Chip({ n, label }: { n: React.ReactNode; label: string }) {
  return <span className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-0.5"><b className="font-medium text-foreground">{n}</b>{label}</span>
}
