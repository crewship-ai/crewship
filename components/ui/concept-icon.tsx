import * as React from "react"

import { cn } from "@/lib/utils"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { ACCENT, accentFor, type Accent, type AccentName } from "@/lib/concept-accents"

/**
 * A concept's icon, in the concept's colour, optionally in the concept's chip.
 *
 * Three things had to be picked every time an icon was rendered — the glyph,
 * the colour, and whether it sits in a tinted square. `concept-icons.ts` fixed
 * the first and `concept-accents.ts` the second; this component is where the
 * three arrive together so a caller writes `<ConceptIcon concept="crews" />`
 * and cannot get any of them wrong.
 *
 * `bare` is the default because most icons in the product are inline next to
 * text, where a chip would be a box around every word. The chip earns its
 * space in a tile, a dialog header or a card header — somewhere the icon is a
 * target rather than a decoration.
 */

export type SurfaceIconComponent = React.ComponentType<{
  className?: string
  style?: React.CSSProperties
}>

export interface ConceptIconProps {
  /** A key of CONCEPT_ICON. Supplies both the glyph and the colour. */
  concept?: string
  /**
   * An explicit glyph, for things that are not product concepts (a file, a
   * clock, a brand mark). Wins over `concept`'s glyph; `accent` still colours it.
   */
  icon?: SurfaceIconComponent
  /** Override the colour. Defaults to the concept's own. */
  accent?: AccentName
  /** `chip` draws the tinted square; `bare` is the glyph alone. */
  variant?: "bare" | "chip"
  /** Chip edge length. The glyph scales with it. */
  size?: "sm" | "md" | "lg"
  className?: string
}

const CHIP_SIZE = {
  sm: "h-6 w-6 rounded-md",
  md: "h-8 w-8 rounded-lg",
  lg: "h-10 w-10 rounded-xl",
} as const

const GLYPH_SIZE = {
  sm: "h-3.5 w-3.5",
  md: "h-4 w-4",
  lg: "h-5 w-5",
} as const

export function ConceptIcon({
  concept,
  icon,
  accent,
  variant = "bare",
  size = "md",
  className,
}: ConceptIconProps) {
  const Glyph: SurfaceIconComponent | undefined =
    icon ?? (concept ? (CONCEPT_ICON as Record<string, SurfaceIconComponent>)[concept] : undefined)
  const tone: Accent = accent ? ACCENT[accent] : accentFor(concept)

  if (!Glyph) return null

  if (variant === "bare") {
    return <Glyph className={cn(GLYPH_SIZE[size], tone.fg, className)} />
  }

  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center justify-center border",
        CHIP_SIZE[size],
        tone.chip,
        className,
      )}
    >
      <Glyph className={cn(GLYPH_SIZE[size], tone.fg)} />
    </span>
  )
}
