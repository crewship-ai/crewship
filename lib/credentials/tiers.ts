/**
 * The Keeper tier, on the console side.
 *
 * `credentials.security_level` (1–4) is the property that decides what happens
 * when an agent asks for a secret: L1 is auto-allowed without a model call, L2
 * and up reach the judge, L3 and up are leased per read, and every L4 read stops
 * for a human. It is the single most consequential field on the row.
 *
 * It was also, until this module, spelled out in three places that could not
 * agree — the create form's picker, a badge table inlined in the credentials
 * page, and nothing at all in the rail or the vault's own numbers. This is the
 * one table, mirroring `internal/keeper/tier.go`, and every credential surface
 * reads it.
 *
 * The colours are the same arcs the routines catalog donut uses, so "amber means
 * look at this" carries across pages rather than being re-invented per chart.
 */

import type { CredentialFacetOption } from "./facets"

export const CREDENTIAL_TIER_LEVELS = [1, 2, 3, 4] as const
export type CredentialTierLevel = (typeof CREDENTIAL_TIER_LEVELS)[number]

/** The bucket for a row whose server did not send a tier at all — see `tierOf`. */
export const UNCLASSIFIED_TIER = "unclassified"

/**
 * The facet value meaning "every tier Keeper mediates per read" — L3 and L4.
 *
 * It exists because the "Guarded · L3+" tile has to be able to select what it
 * counts. Pointing it at tier "3" made the tile a small lie: it reported five
 * credentials and filtered to four, silently dropping the L4 that is the whole
 * reason anyone looks at the number.
 */
export const GUARDED_TIER = "guarded"

export interface CredentialTier {
  level: CredentialTierLevel
  /** Bare tier, for a chip with no room: "L3". */
  short: string
  /** Operator-facing name, matching keeper.SecurityLevel.Label: "L3 · high". */
  label: string
  /** What a credential at this tier can reach. */
  blast: string
  /** What the tier does to a read — the operational cost of choosing it. */
  consequence: string
  /** Arc/bar colour. */
  color: string
  /** Classes for the inline badge. */
  badgeClass: string
  /** Classes for a bare status dot. */
  dotClass: string
}

/**
 * The tier table, mirroring `tierPolicies` in internal/keeper/tier.go.
 *
 * `blast` and `consequence` are not decoration: L4 turns every read into a human
 * approval and forces the four-eyes rule, which is a real operational cost. An
 * operator choosing a tier is choosing that, so the picker says so before they
 * pick rather than after.
 */
export const CREDENTIAL_TIERS: readonly CredentialTier[] = [
  {
    level: 1,
    short: "L1",
    label: "L1 · low",
    blast: "Read-only or low-value (npm read token, public API key)",
    consequence: "Auto-approved when the agent states an intent — no model call, no cost.",
    color: "rgb(148, 163, 184)",
    // L1 is the default and the majority. It gets a badge — an operator asking
    // "how guarded is this?" deserves an answer on every row, not silence that
    // could equally mean "no tier" — but a colourless one, so the rows that
    // carry real blast radius are still the ones the eye lands on.
    badgeClass: "border-white/15 text-muted-foreground",
    dotClass: "bg-muted-foreground/50",
  },
  {
    level: 2,
    short: "L2",
    label: "L2 · medium",
    blast: "Write access to a non-production system (GitHub write, staging DB)",
    consequence: "Every read is judged by the Keeper model.",
    color: "rgb(96, 165, 250)",
    badgeClass: "border-info/40 text-info",
    dotClass: "bg-info",
  },
  {
    level: 3,
    short: "L3",
    label: "L3 · high",
    blast: "Admin access to real infrastructure (SSH, database admin, cloud account)",
    consequence:
      "Judged with extra checks, needs a substantive intent, and auto-leases rather than granting standing access.",
    color: "rgb(251, 191, 36)",
    badgeClass: "border-warn/50 text-warn",
    dotClass: "bg-warn",
  },
  {
    level: 4,
    short: "L4",
    label: "L4 · critical",
    blast: "Production administration, payments, or customer data at scale",
    consequence:
      "A human approves every read — the model can recommend but never grant — and whoever's agent asked cannot be the one who approves.",
    color: "rgb(248, 113, 113)",
    badgeClass: "border-destructive/50 text-destructive",
    dotClass: "bg-destructive",
  },
] as const

