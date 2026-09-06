/**
 * The arithmetic behind the credentials overview.
 *
 * The page it replaces gave its main pane to a table of every secret in the
 * workspace, beside a rail that was already that same list — searchable, iconed
 * and filtered. /routines faced exactly this and answered it by making the
 * landing pane a dashboard, so the rail answers "which one?" and the pane
 * answers "what is the state of the vault?".
 *
 * What an operator asks on arrival, in order: how much of this is guarded and
 * how much is wide open, what is broken, what is about to expire, what is
 * actually being used. Those are the functions below.
 *
 * Framework-free on purpose, mirroring lib/routines-overview.ts: these are the
 * numbers a reader will argue with, and they should be arguable without
 * rendering anything. Inputs are structural — the minimum field set each
 * function reads — so a change to an unrelated column cannot break them.
 */

import { credentialTypeLabel } from "./item-types"
import {
  daysUntilExpiry,
  deriveCredentialStatus,
  needsAttention,
  EXPIRY_WARNING_DAYS,
  type CredentialLike,
} from "./facets"

/** What the overview reads off a credential, on top of the facet fields. */
export interface OverviewCredential extends CredentialLike {
  type: string
  _count_agent_credentials?: number
}

export interface TypeBreakdownRow {
  /** The server type, e.g. "API_KEY" — the row's identity. */
  type: string
  /** Short lowercase name, e.g. "api key". */
  label: string
  count: number
  /** Share of the vault, 0–1, for the bar width. */
  share: number
}

/**
 * The vault by credential type, commonest first.
 *
 * Server types that collapse to the same label collapse to the same ROW —
 * SECRET and GENERIC_SECRET are both "secret", and printing two rows with
 * identical names and different counts would read as a rendering bug rather
 * than as a storage distinction. The row keeps the first server type it saw so
 * it still has a stable key.
 */
export function typeBreakdown(credentials: OverviewCredential[]): TypeBreakdownRow[] {
  const rows = new Map<string, { type: string; count: number }>()
  for (const c of credentials) {
    const label = credentialTypeLabel(c.type)
    const cur = rows.get(label)
    if (cur) cur.count++
    else rows.set(label, { type: c.type, count: 1 })
  }
  const total = credentials.length
  return Array.from(rows.entries())
    .map(([label, { type, count }]) => ({
      type,
      label,
      count,
      // A zero-length vault has no rows to divide, so this branch only exists to
      // keep the function total — never to paper over a real division by zero.
      share: total === 0 ? 0 : count / total,
    }))
    .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
}

export type AttentionTone = "error" | "warn"

export interface AttentionItem {
  id: string
  name: string
  provider: string
  /** Why this row is here, in the words the operator needs to act on. */
  reason: string
  tone: AttentionTone
  /**
   * Where the fix actually happens, when it is not on this page.
   *
   * Only agent-proposed credentials have one: approving or rejecting them is an
   * inbox action, and "go approve it" has to be one click rather than a hint
   * the reader turns into a scavenger hunt. The list row used to carry this
   * link; it moved here when the list did.
   */
  href?: string
}

/**
 * Why an errored credential is errored, in one phrase.
 *
 * `deriveCredentialStatus` folds five distinct server states into "Error", which
 * is right for a dot and useless for a queue: "revoked" and "rate limited" call
 * for opposite actions. A past expiry beats the stored status, because a row
 * whose token ran out yesterday is expired whatever the last check recorded.
 */
function errorReason(status: string, days: number | null): string {
  if (days !== null && days < 0) return "expired"
  switch (status) {
    case "EXPIRED":
      return "expired"
    case "REVOKED":
      return "revoked"
    case "RATE_LIMITED":
      return "rate limited"
    case "ERROR":
      return "the last check failed"
    default:
      return "not usable"
  }
}

