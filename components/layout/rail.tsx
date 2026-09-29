"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { accentFor } from "@/lib/concept-accents"

/**
 * The app rail's one grid. Every row — workspace, destinations, the pin
 * toggle, the build — is a 2.25rem tile column and a label, and the tile
 * column sits at the same x in the rail and in the pinned panel. Opening the
 * panel therefore only widens it: nothing moves, nothing resizes, the labels
 * fade in a beat later (globals.css `.rail-*`). It used to switch on
 * data-collapsible: button size, padding, group spacing and the group labels
 * all changed at once, which is the "many steps" an open looked like.
 *
 * Sizes are rem, not spacing units: this project's --spacing is 0.23rem, and
 * a w-8 that was meant to be 32px was 29px — the tile overflowed and sat
 * off-centre.
 */

/** A SidebarMenuButton as a rail row: fixed height, no padding, the tile's
 *  own width when collapsed (so no second background shows behind it). */
export const RAIL_ROW =
  "h-[2.25rem]! p-0! gap-3! rounded-[0.7rem]! group-data-[collapsible=icon]:w-[2.25rem]! group-data-[collapsible=icon]:h-[2.25rem]! group-data-[collapsible=icon]:p-0! group-data-[collapsible=icon]:gap-3! group-data-[collapsible=icon]:bg-transparent! group-data-[collapsible=icon]:hover:bg-transparent!"

/** The concept a destination stands for, so its tile wears that colour. */
export function conceptOf(href: string): string {
  if (href === "/") return "dashboard"
  const first = href.slice(1).split("/")[0]
  return first === "chat" ? "sessions" : first
}

/**
 * A destination's icon in a Harbor tile, the same one a nested page's
 * collapsed panel shows. Only the page you are on, or the one under the
 * pointer, is tinted in its concept's colour; the rest stay neutral.
 */
export function RailTile({ icon: Icon, concept, active, badge, children, className }: {
  icon?: React.ElementType
  concept?: string
  active?: boolean
  /** A count shown on the tile's corner, in both states. */
  badge?: number
  children?: React.ReactNode
  className?: string
}) {
  return (
    <span
      data-slot="rail-tile"
      data-active={active ? "true" : undefined}
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

/** Text beside a tile: present in both states, faded out in the rail. */
export function RailLabel({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("rail-label min-w-0 flex-1 truncate", className)}>{children}</span>
}

/**
 * A group's header at one height in both states: a short rule on the tile
 * axis, then the name. In the rail the gap and the rule separate the groups;
 * open, the name appears after the rule and nothing below it moves.
 */
export function RailGroupHead({ label }: { label: string }) {
  return (
    <div className="rail-group-head" data-slot="rail-group-head">
      <span className="rail-group-rule" aria-hidden><i /></span>
      <span className="rail-label rail-group-name">{label}</span>
    </div>
  )
}
