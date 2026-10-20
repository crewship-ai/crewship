import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * One option among a few, as a tile (Harbor 2 kit, COM-08): glyph, title, one
 * line. `aria-pressed` carries the choice, so the tile reads the way it looks.
 * For two to five short, equal options use `Segmented`; a tile is for options
 * that need a sentence each.
 */
export function ChoiceTile({
  icon: Icon,
  title,
  description,
  active,
  onClick,
  leading,
  meta,
  disabled,
  className,
}: {
  icon?: LucideIcon
  /** Replaces the glyph: a crew icon, an avatar. */
  leading?: React.ReactNode
  title: React.ReactNode
  description?: React.ReactNode
  active: boolean
  onClick: () => void
  meta?: React.ReactNode
  disabled?: boolean
  className?: string
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      disabled={disabled}
      data-slot="choice-tile"
      className={cn(
        "flex w-full min-w-0 items-start gap-3 rounded-lg border p-3 text-left transition-colors coarse:min-h-[44px] disabled:cursor-not-allowed disabled:opacity-50",
        active ? "border-primary bg-primary/5 ring-2 ring-primary/15" : "border-border bg-card hover:border-line-strong",
        className,
      )}
    >
      {leading ??
        (Icon && (
          <span
            aria-hidden
            className={cn(
              "flex size-7 shrink-0 items-center justify-center rounded-md",
              active ? "bg-primary/15 text-primary-hover" : "bg-muted text-muted-foreground",
            )}
          >
            <Icon className="size-4" />
          </span>
        ))}
      <span className="min-w-0 flex-1">
        <span className="block text-body font-medium text-foreground">{title}</span>
        {description != null && <span className="mt-0.5 block text-label leading-relaxed text-muted-foreground">{description}</span>}
      </span>
      {meta != null && <span className="shrink-0 text-label tabular-nums text-muted-foreground">{meta}</span>}
    </button>
  )
}
