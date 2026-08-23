"use client"

import { Sparkles } from "lucide-react"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import { cn } from "@/lib/utils"
import type { AgentPersona } from "@/lib/entities"

interface PersonaChipProps {
  persona: AgentPersona
  active: boolean
  onClick: () => void
}

/** Small pill with the persona's avatar + name + role title.
 *  Renders in the top "templates" row of the create-agent dialog. */
export function PersonaChip({ persona, active, onClick }: PersonaChipProps) {
  // Upgrade lazy-loaded DiceBear styles from placeholder to real avatar.
  useAvatarStylesVersion()
  return (
    <button
      type="button"
      onClick={onClick}
      title={`${persona.name} — ${persona.roleTitle}`}
      aria-pressed={active}
      aria-label={`${persona.name} — ${persona.roleTitle}${active ? ", selected" : ""}`}
      className={cn(
        // `max-sm:min-h-12`: unpadded, this pill's own content (a 22px avatar
        // plus one line of 12px text) tops out around 31px tall — comfortably
        // clickable with a mouse, short of the 44px floor with a thumb. A
        // `min-h` rather than more padding keeps the pill's visual size
        // (avatar, text) untouched and just grows the box the row's
        // `items-center` already centers it in.
        "shrink-0 inline-flex items-center gap-2 rounded-full pl-1 pr-3 py-1 border text-[12px] transition-colors max-sm:min-h-12",
        active
          ? "bg-primary/15 border-primary/45 text-primary"
          : "bg-card-2 border-white/[0.08] text-foreground/85 hover:border-white/[0.15] hover:bg-white/[0.03]",
      )}
    >
      <span className="w-[22px] h-[22px] rounded-full overflow-hidden border border-white/[0.10] bg-muted shrink-0">
        <img
          src={getAgentAvatarUrl(persona.suggestedSlug, persona.avatarStyle)}
          alt=""
          className="w-full h-full"
        />
      </span>
      <span className="font-medium">{persona.name}</span>
      <span className={cn("text-[10.5px]", active ? "text-primary/75" : "text-muted-foreground")}>
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
      aria-pressed={active}
      aria-label={`Start blank — no template${active ? ", selected" : ""}`}
      className={cn(
        "shrink-0 inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 border text-[12px] transition-colors max-sm:min-h-12",
        active
          ? "bg-primary/15 border-primary/45 text-primary"
          : "bg-transparent border-white/[0.10] border-dashed text-muted-foreground hover:border-white/[0.20] hover:text-foreground/80",
      )}
    >
      <Sparkles className="h-3 w-3" />
      Blank
    </button>
  )
}
