import * as React from "react"

import { StatusPill } from "@/components/ui/status-pill"
import type { StatusConfigEntryWithIcon } from "@/lib/status-config"

/**
 * A StatusPill that carries its feature's glyph in place of the dot.
 *
 * Features keep their own status → entry map (mission vs task vs assignment vs
 * escalation statuses differ), but each entry names a StatusPill tone, so the
 * chrome and the colours are the one status pill's (chip tokens), not a
 * per-feature palette.
 *
 * The default glyph is `<entry.icon className="h-3 w-3" />`. Callers that
 * need a different one pass `icon`; `live` drops the glyph for the pill's own
 * pulsing dot — only for something that is genuinely running.
 */
export function StatusIconBadge({
  entry,
  icon,
  live = false,
}: {
  entry: StatusConfigEntryWithIcon
  icon?: React.ReactNode
  live?: boolean
}) {
  const Icon = entry.icon
  return (
    <StatusPill
      tone={entry.tone}
      label={entry.label}
      live={live}
      icon={live ? undefined : (icon ?? <Icon className="h-3 w-3" />)}
    />
  )
}
