"use client"

import { CONCEPT_ICON } from "@/lib/concept-icons"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import type { IncomingTarget } from "./incoming-model"

/** Match the target's identity on its own screen; status is rendered separately. */
export function IncomingTargetAvatar({ target }: { target: IncomingTarget }) {
  if (target.kind === "agent") {
    return (
      <AgentAvatar
        seed={target.avatar_seed || target.name}
        style={target.avatar_style || target.crew?.avatar_style}
        agentId={target.id}
        avatarUrl={target.avatar_url}
        className="size-6 shrink-0 rounded-lg"
      />
    )
  }
  if (target.kind === "routine") {
    return (
      <CrewIcon
        icon={resolveRoutineIcon(target)}
        color={resolveRoutineColor(target)}
        size="sm"
        className="size-6"
      />
    )
  }
  return (
    <CONCEPT_ICON.pages
      className="size-5 shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  )
}
