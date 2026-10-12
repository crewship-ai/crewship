"use client"

import { useMemo, useState } from "react"
import { CalendarDays, Check, KeyRound, Lock, ShieldAlert, Trash2, UserRound } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import {
  CAPABILITY_GROUPS,
  HIGH_STAKES_CAPABILITIES,
  capabilityAriaLabel,
  capabilityLock,
  useToggleCapability,
} from "@/components/admin/member-capabilities"
import { MemberResourceAccess } from "@/components/features/settings/member-resource-access"
import { CAPABILITY_LABELS, Capability } from "@/lib/capabilities"

import { ASSIGNABLE_ROLES, ROLE_LABEL, ROLE_SUMMARY_BY_ROLE, extraCapabilities, relativeTime, roleChoices, type Member } from "./members-model"

/** A role's colour, the same in the panel, the profile and the matrix, so a
 *  role reads the same wherever it appears. */
export const ROLE_TONE: Record<string, string> = {
  OWNER: "border-purple/30 bg-purple/10 text-purple-hover",
  ADMIN: "border-info/30 bg-info/10 text-info",
  MANAGER: "border-notice/30 bg-notice/10 text-notice",
  MEMBER: "border-gold/30 bg-gold/10 text-gold",
  VIEWER: "border-border bg-surface-subtle text-muted-foreground",
}
export const ROLE_DOT: Record<string, string> = {
  OWNER: "bg-purple", ADMIN: "bg-info", MANAGER: "bg-notice", MEMBER: "bg-gold", VIEWER: "bg-muted-foreground",
}

export function RolePill({ role, className }: { role: string; className?: string }) {
  return (
    <span className={cn("inline-flex h-[22px] items-center rounded-full border px-2 text-micro font-semibold", ROLE_TONE[role] ?? ROLE_TONE.VIEWER, className)}>
      {ROLE_LABEL[role] ?? role}
    </span>
  )
}

