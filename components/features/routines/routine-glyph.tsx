import * as React from "react"

import { cn } from "@/lib/utils"
import { getCrewDotColor, getCrewIconDef } from "@/lib/crew-icons"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"

// A routine's glyph in the routine's own colour — the colour is part of its
// identity, the same way a crew's is, and the one place every surface (the
// explorer, the dashboard, the calendar) draws a routine. `tile` is the Harbor
// icon tile tinted with that colour; `bare` is the glyph alone for dense lists.
// Status (running, failed) is never this colour's job: it stays on the status
// dot and the pill beside the glyph.

const TILE = {
  sm: "h-6 w-6 rounded-sm",
  md: "h-7 w-7 rounded-md",
} as const

/** The routine's colour as a CSS value, whether stored as hex, palette id or
 *  derived from the slug. */
export function routineTint(routine: { slug: string; color?: string | null }): string {
  return getCrewDotColor(resolveRoutineColor(routine))
}

export function RoutineGlyph({
  routine,
  variant = "tile",
  size = "md",
  className,
}: {
  routine: { slug: string; icon?: string | null; color?: string | null }
  variant?: "tile" | "bare"
  size?: "sm" | "md"
  className?: string
}) {
  const Glyph = getCrewIconDef(resolveRoutineIcon(routine)).icon
  const tint = routineTint(routine)
  if (variant === "bare") {
    return <Glyph data-slot="routine-glyph" aria-hidden className={cn("h-3.5 w-3.5 shrink-0", className)} style={{ color: tint }} />
  }
  return (
    <span
      data-slot="routine-glyph"
      aria-hidden
      className={cn("icon-tile inline-flex shrink-0 items-center justify-center", TILE[size], className)}
      style={{ "--ic": tint } as React.CSSProperties}
    >
      <Glyph className="h-3.5 w-3.5" />
    </span>
  )
}
