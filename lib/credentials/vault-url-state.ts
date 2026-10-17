/**
 * The Vault's address (Harbor 2, /credentials).
 *
 * Everything that decides what the page shows lives in the query string, so a
 * link from another page lands on the same list a person was looking at, and
 * a reload keeps it:
 *
 *   ?space=secrets|providers     the panel's space (`?tab=providers` is the old
 *                                spelling the agent's "Pays with" picker uses)
 *   ?view=attention|recent|…     the panel view or the health tile
 *   ?type= ?brand= ?crew= ?tier= ?class= ?scope= ?tag= ?agent=   facets
 *   ?q=                          search
 *   ?group=type                  table grouping
 *   ?id=<credential>             the secret page
 *   ?tab=overview|access|…       its tab
 *
 * List facets repeat the key (`?type=API_KEY&type=SSH_KEY`); a comma-joined
 * value is read the same way so a hand-written link works too.
 */

import {
  CLASSIFICATIONS,
  EMPTY_CREDENTIAL_FILTERS,
  type CredentialFilters,
  type CredentialStatusFilter,
} from "./facets"

export type VaultSpace = "secrets" | "providers"
export type VaultGroup = "none" | "type"
export const SECRET_TABS = ["overview", "access", "history", "activity", "settings"] as const
export type SecretTab = (typeof SECRET_TABS)[number]

const VIEWS: readonly CredentialStatusFilter[] = [
  "all",
  "attention",
  "missing-tool",
  "recent",
  "unused",
  "rejected",
  "expiring",
]

export interface VaultUrlState {
  space: VaultSpace
  filters: CredentialFilters
  group: VaultGroup
  /** The open credential, if any. */
  id: string | null
  /** The secret page's tab; null = Overview. */
  tab: SecretTab | null
}

export const DEFAULT_VAULT_STATE: VaultUrlState = {
  space: "secrets",
  filters: EMPTY_CREDENTIAL_FILTERS,
  group: "none",
  id: null,
  tab: null,
}

/** What the parser needs — URLSearchParams and Next's ReadonlyURLSearchParams both fit. */
export interface QueryLike {
  get(name: string): string | null
  getAll(name: string): string[]
}

function list(q: QueryLike, key: string, upper = false): string[] {
  const out: string[] = []
  for (const raw of q.getAll(key)) {
    for (const part of raw.split(",")) {
      const v = part.trim()
      if (!v) continue
      const value = upper ? v.toUpperCase() : v
      if (!out.includes(value)) out.push(value)
    }
  }
  return out
}

export function parseVaultUrl(q: QueryLike): VaultUrlState {
  const tabRaw = q.get("tab")
  const spaceRaw = q.get("space")
  const space: VaultSpace = spaceRaw === "providers" || tabRaw === "providers" ? "providers" : "secrets"
  const viewRaw = q.get("view") as CredentialStatusFilter | null
  const view = viewRaw && VIEWS.includes(viewRaw) ? viewRaw : "all"
  const tierRaw = q.get("tier")
  const tier = tierRaw && /^(?:[1-4]|guarded|unclassified)$/.test(tierRaw) ? tierRaw : null
  const tab = tabRaw && (SECRET_TABS as readonly string[]).includes(tabRaw) ? (tabRaw as SecretTab) : null
  return {
    space,
    filters: {
      ...EMPTY_CREDENTIAL_FILTERS,
      status: view,
      shape: list(q, "type", true),
      brand: list(q, "brand", true),
      crew: list(q, "crew"),
      tier,
      classification: list(q, "class", true).filter((c) => (CLASSIFICATIONS as readonly string[]).includes(c)),
      scope: list(q, "scope").map((s) => (s.toUpperCase() === "WORKSPACE" || s.toUpperCase() === "CREW" ? s.toUpperCase() : s)),
      tag: list(q, "tag"),
      agentId: list(q, "agent"),
      search: q.get("q") ?? "",
    },
    group: q.get("group") === "type" ? "type" : "none",
    id: q.get("id") || null,
    tab: tab === "overview" ? null : tab,
  }
}

/** The query string for a state, defaults left out. Starts with "?" or is "". */
export function buildVaultQuery(state: VaultUrlState): string {
  const p = new URLSearchParams()
  const f = state.filters
  if (state.space === "providers") p.set("space", "providers")
  if (f.status !== "all") p.set("view", f.status)
  for (const v of f.shape) p.append("type", v)
  for (const v of f.brand) p.append("brand", v)
  for (const v of f.crew) p.append("crew", v)
  if (f.tier) p.set("tier", f.tier)
  for (const v of f.classification) p.append("class", v)
  for (const v of f.scope) p.append("scope", v)
  for (const v of f.tag) p.append("tag", v)
  for (const v of f.agentId) p.append("agent", v)
  if (f.search.trim()) p.set("q", f.search)
  if (state.group !== "none") p.set("group", state.group)
  if (state.id) p.set("id", state.id)
  if (state.id && state.tab) p.set("tab", state.tab)
  const s = p.toString()
  return s ? `?${s}` : ""
}

/** How many facets narrow the list (the view and the search are not facets). */
export function activeFacetCount(f: CredentialFilters): number {
  return (
    f.shape.length +
    f.brand.length +
    f.crew.length +
    (f.tier ? 1 : 0) +
    f.classification.length +
    f.scope.length +
    f.tag.length +
    f.agentId.length
  )
}

/** Resolve `?crew=` tokens — a link may carry a crew's slug or its id. */
export function resolveCrewTokens(
  tokens: string[],
  crews: Record<string, { id: string; slug?: string | null }>,
): string[] {
  const bySlug = new Map<string, string>()
  for (const c of Object.values(crews)) if (c.slug) bySlug.set(c.slug, c.id)
  return tokens.map((t) => (crews[t] ? t : bySlug.get(t) ?? t))
}