function Fact({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-lg border border-border bg-surface-subtle px-2.5 text-xs text-muted-foreground", className)}>{children}</span>
}

/** A card in the Crew links style: a tinted header with a title, a count and a
 *  hint, then rows. */
function Block({ title, count, hint, tone, children, className }: { title: string; count?: React.ReactNode; hint?: string; tone?: "danger"; children: React.ReactNode; className?: string }) {
  return (
    <section className={cn("min-w-0 overflow-hidden rounded-card border bg-card", tone === "danger" ? "border-destructive/30" : "border-border", className)} aria-label={title}>
      <header className={cn("flex flex-wrap items-center gap-x-2 gap-y-0.5 border-b px-4 py-2.5", tone === "danger" ? "border-destructive/20 bg-destructive/5" : "border-border/60 bg-surface-subtle")}>
        <h3 className={cn("text-sm font-semibold", tone === "danger" && "text-destructive")}>{title}</h3>
        {count != null && <span className="font-mono text-micro text-muted-foreground">{count}</span>}
        {hint && <span className="ml-auto text-xs text-muted-foreground">{hint}</span>}
      </header>
      {children}
    </section>
  )
}

/**
 * One person, laid out like a crew in Crew links: a profile card that says who
 * they are, then the two things that decide what they may do — their role and
 * the capabilities granted on top of it — as cards side by side, then the
 * membership actions.
 *
 * Every control keeps the gate it had: role change at MANAGER+ down the
 * ladder; capabilities, resource access and removal at ADMIN+. What a caller
 * may not change reads as text with a lock, not as a dead control.
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
}) {
  const label = personLabel(member.user.full_name, member.user.email)
  const isSelf = currentUserId === member.user.id
  const isOwner = member.role === "OWNER"
  // Capabilities are an ADMIN+ read (the bulk endpoint 403s otherwise).
  const showCaps = isAdmin && Boolean(currentUserId)
  const extras = extraCapabilities(granted)
  const risky = extras.filter((c) => HIGH_STAKES_CAPABILITIES.has(c))

  return (
    <section className="space-y-4" data-slot="member-detail" aria-label={`Member details: ${label}`}>
      <section className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-3 rounded-card border border-border bg-card p-4" aria-label={`${label} profile`} data-slot="member-profile">
        <UserAvatar name={member.user.full_name} email={member.user.email} src={member.user.avatar_url} className="h-12 w-12" textClassName="text-base" />
        <div className="min-w-0">
          <h2 className="flex flex-wrap items-baseline gap-x-2 text-lg font-semibold tracking-[-0.02em]">
            <span className="min-w-0 truncate">{label}</span>
            {isSelf && <span className="text-xs font-normal text-muted-foreground">you</span>}
          </h2>
          {member.user.full_name?.trim() && <p className="truncate font-mono text-micro text-muted-foreground">{member.user.email}</p>}
          <div className="mt-2.5 flex flex-wrap gap-1.5" data-slot="member-facts">
            <RolePill role={member.role} className="h-7 rounded-lg px-2.5 text-xs" />
            <Fact><CalendarDays className="h-3.5 w-3.5" aria-hidden />Joined {relativeTime(member.created_at)}</Fact>
            {showCaps && (
              <Fact>
                <UserRound className="h-3.5 w-3.5" aria-hidden />
                {capsLoading ? "…" : extras.length === 0 ? "No extra capabilities" : `${extras.length} extra ${extras.length === 1 ? "capability" : "capabilities"}`}
              </Fact>
            )}
            {showCaps && risky.length > 0 && (
              <Fact className="border-destructive/30 bg-destructive/5 text-destructive">
                <ShieldAlert className="h-3.5 w-3.5" aria-hidden />{risky.length} high-stakes
              </Fact>
            )}
          </div>
        </div>
      </section>

      <div className={cn("grid items-start gap-4", showCaps && "xl:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]")}>
        <Block title="Role" hint={isOwner ? "fixed for the owner" : "what the tier grants"}>
          <RoleControl member={member} callerRole={callerRole} isSelf={isSelf} label={label} onChangeRole={onChangeRole} />
        </Block>
        {showCaps && (
          <Block title="Capabilities" count={capsLoading ? "…" : extras.length} hint="on top of the role · applies at once">
            <CapabilitySwitches member={member} workspaceId={workspaceId} currentUserId={currentUserId as string} granted={granted} isLoading={capsLoading} label={label} />
          </Block>
        )}
      </div>

      {showCaps && (
        <Block title="Resource access" hint="crews, routines and pages this person reaches">
          <div className="px-4 pb-4"><MemberResourceAccess workspaceId={workspaceId} memberId={member.id} label={label} role={member.role} /></div>
        </Block>
      )}

      {isAdmin && !isOwner && !isSelf && (
        <Block title="Remove from workspace" tone="danger">
          <div className="flex flex-wrap items-center gap-3 px-4 py-3">
            <p className="min-w-0 flex-1 basis-56 text-xs text-muted-foreground">{label} loses access to every crew, routine and page in this workspace. This cannot be undone.</p>
            <RemoveMember label={label} onConfirm={() => onRemove(member)} />
          </div>
        </Block>
      )}
    </section>
  )
}

/**
 * The roles as a list of choices, each with what it grants, the current one
 * marked — choosing a role is choosing between those sentences, so they sit
 * next to the radio rather than behind a help popover. A confirm dialog gates
 * the change: a role is the widest grant on the page.
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
  const roles = [...ASSIGNABLE_ROLES].reverse()

  // Nothing this caller may change: say what the role is and means, with no
  // radio that only pretends to be one.
  if (choices.length === 0) {
    return (
      <div data-slot="member-role-readonly">
        <div className="flex items-start gap-3 px-4 py-3">
          <span className={cn("mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full", ROLE_DOT[member.role])} aria-hidden />
          <span className="min-w-0 flex-1">
            <span className="flex items-center gap-2 text-control font-medium">
              {ROLE_LABEL[member.role] ?? member.role}
              <Lock className="h-3 w-3 text-muted-foreground" aria-hidden />
            </span>
            <span className="mt-0.5 block text-label text-muted-foreground">{ROLE_SUMMARY_BY_ROLE[member.role] ?? "Permissions are defined by this role."}</span>
          </span>
        </div>
        <p className="border-t border-border/40 px-4 py-2 text-label text-muted-foreground">
          {member.role === "OWNER"
            ? "The owner's role and capabilities are immutable — ownership moves through its own transfer."
            : isSelf ? "You cannot change your own role." : "Your role cannot change this person's role."}
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
      <div role="radiogroup" aria-label={`Role for ${label}`} className="flex flex-col">
        {roles.map((r) => {
          const current = member.role === r
          const allowed = choices.includes(r)
          const locked = !current && !allowed
          return (
            <button
              key={r}
              type="button"
              role="radio"
              aria-checked={current}
              aria-label={ROLE_LABEL[r] ?? r}
              disabled={saving || locked}
              onClick={() => { if (!current && allowed) setPendingRole(r) }}
              className={cn(
                "kit-tap flex items-start gap-3 border-b border-border/40 px-4 py-2.5 text-left transition-colors last:border-b-0",
                current ? "bg-primary/[0.06]" : allowed ? "hover:bg-[var(--row-hover-bg)]" : "cursor-default",
              )}
            >
              <span aria-hidden className={cn("mt-0.5 grid h-4 w-4 shrink-0 place-items-center rounded-full border", current ? "border-primary bg-primary text-primary-foreground" : "border-control-border")}>
                {current && <Check className="h-2.5 w-2.5" strokeWidth={3} />}
              </span>
              <span className="min-w-0 flex-1">
                <span className={cn("flex items-center gap-2 text-control", locked && "text-muted-foreground")}>
                  <span className={cn("h-1.5 w-1.5 rounded-full", ROLE_DOT[r])} aria-hidden />
                  <span className={cn(current && "font-medium")}>{ROLE_LABEL[r] ?? r}</span>
                  {locked && <Lock className="h-3 w-3 text-muted-foreground-soft" aria-label="You cannot assign this role" />}
                </span>
                <span className="mt-0.5 block text-label leading-snug text-muted-foreground">{ROLE_SUMMARY_BY_ROLE[r] ?? ""}</span>
              </span>
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

/**
 * The capabilities as switch rows grouped Routines / Content / Credentials,
 * each group a labelled band inside the card; the credentials band is in the
 * destructive tone. Same toggle and locks as the matrix (useToggleCapability,
 * capabilityLock), so the two views can never disagree.
 */
function CapabilitySwitches({ member, workspaceId, currentUserId, granted, isLoading, label }: {
  member: Member; workspaceId: string; currentUserId: string; granted: string[]; isLoading: boolean; label: string
}) {
  const grantedSet = useMemo(() => new Set(granted), [granted])
  const mutation = useToggleCapability(workspaceId)
  return (
    <div>
      {CAPABILITY_GROUPS.map((group) => (
        <section key={group.key} aria-label={group.label}>
          <h4 className={cn("flex items-center gap-1.5 border-b border-border/40 px-4 pb-1 pt-2.5 font-mono text-[10.5px] font-medium uppercase tracking-[0.08em]", group.highStakes ? "text-destructive" : "text-muted-foreground-soft")}>
            {group.highStakes && <KeyRound className="h-3 w-3" aria-hidden />}
            {group.label}
            {group.highStakes && <span className="normal-case tracking-normal">· high-stakes</span>}
          </h4>
          {group.caps.map((cap) => {
            const isGranted = cap === Capability.Chat || grantedSet.has(cap)
            const lock = capabilityLock(member, cap, currentUserId)
            const meta = CAPABILITY_LABELS[cap]
            return (
              <button
                key={cap}
                type="button"
                role="switch"
                aria-checked={isGranted}
                aria-label={capabilityAriaLabel(cap, label, isGranted, lock)}
                aria-disabled={lock !== null}
                disabled={lock !== null || isLoading || mutation.isPending}
                data-capability={cap}
                onClick={() => mutation.mutate({ userId: member.user.id, cap, next: !isGranted })}
                className={cn(
                  "kit-tap flex w-full items-center gap-3 border-b border-border/40 px-4 py-2 text-left transition-colors last:border-b-0",
                  lock ? "cursor-default" : "hover:bg-[var(--row-hover-bg)]",
                )}
              >
                <span className="min-w-0 flex-1">
                  <span className={cn("block text-control leading-tight", HIGH_STAKES_CAPABILITIES.has(cap) && "text-destructive")}>{meta.en}</span>
                  <span className="mt-0.5 block text-label leading-snug text-muted-foreground">{lock === "role" ? "Comes with every role." : meta.description}</span>
                </span>
                <SwitchTrack on={isGranted} locked={lock !== null} />
              </button>
            )
          })}
        </section>
      ))}
    </div>
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
