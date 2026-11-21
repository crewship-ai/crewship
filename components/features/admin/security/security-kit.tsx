"use client"

import * as React from "react"
import { Server } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * Small pieces the Security page and the Keeper cards share. Local to this
 * area: the kit has `Segmented` for short equal options and `ChoiceTile` for
 * pressed tiles, but not a radio list whose options carry a sentence each.
 */

export interface Choice<T extends string> {
  value: T
  title: string
  description?: string
}

/**
 * One choice from a few that each need a sentence: a radio group, so the
 * arrow keys and a screen reader treat it as the single choice it is.
 */
export function OptionList<T extends string>({ label, value, onChange, options, disabled, className, "data-testid": testId }: {
  label: string
  value: T
  onChange: (v: T) => void
  options: readonly Choice<T>[]
  disabled?: boolean
  className?: string
  "data-testid"?: string
}) {
  return (
    <div role="radiogroup" aria-label={label} data-testid={testId} className={cn("flex flex-col gap-1.5 px-4 py-3", className)}>
      {options.map((o) => {
        const on = o.value === value
        return (
          <button key={o.value} type="button" role="radio" aria-checked={on} disabled={disabled} onClick={() => onChange(o.value)}
            className={cn("flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors coarse:min-h-[44px] disabled:cursor-not-allowed disabled:opacity-60",
              on ? "border-primary bg-primary/5 ring-2 ring-primary/15" : "border-border bg-card hover:border-line-strong")}>
            <span aria-hidden className={cn("mt-0.5 grid size-4 shrink-0 place-items-center rounded-full border", on ? "border-primary" : "border-control-border")}>
              {on && <span className="size-2 rounded-full bg-primary" />}
            </span>
            <span className="min-w-0 flex-1">
              <span className="block text-control font-medium text-foreground">{o.title}</span>
              {o.description && <span className="mt-0.5 block text-label leading-snug text-muted-foreground">{o.description}</span>}
            </span>
          </button>
        )
      })}
    </div>
  )
}

/** "Whole instance": the mark on a setting that reaches every workspace. */
export function WholeInstanceMark({ className }: { className?: string }) {
  return (
    <span data-slot="whole-instance" title="Applies to every workspace on this server"
      className={cn("inline-flex items-center gap-1.5 rounded-full bg-chip-info-bg px-2.5 py-1 text-label font-medium text-chip-info-fg", className)}>
      <Server className="size-3" aria-hidden />Whole instance
    </span>
  )
}

/** The usual values one click away, for a value any number may also take. */
export function QuickPicks({ label, value, options, onPick, disabled }: {
  label: string
  value: string
  options: readonly { value: string; label: string }[]
  onPick: (v: string) => void
  disabled?: boolean
}) {
  return (
    <span role="group" aria-label={label} className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5 max-md:hidden">
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={value === o.value} disabled={disabled} onClick={() => onPick(o.value)}
          className={cn("h-7 rounded-md px-2.5 text-control transition-colors disabled:opacity-50",
            value === o.value ? "bg-card font-medium text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
          {o.label}
        </button>
      ))}
    </span>
  )
}
