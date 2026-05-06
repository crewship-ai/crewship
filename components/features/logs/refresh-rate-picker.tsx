"use client"

import { Zap } from "lucide-react"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

/**
 * Refresh cadence for the journal view.
 *   "live"  → pure SSE, no extra polling. Backend pushes; frontend
 *             reacts. The default.
 *   "off"   → no auto-update. SSE still arrives but the toolbar pill
 *             label flips to "Off" so the user knows nothing is being
 *             pulled in the background.
 *   "5s" / "10s" / "30s" / "1m" → poll the journal endpoint at that
 *             cadence on top of SSE. Mostly a safety net for flaky
 *             SSE connections; default is `live` to keep backend
 *             load minimal.
 */
export type RefreshRate = "live" | "off" | "5s" | "10s" | "30s" | "1m"

interface RefreshRatePickerProps {
  value: RefreshRate
  onChange: (v: RefreshRate) => void
}

const OPTIONS: Array<{ value: RefreshRate; label: string }> = [
  { value: "live", label: "Live (SSE)" },
  { value: "5s", label: "Every 5s" },
  { value: "10s", label: "Every 10s" },
  { value: "30s", label: "Every 30s" },
  { value: "1m", label: "Every 1m" },
  { value: "off", label: "Off" },
]

const SHORT_LABEL: Record<RefreshRate, string> = {
  live: "Live",
  off: "Off",
  "5s": "5s",
  "10s": "10s",
  "30s": "30s",
  "1m": "1m",
}

export function RefreshRatePicker({ value, onChange }: RefreshRatePickerProps) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as RefreshRate)}>
      <SelectTrigger
        size="sm"
        className="h-7 px-2 text-[11px] gap-1 font-mono [&>svg]:opacity-60"
        aria-label="Refresh rate"
      >
        <Zap className="h-3 w-3 opacity-70" />
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

/** Convert a refresh rate to milliseconds; null = no polling. */
export function refreshRateMs(r: RefreshRate): number | null {
  switch (r) {
    case "5s": return 5_000
    case "10s": return 10_000
    case "30s": return 30_000
    case "1m": return 60_000
    case "off":
    case "live":
    default: return null
  }
}
