"use client"

/**
 * The Vault's health strip (Harbor 2): what needs a person, counted, each tile
 * a door into the view that lists exactly those secrets. The counts and the
 * views read the same predicates (lib/credentials/facets.ts), so a tile can
 * never promise five and open four.
 *
 * Two tiles the design draws are not here yet: "Old values" needs the date a
 * value was last replaced on the list payload, and "Shared with everyone" is
 * a folder grant. Neither exists on the server today, so neither is drawn.
 */

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { AlertTriangle, CircleDashed, Clock } from "lucide-react"

import {
  isExpiringSoon,
  isRecentlyUsed,
  isRejected,
  type CredentialLike,
  type CredentialStatusFilter,
} from "@/lib/credentials/facets"
import { cn } from "@/lib/utils"

export interface VaultHealthCounts {
  rejected: number
  expiring: number
  unused: number
}

export function buildVaultHealth(credentials: CredentialLike[], now = Date.now()): VaultHealthCounts {
  let rejected = 0
  let expiring = 0
  let unused = 0
  for (const c of credentials) {
    if (isRejected(c, now)) rejected++
    if (isExpiringSoon(c, now)) expiring++
    if (!isRecentlyUsed(c, now)) unused++
  }
  return { rejected, expiring, unused }
}

interface Tile {
  view: CredentialStatusFilter
  icon: LucideIcon
  label: string
  hint: string
  count: number
  tone: "bad" | "warn" | "muted"
}

const TONE: Record<Tile["tone"], string> = {
  bad: "text-destructive",
  warn: "text-warn",
  muted: "text-muted-foreground",
}

export function VaultHealth({
  counts,
  onOpen,
  className,
}: {
  counts: VaultHealthCounts
  onOpen: (view: CredentialStatusFilter) => void
  className?: string
}) {
  const tiles: Tile[] = [
    { view: "rejected", icon: AlertTriangle, label: "Rejected", hint: "Runs that use them stop", count: counts.rejected, tone: "bad" },
    { view: "expiring", icon: Clock, label: "Expire within 14 days", hint: "Certificates and tokens with a date", count: counts.expiring, tone: "warn" },
    { view: "unused", icon: CircleDashed, label: "Not used in 30 days", hint: "Candidates to delete", count: counts.unused, tone: "muted" },
  ]
  return (
    <div role="group" aria-label="Vault health" className={cn("grid grid-cols-2 gap-2 md:grid-cols-3", className)}>
      {tiles.map((t) => {
        const Icon = t.icon
        return (
          <button
            key={t.view}
            type="button"
            data-health={t.view}
            onClick={() => onOpen(t.view)}
            aria-label={`${t.count} ${t.label.toLowerCase()} — open the list`}
            className="kit-tap grid min-w-0 grid-cols-[auto_1fr] items-center gap-x-2 gap-y-0.5 rounded-xl border border-border bg-card px-3 py-2.5 text-left transition-colors hover:border-foreground/25 focus-visible:outline-2 focus-visible:outline-ring"
          >
            <Icon className={cn("size-4", t.count > 0 ? TONE[t.tone] : "text-muted-foreground-soft")} aria-hidden />
            <b className="text-heading font-semibold tabular-nums text-foreground">{t.count}</b>
            <span className="col-span-2 min-w-0 text-label text-foreground/85">
              {t.label}
              <small className="block truncate text-micro text-muted-foreground">{t.hint}</small>
            </span>
          </button>
        )
      })}
    </div>
  )
}
