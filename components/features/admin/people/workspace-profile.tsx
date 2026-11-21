"use client"

import * as React from "react"
import Link from "next/link"
import { AlertTriangle, Building2, Check, ChevronRight, CircleDollarSign, House, Layers, Mail, Pencil, Plus, Settings as SettingsIcon, Users, Activity, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ChoiceTile } from "@/components/ui/choice-tile"
import { Combobox } from "@/components/ui/combobox"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Input } from "@/components/ui/input"
import { ShowMore } from "@/components/ui/show-more"
import { StatusPill } from "@/components/ui/status-pill"
import { useShowMore } from "@/hooks/use-show-more"
import { SettingsCard, SettingsDangerCard, SettingsKpiStrip, SettingsRow, controlHeight } from "@/components/features/settings/shared"
import { WorkspaceTile, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  daysUntil, displayName, isGuest, isLastOwner, isPersonal, membersOf, personStatus, roleIn, type Person, type Role, type Workspace,
} from "./people-model"
import { PersonLabel, RoleSelect, StatusChip, roleLabel } from "./people-parts"
import { personOptions } from "./people-pickers"
import { AdminAuditCard } from "./admin-audit-card"
import type { SetupLink, usePeople } from "./use-people"
import { cn } from "@/lib/utils"

type Actions = ReturnType<typeof usePeople>["actions"]

/**
 * One workspace, the way an instance admin looks after it: its figures, who is
 * in it and as what, who still has to finish signing up, whether it is a
 * workspace or a personal account, and — last — handing it over or deleting
 * it. Its own settings stay in Settings, one link away.
 */
