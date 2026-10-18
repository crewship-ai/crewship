"use client"

import { useState } from "react"
import { ChevronRight, Copy, Lock, Mail, TriangleAlert } from "lucide-react"
import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { StatusPill } from "@/components/ui/status-pill"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"

import { formatSettingsDate } from "@/components/features/settings/shared"
import { ROLE_LABEL, aboveRole, type Member } from "./members-model"
import { AreaChips, AreaIcons, RestrictedPill, RolePill } from "./member-bits"
import { daysUntil, expiryText, reissueInvitation, revokeInvitation, type Invitation } from "./use-invitations"

export interface PeopleGroup {
  key: string
  label: string
  people: Member[]
}

/** "3 Sep", with the year once it is not this one (v67: day first, English). */
export function joinedDate(iso: string): string {
  return formatSettingsDate(iso, { year: "if-not-current" })
}

// v67 `.m-grp`: a small sans group row ("OWNERS · 1"), and a sans sentence-case head.
const groupHead = "sticky top-0 z-[1] border-b border-border/60 bg-surface-subtle px-4 py-1.5 text-left text-micro font-semibold uppercase tracking-[0.04em] text-muted-foreground"
const cell = "border-b border-border/60 px-3 py-2.5 align-middle first:pl-4 last:pr-4"
const head = "border-b border-border/60 bg-surface-subtle px-3 py-2 text-left text-label font-medium text-muted-foreground first:pl-4 last:pr-4"

/**
 * The roster on a desktop: people grouped (by role, or A–Z as one group), one
 * row each — who, their role, what they may do as area chips, when they
 * joined. The name opens the person; a ticked box joins the selection bar.
 * `caps` is the drafted set; a row whose draft differs from the server is
 * marked unsaved.
 */
export function PeopleTable({
  groups, caps, serverCaps, currentUserId, canSelect, selected, openedId, onSelect, onSelectMany, onOpen,
}: {
  groups: PeopleGroup[]
  caps: Record<string, string[]> | undefined
  serverCaps: Record<string, string[]> | undefined
  currentUserId?: string
  canSelect: boolean
  selected: Set<string>
  openedId: string
  onSelect: (userId: string, on: boolean) => void
  onSelectMany: (userIds: string[], on: boolean) => void
  onOpen: (m: Member) => void
}) {
  const selectable = (m: Member) => canSelect && m.role !== "OWNER" && m.user.id !== currentUserId
  const all = groups.flatMap((g) => g.people).filter(selectable)
  const allOn = all.length > 0 && all.every((m) => selected.has(m.user.id))
  const cols = (canSelect ? 1 : 0) + 3 + (caps ? 1 : 0) + 1

  return (
    <table className="w-full table-fixed border-separate border-spacing-0 text-control" aria-label="People and what they may do">
      <thead>
        <tr>
          {canSelect && (
            <th className={cn(head, "w-10")}>
              <input type="checkbox" aria-label="Select everyone shown" checked={allOn} disabled={all.length === 0}
                onChange={(e) => onSelectMany(all.map((m) => m.user.id), e.target.checked)} className="size-3.5 accent-[var(--primary)]" />
            </th>
          )}
          <th className={cn(head, caps ? "w-[32%]" : "w-1/2")}>Person</th>
          <th className={cn(head, "w-32")}>Role</th>
          {caps && <th className={head}>Can do</th>}
          <th className={cn(head, "w-24")}>Joined</th>
          <th className={cn(head, "w-10")}><span className="sr-only">Open</span></th>
        </tr>
      </thead>
      {groups.map((g) => (
        <tbody key={g.key}>
          <tr>
            <th scope="rowgroup" colSpan={cols} className={groupHead}>
              {g.label}<span className="ml-1.5 font-normal tabular-nums text-muted-foreground-soft">· {g.people.length}</span>
            </th>
          </tr>
          {g.people.map((m) => {
            const label = personLabel(m.user.full_name, m.user.email)
            const isSelf = m.user.id === currentUserId
            const granted = caps?.[m.user.id]
            const unsaved = Boolean(caps && serverCaps) && JSON.stringify([...(granted ?? [])].sort()) !== JSON.stringify([...(serverCaps?.[m.user.id] ?? [])].sort())
            const above = caps ? aboveRole(m, granted) : []
            return (
              <tr key={m.id} data-member={m.user.id} data-unsaved={unsaved || undefined}
                onClick={() => onOpen(m)}
                className={cn("group cursor-pointer transition-colors hover:bg-[var(--row-hover-bg)]", openedId === m.user.id && "row-selected")}>
                {canSelect && (
                  <td className={cell} onClick={(e) => e.stopPropagation()}>
                    <input type="checkbox" aria-label={`Select ${label}`} checked={selected.has(m.user.id)} disabled={!selectable(m)}
                      onChange={(e) => onSelect(m.user.id, e.target.checked)} className="size-3.5 accent-[var(--primary)] disabled:opacity-30" />
                  </td>
                )}
                <td className={cell}>
                  <button type="button" onClick={(e) => { e.stopPropagation(); onOpen(m) }} title={`${label} · ${m.user.email}`}
                    className="flex w-full min-w-0 items-center gap-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded-md">
                    <UserAvatar name={m.user.full_name} email={m.user.email} src={m.user.avatar_url} className="h-7 w-7" textClassName="text-micro" />
                    <span className="min-w-0">
                      <span className="flex items-center gap-1.5 truncate text-control text-foreground">
                        <span className="truncate">{label}</span>
                        {isSelf && <span className="text-micro text-muted-foreground">you</span>}
                        {unsaved && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--save-bar-fg)]" title="Unsaved" />}
                      </span>
                      {m.user.full_name?.trim() && <span className="block truncate text-micro text-muted-foreground-soft">{m.user.email}</span>}
                    </span>
                  </button>
                </td>
                <td className={cell}>
                  <span className="inline-flex items-center gap-1.5">
                    <RolePill role={m.role} />
                    <RestrictedPill mode={m.access_mode} />
                    {above.length > 0 && (
                      <span className="text-warn" title={`Above their role: a ${ROLE_LABEL[m.role]} who holds high-stakes access`}>
                        <TriangleAlert className="h-3.5 w-3.5" aria-label="Above their role" />
                      </span>
                    )}
                  </span>
                </td>
                {caps && <td className={cn(cell, "overflow-hidden")}><AreaChips granted={granted} /></td>}
                <td className={cn(cell, "text-label tabular-nums text-muted-foreground")}>{joinedDate(m.created_at)}</td>
                <td className={cn(cell, "text-muted-foreground-soft group-hover:text-foreground")}><ChevronRight className="h-3.5 w-3.5" aria-hidden /></td>
              </tr>
            )
          })}
        </tbody>
      ))}
    </table>
  )
}

