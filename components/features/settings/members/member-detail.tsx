"use client"

import { useState } from "react"
import { ArrowLeft, Lock, Trash2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { MemberCapabilityToggles } from "@/components/admin/member-capabilities"
import { MemberResourceAccess } from "@/components/features/settings/member-resource-access"

import { ASSIGNABLE_ROLES, ROLE_LABEL, ROLE_SUMMARY_BY_ROLE, relativeTime, roleChoices, type Member } from "./members-model"

const eyebrow = "font-mono text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground-soft"

/**
 * One person: who they are, their role, what they may do beyond it, and the
 * two membership actions (resource access, remove).
 *
 * Every control keeps the gate the in-page Settings section had — role change
 * at MANAGER+ down the ladder, capabilities / resource access / removal at
 * ADMIN+ — and everything a caller may not change reads as text, not as a
 * disabled control that invites a click.
 */
export function MemberDetail({
  member,
  workspaceId,
  currentUserId,
  callerRole,
  isAdmin,
  granted,
  capsLoading,
  onChangeRole,
  onRemove,
  onBack,
}: {
  member: Member
  workspaceId: string
  currentUserId?: string
  callerRole?: string | null
  isAdmin: boolean
  granted: string[]
  capsLoading: boolean
  onChangeRole: (member: Member, role: string) => Promise<boolean>
  onRemove: (member: Member) => Promise<boolean>
  /** Below lg the detail replaces the list; this is the way back to it. */
  onBack: () => void
}) {
  const label = personLabel(member.user.full_name, member.user.email)
  const isSelf = currentUserId === member.user.id
  const isOwner = member.role === "OWNER"
  // Capabilities are an ADMIN+ read (the bulk endpoint 403s otherwise), so
  // non-admins see the role explanation alone.
  const showCaps = isAdmin && Boolean(currentUserId)
  const hasName = Boolean((member.user.full_name ?? "").trim())

  return (
    <section aria-label={`Member details: ${label}`} className="space-y-4" data-slot="member-detail">
      <button
        type="button"
        onClick={onBack}
        className="inline-flex h-7 items-center gap-1.5 rounded-md border border-border bg-background pl-1.5 pr-2.5 text-xs font-medium text-foreground/85 shadow-xs hover:bg-accent lg:hidden"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        All people
      </button>

      <header className="flex items-center gap-3">
        <UserAvatar name={member.user.full_name} email={member.user.email} src={member.user.avatar_url} className="h-11 w-11" textClassName="text-sm" />
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold">{label}{isSelf && <span className="ml-1.5 text-xs font-normal text-muted-foreground">(you)</span>}</h2>
          {hasName && <p className="truncate font-mono text-micro text-muted-foreground">{member.user.email}</p>}
          <p className="text-micro text-muted-foreground-soft">Joined {relativeTime(member.created_at)}</p>
        </div>
      </header>

      <div className="space-y-1.5">
        <h3 className={eyebrow}>Role</h3>
        <RoleControl member={member} callerRole={callerRole} isSelf={isSelf} label={label} onChangeRole={onChangeRole} />
        <p className="text-label text-muted-foreground">
          {ROLE_SUMMARY_BY_ROLE[member.role] ?? "Permissions are defined by this role."}
          {isOwner && " The owner's role and capabilities are immutable."}
        </p>
      </div>

      {showCaps && (
        <div className="space-y-1.5">
          <div className="flex items-baseline gap-2">
            <h3 className={eyebrow}>Capabilities</h3>
            <span className="text-micro text-muted-foreground-soft">on top of the role · applies immediately</span>
          </div>
          <MemberCapabilityToggles
            member={member}
            workspaceId={workspaceId}
            currentUserId={currentUserId as string}
            granted={granted}
            isLoading={capsLoading}
          />
        </div>
      )}

      {showCaps && (
        <MemberResourceAccess workspaceId={workspaceId} memberId={member.id} label={label} role={member.role} />
      )}

      {isAdmin && !isOwner && !isSelf && <RemoveMember label={label} onConfirm={() => onRemove(member)} />}
    </section>
  )
}

/**
 * The role as a segmented control — Viewer / Member / Manager / Admin — when
 * the caller may change it, otherwise as plain text. A confirm dialog gates
 * the PATCH: a role is the widest grant on the page, and one stray click on
 * a segment should not be enough to change it.
 */
function RoleControl({
  member,
  callerRole,
  isSelf,
  label,
  onChangeRole,
}: {
  member: Member
  callerRole?: string | null
  isSelf: boolean
  label: string
  onChangeRole: (member: Member, role: string) => Promise<boolean>
}) {
  const [pendingRole, setPendingRole] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const choices = roleChoices(callerRole, member, isSelf)

  if (choices.length === 0) {
    return (
      <p className="inline-flex items-center gap-1.5 text-xs font-medium" data-slot="member-role-readonly">
        {ROLE_LABEL[member.role] ?? member.role}
        <Lock className="h-3 w-3 text-muted-foreground" aria-hidden />
        <span className="sr-only">(you cannot change this role)</span>
      </p>
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
      <div role="radiogroup" aria-label={`Role for ${label}`} className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
        {ASSIGNABLE_ROLES.map((r) => {
          const current = member.role === r
          // A role above the caller's own is shown, so the ladder reads whole,
          // but cannot be picked.
          const allowed = choices.includes(r)
          return (
            <button
              key={r}
              type="button"
              role="radio"
              aria-checked={current}
              disabled={saving || (!allowed && !current)}
              onClick={() => { if (!current && allowed) setPendingRole(r) }}
              className={cn(
                "h-7 rounded-md px-2.5 text-xs transition-colors disabled:opacity-50",
                current ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {ROLE_LABEL[r]}
            </button>
          )
        })}
      </div>

      <AlertDialog open={pendingRole !== null} onOpenChange={(o) => { if (!o) setPendingRole(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">Change member role</AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Change <span className="font-medium text-foreground">{label}</span> from{" "}
              <span className="font-medium">{member.role}</span> to <span className="font-medium">{pendingRole}</span>?
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs" disabled={saving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 text-xs"
              disabled={saving}
              onClick={(e) => {
                // Keep the dialog open while the PATCH is in flight; a second
                // click can't fire a duplicate request.
                e.preventDefault()
                if (pendingRole && !saving) void apply(pendingRole)
              }}
            >
              Change role
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function RemoveMember({ label, onConfirm }: { label: string; onConfirm: () => Promise<boolean> }) {
  const [removing, setRemoving] = useState(false)
  return (
    <div className="border-t border-border/60 pt-3">
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button variant="outline" size="sm" className="h-7 gap-1.5 px-2.5 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive" disabled={removing}>
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
    </div>
  )
}
