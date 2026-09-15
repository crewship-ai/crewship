"use client"

import { CONCEPT_ICON } from "@/lib/concept-icons"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CREW_ICONS, getCrewIconDef } from "@/lib/entities"
import { GRADIENT_PALETTES } from "@/lib/crew-icons"
import { cn } from "@/lib/utils"

/**
 * How a page's own avatar is drawn everywhere it appears (#2563): its icon
 * from the crew icon set, in its palette colour — the same two things a
 * crew, an agent and a page folder carry, chosen through the same picker.
 *
 * Two rules, both borrowed from `folder-glyph.tsx` and kept for the same
 * reasons:
 *
 *  · A page with no icon wears the pages concept glyph, and so does one whose
 *    icon name this build does not know — `getCrewIconDef` would fall back to
 *    the FIRST icon in the catalogue (a briefcase), which is a wrong answer
 *    drawn confidently.
 *  · A page with no colour draws in the surrounding text colour, never in the
 *    palette's default: "no colour" and "blue" must not look the same.
 *
 * The avatar never encodes freshness. Colour on a panel means state (§9b.4);
 * colour on the page's avatar means nothing but the author's choice, and the
 * state glyph (`page-state.tsx`) is drawn beside it, always — the rail, the
 * editor header and the page header all keep the two apart.
 */

const KNOWN = new Set(CREW_ICONS.map((i) => i.name))

export function pageIconOf(icon: string | null | undefined) {
  return icon && KNOWN.has(icon) ? getCrewIconDef(icon).icon : CONCEPT_ICON.pages
}

/** The palette entry behind a colour id, or null for an id the palette does not carry. */
function pageColorClasses(color: string): { glyph: string; swatch: string } | null {
  const palette = GRADIENT_PALETTES.find((p) => p.id === color)
  return palette ? { glyph: palette.glyph, swatch: palette.swatch } : null
}

/** The icon alone, 14 px by default — the rail's register. */
export function PageGlyph({
  icon,
  color,
  className,
}: {
  icon: string | null | undefined
  color?: string | null
  className?: string
}) {
  const Icon = pageIconOf(icon)
  // A class from the palette, never an inline style: `components/` is
  // Tailwind-only, and a page's colour is one of the palette's ids by
  // construction (the server refuses anything else, pages_handler.go).
  return (
    <Icon
      data-slot="page-glyph"
      data-icon={icon || undefined}
      data-color={color || undefined}
      className={cn("h-3.5 w-3.5 shrink-0", color && pageColorClasses(color)?.glyph, className)}
      aria-hidden
    />
  )
}

/**
 * The boxed form for a header: the crew's own avatar tile when the page has
 * an icon, and the neutral concept tile — the one the editor header has
 * always drawn — when it has none. A page without an avatar must look the
 * way it looked before avatars existed, not like a page wearing a default.
 */
export function PageAvatar({
  icon,
  color,
  size = "md",
  className,
}: {
  icon: string | null | undefined
  color?: string | null
  size?: "sm" | "md"
  className?: string
}) {
  if (icon && KNOWN.has(icon)) {
    return <CrewIcon icon={icon} color={color ?? undefined} size={size} className={className} />
  }
  const Icon = CONCEPT_ICON.pages
  return (
    <div
      data-slot="page-avatar-default"
      className={cn(
        "flex shrink-0 items-center justify-center border border-border/60 bg-surface-raised",
        size === "sm" ? "h-7 w-7 rounded-lg" : "h-10 w-10 rounded-xl",
        className,
      )}
    >
      <Icon className={cn(size === "sm" ? "h-3.5 w-3.5" : "h-5 w-5", "text-muted-foreground")} aria-hidden />
    </div>
  )
}