/** The phone's roster: one tappable row per person — role and what they may
 *  do as icons under the name. */
export function PeopleList({ groups, caps, currentUserId, onOpen }: {
  groups: PeopleGroup[]
  caps: Record<string, string[]> | undefined
  currentUserId?: string
  onOpen: (m: Member) => void
}) {
  return (
    <div data-slot="members-list">
      {groups.map((g) => (
        <section key={g.key} aria-label={g.label}>
          <h3 className="border-b border-border/60 bg-surface-subtle px-4 py-1.5 text-micro font-semibold uppercase tracking-[0.04em] text-muted-foreground">
            {g.label}<span className="ml-2 font-normal text-muted-foreground-soft">{g.people.length}</span>
          </h3>
          {g.people.map((m) => {
            const label = personLabel(m.user.full_name, m.user.email)
            const above = caps ? aboveRole(m, caps[m.user.id]) : []
            return (
              <button key={m.id} type="button" onClick={() => onOpen(m)} title={`${label} · ${m.user.email}`} data-member={m.user.id}
                className="flex min-h-[60px] w-full items-center gap-3 border-b border-border/60 px-4 py-2 text-left active:bg-[var(--row-hover-bg)]">
                <UserAvatar name={m.user.full_name} email={m.user.email} src={m.user.avatar_url} className="h-9 w-9" textClassName="text-label" />
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1.5 text-body">
                    <span className="truncate">{label}</span>
                    {m.user.id === currentUserId && <span className="text-micro text-muted-foreground">you</span>}
                    {above.length > 0 && <TriangleAlert className="h-3.5 w-3.5 shrink-0 text-warn" aria-label="Above their role" />}
                  </span>
                  <span className="mt-0.5 flex items-center gap-2 text-label text-muted-foreground">
                    {ROLE_LABEL[m.role] ?? m.role}
                    {m.access_mode === "restricted" && <><span className="text-muted-foreground-soft">·</span>Restricted</>}
                    {caps && <><span className="text-muted-foreground-soft">·</span><AreaIcons granted={caps[m.user.id]} /></>}
                  </span>
                </span>
                <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
              </button>
            )
          })}
        </section>
      ))}
    </div>
  )
}

/** Pending invitations: who, the role they will get, who sent it and when it
 *  runs out. */
/**
 * Pending invitations. With `manage` (OWNER/ADMIN, as the server requires)
 * each row offers Reissue — a new link, shown once, valid 7 days; the old one
 * stops working — and Revoke after a confirmation. An ADMIN invitation is the
 * owner's to manage: for anyone else the row says so instead.
 */
