"use client"

import { ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"

/** Collapsible section header with chevron toggle and optional action slot. */
export function SectionHeader({
  title,
  open,
  onToggle,
  action,
}: {
  title: string
  open: boolean
  onToggle: () => void
  action?: React.ReactNode
}) {
  return (
    <div className="group/sh flex items-center px-2 py-1.5">
      <button
        onClick={onToggle}
        className="flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground/80 hover:text-muted-foreground transition-colors"
      >
        <ChevronDown className={cn("h-2.5 w-2.5 transition-transform duration-200", !open && "-rotate-90")} />
        {title}
      </button>
      <div className="flex-1" />
      <div className="opacity-0 group-hover/sh:opacity-100 transition-opacity">
        {action}
      </div>
    </div>
  )
}

/** Label-value row used in side-panel property sections. */
export function PropertyRow({
  label,
  children,
  className,
}: {
  label?: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex items-center px-2 py-1 mx-1 rounded hover:bg-white/[0.03] transition-colors cursor-pointer overflow-hidden",
        className,
      )}
    >
      {label && (
        <span className="text-[11px] text-muted-foreground w-[72px] shrink-0">{label}</span>
      )}
      <span className="flex-1 flex items-center gap-[5px] justify-end text-[11.5px] text-foreground/80 min-w-0 truncate">
        {children}
      </span>
    </div>
  )
}
