"use client"

import { useState } from "react"
import Link from "next/link"
import { ChevronDown, ChevronRight, Layers, ListChecks, Lock, TriangleAlert } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { personLabel } from "@/components/ui/user-avatar"
import { SettingsCard, SettingsDangerCard, SettingsRow } from "@/components/features/settings/shared"
import { capabilityLock, HIGH_STAKES_CAPABILITIES } from "@/components/admin/member-capabilities"
import { MemberResourceAccess } from "@/components/features/settings/member-resource-access"
import { Capability, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"

import {
  ACCESS_PROFILES, AREAS, ASSIGNABLE_ROLES, CAPABILITY_HINT, CAPABILITY_NAME, ROLE_LABEL, ROLE_SUMMARY_BY_ROLE,
  aboveRole, accessProfile, extraCapabilities, profileDiffText, roleChoices, PROFILE_LABEL, type Member,
} from "./members-model"
import { AREA_ICON, CAPABILITY_ICON, ConfirmWhen, ROLE_ICON, RadioOption, RolePill, abilityAriaLabel } from "./member-bits"
import { RemoveMember } from "./remove-member"
import type { RemoveOutcome } from "./use-members"

export { RolePill } from "./member-bits"

/**
 * One person, in the panel beside the roster (full screen on a phone). Top to
 * bottom: a warning when their role and their abilities disagree, with both
 * ways out; then three cards for three questions —
 *
 *   Role       what they manage (people, crews, settings);
 *   Abilities  what they may do themselves: a preset, then single abilities;
 *   Reach      where those apply: the whole workspace, or a Guest's shares —
 *
 * and the way out. A role change applies at once; abilities and reach are
 * drafts that wait for the page's Save. Ability writes go back through the
 * page (`onCell`, `onProfile`, `onRevoke`) so a change made here is drafted
 * and confirmed exactly like the same change anywhere else.
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
  tryRemove,
  handOverPage,
  onRemoved,
  onCell,
  onProfile,
  onRevoke,
  revealEnabled = null,
  onAccessSaved,
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
  tryRemove: (member: Member) => Promise<RemoveOutcome>
  handOverPage: (member: Member, pageId: string, successorUserId: string) => Promise<string | null>
  onRemoved: (member: Member) => void
  onCell: (m: Member, cap: CapabilityValue, next: boolean) => void
  onProfile: (m: Member, profile: CapabilityBundle) => void
  onRevoke: (m: Member, caps: CapabilityValue[]) => void
  /** The workspace's reveal switch (Access & Secrets); null when unknown. */
  revealEnabled?: boolean | null
  /** A saved reach changes the roster's Guest marker; reload it. */
  onAccessSaved?: () => void
}) {
  const label = personLabel(member.user.full_name, member.user.email)
  const isSelf = currentUserId === member.user.id
  const isOwner = member.role === "OWNER"
  const showCaps = isAdmin && Boolean(currentUserId)
  const choices = roleChoices(callerRole, member, isSelf)
  const abilitiesLocked = isOwner || isSelf || !isAdmin
  const above = showCaps ? aboveRole(member, granted) : []
  const [pendingRole, setPendingRole] = useState<string | null>(null)
  const RoleIcon = ROLE_ICON[member.role]

  return (
    <div className="space-y-4" data-slot="member-detail" aria-label={`Member details: ${label}`} role="region">
      {above.length > 0 && !abilitiesLocked && (
        <div role="status" data-slot="member-above-role" className="flex items-start gap-2.5 rounded-xl border border-warn/40 bg-warn/[0.07] px-3.5 py-3">
          <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0 text-warn" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="text-control font-medium">More than a {ROLE_LABEL[member.role]} manages</p>
            <p className="mt-0.5 text-label text-muted-foreground">
              Can {above.map((c) => CAPABILITY_NAME[c].toLowerCase()).join(", ")}. Raise the role, or turn {above.length === 1 ? "it" : "them"} off.
            </p>
            <div className="mt-2.5 flex flex-wrap gap-1.5">
              <Button variant="outline" size="sm" className="px-2.5 text-xs coarse:h-[44px]" disabled={busy} onClick={() => onRevoke(member, above)}>
                Turn {above.length === 1 ? "it" : "them"} off
              </Button>
              {choices.includes("MANAGER") && (
                <Button variant="outline" size="sm" className="px-2.5 text-xs coarse:h-[44px]" onClick={() => setPendingRole("MANAGER")}>Make Manager</Button>
              )}
            </div>
          </div>
        </div>
      )}

      <SettingsCard icon={RoleIcon} title="Role" description="What they manage">
        <div className="space-y-2 px-4 py-3">
          <RoleControl member={member} choices={choices} isSelf={isSelf} label={label} onChangeRole={onChangeRole}
            pendingRole={pendingRole} setPendingRole={setPendingRole} />
        </div>
      </SettingsCard>

      {showCaps && (
        <SettingsCard icon={ListChecks} title="Abilities" description="What they may do themselves">
          <AbilitiesCard member={member} label={label} currentUserId={currentUserId} isAdmin={isAdmin} granted={granted} serverGranted={serverGranted}
            capsLoading={capsLoading} busy={busy} locked={abilitiesLocked} revealEnabled={revealEnabled}
            onCell={onCell} onProfile={onProfile} />
        </SettingsCard>
      )}

      {showCaps && (
        <SettingsCard icon={Layers} tint="var(--purple)" title="Reach" description="Where the role and abilities apply">
          <MemberResourceAccess workspaceId={workspaceId} memberId={member.id} label={label} role={member.role} onSaved={onAccessSaved} />
        </SettingsCard>
      )}

      {isAdmin && !isOwner && !isSelf && (
        <SettingsDangerCard title="Remove from workspace">
          <SettingsRow label={`${label} loses every crew, routine and artifact here at once`} border={false}>
            <RemoveMember member={member} label={label} tryRemove={tryRemove} handOverPage={handOverPage} onRemoved={onRemoved} />
          </SettingsRow>
        </SettingsDangerCard>
      )}
    </div>
  )
}

