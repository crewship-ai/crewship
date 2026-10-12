"use client"

import type { CSSProperties } from "react"
import { Check, ChevronRight, Info, TriangleAlert } from "lucide-react"

import { StatusPill } from "@/components/ui/status-pill"
import { personLabel } from "@/components/ui/user-avatar"
import { SettingsCard } from "@/components/features/settings/shared"
import { CAPABILITY_LABELS, Capability, hasCapability } from "@/lib/capabilities"

import { ROLE_LABEL, aboveRole, type Member } from "./members-model"
import { daysUntil, type Invitation } from "./use-invitations"

export interface Finding {
  key: string
  severity: "high" | "medium" | "low"
  title: string
  body: string
  verb: string
  run: () => void
}

/**
 * What on the roster needs a person, most urgent first: who can read secrets
 * in plaintext, who holds more than their role suggests, and invitations about
 * to run out. Each finding carries the verb that deals with it.
 */
export function membersFindings({ members, caps, invitations, onFlag, onOpen, onInvited }: {
  members: Member[]
  caps: Record<string, string[]>
  invitations: Invitation[]
  onFlag: (flag: "reveal" | "risky-low-role") => void
  onOpen: (m: Member) => void
  onInvited: () => void
}): Finding[] {
  const out: Finding[] = []
  const revealers = members.filter((m) => hasCapability(caps[m.user.id], Capability.CredentialReveal))
  if (revealers.length > 0) {
    const admins = revealers.filter((m) => m.role === "OWNER" || m.role === "ADMIN").length
    out.push({
      key: "reveal", severity: revealers.length > 3 ? "high" : "medium",
      title: `${revealers.length} ${revealers.length === 1 ? "person can" : "people can"} reveal secrets in plaintext.`,
      body: `${admins ? `${admins} of them ${admins === 1 ? "is an owner or admin" : "are owners or admins"}. ` : ""}Reveal is granted per person — check each one still needs it.`,
      verb: `Review ${revealers.length}`, run: () => onFlag("reveal"),
    })
  }
  const above = members.map((m) => ({ m, caps: aboveRole(m, caps[m.user.id]) })).filter((x) => x.caps.length > 0)
  if (above.length === 1) {
    const { m, caps: held } = above[0]
    const what = held.includes(Capability.CredentialReveal) ? "reveal secrets" : CAPABILITY_LABELS[held[0]].en.toLowerCase()
    out.push({
      key: "above", severity: "medium",
      title: `${personLabel(m.user.full_name, m.user.email)} can ${what} as a ${ROLE_LABEL[m.role]}.`,
      body: "Their role does not manage credentials or skills. Raise the role, or turn the access off.",
      verb: "Open", run: () => onOpen(m),
    })
  } else if (above.length > 1) {
    out.push({
      key: "above", severity: "medium",
      title: `${above.length} Members or Viewers hold skills or secrets.`,
      body: "Their roles do not manage credentials or skills. Raise each role, or turn the access off.",
      verb: `Review ${above.length}`, run: () => onFlag("risky-low-role"),
    })
  }
  const ending = invitations.filter((i) => daysUntil(i.expires_at) <= 2)
  if (ending.length > 0) {
    const expired = ending.filter((i) => daysUntil(i.expires_at) < 0).length
    out.push({
      key: "invites", severity: "low",
      title: ending.length === 1 ? `The invitation for ${ending[0].email} ${expired ? "has expired" : "expires soon"}.` : `${ending.length} invitations ${expired ? "have expired or expire soon" : "expire soon"}.`,
      body: "An expired invitation cannot be accepted. Invite the person again.",
      verb: "Show", run: onInvited,
    })
  }
  return out
}

const TINT: Record<Finding["severity"], string> = { high: "var(--destructive)", medium: "var(--warn)", low: "var(--info)" }
const TONE = { high: "danger", medium: "warn", low: "muted" } as const

export function MembersAttention({ findings }: { findings: Finding[] }) {
  return (
    <SettingsCard icon={TriangleAlert} tint={findings.length ? "var(--warn)" : "var(--success)"} title="Needs attention"
      description={findings.length ? `${findings.length} ${findings.length === 1 ? "thing" : "things"} about who can do what here` : "Who can do what in this workspace"}>
      {findings.length === 0 ? (
        <p className="flex items-center gap-2 px-4 py-3.5 text-label text-muted-foreground" data-slot="members-all-clear">
          <Check className="h-3.5 w-3.5 text-success" aria-hidden />Nothing needs attention — nobody holds more than their role suggests.
        </p>
      ) : findings.map((f) => (
        <div key={f.key} data-finding={f.key} className="flex items-center gap-3 border-b border-border/60 px-4 py-3 last:border-b-0">
          <span className="icon-tile grid h-7 w-7 shrink-0 place-items-center rounded-lg" style={{ "--ic": TINT[f.severity] } as CSSProperties} aria-hidden>
            {f.severity === "low" ? <Info className="h-3.5 w-3.5" /> : <TriangleAlert className="h-3.5 w-3.5" />}
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <StatusPill tone={TONE[f.severity]} label={f.severity.charAt(0).toUpperCase() + f.severity.slice(1)} />
              <p className="min-w-0 text-label font-medium leading-snug text-foreground">{f.title}</p>
            </div>
            <p className="mt-0.5 text-label leading-snug text-muted-foreground">{f.body}</p>
          </div>
          <button type="button" onClick={f.run}
            className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-label font-medium text-foreground/90 transition-colors hover:border-primary/50 hover:text-primary-hover">
            {f.verb}<ChevronRight className="h-3 w-3" aria-hidden />
          </button>
        </div>
      ))}
    </SettingsCard>
  )
}
