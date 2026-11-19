"use client"

import { useState } from "react"
import Link from "next/link"
import { Check, ChevronRight, Eye, LockOpen, Mail, TriangleAlert, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Combobox } from "@/components/ui/combobox"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { Capability, hasCapability, type CapabilityValue } from "@/lib/capabilities"
import { SettingsCard } from "@/components/features/settings/shared"

import { CAPABILITY_NAME, ROLE_LABEL, aboveRole, type Member } from "./members-model"
import { InvitationRows } from "./invitations-card"
import { invitationNeedsLook, type Invitation } from "./use-invitations"

/** The three kinds of finding, each a card of its own. */
export type LookKind = "above" | "invites" | "reveal"

export interface LookGroup {
  kind: LookKind
  severity: "high" | "medium" | "low"
  title: string
  hint: string
  /** The people of an `above` or `reveal` finding, with what they hold. */
  people: { member: Member; held: CapabilityValue[] }[]
  /** The invitations of an `invites` finding. */
  invitations: Invitation[]
}

/** How many rows a card shows before it hands over to the filtered list. */
export const LOOK_CARD_ROWS = 6

const RANK = { high: 0, medium: 1, low: 2 } as const
const ORDER: Record<LookKind, number> = { above: 0, invites: 1, reveal: 2 }

/**
 * What on the roster needs a person, by kind, most urgent first: people who
 * hold more than their role manages, invitations that ran out or end within
 * two days, and who can read secrets in plaintext. The severities are the
 * ones the page always had — reveal is "low" while the workspace switch is
 * off (the grant does nothing), "high" past three holders, else "medium";
 * the others are medium and low — and they only order the cards.
 */
export function lookGroups({ members, caps, invitations, revealEnabled = null }: {
  members: Member[]
  caps: Record<string, string[]>
  invitations: Invitation[]
  /** The workspace's reveal switch; null when unknown. */
  revealEnabled?: boolean | null
}): LookGroup[] {
  const out: LookGroup[] = []
  const above = members.map((member) => ({ member, held: aboveRole(member, caps[member.user.id]) })).filter((x) => x.held.length > 0)
  if (above.length > 0) {
    out.push({
      kind: "above", severity: "medium", title: "More than their role manages",
      hint: "A Member or Viewer who can create skills or handle secrets. Raise the role, or turn the ability off.",
      people: above, invitations: [],
    })
  }
  const ending = invitations.filter((i) => invitationNeedsLook(i))
  if (ending.length > 0) {
    out.push({
      kind: "invites", severity: "low", title: "Invitations that ran out or end soon",
      hint: "An expired link cannot be accepted. Invite again, or revoke it.",
      people: [], invitations: ending,
    })
  }
  const revealers = members.filter((m) => hasCapability(caps[m.user.id], Capability.CredentialReveal))
  if (revealers.length > 0) {
    out.push({
      kind: "reveal", severity: revealEnabled === false ? "low" : revealers.length > 3 ? "high" : "medium", title: "Can reveal secrets",
      hint: "Reads a stored secret in plaintext. Two holders is usually enough.",
      people: revealers.map((member) => ({ member, held: [Capability.CredentialReveal as CapabilityValue] })), invitations: [],
    })
  }
  return out.sort((a, b) => RANK[a.severity] - RANK[b.severity] || ORDER[a.kind] - ORDER[b.kind])
}

/** Rows over all cards: the figure the banner and the panel show. */
export function lookCount(groups: readonly LookGroup[]): number {
  return groups.reduce((n, g) => n + g.people.length + g.invitations.length, 0)
}

const GROUP_ICON: Record<LookKind, LucideIcon> = { above: TriangleAlert, invites: Mail, reveal: Eye }

/** The people (and invitations) a finding names, in the words of the banner:
 *  "More than their role manages · 6 · Can reveal secrets · 8". */
export function lookSummary(groups: readonly LookGroup[]): string {
  return groups.map((g) => `${g.title} · ${g.people.length + g.invitations.length}`).join(" · ")
}

/**
 * Settings › Members › Needs a look: one card per kind of finding, each
 * listing the people with the action that deals with it. A card shows six
 * rows and hands over to the filtered list. The reveal card also holds the
 * workspace's reveal switch (read-only here: it is the Owner's, in Access &
 * Secrets) and the one place to grant reveal to someone else.
 */
