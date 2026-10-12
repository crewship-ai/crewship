"use client"

import { ChevronDown, Lock } from "lucide-react"

import { cn } from "@/lib/utils"
import { Checkbox } from "@/components/ui/checkbox"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { capabilityAriaLabel, capabilityLock } from "@/components/admin/member-capabilities"
import { CAPABILITY_LABELS, type CapabilityBundle, type CapabilityValue } from "@/lib/capabilities"

import {
  ACCESS_PROFILES, CAPABILITY_COLUMNS, CAPABILITY_SHORT, PROFILE_LABEL, accessProfile, cellState, exceptionCount, profileText,
  type AccessProfile, type CellState, type Member,
} from "./members-model"
import { Dot, RolePill } from "./member-bits"

export interface MatrixGroup {
  key: string
  label: string
  people: Member[]
}

/** Whether this person's row can be changed by the caller at all. */
export function rowLocked(m: Member, currentUserId: string | undefined, canEdit: boolean): boolean {
  return !canEdit || m.role === "OWNER" || m.user.id === currentUserId
}

/**
 * Everyone against every capability, on one screen: the page's main view.
 *
 * A row reads left to right — who, what they manage (role), what they may do
 * (profile, with its exceptions spelled out), then one dot per capability in
 * three groups: Routines · Content · Secrets. A dot is filled when held; an
 * orange ring marks an exception to the profile (held though the profile
 * lacks it, or missing though the profile has it). Secrets are drawn in the
 * destructive tone.
 *
 * A click on a dot grants or revokes that one capability; the profile button
 * replaces the whole set with a profile. The page decides whether to confirm
 * first (`onCell`, `onProfile`). Owners and the caller's own row are locked,
 * as the API demands; without capability access the matrix is just the roster.
 */
export function AccessMatrix({
  groups,
  caps,
  serverCaps,
  currentUserId,
  canEdit,
  selected,
  onSelect,
  onSelectMany,
  onOpen,
  onCell,
  onProfile,
  busy,
}: {
  groups: MatrixGroup[]
  /** user id → capabilities as drafted, or undefined when the caller cannot
   *  read them. */
  caps: Record<string, string[]> | undefined
  /** The same before the draft: a cell that differs is marked unsaved. */
  serverCaps?: Record<string, string[]>
  currentUserId?: string
  canEdit: boolean
  selected: ReadonlySet<string>
  onSelect: (userId: string, on: boolean) => void
  onSelectMany: (userIds: string[], on: boolean) => void
  onOpen: (m: Member) => void
  onCell: (m: Member, cap: CapabilityValue, next: boolean) => void
  onProfile: (m: Member, profile: CapabilityBundle) => void
  busy: boolean
}) {
  const showCaps = Boolean(caps)
  const selectable = groups.flatMap((g) => g.people).filter((m) => !rowLocked(m, currentUserId, canEdit)).map((m) => m.user.id)
  const allOn = selectable.length > 0 && selectable.every((id) => selected.has(id))
  const someOn = selectable.some((id) => selected.has(id))
  const capCount = CAPABILITY_COLUMNS.reduce((n, g) => n + g.caps.length, 0)
  const colSpan = (canEdit ? 1 : 0) + 3 + (showCaps ? capCount : 0)
  const head = "font-mono text-micro font-medium uppercase tracking-[0.08em]"

  return (
    <div className="overflow-x-auto" data-slot="access-matrix">
      <table className="w-full min-w-[880px] border-separate border-spacing-0 text-control" aria-label="People and what they may do">
        <thead>
          {showCaps && (
            <tr>
              <th colSpan={(canEdit ? 1 : 0) + 3} className="bg-surface-subtle" />
              {CAPABILITY_COLUMNS.map((g) => (
                <th key={g.key} colSpan={g.caps.length} scope="colgroup"
                  className={cn("bg-surface-subtle px-1 pt-2 text-center", head, g.danger ? "text-destructive" : "text-primary-hover")}>
                  <span className={cn("block border-b pb-1", g.danger ? "border-destructive/40" : "border-primary/30")}>{g.label}</span>
                </th>
              ))}
            </tr>
          )}
          <tr className="text-left">
            {canEdit && (
              <th scope="col" className="w-9 border-b border-border/60 bg-surface-subtle pl-4 pr-1 py-2">
                <Checkbox aria-label="Select everyone shown" checked={allOn ? true : someOn ? "indeterminate" : false}
                  disabled={selectable.length === 0} onCheckedChange={(v) => onSelectMany(selectable, v === true)} />
              </th>
            )}
            <th scope="col" className={cn("border-b border-border/60 bg-surface-subtle px-3 py-2", head, "text-muted-foreground", !canEdit && "pl-4")}>Person</th>
            <th scope="col" className={cn("border-b border-border/60 bg-surface-subtle px-2 py-2", head, "text-muted-foreground")}>Role</th>
            <th scope="col" className={cn("border-b border-border/60 bg-surface-subtle px-2 py-2", head, "text-muted-foreground")}>{showCaps ? "Access profile" : ""}</th>
            {showCaps && CAPABILITY_COLUMNS.flatMap((g) => g.caps.map((c, i) => (
              <th key={c.cap} scope="col" title={`${CAPABILITY_LABELS[c.cap].en} — ${CAPABILITY_LABELS[c.cap].description}`}
                className={cn("w-[52px] border-b border-border/60 bg-surface-subtle px-0.5 py-2 text-center text-[11px] font-medium",
                  g.danger || c.cap === "skill.create" ? "text-destructive" : "text-muted-foreground",
                  i === 0 && "border-l border-l-border/40")}>
                <span className="sr-only">{CAPABILITY_LABELS[c.cap].en}</span>
                <span aria-hidden>{c.short}</span>
              </th>
            )))}
          </tr>
        </thead>
        {groups.map((g) => (
          <tbody key={g.key}>
            <tr>
              <th colSpan={colSpan} scope="rowgroup" className="border-b border-border/40 bg-card px-4 py-1.5 text-left font-mono text-micro font-medium uppercase tracking-[0.08em] text-muted-foreground">
                {g.label}
                <span className="ml-2 font-normal normal-case tracking-normal text-muted-foreground-soft">{g.people.length}</span>
              </th>
            </tr>
            {g.people.map((m) => (
              <Row key={m.id} m={m} caps={caps} saved={serverCaps?.[m.user.id]} currentUserId={currentUserId} canEdit={canEdit} selected={selected.has(m.user.id)}
                onSelect={onSelect} onOpen={onOpen} onCell={onCell} onProfile={onProfile} busy={busy} />
            ))}
          </tbody>
        ))}
      </table>
    </div>
  )
}

