"use client"

import type { CSSProperties, ElementType } from "react"

import { Briefcase, Crown, Eye, FileText, LockOpen, MessageSquare, RotateCcw, Shield, User, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Capability, hasCapability } from "@/lib/capabilities"
import { CONCEPT_ICON } from "@/lib/concept-icons"

import { ROLE_LABEL, areasHeld, chatOnly, type AreaKey } from "./members-model"

/** An icon per area and per capability, the same in the roster, the panel and
 *  the phone list. Where the product already has an icon for the concept
 *  (lib/concept-icons) it is that one — Routines is the rail's ScrollText,
 *  an issue its CircleDot — so a grant wears the face of what it grants. */
export const AREA_ICON: Record<AreaKey, LucideIcon> = { routines: CONCEPT_ICON.routines, content: FileText, secrets: CONCEPT_ICON.credentials }
export const CAPABILITY_ICON: Record<string, LucideIcon> = {
  [Capability.RoutineCreate]: CONCEPT_ICON.routines,
  [Capability.RoutineRun]: CONCEPT_ICON.triggers,
  [Capability.IssueCreate]: CONCEPT_ICON.issues,
  [Capability.PageCreate]: CONCEPT_ICON.pages,
  [Capability.MemoryWrite]: CONCEPT_ICON.memory,
  [Capability.SkillCreate]: CONCEPT_ICON.skills,
  [Capability.CredentialCreate]: CONCEPT_ICON.credentials,
  [Capability.CredentialRotate]: RotateCcw,
  [Capability.CredentialReveal]: LockOpen,
}
/** One icon per role, highest first: the crown, the shield, the briefcase of
 *  someone who runs crews, a person, an eye that only looks. */
export const ROLE_ICON: Record<string, LucideIcon> = { OWNER: Crown, ADMIN: Shield, MANAGER: Briefcase, MEMBER: User, VIEWER: Eye }

/** A role, neutral: severity on this page belongs to the access chips and the
 *  findings, so a role never competes with them for colour. */
export function RolePill({ role, className }: { role: string; className?: string }) {
  const Icon = ROLE_ICON[role]
  return (
    <span data-slot="role-pill" className={cn("inline-flex h-[22px] shrink-0 items-center gap-1.5 rounded-md border border-border bg-surface-subtle px-2 text-label text-foreground/90", className)}>
      {Icon && <Icon className="h-3 w-3 text-muted-foreground" aria-hidden />}
      {ROLE_LABEL[role] ?? role}
    </span>
  )
}

/**
 * What a person may do, as at most three chips: an area held in full reads
 * "Routines", in part "Content 3/4"; Reveal turns the secrets chip red and
 * names it. Nothing beyond chat reads "Chat only".
 */
export function AreaChips({ granted, className }: { granted: readonly string[] | undefined; className?: string }) {
  if (chatOnly(granted)) {
    return (
      <span className={cn("inline-flex h-[22px] items-center gap-1.5 rounded-md border border-dashed border-border px-2 text-label text-muted-foreground", className)} data-area="chat">
        <MessageSquare className="h-3 w-3" aria-hidden />Chat only
      </span>
    )
  }
  const reveal = hasCapability(granted, Capability.CredentialReveal)
  return (
    <span className={cn("flex min-w-0 flex-wrap gap-1.5", className)}>
      {areasHeld(granted).filter((a) => a.held > 0).map((a) => {
        const Icon = AREA_ICON[a.key]
        const danger = a.key === "secrets" && reveal
        return (
          <span key={a.key} data-area={a.key}
            title={`${a.label}: ${a.held} of ${a.total}${danger ? " · can reveal secrets" : ""}`}
            className={cn("inline-flex h-[22px] items-center gap-1.5 whitespace-nowrap rounded-md border px-2 text-label",
              danger ? "border-destructive/30 bg-destructive/10 text-destructive" : "border-primary/25 bg-primary/[0.08] text-foreground/90")}>
            <Icon className={cn("h-3 w-3", danger ? "text-destructive" : "text-primary-hover")} aria-hidden />
            {a.label}
            {danger ? <span>· reveal</span> : a.held < a.total && <span className="font-mono text-micro text-muted-foreground">{a.held}/{a.total}</span>}
          </span>
        )
      })}
    </span>
  )
}

/** The same, as bare icons — a phone row's second line. */
export function AreaIcons({ granted }: { granted: readonly string[] | undefined }) {
  if (chatOnly(granted)) return <span>Chat only</span>
  const reveal = hasCapability(granted, Capability.CredentialReveal)
  return (
    <span className="inline-flex items-center gap-1.5">
      {areasHeld(granted).filter((a) => a.held > 0).map((a) => {
        const Icon = AREA_ICON[a.key]
        const danger = a.key === "secrets" && reveal
        return <Icon key={a.key} data-area={a.key} className={cn("h-3.5 w-3.5", danger ? "text-destructive" : "text-primary-hover")} aria-label={danger ? "Secrets, can reveal" : a.label} />
      })}
    </span>
  )
}

/** The icon tile every member dialog opens with — Add in the brand tint,
 *  Remove in red — so the two read as one family. */
export function MemberDialogIcon({ icon: Icon, tint = "var(--primary)" }: { icon: ElementType; tint?: string }) {
  return (
    <span className="icon-tile grid h-10 w-10 shrink-0 place-items-center rounded-xl" style={{ "--ic": tint } as CSSProperties} aria-hidden>
      <Icon className="h-[18px] w-[18px]" />
    </span>
  )
}
