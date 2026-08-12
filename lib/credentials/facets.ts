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

import { getBrand } from "@/lib/credential-providers/registry"
import { credentialTypeLabel } from "./item-types"
import { GUARDED_TIER, UNCLASSIFIED_TIER, tierOf } from "./tiers"

/** The subset of the credential payload the facets reason about. */
export interface CredentialLike {
  id: string
  name: string
  description?: string | null
  provider: string
  /** The credential shape — API_KEY, SSH_KEY, CERTIFICATE… Optional because
   *  several callers pass a narrowed row that has no use for it. */
  type?: string
  status: string
  scope: string
  crew_ids?: string[] | null
  account_label?: string | null
  tags?: string[] | null
  token_expires_at?: string | null
  last_used_at?: string | null
  /** Keeper tier, 1–4. Absent on an older server — see `tierOf`. */
  security_level?: number | null
  /** Agents holding this credential. Index-aligned with `agent_names` by the
   *  server (see splitAgentRefs in internal/api/credentials.go). */
  agent_ids?: string[] | null
  agent_names?: string[] | null
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

export interface CredentialFacetOption {
  value: string
  label: string
  count: number
  /**
   * The providers actually behind this option, commonest first.
   *
   * Lets a row be drawn with the brand marks it contains instead of one
   * generic glyph repeated down the list. "GitHub" beside the GitHub mark is a
   * row you recognise; the same shape on every row is a row you have to read.
   */
  providers?: string[]
}

/**
 * Tags in use, commonest first, each with the count of credentials carrying it.
 *
 * Tags are how a user categorises a credential now that the derived category
 * facet is gone: they are typed by the person who knows what the secret is
 * for, and nothing infers them.
 *
 * The dropdown used to render tags with `count: 0` and hide the number, which
 * made the tag group the only facet that could not tell you how much it would
 * narrow the list — and put the rarest tag first as often as not, because the
 * list was sorted alphabetically over a set.
 */
export function buildTagFacet(credentials: CredentialLike[]): CredentialFacetOption[] {
  const counts = new Map<string, number>()
  for (const c of credentials) {
    for (const t of c.tags ?? []) counts.set(t, (counts.get(t) ?? 0) + 1)
  }
  return Array.from(counts.entries())
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([tag, count]) => ({ value: tag, label: tag, count }))
}

/**
 * The agents that hold at least one credential, by name, with their id as the
 * facet value.
 *
 * The id is the point: it is what an avatar is keyed by, so this facet can show
 * the same face for an agent that every other page shows. Deriving one from the
 * name would give the same agent two different faces depending on which page
 * you were looking at.
 *
 * An agent the server named but did not id is skipped rather than shown with a
 * synthesised key — a row that cannot be filtered by is a control that does
 * nothing.
 */
export function buildAgentFacet(credentials: CredentialLike[]): CredentialFacetOption[] {
  const byId = new Map<string, { name: string; count: number }>()
  for (const c of credentials) {
    const ids = c.agent_ids ?? []
    const names = c.agent_names ?? []
    for (let i = 0; i < ids.length; i++) {
      const id = ids[i]
      if (!id) continue
      const cur = byId.get(id)
      if (cur) cur.count++
      else byId.set(id, { name: names[i] || id.slice(0, 8), count: 1 })
    }
  }
  return Array.from(byId.entries())
    .map(([id, { name, count }]) => ({ value: id, label: name, count }))
    .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
}

/**
 * The brand a credential belongs to, as a facet.
 *
 * This used to be a CATEGORY facet — "AI & inference", "Source control",
 * "Cloud & infra" — derived from the provider through the registry. It was the
 * one control on the page the user could never set: the create flow asks which
 * SHAPE a credential is and which BRAND it belongs to, and the word "category"
 * appears nowhere in it. So the rail filtered on a property nobody chose, and
 * the two properties people do choose could not be filtered on at all.
 *
 * Brand is the honest replacement. It is what the picker sets, it is the icon
 * on every row, and "show me everything GitHub" is a question an operator
 * actually has. The category groupings survive only inside the brand picker,
 * where they are a way to browse several hundred marks rather than a thing you
 * are asked to declare.
 */
