"use client"

import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * The 24 px tile a panel row's icon sits on (v67): a grey rounded square with a
 * 14 px glyph; the open row's is in the accent. Rows are 30 px, so the tile is
 * 3 px clear of the row edge top and bottom.
 */
export function SidebarRowTile({ icon: Icon, active = false, className }: { icon: LucideIcon; active?: boolean; className?: string }) {
  return (
    <span
      data-slot="row-tile"
      aria-hidden
      className={cn(
        "grid size-6 shrink-0 place-items-center rounded-md border",
        active ? "icon-tile" : "border-border bg-surface-subtle text-muted-foreground",
        className,
      )}
    >
      <Icon className="size-3.5" />
    </span>
  )
}
