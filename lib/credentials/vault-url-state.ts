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
 *   ?id=<credential>             the secret page (or an AI provider account's page)
 *   ?tab=overview|access|…       its tab
 *
 * In the AI providers space (`?space=providers`) the same keys mean the
 * providers' own things:
 *
 *   ?view=attention|unassigned|mine   the panel view or the health tile
 *   ?provider= ?mode= ?owner=         facets; exactly one ?provider= is that
 *                                     provider's page
 *   ?q=                               search
 *   ?layout=cards|list                the list's look (default: list from 13 accounts)
 *   ?group=provider|owner|pool        list grouping
 *   ?pool=<id>&tab=order|settings     an account group's page
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
import { EMPTY_LOGIN_FILTERS, type LoginFilters, type LoginStatusFilter } from "./provider-logins"

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

export const PROVIDER_VIEWS: readonly LoginStatusFilter[] = ["all", "attention", "unassigned", "mine"]
export type ProvidersLayout = "cards" | "list"
export type ProvidersGroup = "none" | "provider" | "owner" | "pool"
export const POOL_TABS = ["accounts", "order", "settings"] as const
export type PoolTab = (typeof POOL_TABS)[number]

export interface ProvidersUrlState {
  filters: LoginFilters
  /** null = the default for the list's size. */
  layout: ProvidersLayout | null
  group: ProvidersGroup
  /** An account group's page. */
  pool: string | null
  /** Its tab; null = Accounts. */
  poolTab: PoolTab | null
}

export const DEFAULT_PROVIDERS_STATE: ProvidersUrlState = {
  filters: EMPTY_LOGIN_FILTERS,
  layout: null,
  group: "none",
  pool: null,
  poolTab: null,
}

export interface VaultUrlState {
  space: VaultSpace
  filters: CredentialFilters
  group: VaultGroup
  /** The AI providers space's own state; the defaults unless `space` is providers. */
  providers: ProvidersUrlState
  /** The open credential, if any. */
  id: string | null
  /** The secret page's tab; null = Overview. */
  tab: SecretTab | null
}

export const DEFAULT_VAULT_STATE: VaultUrlState = {
  space: "secrets",
  filters: EMPTY_CREDENTIAL_FILTERS,
  group: "none",
  providers: DEFAULT_PROVIDERS_STATE,
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

function parseTab(raw: string | null): SecretTab | null {
  const tab = raw && (SECRET_TABS as readonly string[]).includes(raw) ? (raw as SecretTab) : null
  return tab === "overview" ? null : tab
}

function parseProviders(q: QueryLike): ProvidersUrlState {
  const viewRaw = q.get("view") as LoginStatusFilter | null
  const layoutRaw = q.get("layout")
  const groupRaw = q.get("group")
  const pool = q.get("pool") || null
  const tabRaw = q.get("tab")
  return {
    filters: {
      ...EMPTY_LOGIN_FILTERS,
      status: viewRaw && PROVIDER_VIEWS.includes(viewRaw) ? viewRaw : "all",
      provider: list(q, "provider", true),
      mode: list(q, "mode").filter((m) => m === "subscription" || m === "api_key"),
      owner: list(q, "owner"),
      search: q.get("q") ?? "",
    },
    layout: layoutRaw === "cards" || layoutRaw === "list" ? layoutRaw : null,
    group: groupRaw === "provider" || groupRaw === "owner" || groupRaw === "pool" ? groupRaw : "none",
    pool,
    poolTab:
      pool && tabRaw && tabRaw !== "accounts" && (POOL_TABS as readonly string[]).includes(tabRaw)
        ? (tabRaw as PoolTab)
        : null,
  }
}

export function parseVaultUrl(q: QueryLike): VaultUrlState {
  const tabRaw = q.get("tab")
  const spaceRaw = q.get("space")
  const space: VaultSpace =
    spaceRaw === "providers" || tabRaw === "providers" || Boolean(q.get("pool")) ? "providers" : "secrets"
  if (space === "providers") {
    return {
      ...DEFAULT_VAULT_STATE,
      space,
      providers: parseProviders(q),
      id: q.get("id") || null,
      tab: parseTab(tabRaw),
    }
  }
  const viewRaw = q.get("view") as CredentialStatusFilter | null
  const view = viewRaw && VIEWS.includes(viewRaw) ? viewRaw : "all"
  const tierRaw = q.get("tier")
  const tier = tierRaw && /^(?:[1-4]|guarded|unclassified)$/.test(tierRaw) ? tierRaw : null
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
    providers: DEFAULT_PROVIDERS_STATE,
    id: q.get("id") || null,
    tab: parseTab(tabRaw),
  }
}

function buildProvidersQuery(state: VaultUrlState, p: URLSearchParams) {
  p.set("space", "providers")
  const pv = state.providers ?? DEFAULT_PROVIDERS_STATE
  const f = pv.filters
  if (f.status !== "all") p.set("view", f.status)
  for (const v of f.provider) p.append("provider", v)
  for (const v of f.mode) p.append("mode", v)
  for (const v of f.owner) p.append("owner", v)
  if (f.search.trim()) p.set("q", f.search)
  if (pv.layout) p.set("layout", pv.layout)
  if (pv.group !== "none") p.set("group", pv.group)
  if (pv.pool) p.set("pool", pv.pool)
  if (state.id) {
    p.set("id", state.id)
    if (state.tab) p.set("tab", state.tab)
  } else if (pv.pool && pv.poolTab) {
    p.set("tab", pv.poolTab)
  }
}

/** The query string for a state, defaults left out. Starts with "?" or is "". */
export function buildVaultQuery(state: VaultUrlState): string {
  const p = new URLSearchParams()
  if (state.space === "providers") {
    buildProvidersQuery(state, p)
    return `?${p.toString()}`
  }
  const f = state.filters
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

/** True when the address names a space, a filter or a page — a bare /credentials does not. */
export function hasExplicitVaultState(q: QueryLike): boolean {
  for (const key of ["space", "tab", "id", "pool", "view", "q", "type", "brand", "crew", "tier", "class", "scope", "tag", "agent", "provider", "mode", "owner"]) {
    if (q.get(key) !== null) return true
  }
  return false
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

/** How many provider facets narrow the accounts (the view and the search are not facets). */
export function activeLoginFacetCount(f: LoginFilters): number {
  return f.provider.length + f.mode.length + f.owner.length
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
