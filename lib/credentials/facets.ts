/**
 * Facet model for the /credentials sidebar (PRD-CREDENTIALS-V2-2026 §3,
 * wireframe screen 1).
 *
 * The page previously carried this logic inline. It moved here for one
 * reason: the sidebar shows a COUNT next to every facet, and a count that
 * disagrees with what clicking it selects is a lie the user has no way to
 * detect. Counting and filtering now come from the same functions, so they
 * cannot drift, and both are testable without rendering a table.
 *
 * The brand registry (`lib/credential-providers/registry.ts`) stays the single
 * source of category and icon — this module reads it, never duplicates it.
 */

import { BRAND_CATEGORIES, getBrand, type BrandCategory } from "@/lib/credential-providers/registry"

/** The subset of the credential payload the facets reason about. */
export interface CredentialLike {
  id: string
  name: string
  description?: string | null
  provider: string
  status: string
  scope: string
  crew_ids?: string[] | null
  account_label?: string | null
  tags?: string[] | null
  token_expires_at?: string | null
  last_used_at?: string | null
}

/**
 * Derived list status. Narrower than the 5-state taxonomy the Integrations
 * page uses: "Available" and "Detected" describe things that are not yet
 * credentials, and a vault row always is one.
 */
export type CredentialListStatus = "Connected" | "Error" | "Stale" | "Pending"

const STALE_THRESHOLD_DAYS = 90
export const EXPIRY_WARNING_DAYS = 30

const DAY_MS = 24 * 3600 * 1000

/** Parse a timestamp, or null. An unparseable value is NO information — it
 *  must never be read as "expired", which would flag a healthy credential. */
function ms(value: string | null | undefined): number | null {
  if (!value) return null
  const t = new Date(value).getTime()
  return Number.isNaN(t) ? null : t
}

export function deriveCredentialStatus(c: CredentialLike): CredentialListStatus {
  // Agent-proposed and not yet approved. Checked first because such a row is
  // not usable by any agent regardless of what its other fields say.
  if (c.status === "PENDING_APPROVAL") return "Pending"
  if (
    c.status === "EXPIRED" ||
    c.status === "REVOKED" ||
    c.status === "ERROR" ||
    c.status === "RATE_LIMITED"
  ) {
    return "Error"
  }
  const expires = ms(c.token_expires_at)
  if (expires !== null && expires < Date.now()) return "Error"
  const used = ms(c.last_used_at)
  if (used !== null && Date.now() - used > STALE_THRESHOLD_DAYS * DAY_MS) return "Stale"
  return "Connected"
}

/** Days until expiry, or null when the credential has no expiry (or a junk one). */
export function daysUntilExpiry(c: CredentialLike): number | null {
  const expires = ms(c.token_expires_at)
  if (expires === null) return null
  return Math.floor((expires - Date.now()) / DAY_MS)
}

export function needsAttention(c: CredentialLike): boolean {
  const status = deriveCredentialStatus(c)
  if (status === "Error" || status === "Stale" || status === "Pending") return true
  const days = daysUntilExpiry(c)
  return days !== null && days < EXPIRY_WARNING_DAYS
}

/** Human labels for the registry's category keys, for the sidebar rows. */
const CATEGORY_LABELS: Partial<Record<BrandCategory, string>> = {
  AI: "AI & inference",
  Source: "Source control",
  Cloud: "Cloud & infra",
  Comms: "Communication",
  Database: "Data & databases",
  DevOps: "CI/CD & DevOps",
}

export function categoryLabel(category: string): string {
  return CATEGORY_LABELS[category as BrandCategory] ?? category
}

/** The registry category for a credential's provider. Unknown → "Other", so
 *  no row is unreachable through the sidebar. */
export function categoryOf(c: CredentialLike): BrandCategory {
  return getBrand(c.provider).category
}

export interface CredentialFacetOption {
  value: string
  label: string
  count: number
}

