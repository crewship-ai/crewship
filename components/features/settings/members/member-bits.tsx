"use client"

import type { CSSProperties, ElementType } from "react"

import { Briefcase, Crown, Eye, FileText, Lock, LockOpen, MessageSquare, RotateCcw, Save, Shield, User, Zap, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { StatusPill } from "@/components/ui/status-pill"
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

/** A role as v67's flat pill: grey for everyone, the accent for the owner.
 *  Severity on this page belongs to the access chips and the findings, so a
 *  role never competes with them for colour. */
export function RolePill({ role, className }: { role: string; className?: string }) {
  return (
    <StatusPill data-slot="role-pill" tone={role === "OWNER" ? "blue" : "muted"} label={ROLE_LABEL[role] ?? role}
      className={cn("font-sans tracking-normal", className)} />
  )
}

/**
 * When a confirmed change lands, said the same way on every Members
 * confirmation (Harbor 2 catalogue `st-highstakes`): a role change is
 * written the moment you confirm; a capability, a preset or a bulk grant goes
 * into the page's draft and lands on Save. The two dialogs looked identical
 * before, and only one of them wrote at once.
 */
export function ConfirmWhen({ when, className }: { when: "now" | "save"; className?: string }) {
  const now = when === "now"
  const Icon = now ? Zap : Save
  return (
    <p data-when={when} className={cn("inline-flex w-fit items-center gap-1.5 rounded-md px-2 py-1 text-label font-medium",
      now ? "bg-chip-warn-bg text-chip-warn-fg" : "bg-chip-info-bg text-chip-info-fg", className)}>
      <Icon className="h-3 w-3 shrink-0" aria-hidden />
      {now ? "Applies now — written as soon as you confirm" : "Takes effect when you save the page"}
    </p>
  )
}

/** Access mode is a separate fact from the role (#2878): a restricted Member
 *  and a trusted Member share the role pill. Only the exception is marked, so
 *  a trusted roster stays quiet. */
export function RestrictedPill({ mode, className }: { mode?: "trusted" | "restricted"; className?: string }) {
  if (mode !== "restricted") return null
  return (
    <StatusPill data-slot="restricted-pill" tone="warn" label="Restricted"
      title="Restricted access: only the resources granted in this member's access policy"
      className={cn("font-sans tracking-normal", className)} />
  )
}

const CHIP = "inline-flex items-center whitespace-nowrap rounded-full px-2 py-px text-micro"

/**
 * What a person may do, as at most three flat chips (v67 `.m-chip`): an area
 * held in full reads "Routines", in part "Content 3/4"; Reveal turns the
 * secrets chip red and names it. Nothing beyond chat reads "Chat only".
 */
export function AreaChips({ granted, className }: { granted: readonly string[] | undefined; className?: string }) {
  if (chatOnly(granted)) {
    return (
      <span className={cn(CHIP, "bg-chip-neutral-bg text-chip-neutral-fg", className)} data-area="chat">Chat only</span>
    )
  }
  const reveal = hasCapability(granted, Capability.CredentialReveal)
  return (
    <span className={cn("flex min-w-0 flex-wrap gap-1", className)}>
      {areasHeld(granted).filter((a) => a.held > 0).map((a) => {
        const danger = a.key === "secrets" && reveal
        return (
          <span key={a.key} data-area={a.key}
            title={`${a.label}: ${a.held} of ${a.total}${danger ? " · can reveal secrets" : ""}`}
            className={cn(CHIP, danger ? "bg-chip-danger-bg text-chip-danger-fg" : "bg-chip-info-bg text-chip-info-fg")}>
            {a.label}
            {danger ? <span className="ml-1">· reveal</span> : a.held < a.total && <span className="ml-1 tabular-nums">{a.held}/{a.total}</span>}
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