function Row({ m, caps, saved, currentUserId, canEdit, selected, onSelect, onOpen, onCell, onProfile, busy }: {
  m: Member
  caps: Record<string, string[]> | undefined
  saved?: string[]
  currentUserId?: string
  canEdit: boolean
  selected: boolean
  onSelect: (userId: string, on: boolean) => void
  onOpen: (m: Member) => void
  onCell: (m: Member, cap: CapabilityValue, next: boolean) => void
  onProfile: (m: Member, profile: CapabilityBundle) => void
  busy: boolean
}) {
  const label = personLabel(m.user.full_name, m.user.email)
  const granted = caps?.[m.user.id] ?? []
  const isOwner = m.role === "OWNER"
  const isSelf = m.user.id === currentUserId
  const locked = rowLocked(m, currentUserId, canEdit)
  const profile = accessProfile(granted)
  const cell = "border-b border-border/40"

  return (
    <tr data-member={m.user.id} className={cn("group", selected ? "bg-primary/[0.06]" : "hover:bg-[var(--row-hover-bg)]")}>
      {canEdit && (
        <td className={cn(cell, "pl-4 pr-1")}>
          <Checkbox aria-label={`Select ${label}`} checked={selected} disabled={locked} onCheckedChange={(v) => onSelect(m.user.id, v === true)} />
        </td>
      )}
      <th scope="row" className={cn(cell, "px-2 py-1 text-left font-normal", !canEdit && "pl-3")}>
        <button type="button" onClick={() => onOpen(m)} title={`${label} · ${m.user.email}`}
          className="flex w-full min-w-0 max-w-[260px] items-center gap-2 rounded-md px-1 py-1 text-left hover:text-primary-hover">
          <UserAvatar name={m.user.full_name} email={m.user.email} src={m.user.avatar_url} className="h-6 w-6" textClassName="text-[9px]" />
          <span className="min-w-0">
            <span className="block truncate text-control">{label}{isSelf && <span className="ml-1.5 text-micro text-muted-foreground">you</span>}</span>
            {m.user.full_name?.trim() && <span className="block truncate font-mono text-[10.5px] text-muted-foreground">{m.user.email}</span>}
          </span>
        </button>
      </th>
      <td className={cn(cell, "px-2")}><RolePill role={m.role} /></td>
      <td className={cn(cell, "px-2")}>
        {caps && (isOwner ? (
          <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"><Lock className="h-3 w-3" aria-hidden />Owner · fixed</span>
        ) : (
          <ProfileButton profile={profile} label={label} locked={locked} busy={busy} onPick={(p) => onProfile(m, p)} />
        ))}
      </td>
      {caps && CAPABILITY_COLUMNS.flatMap((g) => g.caps.map((c, i) => {
        const state: CellState = isOwner ? (granted.includes(c.cap) ? "profile" : "off") : cellState(profile, granted, c.cap)
        const on = state === "profile" || state === "added"
        const lock = capabilityLock(m, c.cap, currentUserId, canEdit)
        const danger = g.danger || c.cap === "skill.create"
        const unsaved = saved !== undefined && on !== saved.includes(c.cap)
        return (
          <td key={c.cap} data-unsaved={unsaved || undefined} className={cn(cell, "p-0 text-center", i === 0 && "border-l border-l-border/40", unsaved && "bg-[var(--save-bar-bg)]")}>
            <button type="button" role="checkbox" aria-checked={on} data-state={state}
              aria-label={capabilityAriaLabel(c.cap, label, on, lock)}
              title={`${CAPABILITY_LABELS[c.cap].en}${state === "added" ? " — added on top of the profile" : state === "removed" ? " — taken away from the profile" : ""}${unsaved ? " · not saved yet" : ""}`}
              disabled={lock !== null || busy}
              onClick={() => onCell(m, c.cap, !on)}
              className={cn("grid h-10 w-full place-items-center", lock === null ? "cursor-pointer" : "cursor-default")}>
              <Dot state={state} danger={danger} />
            </button>
          </td>
        )
      }))}
    </tr>
  )
}