export function InvitationList({ invitations, workspaceId, manage = false, callerRole, onChanged }: {
  invitations: Invitation[]
  workspaceId?: string
  manage?: boolean
  callerRole?: string | null
  /** Re-read the list after a reissue or revoke. */
  onChanged?: () => void
}) {
  const [links, setLinks] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<Invitation | null>(null)

  if (invitations.length === 0) {
    return <p className="px-4 py-6 text-center text-label text-muted-foreground">No pending invitations.</p>
  }

  async function reissue(inv: Invitation) {
    if (!workspaceId) return
    setBusy(inv.id)
    try {
      const { link } = await reissueInvitation(workspaceId, inv.id)
      setLinks((l) => ({ ...l, [inv.id]: link }))
      toast.success(`New link for ${inv.email}`, { description: "Valid 7 days. The old link no longer works." })
      onChanged?.()
    } catch (e) {
      toast.error(`Couldn’t reissue the invitation for ${inv.email}`, { description: e instanceof Error ? e.message : undefined })
    } finally {
      setBusy(null)
    }
  }

  async function revoke() {
    if (!workspaceId || !revoking) return
    try {
      await revokeInvitation(workspaceId, revoking.id)
    } catch (e) {
      toast.error(`Couldn’t revoke the invitation for ${revoking.email}`, { description: e instanceof Error ? e.message : undefined })
      throw e
    }
    toast.success(`Invitation for ${revoking.email} revoked`)
    onChanged?.()
  }

  return (
    <>
    <ul data-slot="members-invitations">
      {invitations.map((inv) => {
        const days = daysUntil(inv.expires_at)
        const inviter = inv.inviter ? personLabel(inv.inviter.full_name ?? null, inv.inviter.email) : null
        const ownersOnly = inv.role === "ADMIN" && callerRole !== "OWNER"
        const link = links[inv.id]
        return (
          <li key={inv.id} className="border-b border-border/60 px-4 py-2.5 last:border-b-0">
            <div className="flex flex-wrap items-center gap-3">
              <span className="grid h-7 w-7 shrink-0 place-items-center rounded-full border border-dashed border-border text-muted-foreground"><Mail className="h-3.5 w-3.5" aria-hidden /></span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-control">{inv.email}</span>
                <span className="block truncate text-micro text-muted-foreground-soft">{inviter ? `invited by ${inviter} · ` : ""}{joinedDate(inv.created_at)}</span>
              </span>
              <RolePill role={inv.role} />
              <StatusPill tone={days < 0 ? "danger" : days <= 2 ? "warn" : "muted"} label={expiryText(inv.expires_at)} />
              {manage && (ownersOnly ? (
                <span className="inline-flex items-center gap-1 text-label text-muted-foreground">
                  <Lock className="h-3 w-3" aria-hidden />Only the owner manages an Admin invitation
                </span>
              ) : (
                <span className="flex items-center gap-1">
                  <Button variant="outline" size="sm" disabled={busy === inv.id} aria-label={`Reissue ${inv.email}`} onClick={() => void reissue(inv)}>
                    {days < 0 ? "Invite again" : "Reissue"}
                  </Button>
                  <Button variant="ghost" size="sm" className="text-destructive hover:bg-destructive/10 hover:text-destructive" aria-label={`Revoke ${inv.email}`} onClick={() => setRevoking(inv)}>
                    Revoke
                  </Button>
                </span>
              ))}
            </div>
            {link && (
              <div className="mt-2 flex flex-wrap items-center gap-2 pl-10" data-slot="invitation-link">
                <input readOnly value={link} aria-label={`New invitation link for ${inv.email}`} onFocus={(e) => e.currentTarget.select()}
                  className="h-8 min-w-0 flex-1 rounded-md border border-control-border bg-surface-subtle px-2.5 font-mono text-label text-foreground" />
                <Button variant="outline" size="sm" className="gap-1" onClick={() => {
                  navigator.clipboard.writeText(link).then(() => toast.success("Link copied"), () => toast.error("Couldn’t copy the link", { duration: 6000 }))
                }}>
                  <Copy className="h-3 w-3" aria-hidden />Copy
                </Button>
                <span className="w-full text-label text-muted-foreground">Copy it now — it won’t be shown again. Valid 7 days.</span>
              </div>
            )}
          </li>
        )
      })}
    </ul>
    <ConfirmDialog
      open={revoking !== null}
      onOpenChange={(o) => { if (!o) setRevoking(null) }}
      title={`Revoke the invitation for ${revoking?.email ?? ""}?`}
      description="The link stops working at once."
      consequences={[
        { tone: "lost", text: "They can no longer join with the link they have" },
        { tone: "kept", text: "You can invite the same address again later" },
      ]}
      confirmLabel="Revoke invitation"
      destructive
      onConfirm={revoke}
    />
    </>
  )
}
