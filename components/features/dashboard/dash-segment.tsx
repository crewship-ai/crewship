"use client"

import * as React from "react"

import { cn } from "@/lib/utils"

/**
 * A segmented switch (Harbor 2 `.seg`): a sunken track, the chosen option
 * raised. Toggle buttons with aria-pressed rather than tabs, because the
 * options filter one view instead of swapping panels.
 *
 * Kit request: the shared kit has no segment yet; this is the Dashboard's
 * until one lands in components/ui.
 */
export function DashSegment<T extends string>({
  options,
  value,
  onChange,
  label,
  className,
}: {
  options: { value: T; label: React.ReactNode; count?: number }[]
  value: T
  onChange: (value: T) => void
  label: string
  className?: string
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className={cn("inline-flex shrink-0 items-center gap-0.5 rounded-md border border-border bg-muted p-0.5", className)}
    >
      {options.map((option) => {
        const on = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={on}
            onClick={() => onChange(option.value)}
            className={cn(
              "inline-flex h-6 items-center gap-1.5 whitespace-nowrap rounded-sm px-2.5 text-control transition-colors coarse:min-h-11",
              "focus-visible:outline-2 focus-visible:outline-primary",
              on ? "bg-card font-medium text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {option.label}
            {option.count != null && <span className="font-mono text-label tabular-nums text-muted-foreground">{option.count}</span>}
          </button>
        )
      })}
    </div>
  )
}
