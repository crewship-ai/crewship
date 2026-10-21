"use client"

import { useEffect, useState, type ReactNode } from "react"
import { Clock } from "lucide-react"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { Button } from "@/components/ui/button"

export type TimeRange =
  | "5m"
  | "15m"
  | "1h"
  | "24h"
  | "7d"
  | "30d"
  | "90d"
  | "all"
  | "custom"

export interface CustomRange {
  fromMs: number
  toMs: number
}

const PRESETS: Array<{ value: TimeRange; label: string }> = [
  { value: "5m", label: "Last 5 minutes" },
  { value: "15m", label: "Last 15 minutes" },
  { value: "1h", label: "Last 1 hour" },
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
  { value: "all", label: "All time" },
  { value: "custom", label: "Custom…" },
]

const SHORT_LABEL: Record<Exclude<TimeRange, "custom">, string> = {
  "5m": "5m",
  "15m": "15m",
  "1h": "1h",
  "24h": "24h",
  "7d": "7d",
  "30d": "30d",
  "90d": "90d",
  all: "All",
}

interface TimeRangePickerProps {
  value: TimeRange
  onChange: (v: TimeRange) => void
  customRange?: CustomRange | null
  onCustomRangeChange?: (r: CustomRange) => void
  /** The presets offered, in order; the Journal's set by default. A page
   *  whose server reads further back (Audit log: 90 days) passes its own. */
  presets?: TimeRange[]
  /** What "Custom…" opens instead of the from/to form — the Audit log's
   *  month calendar. `close` dismisses the popover. */
  renderCustom?: (close: () => void) => ReactNode
  /** The trigger's text while a custom range is active, when the page
   *  formats it itself ("5 Oct – 11 Oct"). */
  customLabel?: string
  /** Sizing for the trigger — the Journal's toolbar is h-7, a sub-bar h-8. */
  triggerClassName?: string
}

const LONG_LABEL: Record<TimeRange, string> = { ...Object.fromEntries(PRESETS.map((p) => [p.value, p.label])), "90d": "Last 90 days" } as Record<TimeRange, string>

/**
 * Compact time-range select for the LogsPanel toolbar. Presets cover
 * the 99% case; selecting "Custom…" opens a popover with absolute
 * datetime inputs so the user can pick an arbitrary [from, to] window.
 */