export function MembersLook({
  groups, currentUserId, callerRole, workspaceId, revealEnabled, holders, grantCandidates, canGrantReveal,
  onTurnOff, onOpen, onShowAll, onGrantReveal, onInvitationsChanged,
}: {
  groups: LookGroup[]
  currentUserId?: string
  callerRole?: string | null
  workspaceId: string
  revealEnabled: boolean | null
  /** Everyone who holds reveal (the reveal card lists them; this is its count). */
  holders: number
  grantCandidates: Member[]
  canGrantReveal: boolean
  onTurnOff: (m: Member, caps: CapabilityValue[]) => void
  onOpen: (m: Member) => void
  onShowAll: (kind: LookKind) => void
  onGrantReveal: (m: Member) => void
  onInvitationsChanged: () => void
}) {
  const reveal = groups.find((g) => g.kind === "reveal")
  const cards = reveal ? groups : [...groups, null]
  // With nothing to look at the page says so, and the reveal card still
  // answers "who may I give reveal to".
  const clear = groups.length === 0
  return (
    <div className="space-y-4" data-slot="members-look">
      {clear && (
        <SettingsCard icon={Check} tint="var(--success)" title="Nothing needs a look" description="Access worth a second pair of eyes shows up here.">
          <p className="px-4 py-3.5 text-label text-muted-foreground" data-slot="members-all-clear">Nobody holds more than their role suggests, and no invitation is about to run out.</p>
        </SettingsCard>
      )}
      {cards.map((g) => {
        if (g === null) {
          // No holder: the reveal card exists for the policy line and the picker.
          return (
            <RevealCard key="reveal" group={null} revealEnabled={revealEnabled} holders={holders} candidates={grantCandidates} canGrant={canGrantReveal}
              currentUserId={currentUserId} onOpen={onOpen} onShowAll={() => onShowAll("reveal")} onGrant={onGrantReveal} />
          )
        }
        if (g.kind === "reveal") {
          return (
            <RevealCard key="reveal" group={g} revealEnabled={revealEnabled} holders={holders} candidates={grantCandidates} canGrant={canGrantReveal}
              currentUserId={currentUserId} onOpen={onOpen} onShowAll={() => onShowAll("reveal")} onGrant={onGrantReveal} />
          )
        }
        const total = g.people.length + g.invitations.length
        const Icon = GROUP_ICON[g.kind]
        return (
          <SettingsCard key={g.kind} icon={Icon} tint={g.severity === "low" ? "var(--info)" : "var(--warn)"} title={`${g.title} · ${total}`} description={g.hint}>
            <div data-finding={g.kind}>
              {g.kind === "invites" ? (
                <InvitationRows invitations={g.invitations.slice(0, LOOK_CARD_ROWS)} workspaceId={workspaceId} manage callerRole={callerRole} onChanged={onInvitationsChanged} />
              ) : (
                <ul>
                  {g.people.slice(0, LOOK_CARD_ROWS).map(({ member, held }) => (
                    <PersonRow key={member.id} member={member} currentUserId={currentUserId}
                      note={`${ROLE_LABEL[member.role] ?? member.role} · can ${held.map((c) => (CAPABILITY_NAME[c] ?? c).toLowerCase()).join(", ")}`}>
                      {member.user.id !== currentUserId && (
                        <Button variant="outline" size="sm" className="coarse:h-[44px]" aria-label={`Turn off ${held.map((c) => CAPABILITY_NAME[c]).join(", ")} for ${personLabel(member.user.full_name, member.user.email)}`}
                          onClick={() => onTurnOff(member, held)}>
                          Turn it off
                        </Button>
                      )}
                      <Button variant="ghost" size="sm" className="coarse:h-[44px]" onClick={() => onOpen(member)}>Open</Button>
                    </PersonRow>
                  ))}
                </ul>
              )}
              <CardMore shown={Math.min(total, LOOK_CARD_ROWS)} total={total} onShowAll={() => onShowAll(g.kind)} />
            </div>
          </SettingsCard>
        )
      })}
    </div>
  )
}

function PersonRow({ member, currentUserId, note, children }: { member: Member; currentUserId?: string; note: string; children: React.ReactNode }) {
  const label = personLabel(member.user.full_name, member.user.email)
  return (
    <li data-member={member.user.id} className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border/60 px-4 py-2.5 last:border-b-0">
      <UserAvatar name={member.user.full_name} email={member.user.email} src={member.user.avatar_url} className="h-7 w-7" textClassName="text-micro" />
      <span className="min-w-0 flex-1 basis-48">
        <span className="flex items-center gap-1.5 text-control">
          <span className="truncate">{label}</span>
          {member.user.id === currentUserId && <span className="text-micro text-muted-foreground">you</span>}
        </span>
        {member.user.full_name?.trim() && <span className="block truncate text-micro text-muted-foreground-soft">{member.user.email}</span>}
      </span>
      <span className="text-label text-muted-foreground">{note}</span>
      <span className="flex items-center gap-1">{children}</span>
    </li>
  )
}

