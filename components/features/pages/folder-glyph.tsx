"use client"

import { Folder } from "lucide-react"

import { CREW_ICONS, getCrewIconDef } from "@/lib/entities"
import { GRADIENT_PALETTES } from "@/lib/crew-icons"
import { cn } from "@/lib/utils"

/**
 * How a folder is drawn everywhere it appears (#2527): its icon from the
 * crew icon set, and its colour as a small dot before the name.
 *
 * A folder with no icon wears the plain folder glyph, and so does one whose
 * icon name this build does not know — `getCrewIconDef` would fall back to
 * the FIRST icon in the catalogue (a briefcase), which is a wrong answer
 * drawn confidently. A folder with no colour has no dot rather than a dot in
 * the default blue: "no colour" and "blue" must not look the same.
 */

const KNOWN = new Set(CREW_ICONS.map((i) => i.name))

export function folderIconOf(icon: string | null | undefined) {
  return icon && KNOWN.has(icon) ? getCrewIconDef(icon).icon : Folder
}

/**
 * The folder's icon, in the folder's colour when it has one — the same
 * symbol the picker previewed, so what was chosen is what the sidebar shows
 * (audit 2026-09-13, F4: the icon rendered grey with the colour off to the
 * side as a dot, which read as two facts instead of one). 14 px by default.
 * Colour never encodes a permission; the sharing marker is separate.
 */
export function FolderGlyph({
  icon,
  color,
  className,
}: {
  icon: string | null | undefined
  color?: string | null
  className?: string
}) {
  const Icon = folderIconOf(icon)
  // A class from the palette, never an inline style: `components/` is
  // Tailwind-only, and a folder's colour is one of the crew palette's ids by
  // construction (the server refuses anything else, pages_folders.go), so
  // every colour has a compiled class. An id the palette does not know draws
  // in the surrounding text colour rather than in a colour nothing checked.
  return (
    <Icon
      data-slot="folder-glyph"
      data-color={color || undefined}
      className={cn("h-3.5 w-3.5 shrink-0", color && folderColorClasses(color)?.glyph, className)}
      aria-hidden
    />
  )
}

export function FolderDot({ color, className }: { color: string | null | undefined; className?: string }) {
  if (!color) return null
  return (
    <span
      data-slot="folder-dot"
      data-color={color}
      aria-hidden
      className={cn("inline-block h-1.5 w-1.5 shrink-0 rounded-full", folderColorClasses(color)?.swatch ?? "bg-muted-foreground/40", className)}
    />
  )
}

/** The palette entry behind a folder colour id, or null for an id the palette does not carry. */
function folderColorClasses(color: string): { glyph: string; swatch: string } | null {
  const palette = GRADIENT_PALETTES.find((p) => p.id === color)
  return palette ? { glyph: palette.glyph, swatch: palette.swatch } : null
}
