"use client"

import { motion, AnimatePresence } from "motion/react"

import { spring } from "@/lib/motion"
import { cn } from "@/lib/utils"
import type { ReactionEntry } from "@/stores/reactions-store"

interface ReactionsRowProps {
  /** Server-shaped tallies: total count plus whether the current user is
   *  one of the reactors. `mine` drives both the highlight and what a
   *  click means (retract mine vs. add mine on top of a teammate's). */
  reactions: Record<string, ReactionEntry>
  onToggle: (emoji: string) => void
  className?: string
}

export function ReactionsRow({
  reactions,
  onToggle,
  className,
}: ReactionsRowProps) {
  const entries = Object.entries(reactions)
  if (!entries.length) return null

  return (
    <div className={cn("flex flex-wrap items-center gap-1", className)}>
      <AnimatePresence initial={false}>
        {entries.map(([emoji, { count, mine }]) => (
          <motion.button
            key={emoji}
            type="button"
            layout
            initial={{ opacity: 0, scale: 0.6 }}
            animate={{ opacity: 1, scale: 1 }}
            exit={{ opacity: 0, scale: 0.6 }}
            transition={spring.bouncy}
            onClick={() => onToggle(emoji)}
            aria-pressed={mine}
            className={cn(
              "inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-xs transition-colors",
              mine
                ? "border-primary/40 bg-primary/10 hover:bg-primary/20"
                : "bg-muted/40 hover:bg-muted/80",
            )}
            aria-label={`${emoji} ${count}`}
          >
            <span className="text-sm leading-none">{emoji}</span>
            <span className="tabular-nums text-[10px] text-muted-foreground">
              {count}
            </span>
          </motion.button>
        ))}
      </AnimatePresence>
    </div>
  )
}