/** "6 of 24 shown" and the way to the whole list. */
function CardMore({ shown, total, onShowAll }: { shown: number; total: number; onShowAll: () => void }) {
  if (total <= shown) return null
  return (
    <div data-slot="show-more" className="flex flex-wrap items-center justify-between gap-2.5 border-t border-border px-4 py-2 text-label text-muted-foreground tabular-nums">
      <span>{shown} of {total} shown</span>
      <Button type="button" variant="outline" size="sm" className="coarse:h-[44px]" onClick={onShowAll}>Show all in the list</Button>
    </div>
  )
}

/**
 * Who can reveal secrets, the workspace switch beside them, and one way to
 * grant it to someone else. A grant goes through the page's draft and its
 * high-stakes question like any other. The switch is the Owner's and lives in
 * Access & Secrets; this card only says where it stands.
 */
function RevealCard({ group, revealEnabled, holders, candidates, canGrant, currentUserId, onOpen, onShowAll, onGrant }: {
  group: LookGroup | null
  revealEnabled: boolean | null
  holders: number
  candidates: Member[]
  canGrant: boolean
  currentUserId?: string
  onOpen: (m: Member) => void
  onShowAll: () => void
  onGrant: (m: Member) => void
}) {
  const [pick, setPick] = useState("")
  const people = group?.people ?? []
  return (
    <SettingsCard icon={LockOpen} tint="var(--destructive)" title={`Can reveal secrets · ${holders}`}
      description="Reads a stored secret in plaintext. Two holders is usually enough.">
      <div data-finding="reveal">
        <div className="flex flex-wrap items-center gap-2 border-b border-border/60 px-4 py-2.5 text-label" data-slot="members-reveal-policy">
          <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", revealEnabled ? "bg-success" : "bg-muted-foreground")} aria-hidden />
          <span className="min-w-0 flex-1 text-muted-foreground">
            {revealEnabled === null ? "The workspace switch is set in Access & Secrets." : <>Reveal is <span className="font-medium text-foreground">{revealEnabled ? "on" : "off"}</span> for this workspace{revealEnabled ? "." : " — a grant does nothing until the Owner turns it on."}</>}
          </span>
          <Link href="/settings?tab=access-secrets" className="inline-flex shrink-0 items-center gap-1 font-medium text-primary-hover hover:underline">
            Access &amp; Secrets<ChevronRight className="h-3 w-3" aria-hidden />
          </Link>
        </div>
        <ul>
          {people.slice(0, LOOK_CARD_ROWS).map(({ member }) => (
            <PersonRow key={member.id} member={member} currentUserId={currentUserId} note={ROLE_LABEL[member.role] ?? member.role}>
              <Button variant="ghost" size="sm" className="coarse:h-[44px]" onClick={() => onOpen(member)}>Open</Button>
            </PersonRow>
          ))}
        </ul>
        {people.length === 0 && <p className="px-4 py-3 text-label text-muted-foreground">Nobody can reveal secrets.</p>}
        <CardMore shown={Math.min(people.length, LOOK_CARD_ROWS)} total={people.length} onShowAll={onShowAll} />
        {canGrant && candidates.length > 0 && (
          <div className="flex flex-col gap-2 border-t border-border/60 px-4 py-2.5 sm:flex-row sm:items-center sm:gap-4">
            <div className="min-w-0 flex-1">
              <div className="text-control">Grant reveal</div>
              <div className="text-label text-muted-foreground">Asks first, then waits for Save like every change here.</div>
            </div>
            <span className="flex items-center gap-2">
              <Combobox ariaLabel="Person to grant reveal to" value={pick || null} onChange={setPick} className="sm:w-60"
                placeholder="Choose a person…" searchPlaceholder="Search people…"
                options={candidates.map((m) => ({
                  value: m.user.id, label: personLabel(m.user.full_name, m.user.email), description: `${ROLE_LABEL[m.role] ?? m.role} · ${m.user.email}`, keywords: [m.user.email],
                }))} />
              <Button type="button" variant="outline" size="sm" className="coarse:h-[44px]" disabled={!pick}
                onClick={() => { const m = candidates.find((c) => c.user.id === pick); if (m) { onGrant(m); setPick("") } }}>
                Grant
              </Button>
            </span>
          </div>
        )}
      </div>
    </SettingsCard>
  )
}