export function WorkspaceProfile({ ws, people, busy, actions, onDeleted, onOpenPerson, onShowLink, showAudit = true }: {
  ws: Workspace
  people: Person[]
  busy: string | null
  actions: Actions
  onDeleted: () => void
  onOpenPerson?: (id: string) => void
  /** Shows a freshly issued setup link, once. */
  onShowLink: (link: SetupLink) => void
  /** The audit log answers an instance admin only. */
  showAudit?: boolean
}) {
  const members = membersOf(people, ws.id).sort((a, b) => displayName(a).localeCompare(displayName(b)))
  const pending = members.filter((p) => personStatus(p) === "setup")
  const candidates = people.filter((p) => !roleIn(p, ws.id))
  const [adding, setAdding] = React.useState(false)
  const [who, setWho] = React.useState("")
  const [addRole, setAddRole] = React.useState<Role>("MEMBER")
  const [removing, setRemoving] = React.useState<string | null>(null)
  const [newOwner, setNewOwner] = React.useState("")
  const [confirm, setConfirm] = React.useState<"transfer" | "delete" | null>(null)
  const nonOwners = members.filter((p) => roleIn(p, ws.id) !== "OWNER")
  const newOwnerPerson = newOwner ? people.find((p) => p.id === newOwner) : undefined
  const personal = isPersonal(ws)
  const [renaming, setRenaming] = React.useState(false)
  const [name, setName] = React.useState(ws.name)
  const memberPage = useShowMore(members, { resetKey: ws.id })
  const waitingPage = useShowMore(pending, { resetKey: ws.id })
  const rename = async () => {
    const next = name.trim()
    if (next.length < 2 || next === ws.name) { setRenaming(false); return }
    if (await actions.updateWorkspace(ws.id, { name: next }, `Renamed to ${next}`)) setRenaming(false)
  }

  React.useEffect(() => { setAdding(false); setRemoving(null); setConfirm(null); setNewOwner(""); setRenaming(false); setName(ws.name) }, [ws.id, ws.name])

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
    if (created) { onShowLink(created.link); setAdding(false); setWho("") }
  }

  const newLink = async (p: Person) => {
    const link = await actions.issueLink(p.id, p.email)
    if (link) onShowLink(link)
  }

  const invited = ws.pending_invitations ?? 0

  return (
    <div className="space-y-4" data-slot="workspace-profile">
      <section aria-label={ws.name} className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-wrap items-start gap-4 p-4">
          <WorkspaceTile id={ws.id} name={ws.name} size="lg" logoUrl={ws.logo_url} />
          <div className="min-w-0 flex-1">
            {renaming ? (
              <form className="flex flex-wrap items-center gap-1.5" onSubmit={(e) => { e.preventDefault(); void rename() }}>
                <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus aria-label="Workspace name" maxLength={100}
                  className={cn(controlHeight, "w-64 max-w-full text-base font-semibold")} onKeyDown={(e) => { if (e.key === "Escape") { setRenaming(false); setName(ws.name) } }} />
                <Button type="submit" size="sm" disabled={name.trim().length < 2 || busy === `w:${ws.id}`}><Check />Save</Button>
                <Button type="button" size="sm" variant="ghost" onClick={() => { setRenaming(false); setName(ws.name) }}>Cancel</Button>
              </form>
            ) : (
              <h2 className="text-lg font-semibold tracking-[-0.01em]">{ws.name}</h2>
            )}
            <p className="mt-0.5 flex flex-wrap gap-x-3 text-label text-muted-foreground">
              <span className="font-mono text-micro" title="The address stays when you rename">{ws.slug}</span>
              <span>Created {new Date(ws.created_at).toLocaleDateString()}</span>
              <span>Active {ago(ws.last_activity_at)}</span>
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            {!renaming && <Button size="sm" variant="outline" onClick={() => setRenaming(true)} aria-label={`Rename ${ws.name}`}><Pencil />Rename</Button>}
            {ws.current && <Button asChild size="sm" variant="outline"><Link href="/settings?tab=general"><SettingsIcon />Settings</Link></Button>}
          </div>
        </div>
      </section>

      <SettingsKpiStrip items={[
        { icon: Users, label: "People", value: <>{members.length}{invited > 0 && <span className="ml-1.5 text-label font-normal text-muted-foreground">{invited} invited</span>}</> },
        { icon: Layers, label: "Crews", value: <>{ws._count_crews}<span className="ml-1.5 text-label font-normal text-muted-foreground">{ws._count_agents} agents</span></> },
        { icon: Activity, label: "Runs · 7 d", value: ws.runs_7d ?? 0 },
        { icon: CircleDollarSign, label: "Spend · 30 d", value: `$${(ws.cost_30d_usd ?? 0).toFixed(0)}` },
      ]} />

      <SettingsCard icon={Users} tint="var(--primary)" title="Members" description="Everyone who can open this workspace"
        actions={
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}><Plus />Add a person</Button>
        }
        footer={<span>Someone new gets an account and a setup link you pass on. No email is sent.</span>}>
        {adding && (
          <div className="grid gap-1.5 border-b border-border px-4 py-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Input list={`cands-${ws.id}`} value={who} onChange={(e) => setWho(e.target.value)} placeholder="Name or email" aria-label="Person to add"
                autoFocus className={cn(controlHeight, "min-w-0 flex-1")} onKeyDown={(e) => { if (e.key === "Enter") void add() }} />
              <datalist id={`cands-${ws.id}`}>
                {candidates.map((p) => <option key={p.id} value={p.email}>{displayName(p)}</option>)}
              </datalist>
              <RoleSelect label="Role" value={addRole} onChange={(r) => r && setAddRole(r)} />
              <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setWho("") }}>Cancel</Button>
              <Button size="sm" onClick={add} disabled={!who.trim() || busy === "create-person"}>Add</Button>
            </div>
          </div>
        )}
        {memberPage.shown.map((p) => {
          const role = roleIn(p, ws.id)!
          const lastOwner = role === "OWNER" && isLastOwner(people, ws.id, p.id)
          const key = `m:${p.id}:${ws.id}`
          return (
            <div key={p.id} className="border-b border-border last:border-b-0">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5">
                <button type="button" className="min-w-0 flex-[1_1_12rem] text-left" onClick={() => onOpenPerson?.(p.id)} disabled={!onOpenPerson}>
                  <PersonLabel person={p} sub={personStatus(p) === "active" ? p.email : <StatusChip person={p} />} />
                </button>
                <span className="ml-auto flex shrink-0 items-center justify-end gap-1.5">
                  {lastOwner ? (
                    <span title="Hand the workspace over below"><StatusPill tone="blue" label="Owner" /></span>
                  ) : (
                    <RoleSelect label={`Role of ${displayName(p)}`} value={role} disabled={busy === key} guest={isGuest(p)}
                      onChange={(r) => r && void actions.setRole(p.id, ws.id, r, `${displayName(p)} is now ${roleLabel(r)} in ${ws.name}`)} />
                  )}
                  <Button size="icon-sm" variant="ghost" className="h-7 w-7 coarse:size-[44px]" disabled={lastOwner || busy === key}
                    aria-label={`Remove ${displayName(p)}`} title={lastOwner ? "The only owner stays; hand the workspace over first" : undefined} onClick={() => setRemoving(p.id)}>
                    <X className="size-3.5" />
                  </Button>
                </span>
              </div>
              <ConfirmDialog open={removing === p.id} onOpenChange={(o) => { if (!o) setRemoving(null) }} destructive
                title={`Remove ${displayName(p)} from ${ws.name}?`}
                consequences={[
                  { tone: "lost", text: `${displayName(p)} loses access to ${ws.name}` },
                  { tone: "kept", text: "Pages they own move to someone who stays" },
                ]}
                confirmLabel="Remove"
                onConfirm={async () => {
                  if (!(await actions.removeAccess(p.id, ws.id, `${displayName(p)} removed from ${ws.name}`))) throw new Error("refused")
                }} />
            </div>
          )
        })}
        {members.length === 0 && <p className="border-b border-border px-4 py-3 text-label text-muted-foreground">No one yet.</p>}
        <ShowMore shown={memberPage.shown.length} total={memberPage.total} nextCount={memberPage.nextCount} paged={memberPage.paged} onMore={memberPage.showMore} noun="people" />
      </SettingsCard>

      {pending.length > 0 && (
        <SettingsCard icon={Mail} tint="var(--purple)" title="Waiting for setup" description="Accounts nobody has chosen a password for yet">
          {waitingPage.shown.map((p) => (
            <SettingsRow key={p.id} label={displayName(p)} description={`Link expires in ${daysUntil(p.setup_link_expires_at)} days`}>
              <Button size="sm" variant="outline" disabled={busy === `l:${p.id}`} onClick={() => void newLink(p)}>New link</Button>
              <Button size="sm" variant="ghost" disabled={busy === `l:${p.id}`} onClick={() => void actions.revokeLink(p.id, "Setup link voided")}>Void</Button>
            </SettingsRow>
          ))}
          <ShowMore shown={waitingPage.shown.length} total={waitingPage.total} nextCount={waitingPage.nextCount} paged={waitingPage.paged} onMore={waitingPage.showMore} noun="people" />
        </SettingsCard>
      )}

      <SettingsCard icon={Building2} tint="var(--muted-foreground)" title="Kind"
        description="A workspace is for a team. A personal account belongs to one person and stays out of the workspace lists.">
        <div role="radiogroup" aria-label="Kind" data-slot="workspace-kind" className="grid gap-2 p-4 sm:grid-cols-2">
          <ChoiceTile icon={Building2} active={!personal} disabled={busy === `w:${ws.id}`} title="Workspace" description="Shown under Workspaces; any number of members"
            onClick={() => { if (personal) void actions.updateWorkspace(ws.id, { kind: "team" }, `${ws.name} is now a workspace`) }} />
          <ChoiceTile icon={House} active={personal} disabled={busy === `w:${ws.id}`} title="Personal account" description="Shown under Personal accounts; saves at once"
            onClick={() => { if (!personal) void actions.updateWorkspace(ws.id, { kind: "personal" }, `${ws.name} is now a personal account`) }} />
        </div>
      </SettingsCard>

      <SettingsDangerCard icon={AlertTriangle} title="Danger zone" description="Who owns this workspace, and whether it exists">
        <SettingsRow label="Transfer ownership" description="The new owner must be a member; the current owner stays on as Admin">
          <Combobox ariaLabel="New owner" value={newOwner || null} onChange={setNewOwner} disabled={nonOwners.length === 0}
            placeholder={nonOwners.length ? "Choose a member" : "No other member"} searchPlaceholder={`Search ${nonOwners.length} members`}
            options={personOptions(nonOwners)} className="w-full sm:w-64" />
          <Button size="sm" variant="outline" className="border-destructive/40 text-destructive" disabled={!newOwner} onClick={() => setConfirm("transfer")}>Transfer</Button>
        </SettingsRow>
        {newOwnerPerson && (
          <ConfirmDialog open={confirm === "transfer"} onOpenChange={(o) => { if (!o) setConfirm(null) }}
            title={`Make ${displayName(newOwnerPerson)} the owner of ${ws.name}?`}
            consequences={[
              { tone: "warn", text: `${displayName(newOwnerPerson)} becomes the owner of ${ws.name}` },
              { tone: "kept", text: "The current owner stays on as Admin" },
            ]}
            confirmLabel="Transfer"
            onConfirm={async () => {
              if (!(await actions.transfer(ws.id, newOwnerPerson.id, `${displayName(newOwnerPerson)} now owns ${ws.name}`))) throw new Error("refused")
              setNewOwner("")
            }} />
        )}
        <SettingsRow label="Delete workspace" description={`Removes ${ws._count_crews} crews and ${ws._count_agents} agents. You type the address to confirm.`}>
          <Button size="sm" variant="outline" className="border-destructive/40 text-destructive" onClick={() => setConfirm("delete")}>Delete…</Button>
        </SettingsRow>
        <ConfirmDialog open={confirm === "delete"} onOpenChange={(o) => { if (!o) setConfirm(null) }} destructive
          title={`Delete ${ws.name}?`}
          description={`${ws.name} and everything in it is deleted. This cannot be undone.`}
          consequences={[
            { tone: "lost", text: `Every crew, agent, routine and artifact in it: ${ws._count_crews} crews, ${ws._count_agents} agents and their history. A backup is the only way back.` },
            { tone: "kept", text: "The people keep their accounts and every other workspace" },
          ]}
          typeToConfirm={ws.slug}
          confirmLabel={`Delete ${ws.name}`}
          onConfirm={async () => {
            if (!(await actions.deleteWorkspace(ws.id, ws.slug, `${ws.name} deleted`))) throw new Error("refused")
            onDeleted()
          }} />
      </SettingsDangerCard>

      {showAudit && (
        <AdminAuditCard entityId={ws.id} title="Admin actions on this workspace" description="What instance admins changed, newest first" people={people} workspaces={[ws]} />
      )}

      <Link href="/admin/people?view=access" className="inline-flex items-center gap-1 text-label text-primary-hover hover:underline">
        Who is where <ChevronRight className="size-3" />
      </Link>
    </div>
  )
}
