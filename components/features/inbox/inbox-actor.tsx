"use client"

import { Cog, ScrollText, ShieldCheck, Users } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { cn } from "@/lib/utils"

import type { Actor } from "./inbox-types"

// =============================================================================
// Actor identity.
//
// One rule, applied everywhere: SQUARE IS A MACHINE, CIRCLE IS A PERSON.
//
// An agent, a routine and the system act on their own and get a rounded-square
// tile — the same shape the roster draws an agent in. A user is a human who
// answered, and gets a circle. Nothing else changes between them, so the shape
// is doing the work rather than a label nobody reads.
//
// This matters most in the archive, where the two appear side by side in one
// row: casey (agent) asked, pavel (user) approved. Drawn alike, that row reads
// as two agents; drawn by shape, it reads at a glance.
// =============================================================================

const GLYPH: Record<string, { icon: typeof ScrollText; tone: string }> = {
  routine: { icon: ScrollText, tone: "bg-purple/20 text-purple" },
  system: { icon: Cog, tone: "bg-notice/20 text-notice" },
  crew: { icon: Users, tone: "bg-info/20 text-info" },
}

// Keeper is the one system sender worth telling apart at a glance: it is the
// security gatekeeper, and it wears a shield everywhere else in the product
// (the admin nav, the decision sheet, the journal). A cog made its notices look
// like a settings change (#1530).
//
// Keyed on the sender SLUG, not the display name — the name is display text and
// could be renamed. The prefix catches Keeper's sub-senders too
// (keeper_skill_review, keeper_behavior, keeper_memory_health, …), which the
// original exact match left wearing the generic mark.
const KEEPER_GLYPH = { icon: ShieldCheck, tone: "bg-success/20 text-success" }

function isKeeper(id: string): boolean {
  return id === "keeper" || id.startsWith("keeper_")
}

export function ActorAvatar({ actor, size = 24 }: { actor: Actor; size?: 20 | 24 | 32 }) {
  const box = size === 32 ? "h-8 w-8" : size === 20 ? "h-5 w-5" : "h-6 w-6"
  const glyphSize = size === 32 ? "h-4 w-4" : "h-3.5 w-3.5"

  if (actor.kind === "agent") {
    return (
      <AgentAvatar
        seed={actor.seed || actor.label}
        className={cn("shrink-0 rounded-md object-cover", box)}
        alt=""
      />
    )
  }

  if (actor.kind === "user") {
    // A circle, and initials rather than a generated face: a person in this
    // product has no avatar to generate from, and inventing one would put a
    // machine-made portrait on a human.
    return (
      <span
        className={cn(
          "grid shrink-0 place-items-center rounded-full bg-primary/15 font-semibold uppercase text-primary",
          box,
          size === 32 ? "text-[11px]" : "text-[9px]",
        )}
      >
        {actor.label.slice(0, 2)}
      </span>
    )
  }

  const slug = (actor.seed || actor.id || "").toLowerCase()
  const g =
    actor.kind === "system" && isKeeper(slug)
      ? KEEPER_GLYPH
      : GLYPH[actor.kind] ?? GLYPH.system
  const Icon = g.icon
  return (
    <span className={cn("grid shrink-0 place-items-center rounded-md", g.tone, box)}>
      <Icon className={glyphSize} />
    </span>
  )
}

/** Avatar + name, with the kind spelled out for anything that is not an agent. */
export function ActorLabel({
  actor,
  size = 24,
  className,
  showKind = false,
}: {
  actor: Actor
  size?: 20 | 24 | 32
  className?: string
  showKind?: boolean
}) {
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-2", className)}>
      <ActorAvatar actor={actor} size={size} />
      <span className="type-row truncate">{actor.label}</span>
      {showKind && actor.kind !== "agent" && (
        <span className="type-meta shrink-0 uppercase tracking-wide text-muted-foreground-soft">
          {actor.kind}
        </span>
      )}
    </span>
  )
}
