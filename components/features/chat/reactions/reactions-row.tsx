"use client"

import { motion, AnimatePresence } from "motion/react"

import { spring } from "@/lib/motion"
import { cn } from "@/lib/utils"

interface ReactionsRowProps {
  reactions: Record<string, number>
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
        {entries.map(([emoji, count]) => (
          <motion.button
            key={emoji}
            type="button"
            layout
            initial={{ opacity: 0, scale: 0.6 }}
            animate={{ opacity: 1, scale: 1 }}
            exit={{ opacity: 0, scale: 0.6 }}
            transition={spring.bouncy}
            onClick={() => onToggle(emoji)}
            className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-1.5 py-0.5 text-xs hover:bg-muted/80 transition-colors"
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
