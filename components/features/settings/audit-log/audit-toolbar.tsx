"use client"

import { useEffect, useState } from "react"
import { CalendarRange, Check, Search, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { cn } from "@/lib/utils"

import {
  AUDIT_CATEGORIES,
  AUDIT_RANGES,
  DEFAULT_AUDIT_FILTERS,
  activeFilterChips,
  rangeLabel,
  sourceSupports,
  type AuditFilters,
} from "./audit-filters"
import { controlHeight } from "@/components/features/settings/shared"

export interface AuditPerson {
  id: string
  label: string
}

const SEARCH_DEBOUNCE_MS = 300

/**
 * The Audit log's filter bar: server-side search, a time range (presets or a
 * custom from–to), person and type, and the active filters as removable
 * chips. Filters a trail does not honour are not offered on that trail —
 * see sourceSupports() in audit-filters.ts.
 */
export function AuditToolbar({
  filters,
  onChange,
  people,
}: {
  filters: AuditFilters
  onChange: (next: Partial<AuditFilters>) => void
  people: AuditPerson[]
}) {
  const supports = sourceSupports(filters.source)
  const peopleById = Object.fromEntries(people.map((p) => [p.id, p.label]))
  const chips = activeFilterChips(filters, peopleById)

  return (
    <div className="border-b border-border/60">
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        {supports.search && <AuditSearch value={filters.q} onChange={(q) => onChange({ q })} />}
        <TimeRangePicker filters={filters} onChange={onChange} />
        {supports.person && (
          <Select value={filters.userId || "__all"} onValueChange={(v) => onChange({ userId: v === "__all" ? "" : v })}>
            <SelectTrigger aria-label="Person" className={cn(controlHeight, "w-[170px]")}>
              <SelectValue placeholder="Everyone" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all" className="text-xs">Everyone</SelectItem>
              {people.map((p) => (
                <SelectItem key={p.id} value={p.id} className="text-xs">{p.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {supports.category && (
          <Select value={filters.category} onValueChange={(category) => onChange({ category })}>
            <SelectTrigger aria-label="Type" className={cn(controlHeight, "w-[140px]")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {AUDIT_CATEGORIES.map((c) => (
                <SelectItem key={c.value} value={c.value} className="text-xs">{c.value === "all" ? "All types" : c.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {!supports.search && (
          <span className="text-label text-muted-foreground">This trail can be narrowed by time only.</span>
        )}
      </div>

      {chips.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 px-4 pb-3" aria-label="Active filters">
          {chips.map((chip) => (
            <button
              key={chip.key}
              type="button"
              onClick={() =>
                onChange(
                  chip.key === "range"
                    ? { range: DEFAULT_AUDIT_FILTERS.range, from: "", to: "" }
                    : { [chip.key]: DEFAULT_AUDIT_FILTERS[chip.key] },
                )
              }
              className="inline-flex h-6 items-center gap-1 rounded-full border border-border bg-surface-subtle pl-2.5 pr-1.5 text-micro text-foreground transition-colors hover:border-line-strong"
              aria-label={`Remove filter ${chip.label}`}
            >
              {chip.label}
              <X className="h-3 w-3 text-muted-foreground" aria-hidden />
            </button>
          ))}
          <button
            type="button"
            onClick={() => onChange({ ...DEFAULT_AUDIT_FILTERS, source: filters.source })}
            className="ml-1 text-micro font-medium text-primary-hover hover:underline"
          >
            Clear all
          </button>
        </div>
      )}
    </div>
  )
}

/** Search the whole trail on the server, not just the loaded page. */
function AuditSearch({ value, onChange }: { value: string; onChange: (q: string) => void }) {
  const [draft, setDraft] = useState(value)
  // Follow outside changes (Clear all, back button).
  useEffect(() => setDraft(value), [value])
  useEffect(() => {
    if (draft === value) return
    const t = setTimeout(() => onChange(draft), SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(t)
  }, [draft, value, onChange])
  return (
    <div className="relative min-w-[200px] flex-1 sm:max-w-[320px]">
      <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden />
      <Input
        type="search"
        aria-label="Search the audit log"
        placeholder="Search action, type or person…"
        className={cn(controlHeight, "pl-8")}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
      />
    </div>
  )
}

/** Presets for the common questions, a from–to range for everything else. */
function TimeRangePicker({ filters, onChange }: { filters: AuditFilters; onChange: (next: Partial<AuditFilters>) => void }) {
  const [open, setOpen] = useState(false)
  const [from, setFrom] = useState(filters.from)
  const [to, setTo] = useState(filters.to)
  useEffect(() => {
    if (open) {
      setFrom(filters.from)
      setTo(filters.to)
    }
  }, [open, filters.from, filters.to])
  const invalid = Boolean(from && to && from > to)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className="h-8 gap-2 text-xs font-normal" aria-label={`Time range: ${rangeLabel(filters)}`}>
          <CalendarRange className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
          {rangeLabel(filters)}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-0">
        <div className="p-1.5" role="listbox" aria-label="Time range presets">
          {AUDIT_RANGES.map((r) => {
            const selected = filters.range === r.value
            return (
              <button
                key={r.value}
                type="button"
                role="option"
                aria-selected={selected}
                onClick={() => {
                  onChange({ range: r.value, from: "", to: "" })
                  setOpen(false)
                }}
                className={cn(
                  "flex w-full items-center justify-between rounded-md px-2.5 py-1.5 text-left text-xs transition-colors hover:bg-accent",
                  selected && "bg-accent font-medium",
                )}
              >
                {r.label}
                {selected && <Check className="h-3.5 w-3.5 text-primary-hover" aria-hidden />}
              </button>
            )
          })}
        </div>
        <form
          className="space-y-2.5 border-t border-border p-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (invalid || (!from && !to)) return
            onChange({ range: "custom", from, to })
            setOpen(false)
          }}
        >
          <div className="eyebrow text-muted-foreground">Custom range</div>
          <div className="grid grid-cols-2 gap-2">
            <label className="space-y-1 text-micro text-muted-foreground">
              From
              <Input type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} className={controlHeight} aria-label="From date" />
            </label>
            <label className="space-y-1 text-micro text-muted-foreground">
              To
              <Input type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} className={controlHeight} aria-label="To date" />
            </label>
          </div>
          {invalid && <p className="text-micro text-destructive">The start is after the end.</p>}
          <Button type="submit" size="sm" className="h-8 w-full text-xs" disabled={invalid || (!from && !to)}>
            Apply range
          </Button>
          <p className="text-micro text-muted-foreground">Both days are included. Times are UTC.</p>
        </form>
      </PopoverContent>
    </Popover>
  )
}
