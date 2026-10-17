"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { accentFor } from "@/lib/concept-accents"

/**
 * The sidebar's one grid (Harbor 2). Every row — workspace, search,
 * destinations, the sidebar mode, Settings, the user — keeps its icon on the
 * same x in the 56 px rail and in the 240 px open sidebar. Opening it only
 * widens it: nothing moves, the labels fade in a beat later
 * (globals.css `.rail-*`). Sizes are px so the rail does not grow with the
 * user's text size.
 */

/** A SidebarMenuButton as a sidebar row (Harbor 2): 30 px tall, a neutral
 *  16 px icon and a 13 px label; the current page is a raised surface. The
 *  shape lives in globals.css (`.rail-row`) so it can override the shadcn
 *  button's own sizes in both states without arbitrary utilities. */
export const RAIL_ROW = "rail-row"

/** The concept a destination stands for, so its tile wears that colour. */
export function conceptOf(href: string): string {
  if (href === "/") return "dashboard"
  const first = href.slice(1).split("/")[0]
  return first === "chat" ? "sessions" : first
}

/**
 * A destination's icon. Harbor 2 draws it neutral (grey, the current page in
 * full ink); `concept` is kept for callers and for the coloured-icons option
 * of the later style gallery. A count shows on the icon's corner in the rail
 * only — the open row uses RailCount.
 */
export function RailTile({ icon: Icon, concept, active, badge, children, className }: {
  icon?: React.ElementType
  concept?: string
  active?: boolean
  /** A count shown on the icon's corner when the sidebar is a rail. */
  badge?: number
  children?: React.ReactNode
  className?: string
}) {
  return (
    <span
      data-slot="rail-tile"
      data-active={active ? "true" : undefined}
      data-concept={concept}
      className={cn("rail-tile", className)}
      style={concept ? ({ "--ic": accentFor(concept).tint } as React.CSSProperties) : undefined}
      aria-hidden
    >
      {Icon ? <Icon /> : children}
      {badge != null && badge > 0 && (
        <span className="rail-badge" data-slot="rail-badge">{badge > 99 ? "99+" : badge}</span>
      )}
    </span>
  )
}

/** A count at the end of an open row (the Inbox's unread). Hidden in the rail. */
export function RailCount({ value }: { value: number }) {
  if (!value) return null
  return (
    <span className="rail-label rail-count" data-slot="rail-count" aria-hidden>
      {value > 99 ? "99+" : value}
    </span>
  )
}

/** Text beside a tile: present in both states, faded out in the rail. */
export function RailLabel({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("rail-label min-w-0 flex-1 truncate", className)}>{children}</span>
}

/**
 * A group's heading (Work, Build). Open, its name in small grey; in the rail,
 * a short hairline on the icon axis. The ungrouped first block has none.
 */
export function RailGroupHead({ label }: { label: string }) {
  if (!label) return null
  return (
    <div className="rail-group-head" data-slot="rail-group-head">
      <span className="rail-group-rule" aria-hidden><i /></span>
      <span className="rail-label rail-group-name">{label}</span>
    </div>
  )
}
