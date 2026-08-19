"use client"

/**
 * The Keeper tier, as a chip.
 *
 * One component for every credential surface — the list row, the rail, the
 * detail header, the overview lists — because the tier was previously drawn by
 * a table inlined in the credentials page and nowhere else. An operator
 * scanning the rail could not see which of their secrets were guarded and which
 * were wide open, and the one place that did show it stopped at L2.
 *
 * The title is the consequence, not the name. "L4" tells a reader nothing they
 * can act on; "a human approves every read" tells them why the row behaves
 * differently from the one above it.
 */

import { Badge } from "@/components/ui/badge"
import { tierMeta, type CredentialTierLevel } from "@/lib/credentials/tiers"
import { cn } from "@/lib/utils"

export interface CredentialTierBadgeProps {
  /** 1–4, or null for a credential whose server did not send a tier. */
  level: CredentialTierLevel | null
  /**
   * The server's own label ("L3 · high"), when the payload carried one. Shown in
   * the tooltip in preference to ours so a tier renamed server-side does not
   * leave the console asserting the old name.
   */
  serverLabel?: string | null
  className?: string
}

export function CredentialTierBadge({ level, serverLabel, className }: CredentialTierBadgeProps) {
  if (level === null) {
    // Not "L1". A tier we were not told is not a tier of one, and a row silently
    // badged as the lowest is the exact misreading this chip exists to prevent.
    return (
      <Badge
        variant="outline"
        className={cn(
          "h-4 shrink-0 border-dashed border-white/15 px-1 font-mono text-[9px] text-muted-foreground-soft",
          className,
        )}
        title="This server did not report a Keeper tier for this credential."
      >
        L?
      </Badge>
    )
  }

  const tier = tierMeta(level)
  return (
    <Badge
      variant="outline"
      className={cn("h-4 shrink-0 px-1 font-mono text-[9px]", tier.badgeClass, className)}
      title={`${serverLabel || tier.label} — ${tier.consequence}`}
    >
      {tier.short}
    </Badge>
  )
}
