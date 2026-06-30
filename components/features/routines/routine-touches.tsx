"use client"

import {
  Puzzle,
  Database,
  Server,
  Terminal,
  Bot,
  Send,
  KeyRound,
  ShieldAlert,
  Workflow,
  type LucideIcon,
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { RoutineManifest } from "@/lib/routine-flow"
import {
  manifestGroups,
  type ManifestChip,
  type ManifestChipTone,
  type ManifestFallbackIcon,
} from "@/lib/routine-manifest"
import { brandIconForType, BrandGlyph } from "./brand-icons"

// RoutineTouches — the "What it touches" capability manifest panel. Renders
// the routine's blast radius as chips grouped by kind (Integrations,
// Datastores, Tools, Agents, Sub-routines, Egress, Credentials). Each chip
// leads with the real app/brand logo (Postgres elephant, Redis, Ansible,
// Slack, …) via the shared brand-icons table, falling back to a generic
// lucide glyph when no logo is known — so this panel and the flow diagram
// read as one design. Grouping/ordering is the pure helper in
// lib/routine-manifest so the dry-run "Would use" panel shares it exactly.
// Risky rows — tools (code/scripts), egress (outbound network), and
// credentials — are highlighted amber so a reviewer can see "what can this
// thing reach + what secrets does it hold" at a glance. Read-only.

const TONE: Record<ManifestChipTone, string> = {
  integ: "border-indigo-500/30 text-indigo-300",
  store: "border-cyan-500/30 text-cyan-300",
  tool: "border-violet-500/30 text-violet-300",
  agent: "border-emerald-500/30 text-emerald-300",
  routine: "border-sky-500/30 text-sky-300",
  risk: "border-amber-500/35 text-amber-400",
}

const FALLBACK_ICON: Record<ManifestFallbackIcon, LucideIcon> = {
  puzzle: Puzzle,
  "store-server": Server,
  "store-db": Database,
  terminal: Terminal,
  bot: Bot,
  routine: Workflow,
  send: Send,
  shield: ShieldAlert,
  key: KeyRound,
}

function Chip({ chip }: { chip: ManifestChip }) {
  const brand = brandIconForType(chip.type)
  const fallback = FALLBACK_ICON[chip.fallback]
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-[7px] border bg-card px-2 py-[3px] text-[11px]",
        TONE[chip.tone],
      )}
    >
      <BrandGlyph brand={brand} fallback={fallback} className="h-3 w-3 shrink-0" />
      {chip.label}
    </span>
  )
}

export function RoutineTouches({ manifest }: { manifest?: RoutineManifest | null }) {
  const groups = manifestGroups(manifest)

  if (groups.length === 0) {
    return (
      <div className="px-1 py-3 text-center text-xs text-muted-foreground">
        This routine declares no external resources.
        <br />
        <span className="text-muted-foreground-soft">Nothing to touch beyond its own steps.</span>
      </div>
    )
  }

  return (
    <div className="px-1">
      {groups.map((g) => (
        <div
          key={g.key}
          className="flex items-start gap-2 border-t border-white/[0.04] py-2 first:border-t-0"
        >
          <div className="w-[88px] shrink-0 pt-[3px] text-[10.5px] text-muted-foreground-soft">
            {g.label}
          </div>
          <div className="flex flex-wrap gap-1.5">
            {g.chips.map((c) => (
              <Chip key={c.key} chip={c} />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}