const BY_LEVEL = new Map<number, CredentialTier>(CREDENTIAL_TIERS.map((t) => [t.level, t]))

/** The colour for the rows whose tier the server never sent. */
export const UNCLASSIFIED_TIER_COLOR = "rgb(71, 85, 105)"

/** What the tier helpers need from a credential. */
export interface TieredCredential {
  security_level?: number | null
}

/**
 * The tier of a credential, or null when the server did not say.
 *
 * Two different unknowns, deliberately answered differently:
 *
 *   · The field is absent or null — an older server, or a payload that predates
 *     the column. We know nothing, and inventing L1 would be a claim that the
 *     credential is unguarded when it may not be. Returns null; the UI shows it
 *     as unclassified rather than as safe.
 *   · The field is present but out of range. That is a stored value the tier
 *     table does not define, and `keeper.SecurityLevel.Tier()` resolves exactly
 *     that case to L4 — unknown blast radius reads as the strictest tier. The
 *     console must agree, or the badge would say "low" about a row the server
 *     guards as critical.
 */
export function tierOf(c: TieredCredential): CredentialTierLevel | null {
  const raw = c.security_level
  if (raw === undefined || raw === null) return null
  if (BY_LEVEL.has(raw)) return raw as CredentialTierLevel
  return 4
}

/** The tier's presentation. Out-of-range resolves to L4, matching `tierOf`. */
export function tierMeta(level: number): CredentialTier {
  return BY_LEVEL.get(level) ?? BY_LEVEL.get(4)!
}

/** Human label for a tier, or "Unclassified" for a row the server never tiered. */
export function tierLabel(level: CredentialTierLevel | null): string {
  return level === null ? "Unclassified" : tierMeta(level).label
}

export interface TierBucket {
  /** Facet value: "1".."4", or "unclassified". */
  key: string
  label: string
  count: number
  color: string
}

/**
 * Every tier as one shape, for the overview donut and the rail's TIER section.
 *
 * All four tiers are returned even at zero. This is the one place in the vault
 * where a zero is worth printing: "L4 · 0" tells an operator that nothing in
 * this workspace stops for a human, which is a fact about the workspace, not an
 * empty control. The unclassified bucket is the opposite — it only appears when
 * something is actually in it, because on a current server nothing ever is.
 */
export function tierBuckets(credentials: TieredCredential[]): TierBucket[] {
  const counts = new Map<string, number>()
  for (const c of credentials) {
    const level = tierOf(c)
    const key = level === null ? UNCLASSIFIED_TIER : String(level)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  const out: TierBucket[] = CREDENTIAL_TIERS.map((t) => ({
    key: String(t.level),
    label: t.label,
    count: counts.get(String(t.level)) ?? 0,
    color: t.color,
  }))
  const unclassified = counts.get(UNCLASSIFIED_TIER) ?? 0
  if (unclassified > 0) {
    out.push({
      key: UNCLASSIFIED_TIER,
      label: "Unclassified",
      count: unclassified,
      color: UNCLASSIFIED_TIER_COLOR,
    })
  }
  return out
}

/** The tier rows for the sidebar facet, in the same shape as the other facets. */
export function buildTierFacet(credentials: TieredCredential[]): CredentialFacetOption[] {
  return tierBuckets(credentials).map((b) => ({ value: b.key, label: b.label, count: b.count }))
}

/**
 * How many credentials Keeper mediates per read rather than handing to the agent
 * for the whole run — L3 and up, the `SelfServiceDelivery: false` half of the
 * tier table.
 *
 * It is the one number that answers "how much of this vault is actually
 * guarded?", which is a different question from how many rows are healthy.
 */
export function guardedCount(credentials: TieredCredential[]): number {
  return credentials.filter((c) => {
    const level = tierOf(c)
    return level !== null && level >= 3
  }).length
}