/** "Builder +Reveal ▾" — the profile in words, with the exceptions in orange,
 *  and a menu that replaces the set with another profile. */
export function ProfileButton({ profile, label, locked, busy, onPick, className }: {
  profile: AccessProfile
  label: string
  locked: boolean
  busy: boolean
  onPick: (p: CapabilityBundle) => void
  className?: string
}) {
  const text = <ProfileText profile={profile} />
  if (locked) return <span className={cn("text-xs", className)}>{text}</span>
  const diff = exceptionCount(profile)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={busy}>
        <button type="button" aria-label={`Access profile for ${label}: ${profileText(profile)}`}
          className={cn("inline-flex h-7 max-w-[220px] items-center gap-1.5 rounded-md border border-border bg-background px-2 text-xs shadow-xs transition-colors hover:bg-accent data-[state=open]:border-primary", className)}>
          <span className="min-w-0 truncate">{text}</span>
          <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72">
        <DropdownMenuLabel className="font-mono text-micro uppercase tracking-[0.08em] text-primary-hover">Access profile · {label}</DropdownMenuLabel>
        {ACCESS_PROFILES.map((p) => {
          const current = p.key === profile.key && diff === 0
          return (
            <DropdownMenuItem key={p.key} onSelect={() => { if (!current) onPick(p.key) }} className="flex-col items-start gap-0.5 py-2">
              <span className="flex w-full items-center gap-2 text-control font-medium">
                {p.label}
                {current && <span className="ml-auto font-mono text-micro text-primary-hover">current</span>}
              </span>
              <span className="text-label text-muted-foreground">{p.summary}</span>
            </DropdownMenuItem>
          )
        })}
        <p className="px-2 pb-1.5 pt-1 text-label text-muted-foreground">
          {diff > 0 ? `Replaces ${diff === 1 ? "the exception" : `all ${diff} exceptions`}. ` : ""}Pages is in no profile — grant it in its column.
        </p>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function ProfileText({ profile }: { profile: AccessProfile }) {
  const diff = [...profile.added.map((c) => `+${CAPABILITY_SHORT[c] ?? c}`), ...profile.removed.map((c) => `−${CAPABILITY_SHORT[c] ?? c}`)]
  return (
    <>
      {PROFILE_LABEL[profile.key]}
      {diff.map((d) => <span key={d} className="ml-1 font-mono text-[10.5px] text-warn">{d}</span>)}
    </>
  )
}