/**
 * Abilities as a radio list — the three presets, and Custom (the nearest
 * preset and what differs) once a single ability was changed — then a folded
 * "Single abilities" that opens the nine switches by area.
 */
function AbilitiesCard({ member, label, currentUserId, isAdmin, granted, serverGranted, capsLoading, busy, locked, revealEnabled, onCell, onProfile }: {
  member: Member
  label: string
  currentUserId?: string
  isAdmin: boolean
  granted: string[]
  serverGranted?: string[]
  capsLoading: boolean
  busy: boolean
  locked: boolean
  revealEnabled: boolean | null
  onCell: (m: Member, cap: CapabilityValue, next: boolean) => void
  onProfile: (m: Member, profile: CapabilityBundle) => void
}) {
  const [open, setOpen] = useState(false)
  const profile = accessProfile(granted)
  const diff = profileDiffText(profile)
  const custom = diff !== ""
  const on = extraCapabilities(granted).length

  if (member.role === "OWNER") {
    return <p className="flex items-center gap-1.5 px-4 py-3 text-label text-muted-foreground"><Lock className="h-3 w-3" aria-hidden />An owner&apos;s role and abilities are fixed. Ownership moves through its own transfer.</p>
  }
  return (
    <div>
      <div role="radiogroup" aria-label={`Abilities for ${label}`} className="space-y-1.5 px-4 py-3">
        {ACCESS_PROFILES.map((p) => (
          <RadioOption key={p.key} checked={!custom && p.key === profile.key} title={p.label} description={p.summary}
            disabled={locked || busy || capsLoading} onSelect={() => onProfile(member, p.key)} />
        ))}
        {custom && <RadioOption checked title="Custom" description={`${PROFILE_LABEL[profile.key]} ${diff}`} disabled={locked || busy || capsLoading} />}
      </div>
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} data-slot="member-single-abilities"
        className="flex w-full items-center gap-2 border-t border-border/60 px-4 py-2.5 text-left text-control coarse:min-h-[44px] hover:bg-[var(--row-hover-bg)]">
        {open ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" aria-hidden /> : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />}
        <span>Single abilities</span>
        <span className="ml-auto text-label tabular-nums text-muted-foreground">{on} of {AREAS.reduce((n, a) => n + a.caps.length, 0)} on</span>
      </button>
      {open && AREAS.map((a) => {
        const AreaIcon = AREA_ICON[a.key]
        const held = a.caps.filter((c) => granted.includes(c.cap)).length
        return (
          <div key={a.key} role="group" aria-label={a.label}>
            <p className={cn("flex items-center gap-1.5 border-y border-border/60 px-4 py-1.5 text-micro font-semibold",
              a.danger ? "bg-destructive/[0.06] text-destructive" : "bg-surface-subtle text-muted-foreground")}>
              <AreaIcon className="h-3 w-3" aria-hidden />{a.label}
              <span className="ml-auto font-normal tabular-nums">{held} of {a.caps.length}</span>
            </p>
            {a.key === "secrets" && revealEnabled !== null && (
              <p className="flex items-center gap-2 border-b border-border/60 px-4 py-2 text-label text-muted-foreground" data-slot="member-reveal-policy">
                <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", revealEnabled ? "bg-success" : "bg-muted-foreground")} aria-hidden />
                <span className="min-w-0 flex-1">Reveal is {revealEnabled ? "on" : "off"} for this workspace{revealEnabled ? "" : " — the ability does nothing until the Owner turns it on"}.</span>
                <Link href="/settings?tab=access-secrets" className="shrink-0 font-medium text-primary-hover hover:underline">Access &amp; Secrets</Link>
              </p>
            )}
            {a.caps.map(({ cap }) => {
              const isOn = granted.includes(cap)
              const lock = capabilityLock(member, cap, currentUserId, isAdmin)
              const unsaved = serverGranted !== undefined && isOn !== serverGranted.includes(cap)
              const Icon = CAPABILITY_ICON[cap]
              const red = cap === Capability.CredentialReveal
              return (
                <button key={cap} type="button" role="switch" aria-checked={isOn} data-capability={cap}
                  aria-label={abilityAriaLabel(cap, label, isOn, lock)}
                  disabled={lock !== null || busy || capsLoading}
                  onClick={() => onCell(member, cap, !isOn)}
                  data-unsaved={unsaved || undefined}
                  className={cn("flex w-full items-center gap-3 border-b border-border/60 px-4 py-2.5 text-left coarse:min-h-[52px]! last:border-b-0",
                    lock === null && "hover:bg-[var(--row-hover-bg)]", unsaved && "bg-[var(--save-bar-bg)]")}>
                  <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg border border-border bg-surface-subtle text-muted-foreground"><Icon className="h-3.5 w-3.5" aria-hidden /></span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-1.5 text-control">
                      {CAPABILITY_NAME[cap]}
                      {HIGH_STAKES_CAPABILITIES.has(cap) && <span className="text-micro font-medium text-destructive">high-stakes</span>}
                      {unsaved && <span className="text-micro text-[var(--save-bar-fg)]">unsaved</span>}
                    </span>
                    <span className="block text-label text-muted-foreground">{CAPABILITY_HINT[cap]}</span>
                  </span>
                  <SwitchTrack on={isOn} red={red} locked={lock !== null} />
                </button>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}

/**
 * The role as a radio list, one line under each. A confirm dialog gates the
 * change: a role is the widest grant on the page, and it applies at once.
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
          {member.role === "OWNER" ? "An owner’s role is fixed. Ownership moves through its own transfer." : isSelf ? "You cannot change your own role." : "Your role cannot change this person’s role."}
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

  const locked = ASSIGNABLE_ROLES.some((r) => !choices.includes(r) && r !== member.role)
  const guest = member.access_mode === "restricted"
  return (
    <>
      <div role="radiogroup" aria-label={`Role for ${label}`} className="space-y-1.5">
        {[...ASSIGNABLE_ROLES].reverse().map((r) => {
          const current = member.role === r
          const allowed = choices.includes(r)
          const why = guest && r === "ADMIN" ? "A Guest cannot be an administrator" : "Your role cannot give this role"
          return (
            <RadioOption key={r} checked={current} title={ROLE_LABEL[r] ?? r} description={ROLE_SUMMARY_BY_ROLE[r]}
              disabled={saving || (!current && !allowed)} why={!current && !allowed ? why : undefined}
              onSelect={() => setPendingRole(r)} />
          )
        })}
      </div>
      {locked && <p className="text-label text-muted-foreground" data-slot="role-why">You can only give roles below your own. Only an Owner makes an Admin.{guest ? " A Guest cannot be an administrator; give them whole-workspace access first." : ""}</p>}

      <AlertDialog open={pendingRole !== null} onOpenChange={(o) => { if (!o) setPendingRole(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">Change role</AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Make <span className="font-medium text-foreground">{label}</span> {pendingRole ? ROLE_LABEL[pendingRole] ?? pendingRole : ""}, not{" "}
              <span className="font-medium">{ROLE_LABEL[member.role] ?? member.role}</span>?
              {" "}Their abilities stay as they are.
            </AlertDialogDescription>
            <ConfirmWhen when="now" />
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs coarse:h-[44px]" disabled={saving}>Cancel</AlertDialogCancel>
            <AlertDialogAction className="h-7 text-xs coarse:h-[44px]" disabled={saving}
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
