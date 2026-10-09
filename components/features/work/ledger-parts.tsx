"use client"

// What the Work queue, Webhook deliveries and their rail draw alike (#3017):
// an agent with its own face, and a crew in its own colour and icon.

import * as React from "react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import type { LedgerAgent, LedgerCrew } from "@/hooks/use-work-items"
import { iconColorProps } from "@/lib/crew-icons"
import { isDeletedAgent } from "@/lib/work-ledger"
import { cn } from "@/lib/utils"

export function LedgerAvatar({ agent, className }: { agent: LedgerAgent | null | undefined; className?: string }) {
  if (!agent) {
    return <span aria-hidden className={cn("inline-block shrink-0 rounded-full border border-dashed border-muted-foreground/50", className)} />
  }
  const deleted = isDeletedAgent(agent)
  return (
    <AgentAvatar
      seed={agent.avatar_seed || agent.id}
      style={agent.avatar_style || undefined}
      avatarUrl={agent.avatar_url}
      // The stored render is backfilled only for a live agent: a deleted one
      // has no row to store against (the server answers 404).
      agentId={deleted ? undefined : agent.id}
      alt=""
      // A deleted agent keeps its face, greyed: the work was still theirs.
      className={cn("shrink-0 rounded-full", deleted && "opacity-50 grayscale", className)}
    />
  )
}

/** The crew in its own colour and icon, as the rest of the product draws it. */
export function CrewChip({ crew, className }: { crew: LedgerCrew; className?: string }) {
  const glyph = iconColorProps(crew.color)
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1", className)}>
      <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="h-4 w-4 rounded [&_svg]:h-2.5 [&_svg]:w-2.5" />
      <span className={cn("truncate text-[10.5px]", glyph.className)} style={glyph.style}>
        {crew.name}
      </span>
    </span>
  )
}
