"use client"

import { useState } from "react"
import { Boxes, Lock, Shield, Sparkles, Trash2, TriangleAlert } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { SettingsCard, SettingsDangerCard, SettingsRow } from "@/components/features/settings/shared"
import { capabilityAriaLabel, capabilityLock } from "@/components/admin/member-capabilities"
import { MemberResourceAccess } from "@/components/features/settings/member-resource-access"
import { CAPABILITY_LABELS, Capability, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"

import {
  ACCESS_PROFILES, AREAS, ASSIGNABLE_ROLES, CAPABILITY_HINT, CAPABILITY_SHORT, ROLE_LABEL, ROLE_SUMMARY_BY_ROLE,
  aboveRole, accessProfile, exceptionCount, roleChoices, type Member,
} from "./members-model"
import { AREA_ICON, CAPABILITY_ICON, RolePill } from "./member-bits"
import { joinedDate } from "./people-table"

export { RolePill } from "./member-bits"

/**
 * One person, in the panel beside the roster (full screen on a phone). Top to
 * bottom: a warning when their role and their access disagree, with both ways
 * out; the role (what they manage); what they may do — a preset, then each
 * ability by area; where they may use it; and the way out.
 *
 * Capability writes go back through the page (`onCell`, `onProfile`,
 * `onRevoke`) so a change made here is drafted and confirmed exactly like the
 * same change on the roster.
 */
export function MemberDetail({
  member,
  workspaceId,
  currentUserId,
  callerRole,
  isAdmin,
  granted,
  serverGranted,
  capsLoading,
  busy,
  onChangeRole,
  onRemove,
  onCell,
  onProfile,
  onRevoke,
}: {
  member: Member
  workspaceId: string
  currentUserId?: string
  callerRole?: string | null
  isAdmin: boolean
  /** As drafted, and as the server holds it: a switch that differs is
   *  marked unsaved. */
  granted: string[]
  serverGranted?: string[]
  capsLoading: boolean
  busy: boolean
  onChangeRole: (member: Member, role: string) => Promise<boolean>
  onRemove: (member: Member) => Promise<boolean>
  onCell: (m: Member, cap: CapabilityValue, next: boolean) => void
  onProfile: (m: Member, profile: CapabilityBundle) => void
  onRevoke: (m: Member, caps: CapabilityValue[]) => void
}) {
  const label = personLabel(member.user.full_name, member.user.email)
  const isSelf = currentUserId === member.user.id
  const isOwner = member.role === "OWNER"
  const showCaps = isAdmin && Boolean(currentUserId)
  const profile = accessProfile(granted)
  const diff = exceptionCount(profile)
  const locked = isOwner || isSelf || !isAdmin
  const choices = roleChoices(callerRole, member, isSelf)
  const above = showCaps ? aboveRole(member, granted) : []
  const [pendingRole, setPendingRole] = useState<string | null>(null)

  return (
    <div className="space-y-4" data-slot="member-detail" aria-label={`Member details: ${label}`} role="region">
      <div className="flex items-start gap-3.5 pr-8" data-slot="member-profile">
        <UserAvatar name={member.user.full_name} email={member.user.email} src={member.user.avatar_url} className="h-11 w-11" textClassName="text-sm" />
        <div className="min-w-0 flex-1">
          <p className="flex items-baseline gap-2 text-base font-semibold tracking-[-0.01em]">
            <span className="truncate">{label}</span>
            {isSelf && <span className="text-xs font-normal text-muted-foreground">you</span>}
          </p>
          {member.user.full_name?.trim() && <p className="truncate font-mono text-micro text-muted-foreground">{member.user.email}</p>}
          <p className="mt-2 flex flex-wrap items-center gap-1.5">
            <RolePill role={member.role} />
            <span className="inline-flex h-[22px] items-center rounded-md bg-surface-subtle px-2 font-mono text-micro text-muted-foreground">joined {joinedDate(member.created_at)}</span>
          </p>
        </div>
      </div>

      {above.length > 0 && !locked && (
        <div role="status" data-slot="member-above-role" className="flex items-start gap-2.5 rounded-xl border border-warn/40 bg-warn/[0.07] px-3.5 py-3">
          <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0 text-warn" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="text-control font-medium">Holds more than a {ROLE_LABEL[member.role]} manages</p>
            <p className="mt-0.5 text-label text-muted-foreground">
              {above.map((c) => CAPABILITY_LABELS[c].en).join(", ")}. Raise the role, or turn {above.length === 1 ? "it" : "them"} off.
            </p>
            <div className="mt-2.5 flex flex-wrap gap-1.5">
              <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" disabled={busy} onClick={() => onRevoke(member, above)}>
                Turn {above.length === 1 ? CAPABILITY_SHORT[above[0]] ?? "it" : "them"} off
              </Button>
              {choices.includes("MANAGER") && (
                <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => setPendingRole("MANAGER")}>Make Manager</Button>
              )}
            </div>
          </div>
        </div>
      )}

      <SettingsCard icon={Shield} title="Role" description="What they manage: people, crews, settings">
        <div className="space-y-2 px-4 py-3">
          <RoleControl member={member} choices={choices} isSelf={isSelf} label={label} onChangeRole={onChangeRole}
            pendingRole={pendingRole} setPendingRole={setPendingRole} />
        </div>
      </SettingsCard>

      {showCaps && (
        <SettingsCard icon={Sparkles} title="What they can do" description={isOwner ? "An owner holds what ownership brings" : "Start from a preset, then switch single abilities"}>
          {isOwner ? (
            <p className="flex items-center gap-1.5 px-4 py-3 text-label text-muted-foreground"><Lock className="h-3 w-3" aria-hidden />An owner&apos;s abilities are fixed — ownership moves through its own transfer.</p>
          ) : (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-4 py-2.5">
              <span className="text-label text-muted-foreground">Preset</span>
              <div role="radiogroup" aria-label={`Preset for ${label}`} className="inline-flex gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
                {ACCESS_PROFILES.map((p) => {
                  const current = p.key === profile.key
                  return (
                    <button key={p.key} type="button" role="radio" aria-checked={current} title={p.summary}
                      disabled={locked || busy || capsLoading}
                      onClick={() => { if (!(current && diff === 0)) onProfile(member, p.key) }}
                      className={cn("h-7 rounded-md px-2.5 text-xs transition-colors disabled:cursor-default",
                        current ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}>
                      {p.label}
                    </button>
                  )
                })}
              </div>
              {diff > 0 && (
                <span className="ml-auto font-mono text-micro text-warn" data-slot="member-exceptions" title="What differs from the preset">
                  {[...profile.added.map((c) => `+${CAPABILITY_SHORT[c] ?? c}`), ...profile.removed.map((c) => `−${CAPABILITY_SHORT[c] ?? c}`)].join(" ")}
                </span>
              )}
            </div>
          )}
          {AREAS.map((a) => {
            const AreaIcon = AREA_ICON[a.key]
            const held = a.caps.filter((c) => granted.includes(c.cap)).length
            return (
              <div key={a.key} role="group" aria-label={a.label}>
                <p className={cn("flex items-center gap-1.5 border-b border-border/60 px-4 py-1.5 font-mono text-micro uppercase tracking-[0.08em]",
                  a.danger ? "bg-destructive/[0.06] text-destructive" : "bg-surface-subtle text-muted-foreground")}>
                  <AreaIcon className="h-3 w-3" aria-hidden />{a.label}
                  <span className="ml-auto normal-case tracking-normal">{held} of {a.caps.length}</span>
                </p>
                {a.caps.map(({ cap }) => {
                  const on = granted.includes(cap)
                  const lock = capabilityLock(member, cap, currentUserId, isAdmin)
                  const unsaved = serverGranted !== undefined && on !== serverGranted.includes(cap)
                  const Icon = CAPABILITY_ICON[cap]
                  const red = cap === Capability.CredentialReveal
                  return (
                    <button key={cap} type="button" role="switch" aria-checked={on} data-capability={cap}
                      aria-label={capabilityAriaLabel(cap, label, on, lock)} title={CAPABILITY_LABELS[cap].description}
                      disabled={lock !== null || busy || capsLoading}
                      onClick={() => onCell(member, cap, !on)}
                      data-unsaved={unsaved || undefined}
                      className={cn("flex w-full items-center gap-3 border-b border-border/60 px-4 py-2.5 text-left coarse:min-h-[52px]",
                        lock === null && "hover:bg-[var(--row-hover-bg)]", unsaved && "bg-[var(--save-bar-bg)]")}>
                      <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg border border-border bg-surface-subtle text-muted-foreground"><Icon className="h-3.5 w-3.5" aria-hidden /></span>
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-1.5 text-control">
                          {CAPABILITY_LABELS[cap].en}
                          {unsaved && <span className="font-mono text-micro text-[var(--save-bar-fg)]">unsaved</span>}
                        </span>
                        <span className="block text-label text-muted-foreground">{CAPABILITY_HINT[cap]}</span>
                      </span>
                      <SwitchTrack on={on} red={red} locked={lock !== null} />
                    </button>
                  )
                })}
              </div>
            )
          })}
        </SettingsCard>
      )}

      {showCaps && (
        <SettingsCard icon={Boxes} tint="var(--purple)" title="Crews & agents" description="Where they may use the abilities above">
          <div className="px-4 pb-3 [&>section]:mt-3">
            <MemberResourceAccess workspaceId={workspaceId} memberId={member.id} label={label} role={member.role} />
          </div>
        </SettingsCard>
      )}

      {isAdmin && !isOwner && !isSelf && (
        <SettingsDangerCard title="Remove from workspace">
          <SettingsRow label={`${label} loses access at once`} description="Every crew, routine and page here. This cannot be undone." border={false}>
            <RemoveMember label={label} onConfirm={() => onRemove(member)} />
          </SettingsRow>
        </SettingsDangerCard>
      )}
    </div>
  )
}

/**
 * The role as a segmented control, the current role's meaning under it. A
 * confirm dialog gates the change: a role is the widest grant on the page.
 * `pendingRole` is lifted so the panel's warning can ask for the same change.
 */
function RoleControl({ member, choices, isSelf, label, onChangeRole, pendingRole, setPendingRole }: {
  member: Member
  choices: string[]
  isSelf: boolean
  label: string
  onChangeRole: (member: Member, role: string) => Promise<boolean>
  pendingRole: string | null
  setPendingRole: (r: string | null) => void
}) {
  const [saving, setSaving] = useState(false)

  if (choices.length === 0) {
    return (
      <div data-slot="member-role-readonly" className="space-y-1">
        <p className="flex items-center gap-2 text-control font-medium">
          <RolePill role={member.role} />
          <Lock className="h-3 w-3 text-muted-foreground" aria-hidden />
        </p>
        <p className="text-label text-muted-foreground">
          {ROLE_SUMMARY_BY_ROLE[member.role] ?? "Permissions are defined by this role"}.{" "}
          {member.role === "OWNER" ? "The owner's role is immutable." : isSelf ? "You cannot change your own role." : "Your role cannot change this person's role."}
        </p>
      </div>
    )
  }

  async function apply(role: string) {
    setSaving(true)
    try {
      await onChangeRole(member, role)
    } finally {
      setSaving(false)
      setPendingRole(null)
    }
  }

  return (
    <>
      <div role="radiogroup" aria-label={`Role for ${label}`} className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5 max-sm:grid max-sm:w-full max-sm:grid-cols-4">
        {ASSIGNABLE_ROLES.map((r) => {
          const current = member.role === r
          const allowed = choices.includes(r)
          return (
            <button key={r} type="button" role="radio" aria-checked={current} aria-label={ROLE_LABEL[r] ?? r}
              disabled={saving || (!current && !allowed)}
              onClick={() => { if (!current && allowed) setPendingRole(r) }}
              className={cn("h-7 rounded-md px-3 text-xs transition-colors coarse:h-9",
                current ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : allowed ? "text-muted-foreground hover:text-foreground" : "cursor-not-allowed text-muted-foreground-soft")}>
              {ROLE_LABEL[r] ?? r}
            </button>
          )
        })}
      </div>
      <p className="text-label text-muted-foreground">{ROLE_SUMMARY_BY_ROLE[member.role] ? `${ROLE_SUMMARY_BY_ROLE[member.role]}.` : ""}</p>

      <AlertDialog open={pendingRole !== null} onOpenChange={(o) => { if (!o) setPendingRole(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">Change member role</AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Change <span className="font-medium text-foreground">{label}</span> from{" "}
              <span className="font-medium">{ROLE_LABEL[member.role] ?? member.role}</span> to <span className="font-medium">{pendingRole ? ROLE_LABEL[pendingRole] ?? pendingRole : ""}</span>?
              {" "}What they can do stays as it is.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs" disabled={saving}>Cancel</AlertDialogCancel>
            <AlertDialogAction className="h-7 text-xs" disabled={saving}
              onClick={(e) => {
                // Keep the dialog open while the PATCH is in flight; a second
                // click can't fire a duplicate request.
                e.preventDefault()
                if (pendingRole && !saving) void apply(pendingRole)
              }}>
              Change role
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function SwitchTrack({ on, red, locked }: { on: boolean; red?: boolean; locked: boolean }) {
  return (
    <span aria-hidden="true" className={cn("relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors", on ? (red ? "bg-destructive" : "bg-primary") : "bg-input", locked && "opacity-60")}>
      <span className={cn("grid size-4 place-items-center rounded-full bg-background shadow-xs transition-transform", on ? "translate-x-[1.125rem]" : "translate-x-0.5")}>
        {locked && <Lock className="size-2.5 text-muted-foreground" />}
      </span>
    </span>
  )
}

function RemoveMember({ label, onConfirm }: { label: string; onConfirm: () => Promise<boolean> }) {
  const [removing, setRemoving] = useState(false)
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="outline" size="sm" className="h-7 gap-1.5 border-destructive/40 px-2.5 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive" disabled={removing}>
          <Trash2 className="h-3 w-3" />
          Remove member
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="text-sm">Remove member</AlertDialogTitle>
          <AlertDialogDescription className="text-xs">
            Are you sure you want to remove <span className="font-medium text-foreground">{label}</span> from this
            workspace? This action cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
          <AlertDialogAction
            className="h-7 text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90"
            onClick={() => {
              setRemoving(true)
              void onConfirm().finally(() => setRemoving(false))
            }}
          >
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
