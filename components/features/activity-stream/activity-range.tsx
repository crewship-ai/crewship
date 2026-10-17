"use client"

import * as React from "react"
import { CalendarDays, ChevronDown, ChevronLeft, ChevronRight, Globe } from "lucide-react"
import type { DateRange } from "react-day-picker"

import { Button } from "@/components/ui/button"
import { Calendar } from "@/components/ui/calendar"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  RANGE_PRESETS,
  rangeBounds,
  rangeDates,
  rangeLabel,
  stepRange,
  type RangeKey,
  type TimeRange,
} from "@/lib/activity-feed"
import { cn } from "@/lib/utils"

/**
 * The time range of Activity (Harbor 2): one joined control in the top bar,
 * ‹ [calendar · Last 7 days · 8 – 15 Oct ▾] ›. The arrows step to a shorter
 * or a longer range; the middle opens presets, a calendar, From / To and the
 * time zone. On a phone it is one chip above the heading.
 */
export function ActivityRange({
  value,
  onChange,
  now,
  compact = false,
}: {
  value: TimeRange
  onChange: (r: TimeRange) => void
  /** The clock the labels are computed against (tests pin it). */
  now: number
  /** Phone: one chip, no arrows. */
  compact?: boolean
}) {
  const [open, setOpen] = React.useState(false)
  const label = rangeLabel(value)
  const dates = rangeDates(value, now)
  const shorter = stepRange(value.key, -1)
  const longer = stepRange(value.key, 1)

  const trigger = compact ? (
    <button
      type="button"
      className="kit-tap inline-flex h-[var(--control-sm)] items-center gap-1.5 rounded-full border border-border bg-card px-3 text-label"
      aria-haspopup="dialog"
    >
      <CalendarDays className="size-3.5 text-primary" aria-hidden />
      <b className="font-semibold">{label}</b>
      <span className="text-muted-foreground">{dates}</span>
      <ChevronDown className="size-3 text-muted-foreground" aria-hidden />
    </button>
  ) : (
    <button
      type="button"
      data-state={open ? "open" : "closed"}
      className={cn(
        "kit-tap inline-flex h-full items-center gap-2 border-x border-border px-2.5 text-label text-foreground hover:bg-muted",
        open && "bg-primary/10",
      )}
      aria-haspopup="dialog"
    >
      <CalendarDays className="size-3.5 text-primary" aria-hidden />
      <b className="font-semibold">{label}</b>
      <span className="border-l border-border pl-2 tabular-nums text-muted-foreground">{dates}</span>
      <ChevronDown className="size-3 text-muted-foreground" aria-hidden />
    </button>
  )

  const popover = (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent align="end" className="w-[min(600px,calc(100vw-24px))] p-0">
        <RangePicker
          value={value}
          now={now}
          onApply={(r) => {
            onChange(r)
            setOpen(false)
          }}
          onCancel={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  )

  if (compact) return popover

  return (
    <span
      role="group"
      aria-label="Time range"
      className="inline-flex h-[var(--control-md)] items-stretch overflow-hidden rounded-lg border border-border bg-card shadow-xs"
    >
      <button
        type="button"
        className="kit-tap inline-flex items-center px-1.5 text-muted-foreground hover:bg-muted disabled:opacity-40"
        aria-label="Shorter range"
        disabled={!shorter}
        onClick={() => shorter && onChange({ key: shorter })}
      >
        <ChevronLeft className="size-3.5" aria-hidden />
      </button>
      {popover}
      <button
        type="button"
        className="kit-tap inline-flex items-center px-1.5 text-muted-foreground hover:bg-muted disabled:opacity-40"
        aria-label="Longer range"
        disabled={!longer}
        onClick={() => longer && onChange({ key: longer })}
      >
        <ChevronRight className="size-3.5" aria-hidden />
      </button>
    </span>
  )
}

const fmtField = (ms: number) =>
  new Date(ms).toLocaleString("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false })

/** `datetime-local` wants local wall-clock time, not ISO in UTC. */
function toLocalInput(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

/** Presets on the left, a calendar with From / To on the right. */
export function RangePicker({
  value,
  now,
  onApply,
  onCancel,
}: {
  value: TimeRange
  now: number
  onApply: (r: TimeRange) => void
  onCancel: () => void
}) {
  const initial = rangeBounds(value, now)
  const [from, setFrom] = React.useState<number>(initial.since)
  const [to, setTo] = React.useState<number>(initial.until ?? now)
  const zone = React.useMemo(() => {
    try {
      return Intl.DateTimeFormat().resolvedOptions().timeZone
    } catch {
      return ""
    }
  }, [])

  // The presets answer to the keys printed beside them: T, Y, W, 1h, 24h, 7d, 30d.
  const typed = React.useRef({ text: "", at: 0 })
  const onKeyDown = (e: React.KeyboardEvent) => {
    const t = e.target as HTMLElement
    if (t.closest("input, textarea")) return
    if (e.metaKey || e.ctrlKey || e.altKey || e.key.length !== 1) return
    const stamp = Date.now()
    const text = (stamp - typed.current.at < 900 ? typed.current.text : "") + e.key.toLowerCase()
    typed.current = { text, at: stamp }
    const hit = RANGE_PRESETS.find((p) => p.kbd.toLowerCase() === text)
    if (hit) {
      e.preventDefault()
      typed.current = { text: "", at: 0 }
      onApply({ key: hit.key })
    }
  }

  const selected: DateRange = { from: new Date(from), to: new Date(to) }

  return (
    <div onKeyDown={onKeyDown} data-slot="activity-range-picker">
      <div className="grid grid-cols-1 sm:grid-cols-[180px_minmax(0,1fr)]">
        <div
          role="listbox"
          aria-label="Presets"
          className="flex flex-wrap gap-1.5 border-b border-border p-2 sm:grid sm:content-start sm:gap-px sm:border-r sm:border-b-0 sm:p-1.5"
        >
          {RANGE_PRESETS.map((p) => {
            const on = value.key === p.key
            return (
              <button
                key={p.key}
                type="button"
                role="option"
                aria-selected={on}
                onClick={() => onApply({ key: p.key as RangeKey })}
                className={cn(
                  "kit-tap flex h-[var(--control-sm)] items-center justify-between gap-2 rounded-full border border-border px-2.5 text-left text-body sm:rounded-md sm:border-0",
                  on ? "bg-primary/10 font-semibold text-chip-info-fg" : "hover:bg-muted",
                )}
              >
                {p.label}
                <kbd className="hidden font-mono text-micro text-muted-foreground not-italic sm:inline">{p.kbd}</kbd>
              </button>
            )
          })}
        </div>
        <div className="grid gap-2.5 p-3">
          <Calendar
            mode="range"
            selected={selected}
            defaultMonth={new Date(to)}
            disabled={{ after: new Date(now) }}
            onSelect={(r) => {
              if (!r?.from) return
              const start = new Date(r.from)
              start.setHours(0, 0, 0, 0)
              setFrom(start.getTime())
              const end = new Date(r.to ?? r.from)
              end.setHours(23, 59, 59, 999)
              setTo(Math.min(end.getTime(), now))
            }}
            className="mx-auto p-0"
          />
          <div className="grid grid-cols-2 gap-2">
            <label className="grid gap-1 text-label text-muted-foreground">
              From
              <input
                type="datetime-local"
                aria-label="From"
                value={toLocalInput(from)}
                max={toLocalInput(now)}
                onChange={(e) => {
                  const ms = Date.parse(e.target.value)
                  if (!Number.isNaN(ms)) setFrom(ms)
                }}
                className="kit-tap h-[var(--control-sm)] rounded-md border border-control-border bg-card px-2 text-control text-foreground"
              />
            </label>
            <label className="grid gap-1 text-label text-muted-foreground">
              To
              <input
                type="datetime-local"
                aria-label="To"
                value={toLocalInput(to)}
                max={toLocalInput(now)}
                onChange={(e) => {
                  const ms = Date.parse(e.target.value)
                  if (!Number.isNaN(ms)) setTo(Math.min(ms, now))
                }}
                className="kit-tap h-[var(--control-sm)] rounded-md border border-control-border bg-card px-2 text-control text-foreground"
              />
            </label>
          </div>
          <p className="sr-only" aria-live="polite">
            {fmtField(from)} to {fmtField(to)}
          </p>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-t border-border bg-muted/40 px-2.5 py-2">
        {zone && (
          <span className="inline-flex items-center gap-1 rounded-full border border-border bg-card px-2 py-0.5 text-label text-muted-foreground" title="Times are shown in this time zone">
            <Globe className="size-3" aria-hidden />
            {zone}
          </span>
        )}
        <span className="flex-1" />
        <Button size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" onClick={() => onApply({ key: "custom", from: Math.min(from, to), to: Math.max(from, to) })}>
          Apply
        </Button>
      </div>
    </div>
  )
}
