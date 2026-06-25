// Shared helpers for surfacing ephemeral ("hired") agents and their
// lifecycle across the fleet table + sidebar. The backend (PR-D F5)
// returns ephemeral/expires_at/expired_at/parent_lead_id/hire_reason on
// every agent row; a non-null expired_at is the canonical "ghost" signal.

export interface EphemeralAgent {
  status?: string
  ephemeral?: boolean
  expires_at?: string | null
  expired_at?: string | null
  parent_lead_id?: string | null
  hire_reason?: string | null
}

/** A non-null expired_at means the ephemeral agent's TTL lapsed (or it was
 *  released) and it's now a preserved "ghost" — greyed out, re-hireable. */
export function isGhost(a: Pick<EphemeralAgent, "expired_at">): boolean {
  return Boolean(a.expired_at)
}

/** Status key to render. Ghosts override the server status column (which
 *  may still read RUNNING/IDLE) with the synthetic EXPIRED variant so the
 *  row's badge + dimming stay in lockstep. Otherwise the server status,
 *  defaulting to IDLE. */
export function effectiveStatus(a: Pick<EphemeralAgent, "status" | "expired_at">): string {
  if (isGhost(a)) return "EXPIRED"
  return a.status || "IDLE"
}

/** Compact remaining-TTL label for a live ephemeral agent, e.g.
 *  "3h 12m left", "8m left", "expiring". Empty string when there's no
 *  expiry or it can't be parsed. `now` is injectable for tests. */
export function ttlRemaining(
  expiresAt: string | null | undefined,
  now: number = Date.now(),
): string {
  if (!expiresAt) return ""
  const ms = new Date(expiresAt).getTime() - now
  if (Number.isNaN(ms)) return ""
  if (ms <= 0) return "expiring"
  const totalMin = Math.floor(ms / 60000)
  // Under a minute still left: don't round down to a misleading "0m left".
  if (totalMin === 0) return "<1m left"
  const h = Math.floor(totalMin / 60)
  const m = totalMin % 60
  if (h >= 1) return `${h}h ${m}m left`
  return `${m}m left`
}

/** The most recent line of the append-only hire_reason audit log. Each
 *  hire/rehire appends "[<ts>] <reason>"; we show the latest reason,
 *  stripped of its timestamp prefix. */
export function latestHireReason(hireReason: string | null | undefined): string {
  if (!hireReason) return ""
  const lines = hireReason.split("\n").map((l) => l.trim()).filter(Boolean)
  const last = lines[lines.length - 1] ?? ""
  return last.replace(/^\[[^\]]*\]\s*/, "")
}
