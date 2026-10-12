"use client"

import { useState } from "react"
import { CalendarDays, Lock, Trash2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { capabilityAriaLabel, capabilityLock } from "@/components/admin/member-capabilities"
import { MemberResourceAccess } from "@/components/features/settings/member-resource-access"
import { CAPABILITY_LABELS, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"

import {
  ACCESS_PROFILES, ASSIGNABLE_ROLES, CAPABILITY_COLUMNS, CAPABILITY_SHORT, ROLE_LABEL, ROLE_SUMMARY_BY_ROLE,
  accessProfile, cellState, exceptionCount, relativeTime, roleChoices, type Member,
} from "./members-model"
import { Dot, RolePill } from "./member-bits"

export { ROLE_DOT, ROLE_TONE, RolePill } from "./member-bits"

function Section({ title, hint, children, tone }: { title: string; hint?: React.ReactNode; children: React.ReactNode; tone?: "danger" }) {
  return (
    <section aria-label={title} className="space-y-2">
      <h3 className={cn("flex items-baseline gap-2 font-mono text-micro font-medium uppercase tracking-[0.08em]", tone === "danger" ? "text-destructive" : "text-primary-hover")}>
        {title}
        {hint && <span className="ml-auto font-sans text-label normal-case tracking-normal text-muted-foreground">{hint}</span>}
      </h3>
      {children}
    </section>
  )
}

/**
 * One person, in the panel beside the matrix (full screen on a phone). Top
 * to bottom it answers the page's two questions in order — what they manage
 * (role) and what they may do (profile, then each capability, the exceptions
 * marked) — then where they reach and the way out.
 *
 * Writes go back through the page (`onCell`, `onProfile`) so a capability
 * changed here is confirmed exactly like the same change in the matrix.
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
}) {
  const label = personLabel(member.user.full_name, member.user.email)
  const isSelf = currentUserId === member.user.id
  const isOwner = member.role === "OWNER"
  const showCaps = isAdmin && Boolean(currentUserId)
  const profile = accessProfile(granted)
  const diff = exceptionCount(profile)
  const locked = isOwner || isSelf || !isAdmin

  return (
    <div className="space-y-6" data-slot="member-detail" aria-label={`Member details: ${label}`} role="region">
      <div className="flex items-center gap-3" data-slot="member-profile">
        <UserAvatar name={member.user.full_name} email={member.user.email} src={member.user.avatar_url} className="h-11 w-11" textClassName="text-sm" />
        <div className="min-w-0 flex-1">
          <p className="flex items-baseline gap-2 truncate text-base font-semibold tracking-[-0.01em]">
            <span className="truncate">{label}</span>
            {isSelf && <span className="text-xs font-normal text-muted-foreground">you</span>}
          </p>
          <p className="flex flex-wrap items-center gap-x-2 font-mono text-micro text-muted-foreground">
            {member.user.full_name?.trim() && <span className="truncate">{member.user.email}</span>}
            <span className="inline-flex items-center gap-1"><CalendarDays className="h-3 w-3" aria-hidden />joined {relativeTime(member.created_at)}</span>
          </p>
        </div>
      </div>

      <Section title="Role · what they manage">
        <RoleControl member={member} callerRole={callerRole} isSelf={isSelf} label={label} onChangeRole={onChangeRole} />
      </Section>

      {showCaps && (
        <Section title="Access profile · what they may do" hint={capsLoading ? "…" : undefined}>
          {isOwner ? (
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground"><Lock className="h-3 w-3" aria-hidden />An owner&apos;s capabilities are fixed — ownership moves through its own transfer.</p>
          ) : (
            <>
              <div role="radiogroup" aria-label={`Access profile for ${label}`} className="grid grid-cols-3 gap-1.5">
                {ACCESS_PROFILES.map((p) => {
                  const current = p.key === profile.key
                  return (
                    <button key={p.key} type="button" role="radio" aria-checked={current} title={p.summary}
                      disabled={locked || busy}
                      onClick={() => { if (!(current && diff === 0)) onProfile(member, p.key) }}
                      className={cn("rounded-lg border px-2.5 py-2 text-left text-xs font-medium transition-colors",
                        current ? "border-primary bg-primary/[0.08] text-foreground ring-1 ring-primary" : "border-border text-muted-foreground hover:bg-accent hover:text-foreground",
                        locked && "cursor-default")}>
                      {p.label}
                    </button>
                  )
                })}
              </div>
              <p className={cn("text-label", diff ? "text-warn" : "text-muted-foreground")} data-slot="member-exceptions">
                {diff === 0
                  ? ACCESS_PROFILES.find((p) => p.key === profile.key)?.summary
                  : `${diff} ${diff === 1 ? "exception" : "exceptions"}: ${[
                    ...profile.added.map((c) => `+ ${CAPABILITY_SHORT[c] ?? c}`),
                    ...profile.removed.map((c) => `− ${CAPABILITY_SHORT[c] ?? c}`),
                  ].join(", ")}`}
              </p>
            </>
          )}
        </Section>
      )}

      {showCaps && (
        <Section title="Capabilities" hint={isOwner || isSelf ? undefined : "save when you are done"}>
          <div className="overflow-hidden rounded-lg border border-border">
            {CAPABILITY_COLUMNS.map((g) => (
              <div key={g.key} role="group" aria-label={g.label}>
                <p className={cn("border-b border-border/50 bg-surface-subtle px-3 py-1 font-mono text-[10px] uppercase tracking-[0.08em]", g.danger ? "text-destructive" : "text-muted-foreground")}>{g.label}</p>
                {g.caps.map(({ cap }) => {
                  const state = isOwner ? (granted.includes(cap) ? "profile" : "off") : cellState(profile, granted, cap)
                  const on = state === "profile" || state === "added"
                  const lock = capabilityLock(member, cap, currentUserId, isAdmin)
                  const danger = g.danger || cap === "skill.create"
                  const unsaved = serverGranted !== undefined && on !== serverGranted.includes(cap)
                  return (
                    <button key={cap} type="button" role="switch" aria-checked={on} data-capability={cap}
                      aria-label={capabilityAriaLabel(cap, label, on, lock)} title={CAPABILITY_LABELS[cap].description}
                      disabled={lock !== null || busy || capsLoading}
                      onClick={() => onCell(member, cap, !on)}
                      data-unsaved={unsaved || undefined}
                      className={cn("flex w-full items-center gap-3 border-b border-border/40 px-3 py-2 text-left last:border-b-0", lock === null && "hover:bg-[var(--row-hover-bg)]", unsaved && "bg-[var(--save-bar-bg)]")}>
                      <Dot state={state} danger={danger} />
                      <span className={cn("min-w-0 flex-1 text-control", danger && "text-destructive")}>{CAPABILITY_LABELS[cap].en}</span>
                      {unsaved ? <span className="font-mono text-[10px] text-[var(--save-bar-fg)]">unsaved</span>
                        : (state === "added" || state === "removed") && <span className="font-mono text-[10px] text-warn">exception</span>}
                      <SwitchTrack on={on} locked={lock !== null} />
                    </button>
                  )
                })}
              </div>
            ))}
          </div>
        </Section>
      )}

      {showCaps && (
        <Section title="Resource access">
          <MemberResourceAccess workspaceId={workspaceId} memberId={member.id} label={label} role={member.role} />
        </Section>
      )}

      {isAdmin && !isOwner && !isSelf && (
        <Section title="Remove from workspace" tone="danger">
          <div className="flex flex-wrap items-center gap-3">
            <p className="min-w-0 flex-1 basis-48 text-label text-muted-foreground">{label} loses access to every crew, routine and page here. This cannot be undone.</p>
            <RemoveMember label={label} onConfirm={() => onRemove(member)} />
          </div>
        </Section>
      )}
    </div>
  )
}

/**
 * The role as a segmented control, the current role's meaning under it. A
 * confirm dialog gates the change: a role is the widest grant on the page.
 */
function RoleControl({ member, callerRole, isSelf, label, onChangeRole }: {
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
      <div role="radiogroup" aria-label={`Role for ${label}`} className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
        {ASSIGNABLE_ROLES.map((r) => {
          const current = member.role === r
          const allowed = choices.includes(r)
          return (
            <button key={r} type="button" role="radio" aria-checked={current} aria-label={ROLE_LABEL[r] ?? r}
              disabled={saving || (!current && !allowed)}
              onClick={() => { if (!current && allowed) setPendingRole(r) }}
              className={cn("h-7 rounded-md px-3 text-xs font-medium transition-colors",
                current ? "bg-primary text-primary-foreground shadow-xs" : allowed ? "text-muted-foreground hover:bg-card hover:text-foreground" : "cursor-not-allowed text-muted-foreground-soft")}>
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
              {" "}Their capabilities stay as they are.
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

function SwitchTrack({ on, locked }: { on: boolean; locked: boolean }) {
  return (
    <span aria-hidden="true" className={cn("relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors", on ? "bg-primary" : "bg-input", locked && "opacity-60")}>
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
        <Button variant="outline" size="sm" className="h-8 gap-1.5 px-3 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive" disabled={removing}>
          <Trash2 className="h-3.5 w-3.5" />
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