export function TimeRangePicker({
  value,
  onChange,
  customRange,
  onCustomRangeChange,
  presets,
  renderCustom,
  customLabel,
  triggerClassName,
}: TimeRangePickerProps) {
  const [customOpen, setCustomOpen] = useState(false)
  const options = presets ? presets.map((v) => ({ value: v, label: LONG_LABEL[v] })) : PRESETS

  const triggerLabel =
    value === "custom" && customLabel
      ? customLabel
      : value === "custom" && customRange
        ? formatCustomLabel(customRange)
        : SHORT_LABEL[(value === "custom" ? "1h" : value) as Exclude<TimeRange, "custom">]

  const handlePreset = (next: string) => {
    if (next === "custom") {
      // After the menu has closed: the Select dismisses on the same pointer
      // event, and the popover opened inside it read that as an outside click
      // and closed again at once.
      setTimeout(() => setCustomOpen(true), 0)
      return
    }
    onChange(next as TimeRange)
  }

  return (
    <Popover open={customOpen} onOpenChange={setCustomOpen}>
      <PopoverTrigger asChild>
        <div className="inline-flex">
          <Select value={value} onValueChange={handlePreset}>
            <SelectTrigger
              size="sm"
              className={triggerClassName ?? "h-7 px-2 text-[11px] gap-1 font-mono [&>svg]:opacity-60"}
              aria-label="Time range"
            >
              <Clock className="h-3 w-3 opacity-70" />
              <SelectValue>{triggerLabel}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {options.map((o) => (
                <SelectItem key={o.value} value={o.value} className="text-xs">
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </PopoverTrigger>
      <PopoverContent align={renderCustom ? "end" : "start"} className={renderCustom ? "w-80 p-3" : "w-72 p-3"}>
        {renderCustom ? renderCustom(() => setCustomOpen(false)) : <CustomRangeForm
          initial={customRange}
          onCancel={() => setCustomOpen(false)}
          onApply={(r) => {
            onCustomRangeChange?.(r)
            onChange("custom")
            setCustomOpen(false)
          }}
        />}
      </PopoverContent>
    </Popover>
  )
}

function CustomRangeForm({
  initial,
  onCancel,
  onApply,
}: {
  initial: CustomRange | null | undefined
  onCancel: () => void
  onApply: (r: CustomRange) => void
}) {
  const [from, setFrom] = useState<string>(() =>
    initial ? toLocalInput(initial.fromMs) : toLocalInput(Date.now() - 60 * 60 * 1000),
  )
  const [to, setTo] = useState<string>(() =>
    initial ? toLocalInput(initial.toMs) : toLocalInput(Date.now()),
  )
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (initial) {
      setFrom(toLocalInput(initial.fromMs))
      setTo(toLocalInput(initial.toMs))
    }
  }, [initial])

  const handleApply = () => {
    const fMs = fromLocalInput(from)
    const tMs = fromLocalInput(to)
    if (!Number.isFinite(fMs) || !Number.isFinite(tMs)) {
      setError("Invalid date format")
      return
    }
    if (fMs >= tMs) {
      setError("From must be earlier than To")
      return
    }
    setError(null)
    onApply({ fromMs: fMs, toMs: tMs })
  }

  return (
    <div className="space-y-3">
      <div className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
        Custom range
      </div>
      <div className="space-y-2">
        <Field label="From">
          <input
            type="datetime-local"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            className="h-7 w-full text-xs font-mono px-2 rounded border border-border/60 bg-background text-foreground"
          />
        </Field>
        <Field label="To">
          <input
            type="datetime-local"
            value={to}
            onChange={(e) => setTo(e.target.value)}
            className="h-7 w-full text-xs font-mono px-2 rounded border border-border/60 bg-background text-foreground"
          />
        </Field>
      </div>
      {error && <div className="text-[11px] text-destructive">{error}</div>}
      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" className="h-7 text-xs" onClick={handleApply}>
          Apply
        </Button>
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="block text-[10px] uppercase tracking-wider text-muted-foreground mb-0.5">
        {label}
      </span>
      {children}
    </label>
  )
}

function toLocalInput(ms: number): string {
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fromLocalInput(s: string): number {
  return new Date(s).getTime()
}

function formatCustomLabel(r: CustomRange): string {
  const f = new Date(r.fromMs)
  const t = new Date(r.toMs)
  const sameDay =
    f.getFullYear() === t.getFullYear() &&
    f.getMonth() === t.getMonth() &&
    f.getDate() === t.getDate()
  const fmtDay = (d: Date) =>
    `${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`
  const fmtTime = (d: Date) =>
    `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
  return sameDay
    ? `${fmtTime(f)}–${fmtTime(t)}`
    : `${fmtDay(f)} ${fmtTime(f)} → ${fmtDay(t)} ${fmtTime(t)}`
}

/** Convert a TimeRange preset into an RFC3339 `since` string. */
export function sinceFromTimeRange(
  range: TimeRange,
  customRange?: CustomRange | null,
): string | undefined {
  const now = Date.now()
  switch (range) {
    case "5m": return new Date(now - 5 * 60 * 1000).toISOString()
    case "15m": return new Date(now - 15 * 60 * 1000).toISOString()
    case "1h": return new Date(now - 60 * 60 * 1000).toISOString()
    case "24h": return new Date(now - 24 * 60 * 60 * 1000).toISOString()
    case "7d": return new Date(now - 7 * 24 * 60 * 60 * 1000).toISOString()
    case "30d": return new Date(now - 30 * 24 * 60 * 60 * 1000).toISOString()
    case "90d": return new Date(now - 90 * 24 * 60 * 60 * 1000).toISOString()
    case "custom":
      return customRange ? new Date(customRange.fromMs).toISOString() : undefined
    default: return undefined
  }
}

/** Upper bound for a range — used by the histogram to set its window. */
export function untilFromTimeRange(
  range: TimeRange,
  customRange?: CustomRange | null,
): number {
  if (range === "custom" && customRange) return customRange.toMs
  return Date.now()
}
