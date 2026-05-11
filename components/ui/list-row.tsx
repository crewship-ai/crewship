"use client"

import * as React from "react"
import { motion } from "motion/react"
import { cn } from "@/lib/utils"
import { selection } from "@/lib/interaction"
import { listRow } from "@/lib/motion"

type ListRowElement = "li" | "div"

interface ListRowProps {
  selected?: boolean
  onSelect?: () => void
  as?: ListRowElement
  className?: string
  children: React.ReactNode
  id?: string
  "aria-label"?: string
  "data-testid"?: string
  /** Style overrides — keep narrow; most styling should be className. */
  style?: React.CSSProperties
  /** Set to false on rare hot paths where layout animation is too expensive. */
  animateLayout?: boolean
}

// ListRow — the canonical interactive row primitive.
// Selection + hover styles come from lib/interaction.ts (single source
// of truth). `layout` makes selection transitions slide rather than
// snap when adjacent rows reflow (e.g. inbox state changes).
export function ListRow({
  selected = false,
  onSelect,
  as = "li",
  className,
  children,
  animateLayout = true,
  ...rest
}: ListRowProps) {
  const Comp = as === "li" ? motion.li : motion.div

  const handleKey = (e: React.KeyboardEvent<HTMLElement>) => {
    if (!onSelect) return
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault()
      onSelect()
    }
  }

  return (
    <Comp
      layout={animateLayout ? listRow.layout : false}
      transition={listRow.transition}
      role={onSelect ? "button" : undefined}
      tabIndex={onSelect ? 0 : undefined}
      onClick={onSelect}
      onKeyDown={onSelect ? handleKey : undefined}
      aria-pressed={onSelect ? selected : undefined}
      data-selected={selected || undefined}
      className={cn(
        selected ? selection.row.selected : selection.row.default,
        className,
      )}
      {...rest}
    >
      {children}
    </Comp>
  )
}