/**
 * What is broken, worst first.
 *
 * Order is Error → Pending → expiring → Stale → missing tool, because that is
 * descending cost of ignoring it: an errored secret is already failing agent
 * runs, a pending one is blocking a human decision, an expiring one will fail
 * on a known date, a stale one is merely suspicious, and a missing tool breaks
 * one crew rather than the credential.
 *
 * `missingToolIds` is passed in rather than derived: readiness comes from a
 * different endpoint, and the count in the rail and this list must read the
 * same set by construction.
 */
export function attentionQueue(
  credentials: OverviewCredential[],
  missingToolIds: ReadonlySet<string>,
  limit: number,
): AttentionItem[] {
  const RANK: Record<string, number> = { error: 0, pending: 1, expiring: 2, stale: 3, tool: 4 }
  const scored: { rank: number; item: AttentionItem }[] = []

  for (const c of credentials) {
    const status = deriveCredentialStatus(c)
    const days = daysUntilExpiry(c)
    const missingTool = missingToolIds.has(c.id)
    if (!needsAttention(c) && !missingTool) continue

    let kind: string
    let reason: string
    let tone: AttentionTone = "warn"
    if (status === "Error") {
      kind = "error"
      tone = "error"
      reason = errorReason(c.status, days)
    } else if (status === "Pending") {
      kind = "pending"
      reason = "proposed by an agent — approve or reject it"
    } else if (days !== null && days >= 0 && days < EXPIRY_WARNING_DAYS) {
      kind = "expiring"
      reason = days === 0 ? "expires today" : `expires in ${days}d`
    } else if (status === "Stale") {
      kind = "stale"
      reason = "unused for over 90 days"
    } else {
      kind = "tool"
      reason = "the CLI that reads it is missing from a crew"
    }

    scored.push({
      rank: RANK[kind],
      item: {
        id: c.id,
        name: c.name,
        provider: c.provider,
        reason,
        tone,
        ...(kind === "pending" ? { href: "/inbox" } : {}),
      },
    })
  }

  return scored
    .sort((a, b) => a.rank - b.rank || a.item.name.localeCompare(b.item.name))
    .slice(0, limit)
    .map((s) => s.item)
}

/**
 * Credentials with a real expiry inside the warning window, soonest first.
 *
 * Already-expired rows are excluded: they are not "about to break", they are
 * broken, and they belong to the attention queue where the reason says so.
 */
export function expiringSoon<T extends OverviewCredential>(
  credentials: T[],
  limit: number,
): { credential: T; days: number }[] {
  const out: { credential: T; days: number }[] = []
  for (const c of credentials) {
    const days = daysUntilExpiry(c)
    if (days === null || days < 0 || days >= EXPIRY_WARNING_DAYS) continue
    out.push({ credential: c, days })
  }
  return out.sort((a, b) => a.days - b.days).slice(0, limit)
}

/** The credentials used most recently, newest first. Never-used rows are omitted. */
export function recentlyUsed<T extends OverviewCredential>(
  credentials: T[],
  limit: number,
): T[] {
  return credentials
    .filter((c) => {
      if (!c.last_used_at) return false
      return !Number.isNaN(new Date(c.last_used_at).getTime())
    })
    .sort(
      (a, b) => new Date(b.last_used_at!).getTime() - new Date(a.last_used_at!).getTime(),
    )
    .slice(0, limit)
}

/** Counts for the KPI strip. One pass, so no two tiles can disagree. */
export function vaultTotals(credentials: OverviewCredential[]): {
  total: number
  active: number
  expiring: number
  linked: number
} {
  let active = 0
  let expiring = 0
  let linked = 0
  for (const c of credentials) {
    if (deriveCredentialStatus(c) === "Connected") active++
    const days = daysUntilExpiry(c)
    if (days !== null && days >= 0 && days < EXPIRY_WARNING_DAYS) expiring++
    if ((c._count_agent_credentials ?? 0) > 0) linked++
  }
  return { total: credentials.length, active, expiring, linked }
}
