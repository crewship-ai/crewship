"use client"

import { Clock } from "lucide-react"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export type TimeRange = "5m" | "15m" | "1h" | "24h" | "7d" | "30d" | "all"

const OPTIONS: Array<{ value: TimeRange; label: string }> = [
  { value: "5m", label: "Last 5 minutes" },
  { value: "15m", label: "Last 15 minutes" },
  { value: "1h", label: "Last 1 hour" },
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
  { value: "all", label: "All time" },
]

const SHORT_LABEL: Record<TimeRange, string> = {
  "5m": "5m",
  "15m": "15m",
  "1h": "1h",
  "24h": "24h",
  "7d": "7d",
  "30d": "30d",
  all: "All",
}

interface TimeRangePickerProps {
  value: TimeRange
  onChange: (v: TimeRange) => void
}

/**
 * Compact preset time-range select for the LogsPanel toolbar.
 * Presets only — custom-range picker can be layered on later.
 */
export function TimeRangePicker({ value, onChange }: TimeRangePickerProps) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as TimeRange)}>
      <SelectTrigger
        size="sm"
        className="h-7 px-2 text-[11px] gap-1 font-mono [&>svg]:opacity-60"
        aria-label="Time range"
      >
        <Clock className="h-3 w-3 opacity-70" />
        <SelectValue>{SHORT_LABEL[value]}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        {OPTIONS.map((o) => (
          <SelectItem key={o.value} value={o.value} className="text-xs">
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** Convert a TimeRange preset into an RFC3339 `since` string (or undefined for "all"). */
export function sinceFromTimeRange(range: TimeRange): string | undefined {
  const now = Date.now()
  switch (range) {
    case "5m": return new Date(now - 5 * 60 * 1000).toISOString()
    case "15m": return new Date(now - 15 * 60 * 1000).toISOString()
    case "1h": return new Date(now - 60 * 60 * 1000).toISOString()
    case "24h": return new Date(now - 24 * 60 * 60 * 1000).toISOString()
    case "7d": return new Date(now - 7 * 24 * 60 * 60 * 1000).toISOString()
    case "30d": return new Date(now - 30 * 24 * 60 * 60 * 1000).toISOString()
    default: return undefined
  }
}
