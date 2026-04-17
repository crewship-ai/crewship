"use client"

import { cn } from "@/lib/utils"
import { PAYMASTER_RANGES, type PaymasterRange } from "@/lib/types/paymaster"

interface TimeRangePickerProps {
  value: PaymasterRange
  onChange: (next: PaymasterRange) => void
  className?: string
}

/**
 * Segmented 1h / 24h / 7d / 30d control. Keyed by the backend's literal
 * range tokens so values round-trip without translation.
 */
export function TimeRangePicker({ value, onChange, className }: TimeRangePickerProps) {
  return (
    <div
      role="radiogroup"
      aria-label="Time range"
      className={cn(
        "inline-flex items-center rounded-md border border-border/60 bg-card p-0.5",
        className,
      )}
    >
      {PAYMASTER_RANGES.map((r) => {
        const active = r === value
        return (
          <button
            key={r}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(r)}
            className={cn(
              "h-6 px-2.5 text-[11px] font-mono uppercase tracking-wider rounded transition-colors",
              active
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {r}
          </button>
        )
      })}
    </div>
  )
}
