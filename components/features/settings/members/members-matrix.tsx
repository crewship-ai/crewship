"use client"

import { Check, Lock } from "lucide-react"

import { cn } from "@/lib/utils"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import {
  HIGH_STAKES_CAPABILITIES,
  capabilityAriaLabel,
  capabilityLock,
  useToggleCapability,
} from "@/components/admin/member-capabilities"
import { ALL_CAPABILITIES, CAPABILITY_LABELS } from "@/lib/capabilities"

import { ROLE_LABEL, capabilityState, type Member } from "./members-model"

/**
 * People × capabilities, for the one question a per-person panel answers
 * badly: "who else can do this?". A cell is one of three things, each drawn
 * differently so the grid reads without a hover:
 *
 *   - comes with the role (chat — every role has it): a lock, never a click;
 *   - granted to this person: a filled check;
 *   - off: an empty box.
 *
 * A click sends the same single-grant PATCH as the switch in the person's
 * panel (useToggleCapability). OWNER rows and the caller's own row stay
 * locked, as everywhere; a caller who cannot edit sees the grid read-only.
 */
export function MembersMatrix({
  members,
  workspaceId,
  currentUserId,
  capsByUser,
  canEdit,
  onSelect,
}: {
  members: Member[]
  workspaceId: string
  currentUserId?: string
  capsByUser: Record<string, string[]> | undefined
  canEdit: boolean
  onSelect: (m: Member) => void
}) {
  const toggle = useToggleCapability(workspaceId)
  const pending = toggle.isPending ? toggle.variables : undefined

  if (members.length === 0) {
    return <p className="rounded-card border border-border bg-card px-4 py-6 text-center text-xs text-muted-foreground">No one matches these filters.</p>
  }

  return (
    <div className="space-y-3">
      <section className="overflow-hidden rounded-card border border-border bg-card" aria-label="Capability matrix">
        {/* Scrolls sideways on a narrow screen with the names pinned, rather
            than squeezing ten columns until their headers are unreadable. */}
        <div className="max-h-[70vh] overflow-auto">
          <table className="border-separate border-spacing-0 text-control" aria-label="People and their capabilities">
            <thead>
              <tr>
                <th scope="col" className="sticky left-0 top-0 z-30 min-w-[180px] border-b border-r border-border bg-surface-subtle px-3 py-2 text-left align-bottom">
                  <span className="eyebrow text-muted-foreground">Person</span>
                </th>
                {ALL_CAPABILITIES.map((cap) => (
                  <th
                    key={cap}
                    scope="col"
                    title={`${CAPABILITY_LABELS[cap].en} — ${CAPABILITY_LABELS[cap].description}`}
                    className={cn(
                      "sticky top-0 z-20 min-w-[76px] border-b border-border bg-surface-subtle px-1.5 py-2 align-bottom text-[10.5px] font-medium leading-tight",
                      HIGH_STAKES_CAPABILITIES.has(cap) ? "text-destructive" : "text-muted-foreground",
                    )}
                  >
                    {CAPABILITY_LABELS[cap].en}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {members.map((m) => {
                const label = personLabel(m.user.full_name, m.user.email)
                const granted = capsByUser?.[m.user.id] ?? []
                return (
                  <tr key={m.id} data-member={m.user.id}>
                    <th scope="row" className="sticky left-0 z-10 border-b border-r border-border/60 bg-card px-2 py-1 text-left font-normal">
                      <button type="button" onClick={() => onSelect(m)} aria-label={`Show ${label}`}
                        className="flex w-full min-w-0 items-center gap-2 rounded-md px-1 py-1 text-left hover:bg-[var(--row-hover-bg)]">
                        <UserAvatar name={m.user.full_name} email={m.user.email} src={m.user.avatar_url} className="h-6 w-6" textClassName="text-[9px]" />
                        <span className="min-w-0">
                          <span className="block truncate text-xs">{label}</span>
                          <span className="block text-micro text-muted-foreground">{ROLE_LABEL[m.role] ?? m.role}</span>
                        </span>
                      </button>
                    </th>
                    {ALL_CAPABILITIES.map((cap) => {
                      const state = capabilityState(granted, cap)
                      const on = state !== "off"
                      const lock = capabilityLock(m, cap, currentUserId, canEdit)
                      const busy = pending?.userId === m.user.id && pending.cap === cap
                      return (
                        <td key={cap} className="h-10 border-b border-border/40 text-center">
                          <button
                            type="button"
                            role="checkbox"
                            aria-checked={on}
                            aria-label={capabilityAriaLabel(cap, label, on, lock)}
                            aria-disabled={lock !== null}
                            disabled={lock !== null || busy}
                            data-state={state}
                            onClick={() => toggle.mutate({ userId: m.user.id, cap, next: !on })}
                            className={cn(
                              "inline-grid h-7 w-7 place-items-center rounded-md border transition-colors",
                              state === "role" && "border-dashed border-border bg-surface-subtle text-muted-foreground",
                              state === "granted" && "border-primary bg-primary text-primary-foreground",
                              state === "off" && "border-border bg-input/30",
                              lock === null ? "cursor-pointer hover:ring-2 hover:ring-primary/20" : "cursor-not-allowed",
                              lock !== null && state !== "role" && "opacity-60",
                            )}
                          >
                            {state === "role" ? <Lock className="h-3 w-3" /> : state === "granted" ? <Check className="h-3.5 w-3.5" /> : null}
                          </button>
                        </td>
                      )
                    })}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </section>
      <MatrixLegend canEdit={canEdit} />
    </div>
  )
}

function MatrixLegend({ canEdit }: { canEdit: boolean }) {
  const swatch = "inline-grid h-5 w-5 place-items-center rounded-md border"
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground" data-slot="members-matrix-legend">
      <span className="inline-flex items-center gap-1.5">
        <span className={cn(swatch, "border-primary bg-primary text-primary-foreground")}><Check className="h-3 w-3" /></span>granted to this person
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className={cn(swatch, "border-dashed border-border bg-surface-subtle")}><Lock className="h-2.5 w-2.5" /></span>comes with every role
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className={cn(swatch, "border-border bg-input/30")} />off
      </span>
      <span className="inline-flex items-center gap-1.5 text-destructive">Red columns are high-stakes</span>
      <span className="ml-auto">{canEdit ? "Owners and your own row cannot be changed." : "Read-only — only admins change capabilities."}</span>
    </div>
  )
}
