"use client"

import { useId, type CSSProperties, type ElementType, type ReactNode } from "react"

import { Briefcase, Crown, Eye, FileText, LockOpen, RotateCcw, Save, Shield, TriangleAlert, User, Zap, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { StatusPill } from "@/components/ui/status-pill"
import { Capability } from "@/lib/capabilities"
import { CONCEPT_ICON } from "@/lib/concept-icons"

import { CAPABILITY_NAME, PROFILE_LABEL, ROLE_LABEL, accessProfile, profileDiffText, type AreaKey } from "./members-model"

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

/** Reach is a separate fact from the role (#2878): a Guest Member and a
 *  Member of the whole workspace share the role pill. Only the exception is
 *  marked, so a roster of trusted people stays quiet. */
export function RestrictedPill({ mode, className }: { mode?: "trusted" | "restricted"; className?: string }) {
  if (mode !== "restricted") return null
  return (
    <StatusPill data-slot="guest-pill" tone="muted" label="Guest"
      title="Guest: reaches only what is shared with them"
      className={cn("font-sans tracking-normal", className)} />
  )
}

/**
 * What a person may do, as ONE readable line: the nearest preset and what
 * differs from it ("Power user + create skills", "Builder without reveal
 * secrets"). An owner holds everything. The words come from `profileText`, so
 * the roster, the CSV and the person sheet never disagree.
 */
export function AbilitiesLine({ role, granted, className }: { role: string; granted: readonly string[] | undefined; className?: string }) {
  if (role === "OWNER") return <span data-slot="abilities-line" className={cn("font-medium text-foreground", className)}>Everything</span>
  const profile = accessProfile(granted)
  const diff = profileDiffText(profile)
  return (
    <span data-slot="abilities-line" data-preset={profile.key} className={cn("min-w-0", className)}>
      <span className="font-medium text-foreground">{PROFILE_LABEL[profile.key]}</span>
      {diff && <>{" "}<span className="text-muted-foreground">{diff}</span></>}
    </span>
  )
}

/** The warning mark of a person who holds more than their role manages. */
export function AboveRoleMark({ role, className }: { role: string; className?: string }) {
  return (
    <span className={cn("inline-flex text-warn", className)} title={`More than a ${ROLE_LABEL[role] ?? role} manages`}>
      <TriangleAlert className="h-3.5 w-3.5" aria-label={`More than a ${ROLE_LABEL[role] ?? role} manages`} />
    </span>
  )
}

/** The red marker of a person who can read stored secrets in plaintext. */
export function RevealMark({ className }: { className?: string }) {
  return (
    <span data-slot="reveal-mark" className={cn("inline-flex items-center gap-1 text-label font-medium text-destructive", className)}
      title="Can read stored secrets in plaintext">
      <Eye className="h-3 w-3" aria-hidden />reveals
    </span>
  )
}

/** The accessible name of one ability switch: the action a click takes, or
 *  why there is none. Said in the page's words, never with the wire id. */
export function abilityAriaLabel(cap: string, who: string, on: boolean, lock: "role" | "owner" | "self" | "readonly" | null): string {
  const name = CAPABILITY_NAME[cap] ?? cap
  switch (lock) {
    case "role": return `Chat is always allowed for ${who}`
    case "owner": return `An owner's abilities are fixed: ${name} for ${who}`
    case "self": return `You cannot change your own abilities: ${name}`
    case "readonly": return `${who} ${on ? "can" : "cannot"}: ${name}`
    default: return on ? `Take away ${name} from ${who}` : `Give ${name} to ${who}`
  }
}

/**
 * One option of a short radio list: a dot, a title, one line (the kit's
 * `x2-opts`). The list is a `radiogroup`; each option is a `radio`. An option
 * can be shown but not chosen (`disabled`), with `why` as its hover reason.
 */
export function RadioOption({ checked, title, description, onSelect, disabled, why, className }: {
  checked: boolean
  title: ReactNode
  description?: ReactNode
  onSelect?: () => void
  disabled?: boolean
  why?: string
  className?: string
}) {
  // The title names the option; the line under it describes it. Kept apart
  // so a radio is called "Manager", not "ManagerCrews, agents and credentials".
  const id = useId()
  return (
    <button type="button" role="radio" aria-checked={checked} disabled={disabled} title={why}
      aria-labelledby={`${id}-t`} aria-describedby={description != null ? `${id}-d` : undefined}
      onClick={() => { if (!checked) onSelect?.() }}
      className={cn("flex w-full min-w-0 items-start gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors coarse:min-h-[44px] disabled:cursor-not-allowed disabled:opacity-60",
        checked ? "border-primary bg-primary/5 ring-2 ring-primary/15" : "border-border bg-card hover:border-line-strong", className)}>
      <span aria-hidden className={cn("mt-0.5 grid h-4 w-4 shrink-0 place-items-center rounded-full border", checked ? "border-primary" : "border-control-border")}>
        {checked && <span className="h-2 w-2 rounded-full bg-primary" />}
      </span>
      <span className="min-w-0 flex-1">
        <span id={`${id}-t`} className="block text-control font-medium text-foreground">{title}</span>
        {description != null && <span id={`${id}-d`} className="mt-0.5 block text-label text-muted-foreground">{description}</span>}
      </span>
    </button>
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
