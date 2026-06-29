import * as React from "react"

import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import type { StatusConfigEntryWithIcon } from "@/lib/status-config"

/**
 * Shared renderer for the "icon-carrying outline badge" pattern:
 *
 *   <Badge variant="outline" className="gap-1 border-0 {entry.className}">
 *     {icon}{entry.label}
 *   </Badge>
 *
 * This is the icon variant of a status badge (as opposed to the dot/plain
 * `StatusBadge` in status-badge.tsx). Features keep their own status→entry
 * map (the status *sets* differ: mission vs task vs assignment vs escalation)
 * but share this RENDERER so the Badge chrome lives in one place.
 *
 * The default icon is `<entry.icon className="h-3 w-3" />`. Callers that need
 * a non-standard glyph (a sized animated icon, a live pulse span) pass it via
 * `icon` — that node is rendered verbatim in place of the default.
 *
 * The leading `gap` class is a prop because call sites differ (`gap-1` vs
 * `gap-1.5`); it is placed first so the emitted className string is identical
 * to the original inline template literals.
 */
export function StatusIconBadge({
  entry,
  gap = "gap-1",
  icon,
}: {
  entry: StatusConfigEntryWithIcon
  gap?: string
  icon?: React.ReactNode
}) {
  const Icon = entry.icon
  return (
    <Badge variant="outline" className={cn(gap, "border-0", entry.className)}>
      {icon ?? <Icon className="h-3 w-3" />}
      {entry.label}
    </Badge>
  )
}
