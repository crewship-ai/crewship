"use client"

import { cn } from "@/lib/utils"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { HIGH_STAKES_CAPABILITIES } from "@/components/admin/member-capabilities"
import { CAPABILITY_LABELS } from "@/lib/capabilities"

import { ROLE_LABEL, extraCapabilities, relativeTime, type Member } from "./members-model"

/** How many extra-capability chips a row shows before folding the rest
 *  into "+N": enough to recognise a person's shape, not so many that the
 *  column wraps. */
const MAX_CHIPS = 2

/**
 * The People view: one row per person — who, role, what they hold beyond
 * the role, when they joined. A row is a button that opens the person in
 * the detail panel; nothing in the row itself mutates, so a stray click on a
 * long roster never changes anyone.
 */
export function MembersList({
  members,
  selectedUserId,
  currentUserId,
  capsByUser,
  showCaps,
  onSelect,
}: {
  members: Member[]
  selectedUserId: string
  currentUserId?: string
  capsByUser: Record<string, string[]> | undefined
  /** False below ADMIN: the capability read is admin-only, so the column is
   *  not drawn at all rather than drawn empty (which would read as "none"). */
  showCaps: boolean
  onSelect: (m: Member) => void
}) {
  if (members.length === 0) {
    return <p className="rounded-card border border-border bg-card px-4 py-6 text-center text-xs text-muted-foreground">No one matches these filters.</p>
  }
  return (
    <ul className="overflow-hidden rounded-card border border-border bg-card" aria-label="People">
      {members.map((m) => {
        const label = personLabel(m.user.full_name, m.user.email)
        const hasName = Boolean((m.user.full_name ?? "").trim())
        const extra = showCaps ? extraCapabilities(capsByUser?.[m.user.id]) : []
        const selected = m.user.id === selectedUserId
        return (
          <li key={m.id} className="border-b border-border/40 last:border-b-0">
            <button
              type="button"
              onClick={() => onSelect(m)}
              aria-pressed={selected}
              aria-label={`Show ${label}`}
              data-member={m.user.id}
              className={cn(
                "row-interactive row-hover flex w-full items-center gap-3 px-3 py-2.5 text-left",
                selected && "row-selected",
              )}
            >
              <UserAvatar name={m.user.full_name} email={m.user.email} src={m.user.avatar_url} className="h-8 w-8" textClassName="text-micro" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-xs font-medium text-foreground">
                  {label}
                  {m.user.id === currentUserId && <span className="ml-1.5 font-normal text-muted-foreground">(you)</span>}
                </span>
                {hasName && <span className="block truncate font-mono text-micro text-muted-foreground-soft">{m.user.email}</span>}
              </span>
              {showCaps && (
                <span className="hidden min-w-0 shrink items-center gap-1 sm:flex" data-slot="member-extra-caps">
                  {extra.slice(0, MAX_CHIPS).map((cap) => (
                    <span key={cap} data-capability={cap}
                      className={cn(
                        "truncate rounded-full px-2 py-0.5 text-micro font-medium",
                        HIGH_STAKES_CAPABILITIES.has(cap) ? "bg-destructive/10 text-destructive" : "bg-primary/10 text-primary-hover",
                      )}>
                      {CAPABILITY_LABELS[cap].en}
                    </span>
                  ))}
                  {extra.length > MAX_CHIPS && (
                    <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 font-mono text-micro text-muted-foreground"
                      title={extra.slice(MAX_CHIPS).map((c) => CAPABILITY_LABELS[c].en).join(", ")}>
                      +{extra.length - MAX_CHIPS}
                    </span>
                  )}
                </span>
              )}
              <span className="shrink-0 rounded-full border border-border bg-muted px-2 py-0.5 text-micro font-medium text-foreground" data-slot="member-role">
                {ROLE_LABEL[m.role] ?? m.role}
              </span>
              {/* The roster carries when someone joined, not when they were
                  last seen — the members endpoint has no activity field. */}
              <span className="hidden w-[52px] shrink-0 text-right font-mono text-micro tabular-nums text-muted-foreground xl:block" title="Joined">
                {relativeTime(m.created_at)}
              </span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}