export function buildBrandFacet(credentials: CredentialLike[]): CredentialFacetOption[] {
  const counts = new Map<string, number>()
  for (const c of credentials) {
    counts.set(c.provider, (counts.get(c.provider) ?? 0) + 1)
  }
  return Array.from(counts.entries())
    .map(([provider, count]) => {
      const brand = getBrand(provider)
      return {
      value: provider,
      // getBrand answers GENERIC_BRAND for anything the registry does not hold,
      // so two different unknown providers would otherwise render two rows
      // reading "Generic secret". The key is ugly and it is at least distinct.
      label: brand.key === "NONE" && provider !== "NONE" ? provider : brand.label,
      count,
      // The row draws itself with its own mark; one provider, so one icon.
      providers: [provider],
      }
    })
    .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
}

/**
 * The shape a credential is — token, login, certificate.
 *
 * The first question the create wizard asks, and until now the only answer it
 * collected that the rail could not filter on. "Show me every certificate" is
 * the question you ask right before an expiry sweep.
 */
export function buildShapeFacet(credentials: CredentialLike[]): CredentialFacetOption[] {
  const counts = new Map<string, { type: string; count: number }>()
  for (const c of credentials) {
    // No type, no row. An empty value produced a label-less row that
    // `applyCredentialFilters` then read as "no filter", so clicking it
    // appeared to do nothing.
    if (!c.type) continue
    const label = credentialTypeLabel(c.type)
    const cur = counts.get(label)
    if (cur) cur.count++
    else counts.set(label, { type: c.type ?? "", count: 1 })
  }
  return Array.from(counts.entries())
    .map(([label, { type, count }]) => ({ value: type, label, count }))
    .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
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
  /** Provider key — "GITHUB". What the brand picker sets and what the icon on
   *  every row already shows. Replaced the old derived `category`. */
  brand: string | null
  /** Credential shape — "CERTIFICATE". The wizard's first question. */
  shape: string | null
  scope: string | null
  tag: string | null
  /**
   * Keeper tier as a facet value — "1".."4", or "unclassified" for the rows a
   * server too old to send `security_level` returned. A string rather than a
   * number so it reads like every other facet and survives a URL round-trip
   * without the 0-is-falsy trap.
   */
  tier: string | null
  /** Agent id — "show me what this agent can read". Null means every agent. */
  agentId: string | null
  search: string
}

export const EMPTY_CREDENTIAL_FILTERS: CredentialFilters = {
  status: "all",
  brand: null,
  shape: null,
  scope: null,
  tag: null,
  tier: null,
  agentId: null,
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
    if (filters.brand && c.provider !== filters.brand) return false
    if (filters.shape && (c.type ?? "") !== filters.shape) return false
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
    if (filters.tier) {
      const level = tierOf(c)
      if (filters.tier === GUARDED_TIER) {
        if (level === null || level < 3) return false
      } else {
        const key = level === null ? UNCLASSIFIED_TIER : String(level)
        if (key !== filters.tier) return false
      }
    }
    if (filters.agentId && !(c.agent_ids ?? []).includes(filters.agentId)) return false
    if (q) {
      // Agent names are searchable too: "which secrets does the deploy bot
      // hold" is asked as often by typing the agent's name as by opening a
      // facet, and the rail's placeholder already promises "a secret or tool".
      const hay = [
        c.name,
        c.account_label ?? "",
        c.description ?? "",
        ...(c.tags ?? []),
        ...(c.agent_names ?? []),
      ]
        .join(" ")
        .toLowerCase()
      if (!hay.includes(q)) return false
    }
    return true
  })
}
