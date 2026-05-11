"use client"

import * as React from "react"
import { motion, AnimatePresence } from "motion/react"
import { X } from "lucide-react"
import { cn } from "@/lib/utils"
import { panel } from "@/lib/motion"

type Side = "right" | "left"

interface SidePanelProps {
  open: boolean
  onClose: () => void
  side?: Side
  width?: number
  className?: string
  /** Accessible label — required for screen readers. */
  ariaLabel: string
  children: React.ReactNode
}

// SidePanel — right/left detail panel with spring slide animation.
// Modeled on the trace-side-panel.tsx pattern (the only previously
// well-animated panel). ESC dismisses; no backdrop dim by design —
// the list behind stays visible and clickable so users can hop between
// items without dismissing the panel each time.
export function SidePanel({
  open,
  onClose,
  side = "right",
  width = 420,
  className,
  ariaLabel,
  children,
}: SidePanelProps) {
  React.useEffect(() => {
    if (!open) return
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose()
    }
    window.addEventListener("keydown", handleKey)
    return () => window.removeEventListener("keydown", handleKey)
  }, [open, onClose])

  const motionConfig = side === "right" ? panel.side : panel.sideLeft
  const borderClass = side === "right" ? "border-l" : "border-r"

  return (
    <AnimatePresence>
      {open && (
        <motion.aside
          role="complementary"
          aria-label={ariaLabel}
          initial={motionConfig.initial}
          animate={motionConfig.animate}
          exit={motionConfig.exit}
          transition={motionConfig.transition}
          style={{ width }}
          className={cn(
            "flex h-full flex-col bg-card",
            borderClass,
            "border-white/[0.06]",
            className,
          )}
        >
          {children}
        </motion.aside>
      )}
    </AnimatePresence>
  )
}

interface SidePanelHeaderProps {
  onClose?: () => void
  title?: React.ReactNode
  subtitle?: React.ReactNode
  className?: string
  children?: React.ReactNode
}

export function SidePanelHeader({
  onClose,
  title,
  subtitle,
  className,
  children,
}: SidePanelHeaderProps) {
  return (
    <div
      className={cn(
        "flex shrink-0 items-center gap-2 border-b border-white/[0.06] px-3 py-2",
        className,
      )}
    >
      {onClose && (
        <button
          type="button"
          onClick={onClose}
          aria-label="Close detail"
          className="rounded p-1 text-muted-foreground/50 hover:text-foreground"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      )}
      {(title || subtitle) && (
        <div className="min-w-0 flex-1">
          {title && <div className="truncate text-sm font-medium">{title}</div>}
          {subtitle && (
            <div className="truncate text-[10px] uppercase tracking-wider text-muted-foreground/60">
              {subtitle}
            </div>
          )}
        </div>
      )}
      {children}
    </div>
  )
}

export function SidePanelBody({
  className,
  children,
}: {
  className?: string
  children: React.ReactNode
}) {
  return (
    <div className={cn("flex-1 overflow-y-auto", className)}>{children}</div>
  )
}

export function SidePanelFooter({
  className,
  children,
}: {
  className?: string
  children: React.ReactNode
}) {
  return (
    <div
      className={cn(
        "shrink-0 border-t border-white/[0.06] px-3 py-2",
        className,
      )}
    >
      {children}
    </div>
  )
}
