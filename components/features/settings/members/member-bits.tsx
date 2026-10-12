"use client"

import {
  Bookmark, CircleDot, Crown, Eye, FileText, KeyRound, MessageSquare, PanelsTopLeft, Play, Plus, RotateCcw,
  Shield, Sparkles, Workflow, type LucideIcon,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Capability, hasCapability } from "@/lib/capabilities"

import { ROLE_LABEL, areasHeld, chatOnly, type AreaKey } from "./members-model"

/** An icon per area and per capability, the same in the roster, the panel and
 *  the phone list. */
export const AREA_ICON: Record<AreaKey, LucideIcon> = { routines: Workflow, content: FileText, secrets: KeyRound }
export const CAPABILITY_ICON: Record<string, LucideIcon> = {
  [Capability.RoutineCreate]: Plus,
  [Capability.RoutineRun]: Play,
  [Capability.IssueCreate]: CircleDot,
  [Capability.PageCreate]: PanelsTopLeft,
  [Capability.MemoryWrite]: Bookmark,
  [Capability.SkillCreate]: Sparkles,
  [Capability.CredentialCreate]: Plus,
  [Capability.CredentialRotate]: RotateCcw,
  [Capability.CredentialReveal]: Eye,
}
const ROLE_ICON: Record<string, LucideIcon> = { OWNER: Crown, ADMIN: Shield }

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
