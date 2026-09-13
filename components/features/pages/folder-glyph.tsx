"use client"

import { Folder } from "lucide-react"

import { CREW_ICONS, getCrewDotColor, getCrewIconDef } from "@/lib/entities"
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

export function FolderGlyph({ icon, className }: { icon: string | null | undefined; className?: string }) {
  const Icon = folderIconOf(icon)
  return <Icon className={cn("h-3.5 w-3.5 shrink-0", className)} aria-hidden />
}

export function FolderDot({ color, className }: { color: string | null | undefined; className?: string }) {
  if (!color) return null
  return (
    <span
      data-slot="folder-dot"
      aria-hidden
      className={cn("inline-block h-1.5 w-1.5 shrink-0 rounded-full", className)}
      style={{ backgroundColor: getCrewDotColor(color) }}
    />
  )
}
