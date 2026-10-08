"use client"

import * as React from "react"
import { AlertTriangle, Building2, KeyRound, Plus, ShieldCheck, UserRound, Database, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { SettingsCard, SettingsDangerCard, SettingsRow, nativeSelect } from "@/components/features/settings/shared"
import { UserDataActions } from "@/components/features/admin/user-data-actions"
import { UserSessions } from "./person-sessions"
import { WorkspaceTile, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  adminSourceLabel, daysUntil, displayName, isLastOwner, personStatus, roleIn, type Person, type Role, type Workspace,
} from "./people-model"
import { InlineConfirm, PersonAvatar, RoleSelect, SetupLinkBox, StatusChip, roleLabel } from "./people-parts"
import type { SetupLink, usePeople } from "./use-people"
import { cn } from "@/lib/utils"

type Actions = ReturnType<typeof usePeople>["actions"]

/**
 * One person, the way an instance admin looks after them: where they work
 * and as what (the card this page exists for), how they sign in, whether they
 * administer the instance, their data, and — last — suspending them.
 */
export function PersonProfile({ person, people, workspaces, workspaceId, meId, busy, actions, onChanged }: {
  person: Person
  people: Person[]
  workspaces: Workspace[]
  /** The caller's workspace: sessions and GDPR are scoped to it on the server. */
  workspaceId: string
  meId: string | undefined
  busy: string | null
  actions: Actions
  onChanged: () => void
}) {
  // Personal data is held per workspace. The admin's own workspace when the
  // person is in it; otherwise one of theirs — an instance admin need not
  // belong to any workspace.
  const [dataWs, setDataWs] = React.useState(() =>
    person.memberships.some((m) => m.workspace_id === workspaceId) ? workspaceId : (person.memberships[0]?.workspace_id ?? ""))

  const name = displayName(person)
  const status = personStatus(person)
  const isMe = person.id === meId
  const [adding, setAdding] = React.useState(false)
  const [addWs, setAddWs] = React.useState("")
  const [addRole, setAddRole] = React.useState<Role>("MEMBER")
  const [removing, setRemoving] = React.useState<string | null>(null)
  const [confirm, setConfirm] = React.useState<"suspend" | null>(null)
  const [link, setLink] = React.useState<SetupLink | null>(null)
  const byId = new Map(workspaces.map((w) => [w.id, w]))
  const available = workspaces.filter((w) => !roleIn(person, w.id))
  const firstName = name.split(/\s+/)[0]

  React.useEffect(() => { setLink(null); setAdding(false); setRemoving(null); setConfirm(null) }, [person.id])

  const add = async () => {
    const wsId = addWs || available[0]?.id
    if (!wsId) return
    const ok = await actions.setRole(person.id, wsId, addRole, `${name} joined ${byId.get(wsId)?.name ?? "the workspace"} as ${roleLabel(addRole)}`)
    if (ok) { setAdding(false); setAddWs("") }
  }

  return (
    <div className="space-y-4" data-slot="person-profile">
      <section aria-label={name} className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-wrap items-start gap-4 p-4">
          <PersonAvatar person={person} className="h-12 w-12" />
          <div className="min-w-0 flex-1">
            <h2 className="flex flex-wrap items-center gap-2 text-lg font-semibold tracking-[-0.01em]">
              {name}
              <StatusChip person={person} />
              {person.instance_admin && (
                <span className="inline-flex h-5 items-center gap-1 rounded-full bg-[color-mix(in_srgb,var(--purple)_16%,transparent)] px-2 font-mono text-[10.5px] text-[var(--purple)]">
                  <ShieldCheck className="h-3 w-3" />Instance admin
                </span>
              )}
            </h2>
            <p className="mt-0.5 text-[12.5px] text-muted-foreground">{person.email}</p>
            <div className="mt-3 flex flex-wrap gap-1.5 text-[11.5px] text-muted-foreground">
              <Meta label="Joined" value={new Date(person.created_at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })} />
              <Meta label="Last active" value={ago(person.last_active_at)} />
              <Meta value={String(person.memberships.length)} label={person.memberships.length === 1 ? "workspace" : "workspaces"} after />
              <Meta value={String(person.active_sessions ?? 0)} label="sessions" after />
              <Meta value={String(person.cli_tokens ?? 0)} label="CLI tokens" after />
            </div>
          </div>
        </div>
      </section>

      <SettingsCard icon={Building2} tint="var(--primary)" title="Workspace access" description="Where this person works, and as what"
        actions={<span className="font-mono text-[11px] text-muted-foreground">{person.memberships.length} of {workspaces.length}</span>}>
        {person.memberships.length === 0 && (
          <p className="border-b border-border px-4 py-3 text-[12px] text-muted-foreground">Not in any workspace. They can sign in but see nothing.</p>
        )}
        {person.memberships.map((m) => {
          const lastOwner = m.role === "OWNER" && isLastOwner(people, m.workspace_id, person.id)
          const key = `m:${person.id}:${m.workspace_id}`
          return (
            <div key={m.workspace_id} className="border-b border-border last:border-b-0">
              <div className="flex items-center gap-3 px-4 py-2.5">
                <WorkspaceTile id={m.workspace_id} name={m.name} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px]">{m.name}</span>
                  <span className="block truncate text-[11px] text-muted-foreground-soft">
                    {lastOwner ? "Only owner · hand it over from the workspace" : `Member since ${new Date(m.joined_at).toLocaleDateString()}`}
                  </span>
                </span>
                <span className="flex shrink-0 items-center justify-end gap-1.5 sm:w-64">
                  {lastOwner ? (
                    <span className="inline-flex h-8 w-36 items-center rounded-md border border-border px-2 text-control text-muted-foreground">Owner</span>
                  ) : (
                    <RoleSelect label={`Role in ${m.name}`} value={m.role as Role} disabled={busy === key}
                      onChange={(r) => r && void actions.setRole(person.id, m.workspace_id, r, `${name} is now ${roleLabel(r)} in ${m.name}`)} />
                  )}
                  <Button size="icon-sm" variant="ghost" className="h-7 w-7" disabled={lastOwner || busy === key}
                    aria-label={`Remove from ${m.name}`} title={lastOwner ? "The only owner stays" : `Remove from ${m.name}`}
                    onClick={() => setRemoving(m.workspace_id)}>
                    <X className="size-3.5" />
                  </Button>
                </span>
              </div>
              {removing === m.workspace_id && (
                <InlineConfirm busy={busy === key}
                  message={`${name} loses access to ${m.name}. Pages they own there move to someone who stays.`}
                  confirmLabel="Remove" onCancel={() => setRemoving(null)}
                  onConfirm={async () => { await actions.removeAccess(person.id, m.workspace_id, `${name} removed from ${m.name}`); setRemoving(null) }} />
              )}
            </div>
          )
        })}
        {available.length > 0 && (
          adding ? (
            <div className="flex flex-wrap items-center gap-2 border-t border-border px-4 py-2.5">
              <select aria-label="Workspace" value={addWs || available[0].id} onChange={(e) => setAddWs(e.target.value)}
                className={cn(nativeSelect, "min-w-0 flex-1")}>
                {available.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
              </select>
              <RoleSelect label="Role" value={addRole} onChange={(r) => r && setAddRole(r)} />
              <Button size="sm" variant="ghost" onClick={() => setAdding(false)}>Cancel</Button>
              <Button size="sm" onClick={add} disabled={busy === "m:" + person.id + ":" + (addWs || available[0].id)}>Add</Button>
            </div>
          ) : (
            <button type="button" onClick={() => setAdding(true)}
              className="flex w-full items-center gap-1.5 border-t border-border px-4 py-2.5 text-left text-[12.5px] text-primary-hover hover:bg-accent">
              <Plus className="size-3.5" />Add {firstName} to a workspace
            </button>
          )
        )}
      </SettingsCard>

      <SettingsCard icon={KeyRound} tint="var(--warn)" title="Sign-in" description="Password, lockout and where they are signed in">
        {status === "setup" ? (
          <>
            <SettingsRow label="Password" description={`Not chosen yet. The setup link expires in ${daysUntil(person.setup_link_expires_at)} days.`}>
              <Button size="sm" variant="outline" disabled={busy === `l:${person.id}`}
                onClick={async () => setLink(await actions.issueLink(person.id, person.email))}>New setup link</Button>
              <Button size="sm" variant="ghost" disabled={busy === `l:${person.id}`}
                onClick={() => void actions.revokeLink(person.id, "Setup link voided")}>Void</Button>
            </SettingsRow>
            {link && <SetupLinkBox url={link.url} expiresAt={link.expires_at} onDone={() => setLink(null)} />}
          </>
        ) : !person.last_active_at ? (
          // Never signed in and no link pending: whether anyone controls the
          // account is the server's call (it refuses a link for one that is),
          // so the page does not claim a password exists.
          <>
            <SettingsRow label="Password" description="Never signed in, and no setup link is pending.">
              <Button size="sm" variant="outline" disabled={busy === `l:${person.id}`}
                onClick={async () => setLink(await actions.issueLink(person.id, person.email))}>Issue setup link</Button>
            </SettingsRow>
            {link && <SetupLinkBox url={link.url} expiresAt={link.expires_at} onDone={() => setLink(null)} />}
          </>
        ) : (
          <SettingsRow label="Password" description="Chosen by the person. Admins never see or set it.">
            <span className="text-[11px] text-muted-foreground">{person.email_verified ? "Email verified" : "Email not verified"}</span>
          </SettingsRow>
        )}
        {status === "locked" ? (
          <SettingsRow label="Account lock" description={`Locked after ${person.failed_login_count ?? 0} failed sign-ins, until ${new Date(person.locked_until!).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`}>
            <Button size="sm" variant="outline" disabled={busy === `u:${person.id}`} onClick={() => void actions.unlock(person.id, `${name} can sign in again`)}>Unlock</Button>
          </SettingsRow>
        ) : (
          <SettingsRow label="Account lock" description={`Not locked · ${person.failed_login_count ?? 0} failed sign-ins`}>
            <span className="text-[11px] text-muted-foreground">OK</span>
          </SettingsRow>
        )}
        <div className="grid gap-4 border-t border-border px-4 py-3">
          <UserSessions user={person} workspaceId={workspaceId} isMe={isMe} onChanged={onChanged} />
        </div>
      </SettingsCard>

      <SettingsCard icon={ShieldCheck} tint="var(--purple)" title="Instance administration" description="Runs the server: people, workspaces, limits, providers">
        <SettingsRow label="Instance admin"
          description={person.instance_admin ? adminSourceLabel(person.instance_admin_source) : "Can administer their own workspaces only"}>
          {person.instance_admin_source === "role" ? (
            <Button size="sm" variant="outline" disabled={isMe || busy === `a:${person.id}`} title={isMe ? "Another instance admin has to remove you" : undefined}
              onClick={() => void actions.setInstanceAdmin(person.id, false, `${name} no longer administers the instance`)}>Remove</Button>
          ) : person.instance_admin_source === "env" ? (
            <span className="text-[11px] text-muted-foreground">Set on the server</span>
          ) : (
            <Button size="sm" variant="outline" disabled={status === "suspended" || busy === `a:${person.id}`}
              onClick={() => void actions.setInstanceAdmin(person.id, true, `${name} now administers the instance`)}>
              {person.instance_admin ? "Name them" : "Make instance admin"}
            </Button>
          )}
        </SettingsRow>
      </SettingsCard>

      <SettingsCard icon={Database} tint="var(--info)" title="Personal data" padded
        description={dataWs ? `Export or erase what ${workspaces.find((w) => w.id === dataWs)?.name ?? "this workspace"} holds about them` : "They belong to no workspace, so no workspace holds data about them"}>
        {person.memberships.length > 1 && (
          <select aria-label="Workspace for personal data" value={dataWs} onChange={(e) => setDataWs(e.target.value)}
            className={cn(nativeSelect, "mb-2")}>
            {person.memberships.map((m) => <option key={m.workspace_id} value={m.workspace_id}>{m.name}</option>)}
          </select>
        )}
        {dataWs && <UserDataActions userId={person.id} email={person.email} workspaceId={dataWs} onErased={onChanged} />}
      </SettingsCard>

      <SettingsDangerCard icon={AlertTriangle} title="Danger zone" description="Ends this person's access everywhere at once">
        {status === "suspended" ? (
          <SettingsRow label="Suspended" description={person.suspended_reason ? `Reason: ${person.suspended_reason}` : `Since ${ago(person.suspended_at)}`}>
            <Button size="sm" variant="outline" disabled={busy === `s:${person.id}`} onClick={() => void actions.reactivate(person.id, `${name} can sign in again`)}>Reactivate</Button>
          </SettingsRow>
        ) : (
          <>
            <SettingsRow label="Suspend account" description="Blocks sign-in and ends every session and CLI token; workspace access stays">
              <Button size="sm" variant="outline" className="border-destructive/40 text-destructive"
                disabled={isMe || person.instance_admin_source === "env" || busy === `s:${person.id}`}
                title={isMe ? "You cannot suspend yourself" : undefined}
                onClick={() => setConfirm("suspend")}>
                <UserRound />Suspend…
              </Button>
            </SettingsRow>
            {confirm === "suspend" && (
              <InlineConfirm busy={busy === `s:${person.id}`}
                message={`${name} is signed out everywhere and cannot sign in until reactivated.`}
                confirmLabel={`Suspend ${firstName}`} onCancel={() => setConfirm(null)}
                onConfirm={async () => { await actions.suspend(person.id, "", `${name} suspended`); setConfirm(null) }} />
            )}
          </>
        )}
      </SettingsDangerCard>
    </div>
  )
}

function Meta({ label, value, after }: { label: string; value: string; after?: boolean }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-0.5">
      {after ? <><b className="font-medium text-foreground">{value}</b>{label}</> : <>{label}<b className="font-medium text-foreground">{value}</b></>}
    </span>
  )
}
