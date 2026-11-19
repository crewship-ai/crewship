"use client"

import * as React from "react"
import { AlertTriangle, Building2, KeyRound, Lock, Plus, ShieldCheck, UserRound, Database, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { SettingsCard, SettingsDangerCard, SettingsRow } from "@/components/features/settings/shared"
import { Combobox } from "@/components/ui/combobox"
import { StatusPill } from "@/components/ui/status-pill"
import { UserDataActions } from "@/components/features/admin/user-data-actions"
import { UserSessions } from "./person-sessions"
import { WorkspaceTile, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import {
  daysUntil, displayName, isLastOwner, isPersonal, personStatus, roleIn, type Person, type Role, type Workspace,
} from "./people-model"
import { PersonAvatar, RoleSelect, StatusChip, roleLabel } from "./people-parts"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { SuspendConfirm } from "./people-dialogs"
import { AdminAuditCard } from "./admin-audit-card"
import type { SetupLink, usePeople } from "./use-people"

type Actions = ReturnType<typeof usePeople>["actions"]

/**
 * One person, the way an instance admin looks after them: where they work
 * and as what (the card this page exists for), how they sign in, whether they
 * administer the instance, their data, and — last — suspending them.
 */
export function PersonProfile({ person, people, workspaces, workspaceId, meId, busy, actions, onChanged, onAddToWorkspace, onShowLink, showAudit = true }: {
  person: Person
  people: Person[]
  workspaces: Workspace[]
  /** The caller's workspace: sessions and GDPR are scoped to it on the server. */
  workspaceId: string
  meId: string | undefined
  busy: string | null
  actions: Actions
  onChanged: () => void
  /** Opens the shared "Add to a workspace" dialog for this person. */
  onAddToWorkspace: (person: Person) => void
  /** Shows a freshly issued setup link, once. */
  onShowLink: (link: SetupLink) => void
  /** The audit log answers an instance admin only. */
  showAudit?: boolean
}) {
  // Personal data is held per workspace. The admin's own workspace when the
  // person is in it; otherwise one of theirs — an instance admin need not
  // belong to any workspace.
  const [dataWs, setDataWs] = React.useState(() =>
    person.memberships.some((m) => m.workspace_id === workspaceId) ? workspaceId : (person.memberships[0]?.workspace_id ?? ""))

  const name = displayName(person)
  const status = personStatus(person)
  const isMe = person.id === meId
  const [removing, setRemoving] = React.useState<string | null>(null)
  const [confirm, setConfirm] = React.useState<"suspend" | null>(null)
  const firstName = name.split(/\s+/)[0]
  const newLink = async () => {
    const link = await actions.issueLink(person.id, person.email)
    if (link) onShowLink(link)
  }

  React.useEffect(() => { setRemoving(null); setConfirm(null) }, [person.id])

  const byId = new Map(workspaces.map((w) => [w.id, w]))
  const adminWhy = {
    env: "Instance owner, set on the server (CREWSHIP_OWNER_EMAIL)",
    role: "Named instance admin",
  }[person.instance_admin_source ?? ""] ?? "Administers only the workspaces they own or manage"
  const dataName = byId.get(dataWs)?.name ?? person.memberships.find((m) => m.workspace_id === dataWs)?.name ?? "this workspace"

  return (
    <div className="space-y-4" data-slot="person-profile">
      <section aria-label={name} className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-wrap items-start gap-4 p-4">
          <PersonAvatar person={person} className="h-12 w-12" />
          <div className="min-w-0 flex-1">
            <h2 className="text-lg font-semibold tracking-[-0.01em]">{name}</h2>
            <p className="mt-0.5 flex flex-wrap gap-x-3 text-label text-muted-foreground">
              <span>{person.email}</span>
              <span>Joined {new Date(person.created_at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}</span>
              <span>Last active {ago(person.last_active_at)}</span>
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <StatusChip person={person} />
            {person.instance_admin && <StatusPill tone="purple" label="Instance admin" />}
          </div>
        </div>
      </section>

      <SettingsCard icon={Building2} tint="var(--primary)" title="Workspaces"
        description={person.memberships.length ? `${person.memberships.length} of ${workspaces.length} · where this person works, and as what` : "In no workspace: they can sign in but see nothing"}>
        {person.memberships.map((m) => {
          const lastOwner = m.role === "OWNER" && isLastOwner(people, m.workspace_id, person.id)
          const key = `m:${person.id}:${m.workspace_id}`
          return (
            <div key={m.workspace_id} className="border-b border-border last:border-b-0">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5">
                <WorkspaceTile id={m.workspace_id} name={m.name} logoUrl={byId.get(m.workspace_id)?.logo_url} />
                <span className="min-w-0 flex-[1_1_10rem]">
                  <span className="block truncate text-control">{m.name}</span>
                  <span className="block truncate text-label text-muted-foreground-soft">Member since {new Date(m.joined_at).toLocaleDateString()}</span>
                </span>
                <span className="ml-auto flex shrink-0 items-center justify-end gap-1.5">
                  {lastOwner ? (
                    <span className="inline-flex items-center gap-1 text-label text-muted-foreground" title="The only owner; hand it over from the workspace">
                      <Lock className="size-3" aria-hidden />Only owner · hand it over from the workspace
                    </span>
                  ) : (
                    <RoleSelect label={`Role in ${m.name}`} value={m.role as Role} disabled={busy === key}
                      onChange={(r) => r && void actions.setRole(person.id, m.workspace_id, r, `${name} is now ${roleLabel(r)} in ${m.name}`)} />
                  )}
                  <Button size="icon-sm" variant="ghost" className="h-7 w-7 coarse:size-11" disabled={lastOwner || busy === key}
                    aria-label={`Remove from ${m.name}`} title={lastOwner ? "The only owner stays" : `Remove from ${m.name}`}
                    onClick={() => setRemoving(m.workspace_id)}>
                    <X className="size-3.5" />
                  </Button>
                </span>
              </div>
              <ConfirmDialog open={removing === m.workspace_id} onOpenChange={(o) => { if (!o) setRemoving(null) }} destructive
                title={`Remove ${name} from ${m.name}?`}
                consequences={[
                  { tone: "lost", text: `${name} loses access to ${m.name}` },
                  { tone: "kept", text: "Pages they own there move to someone who stays" },
                ]}
                confirmLabel="Remove"
                onConfirm={async () => {
                  if (!(await actions.removeAccess(person.id, m.workspace_id, `${name} removed from ${m.name}`))) throw new Error("refused")
                }} />
            </div>
          )
        })}
        {workspaces.some((w) => !roleIn(person, w.id) && !isPersonal(w)) && (
          <div className="border-t border-border px-4 py-2.5 first:border-t-0">
            <Button size="sm" variant="outline" onClick={() => onAddToWorkspace(person)}><Plus />Add {firstName} to a workspace</Button>
          </div>
        )}
      </SettingsCard>

      <SettingsCard icon={KeyRound} tint="var(--warn)" title="Sign-in" description="Password, lock-out and where they are signed in">
        {status === "setup" ? (
          <SettingsRow label="Password" description={`Not chosen yet. The setup link expires in ${daysUntil(person.setup_link_expires_at)} days.`}>
            <Button size="sm" variant="outline" disabled={busy === `l:${person.id}`} onClick={() => void newLink()}>New setup link</Button>
            <Button size="sm" variant="ghost" disabled={busy === `l:${person.id}`}
              onClick={() => void actions.revokeLink(person.id, "Setup link voided")}>Void</Button>
          </SettingsRow>
        ) : !person.last_active_at ? (
          // Never signed in and no link pending: whether anyone controls the
          // account is the server's call (it refuses a link for one that is),
          // so the page does not claim a password exists.
          <SettingsRow label="Password" description="Never signed in, and no setup link is pending.">
            <Button size="sm" variant="outline" disabled={busy === `l:${person.id}`} onClick={() => void newLink()}>Issue setup link</Button>
          </SettingsRow>
        ) : (
          <SettingsRow label="Password" description="Chosen by the person. Admins never see or set it.">
            <span className="text-label text-muted-foreground">{person.email_verified ? "Email verified" : "Email not verified"}</span>
          </SettingsRow>
        )}
        {status === "locked" ? (
          <SettingsRow label="Account lock" description={`Locked after ${person.failed_login_count ?? 0} failed sign-ins, until ${new Date(person.locked_until!).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`}>
            <Button size="sm" variant="outline" disabled={busy === `u:${person.id}`} onClick={() => void actions.unlock(person.id, `${name} can sign in again`)}>Unlock</Button>
          </SettingsRow>
        ) : (
          <SettingsRow label="Account lock" description={`Not locked · ${person.failed_login_count ?? 0} failed sign-ins`}>
            <span className="text-label text-muted-foreground">Not locked</span>
          </SettingsRow>
        )}
        <div className="grid gap-4 border-t border-border px-4 py-3">
          <UserSessions user={person} workspaceId={workspaceId} isMe={isMe} onChanged={onChanged} />
        </div>
      </SettingsCard>

      <SettingsCard icon={ShieldCheck} tint="var(--purple)" title="Instance administration" description="Runs the server: people, every workspace, limits, backups">
        <SettingsRow label="Instance admin"
          description={<>
            {adminWhy}
            {person.instance_admin_source === "role" && isMe && <><br />Another instance admin has to remove you.</>}
            {!person.instance_admin && status === "suspended" && <><br />A suspended account cannot become an instance admin.</>}
          </>}>
          {person.instance_admin_source === "role" ? (
            <Button size="sm" variant="outline" disabled={isMe || busy === `a:${person.id}`} title={isMe ? "Another instance admin has to remove you" : undefined}
              onClick={() => void actions.setInstanceAdmin(person.id, false, `${name} no longer administers the instance`)}>Remove</Button>
          ) : person.instance_admin_source === "env" ? (
            <span className="inline-flex items-center gap-1 text-label text-muted-foreground"><Lock className="size-3" aria-hidden />Set on the server</span>
          ) : (
            <Button size="sm" variant="outline" disabled={status === "suspended" || busy === `a:${person.id}`}
              title={status === "suspended" ? "A suspended account cannot become an instance admin" : undefined}
              onClick={() => void actions.setInstanceAdmin(person.id, true, `${name} now administers the instance`)}>
              {person.instance_admin ? "Name them" : "Make instance admin"}
            </Button>
          )}
        </SettingsRow>
      </SettingsCard>

      <SettingsCard icon={Database} tint="var(--info)" title="Personal data"
        description="Export or erase what a workspace holds about them">
        {person.memberships.length > 1 ? (
          <SettingsRow label="Workspace" description="Personal data is held per workspace">
            <Combobox ariaLabel="Workspace for personal data" value={dataWs} onChange={setDataWs} className="w-64"
              options={person.memberships.map((m) => ({ value: m.workspace_id, label: m.name, description: m.slug }))} />
          </SettingsRow>
        ) : (
          <SettingsRow label="Workspace" description={dataWs ? undefined : "They belong to no workspace, so no workspace holds data about them"}>
            <span className="text-label text-muted-foreground">{dataWs ? dataName : "No workspace"}</span>
          </SettingsRow>
        )}
        {dataWs && (
          <div className="px-4 py-3">
            <UserDataActions userId={person.id} email={person.email} workspaceId={dataWs} onErased={onChanged} />
          </div>
        )}
      </SettingsCard>

      {showAudit && (
        <AdminAuditCard entityId={person.id} title="Admin actions on this account" description="What instance admins changed, newest first" people={people} workspaces={workspaces} />
      )}

      <SettingsDangerCard icon={AlertTriangle} title={status === "suspended" ? "Suspended" : "Suspend account"}
        description="Ends their sessions first, then blocks sign-in everywhere. Memberships and data stay.">
        {status === "suspended" ? (
          <SettingsRow label="Suspended" description={person.suspended_reason ? `Reason: ${person.suspended_reason}` : `Since ${ago(person.suspended_at)}`}>
            <Button size="sm" variant="outline" disabled={busy === `s:${person.id}`} onClick={() => void actions.reactivate(person.id, `${name} can sign in again`)}>Reactivate</Button>
          </SettingsRow>
        ) : (
          <>
            <SettingsRow label="Suspend account"
              description={isMe ? "You cannot suspend yourself" : person.instance_admin_source === "env" ? "Set on the server; the instance owner cannot be suspended here" : "Blocks sign-in and ends every session and CLI token; workspace access stays"}>
              <Button size="sm" variant="outline" className="border-destructive/40 text-destructive"
                disabled={isMe || person.instance_admin_source === "env" || busy === `s:${person.id}`}
                title={isMe ? "You cannot suspend yourself" : person.instance_admin_source === "env" ? "The instance owner is set on the server" : undefined}
                onClick={() => setConfirm("suspend")}>
                <UserRound />Suspend…
              </Button>
            </SettingsRow>
            <SuspendConfirm open={confirm === "suspend"} onOpenChange={(o) => { if (!o) setConfirm(null) }} people={[person]}
              onSuspend={async (reason) => {
                if (!(await actions.suspend(person.id, reason, `${name} suspended`))) throw new Error("refused")
              }} />
          </>
        )}
      </SettingsDangerCard>
    </div>
  )
}