export function buildCategoryFacet(credentials: CredentialLike[]): CredentialFacetOption[] {
  const counts = new Map<string, number>()
  for (const c of credentials) {
    const key = categoryOf(c)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return BRAND_CATEGORIES.filter((cat) => counts.has(cat)).map((cat) => ({
    value: cat,
    label: categoryLabel(cat),
    count: counts.get(cat)!,
  }))
}

/**
 * Scope rows: one for workspace-wide, then one per crew that actually holds a
 * credential. A CREW-scoped credential with no crew link (legacy rows created
 * before crew_ids existed) gets a bucket of its own rather than vanishing.
 *
 * `crewNames` is a best-effort id → name map. A crew we cannot name still gets
 * a row labelled by its id: hiding the row would hide its credentials.
 */
export function buildScopeFacet(
  credentials: CredentialLike[],
  crewNames: Record<string, string>,
): CredentialFacetOption[] {
  let workspace = 0
  let unlinkedCrew = 0
  const perCrew = new Map<string, number>()
  const order: string[] = []

  for (const c of credentials) {
    if (c.scope !== "CREW") {
      workspace++
      continue
    }
    const ids = c.crew_ids ?? []
    if (ids.length === 0) {
      unlinkedCrew++
      continue
    }
    for (const id of ids) {
      if (!perCrew.has(id)) order.push(id)
      perCrew.set(id, (perCrew.get(id) ?? 0) + 1)
    }
  }

  const out: CredentialFacetOption[] = []
  if (workspace > 0) out.push({ value: "WORKSPACE", label: "Workspace", count: workspace })
  if (unlinkedCrew > 0) out.push({ value: "CREW", label: "Crew-scoped", count: unlinkedCrew })
  for (const id of order) {
    out.push({
      value: `crew:${id}`,
      // One text node on purpose: the crew name never stands alone in the
      // sidebar, so it cannot be confused with a credential of the same name.
      label: `Crew · ${crewNames[id] ?? id}`,
      count: perCrew.get(id)!,
    })
  }
  return out
}

export type CredentialStatusFilter = "all" | "attention" | "missing-tool"

export interface CredentialFilters {
  status: CredentialStatusFilter
  category: string | null
  scope: string | null
  tag: string | null
  search: string
}

export const EMPTY_CREDENTIAL_FILTERS: CredentialFilters = {
  status: "all",
  category: null,
  scope: null,
  tag: null,
  search: "",
}

/**
 * Apply every facet at once. `missingToolIds` is the set of credential ids the
 * readiness endpoint reported a gap for — passed in rather than derived here,
 * so the sidebar's count and this filter read the same set by construction.
 */
export function applyCredentialFilters<T extends CredentialLike>(
  credentials: T[],
  filters: CredentialFilters,
  missingToolIds: ReadonlySet<string>,
): T[] {
  const q = filters.search.trim().toLowerCase()
  return credentials.filter((c) => {
    if (filters.status === "attention" && !needsAttention(c)) return false
    if (filters.status === "missing-tool" && !missingToolIds.has(c.id)) return false
    if (filters.category && categoryOf(c) !== filters.category) return false
    if (filters.scope) {
      if (filters.scope === "WORKSPACE") {
        if (c.scope === "CREW") return false
      } else if (filters.scope === "CREW") {
        if (c.scope !== "CREW" || (c.crew_ids ?? []).length > 0) return false
      } else {
        const crewId = filters.scope.slice("crew:".length)
        if (!(c.crew_ids ?? []).includes(crewId)) return false
      }
    }
    if (filters.tag && !(c.tags ?? []).includes(filters.tag)) return false
    if (q) {
      const hay = [c.name, c.account_label ?? "", c.description ?? "", ...(c.tags ?? [])]
        .join(" ")
        .toLowerCase()
      if (!hay.includes(q)) return false
    }
    return true
  })
}
