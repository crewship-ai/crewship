"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

export interface SegmentedOption<T extends string> {
  value: T
  label: React.ReactNode
  icon?: LucideIcon
  /** Accessible name when `label` is only an icon or a short glyph. */
  ariaLabel?: string
  disabled?: boolean
  /** A count after the label (Dashboard window filters); not part of the name. */
  count?: number
  /** A hint on hover: the reason a segment is disabled, or what it shows. */
  title?: string
}

/**
 * Segmented control (Harbor 2 kit, v67 `.seg`): two to five mutually
 * exclusive views of the same thing — List / Board, Week / Month, Light /
 * Dark / System. One radio group: the arrow keys move and select, Home / End
 * jump, only the selected segment is in the tab order. `fill` stretches the
 * segments across the width (a phone toolbar, a panel header).
 *
 * The one kit control (COM-02, #3481). Local copies move here in their own
 * area work: Dashboard `DashSegment` (`label` → `ariaLabel`), Issues'
 * display `Segmented` (`id` → `value`, `label` → `ariaLabel`), Vault AI
 * providers and Give to agents, Routines' create dialogs and Calendar's
 * One time / Repeating (`hint` → `title`, plus `disabled` when blocked).
 */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  ariaLabel,
  size = "sm",
  fill = false,
  className,
}: {
  value: T
  onChange: (value: T) => void
  options: readonly SegmentedOption<T>[]
  /** The group's name for assistive technology ("View", "Range"). */
  ariaLabel: string
  size?: "xs" | "sm"
  fill?: boolean
  className?: string
}) {
  const refs = React.useRef<(HTMLButtonElement | null)[]>([])
  const enabled = options.map((o, i) => (o.disabled ? -1 : i)).filter((i) => i >= 0)
  const selectedIndex = options.findIndex((o) => o.value === value)
  // With no match, the first enabled segment takes the tab stop so the group
  // is still reachable.
  const tabStop = selectedIndex >= 0 && !options[selectedIndex].disabled ? selectedIndex : enabled[0]

  const move = (from: number, step: 1 | -1 | "first" | "last") => {
    if (enabled.length === 0) return
    let next: number
    if (step === "first") next = enabled[0]
    else if (step === "last") next = enabled[enabled.length - 1]
    else {
      const pos = enabled.indexOf(from)
      next = enabled[(pos + step + enabled.length) % enabled.length]
    }
    refs.current[next]?.focus()
    onChange(options[next].value)
  }

  const onKeyDown = (e: React.KeyboardEvent, index: number) => {
    const keys: Record<string, 1 | -1 | "first" | "last"> = {
      ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1, Home: "first", End: "last",
    }
    const step = keys[e.key]
    if (step === undefined) return
    e.preventDefault()
    move(index, step)
  }

  return (
    <div
      role="radiogroup"
      aria-label={ariaLabel}
      data-slot="segmented"
      className={cn(
        "inline-flex max-w-full shrink-0 gap-0.5 overflow-x-auto rounded-lg border border-border bg-surface-subtle p-0.5",
        fill && "flex w-full",
        className,
      )}
    >
      {options.map((o, i) => {
        const on = o.value === value
        const Icon = o.icon
        return (
          <button
            key={o.value}
            ref={(el) => { refs.current[i] = el }}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={o.ariaLabel}
            title={o.title}
            disabled={o.disabled}
            tabIndex={i === tabStop ? 0 : -1}
            onClick={() => { if (!on) onChange(o.value) }}
            onKeyDown={(e) => onKeyDown(e, i)}
            data-state={on ? "on" : "off"}
            className={cn(
              "inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-md px-2.5 transition-colors coarse:h-[44px]",
              // A disabled segment keeps pointer events, so its title (the
              // reason) still shows on hover.
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-45",
              size === "xs" ? "h-6 text-xs" : "h-7 text-sm",
              fill && "flex-1",
              on
                ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {Icon && <Icon className="size-3.5 shrink-0" aria-hidden />}
            {o.label}
            {o.count != null && (
              <span data-slot="segmented-count" aria-hidden className="font-mono text-micro tabular-nums text-muted-foreground-soft">
                {o.count}
              </span>
            )}
          </button>
        )
      })}
    </div>
  )
}
