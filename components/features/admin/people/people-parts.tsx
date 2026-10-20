"use client"

import * as React from "react"
import { toast } from "@/lib/toast"
import { Copy, Link2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { UserAvatar } from "@/components/ui/user-avatar"
import { daysUntil, displayName, personStatus, ROLES, STATUS_LABEL, type Person, type PersonStatus, type Role } from "./people-model"
import { nativeSelect } from "@/components/features/settings/shared"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import { ROLE_ICON } from "@/components/features/settings/members/member-bits"

export const roleLabel = (r: string) => r.charAt(0) + r.slice(1).toLowerCase()

/** The one control for a role, the same width everywhere it appears. */
export function RoleSelect({ value, onChange, disabled, label, allowNone, className }: {
  value: Role | null
  onChange: (r: Role | null) => void
  disabled?: boolean
  label: string
  /** Offer "No access" — the matrix and the Add person dialog use it. */
  allowNone?: boolean
  className?: string
}) {
  return (
    <select
      aria-label={label}
      value={value ?? ""}
      disabled={disabled}
      onChange={(e) => onChange((e.target.value || null) as Role | null)}
      className={cn(nativeSelect, "w-36", className)}
    >
      {allowNone && <option value="">No access</option>}
      {ROLES.map((r) => <option key={r} value={r}>{roleLabel(r)}</option>)}
    </select>
  )
}

/**
 * A role as a row of icon buttons — for the dialogs, where the choice is the
 * point; tables keep the compact RoleSelect. Owner is never offered: a
 * workspace gets its owner when it is created or handed over.
 */
export function RoleSegmented({ value, onChange, label, allowNone, disabled }: {
  value: Role | null
  onChange: (r: Role | null) => void
  label: string
  allowNone?: boolean
  disabled?: boolean
}) {
  const options: (Role | null)[] = [...(allowNone ? [null] : []), "ADMIN", "MANAGER", "MEMBER", "VIEWER"]
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
      {options.map((r) => {
        const Icon = r ? ROLE_ICON[r] : null
        const on = value === r
        return (
          <button key={r ?? "none"} type="button" role="radio" aria-checked={on} disabled={disabled} onClick={() => onChange(r)}
            className={cn("inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs transition-colors disabled:opacity-50 coarse:h-9",
              on ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}>
            {Icon && <Icon className="h-3 w-3" aria-hidden />}{r ? roleLabel(r) : "None"}
          </button>
        )
      })}
    </div>
  )
}

const STATUS_TONE: Record<PersonStatus, StatusTone> = {
  active: "success",
  setup: "blue",
  locked: "warn",
  suspended: "muted",
}

export function StatusChip({ person, now }: { person: Person; now?: number }) {
  const s = personStatus(person, now)
  return <StatusPill tone={STATUS_TONE[s]} label={STATUS_LABEL[s]} />
}

export function PersonAvatar({ person, className }: { person: Person; className?: string }) {
  return <UserAvatar name={person.full_name} email={person.email} src={person.avatar_url} className={cn("h-7 w-7", className)} />
}

export function PersonLabel({ person, sub }: { person: Person; sub?: React.ReactNode }) {
  return (
    <span className="flex min-w-0 items-center gap-2.5">
      <PersonAvatar person={person} />
      <span className="min-w-0">
        <span className="block truncate text-control">{displayName(person)}</span>
        <span className="block truncate text-label text-muted-foreground">{sub ?? person.email}</span>
      </span>
    </span>
  )
}

/** A freshly issued setup link: shown once, copied, never stored. */
export function SetupLinkBox({ url, expiresAt, onDone }: { url: string; expiresAt: string; onDone?: () => void }) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url)
      toast.success("Setup link copied")
    } catch {
      toast.error("Copy it by hand; the clipboard is not available here")
    }
  }
  return (
    <div className="mx-4 my-3 flex flex-wrap items-center gap-2 rounded-lg border border-dashed border-primary/45 bg-primary/[0.06] px-3 py-2" data-slot="setup-link">
      <Link2 className="h-3.5 w-3.5 shrink-0 text-primary-hover" />
      <Input readOnly value={url} aria-label="Setup link" onFocus={(e) => e.currentTarget.select()}
        className="h-7 min-w-0 flex-1 border-none bg-transparent px-0 font-mono text-micro text-primary-hover shadow-none focus-visible:ring-0" />
      <span className="text-label text-muted-foreground">valid {daysUntil(expiresAt)} days · shown once</span>
      <Button size="xs" variant="outline" onClick={copy}><Copy />Copy</Button>
      {onDone && <Button size="xs" variant="ghost" onClick={onDone}>Done</Button>}
    </div>
  )
}

