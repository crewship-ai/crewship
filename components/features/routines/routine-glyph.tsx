import * as React from "react"

import { cn } from "@/lib/utils"
import { getCrewIconDef } from "@/lib/crew-icons"
import { resolveRoutineIcon } from "@/lib/routine-identity"

// A routine's glyph in one tint. The glyph still tells two routines apart at a
// glance; the per-routine colour did not — thirty rows in thirty hues read as
// noise, and the colour competed with the status dot that actually carries
// meaning. `tile` is the Harbor icon tile in the brand tint; `bare` is the
// glyph alone for dense lists (the explorer).

const TILE = {
  sm: "h-6 w-6 rounded-[7px]",
  md: "h-7 w-7 rounded-md",
} as const

export function RoutineGlyph({
  routine,
  variant = "tile",
  size = "md",
  className,
}: {
  routine: { slug: string; icon?: string | null }
  variant?: "tile" | "bare"
  size?: "sm" | "md"
  className?: string
}) {
  const Glyph = getCrewIconDef(resolveRoutineIcon(routine)).icon
  if (variant === "bare") {
    return <Glyph data-slot="routine-glyph" aria-hidden className={cn("h-3.5 w-3.5 shrink-0 text-muted-foreground", className)} />
  }
  return (
    <span
      data-slot="routine-glyph"
      aria-hidden
      className={cn("icon-tile inline-flex shrink-0 items-center justify-center", TILE[size], className)}
      style={{ "--ic": "var(--primary)" } as React.CSSProperties}
    >
      <Glyph className="h-3.5 w-3.5" />
    </span>
  )
}
