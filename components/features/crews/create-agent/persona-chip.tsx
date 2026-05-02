"use client"

import { Sparkles } from "lucide-react"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { cn } from "@/lib/utils"
import type { AgentPersona } from "@/lib/agent-personas"

interface PersonaChipProps {
  persona: AgentPersona
  active: boolean
  onClick: () => void
}

/** Small pill with the persona's avatar + name + role title.
 *  Renders in the top "templates" row of the create-agent dialog. */
export function PersonaChip({ persona, active, onClick }: PersonaChipProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={`${persona.name} — ${persona.roleTitle}`}
      className={cn(
        "shrink-0 inline-flex items-center gap-2 rounded-full pl-1 pr-3 py-1 border text-[12px] transition-colors",
        active
          ? "bg-blue-500/15 border-blue-400/45 text-blue-300"
          : "bg-card-2 border-white/[0.08] text-foreground/85 hover:border-white/[0.15] hover:bg-white/[0.03]",
      )}
    >
      <span className="w-[22px] h-[22px] rounded-full overflow-hidden border border-white/[0.10] bg-zinc-900 shrink-0">
        <img
          src={getAgentAvatarUrl(persona.suggestedSlug, persona.avatarStyle)}
          alt=""
          className="w-full h-full"
        />
      </span>
      <span className="font-medium">{persona.name}</span>
      <span className={cn("text-[10.5px]", active ? "text-blue-400/75" : "text-muted-foreground")}>
        {persona.roleTitle}
      </span>
    </button>
  )
}

/** "Blank" alternative — shown after the persona chips. Picks no template. */
export function BlankChip({ active, onClick }: { active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title="Skip the template — start blank"
      className={cn(
        "shrink-0 inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 border text-[12px] transition-colors",
        active
          ? "bg-blue-500/15 border-blue-400/45 text-blue-300"
          : "bg-transparent border-white/[0.10] border-dashed text-muted-foreground hover:border-white/[0.20] hover:text-foreground/80",
      )}
    >
      <Sparkles className="h-3 w-3" />
      Blank
    </button>
  )
}
