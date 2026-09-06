/**
 * Provider logins — the seat an agent PAYS WITH, as opposed to a secret it
 * USES (docs/prd/provider-logins.md §1.1, §6, §10).
 *
 * Everything here reads the `login` object the credentials API attaches to a
 * row (§10.1) and nothing else: status, the rail's facets, the four stat tiles
 * and the strings the list prints all derive from the same fields, so a count
 * in the rail can never disagree with what clicking it selects, and a tile can
 * never claim a number the server did not send. Where the server sends null
 * (quota before P-D, an unknown expiry) the answer is "unknown", never a
 * placeholder figure.
 */

import { getBrand } from "@/lib/credential-providers/registry"
import { EXPIRY_WARNING_DAYS } from "./facets"
import type { ProviderLoginMode } from "./item-types"

export type { ProviderLoginMode }

export type ProviderLoginRefreshStatus = "ok" | "pending" | "failed" | "needs_relogin" | "none"

/** The `login` object of contract §10.1, field for field. */
export interface ProviderLogin {
  mode: ProviderLoginMode
  provider: string
  plan: string | null
  plan_label: string | null
  owner_user_id: string | null
  owner_email: string | null
  /** Access-token / setup-token expiry. null = unknown or never. */
  expires_at: string | null
  refresh: {
    supported: boolean
    status: ProviderLoginRefreshStatus
    last_at: string | null
    next_at: string | null
    error: string | null
  }
  /** P-D. null until the server reads the provider's rate-limit windows. */
  quota: {
    window_5h_pct: number | null
    window_weekly_pct: number | null
    resets_at: string | null
  } | null
  delivery: { kind: "env" | "file"; target: string }
  pays_for: { agents: number; crews: number }
}

/** A credential row that may carry a login. */
export interface LoginCredential {
  id: string
  name: string
  provider: string
  status: string
  type?: string
  token_expires_at?: string | null
  last_used_at?: string | null
  login?: ProviderLogin | null
}

export function hasLogin<T extends LoginCredential>(c: T): c is T & { login: ProviderLogin } {
  return c.login != null && typeof c.login === "object"
}

/**
 * What the row says about itself, in the order the questions bind.
 *
 * A revoked or expired seat is unusable whatever its quota says; a seat that
 * lost its refresh needs a person before it needs anything else; a seat at its
 * window limit is paused, not broken; a seat about to expire still works today.
 */
export type ProviderLoginStatus =
  | "active"
  | "at_limit"
  | "expiring"
  | "needs_relogin"
  | "expired"
  | "revoked"
  | "pending"

const DAY_MS = 24 * 3600 * 1000

function ms(value: string | null | undefined): number | null {
  if (!value) return null
  const t = new Date(value).getTime()
  return Number.isNaN(t) ? null : t
}

/** The expiry the login reports, falling back to the row's own column. */
export function loginExpiresAt(c: LoginCredential): string | null {
  return c.login?.expires_at ?? c.token_expires_at ?? null
}

/** Days until the seat expires; null when the server does not know. */
export function loginDaysUntilExpiry(c: LoginCredential, now = Date.now()): number | null {
  const t = ms(loginExpiresAt(c))
  if (t === null) return null
  return Math.floor((t - now) / DAY_MS)
}

export function isAtLimit(c: LoginCredential, now = Date.now()): boolean {
  if (c.status === "RATE_LIMITED") return true
  const q = c.login?.quota
  if (!q) return false
  // A window that has already reset is not a limit any more, whatever the
  // percentage said when it was read.
  const resets = ms(q.resets_at)
  if (resets !== null && resets <= now) return false
  return (q.window_5h_pct ?? 0) >= 100 || (q.window_weekly_pct ?? 0) >= 100
}

export function deriveLoginStatus(c: LoginCredential, now = Date.now()): ProviderLoginStatus {
  if (c.status === "PENDING_APPROVAL") return "pending"
  if (c.status === "REVOKED") return "revoked"
  const days = loginDaysUntilExpiry(c, now)
  if (c.status === "EXPIRED" || (days !== null && days < 0)) return "expired"
  if (c.login?.refresh.status === "needs_relogin") return "needs_relogin"
  if (isAtLimit(c, now)) return "at_limit"
  if (days !== null && days < EXPIRY_WARNING_DAYS) return "expiring"
  return "active"
}

export type LoginStatusTone = "success" | "warn" | "destructive" | "default"

/** What a status pill prints — "at limit → 11:55", the wireframe's own words. */
export function loginStatusLabel(c: LoginCredential, now = Date.now()): { label: string; tone: LoginStatusTone; status: ProviderLoginStatus } {
  const status = deriveLoginStatus(c, now)
  switch (status) {
    case "at_limit": {
      const resets = c.login?.quota?.resets_at ? formatClock(c.login.quota.resets_at) : null
      return { label: resets ? `at limit → ${resets}` : "at limit", tone: "warn", status }
    }
    case "expiring":
      return { label: "expiring", tone: "warn", status }
    case "needs_relogin":
      return { label: "needs re-login", tone: "destructive", status }
    case "expired":
      return { label: "expired", tone: "destructive", status }
    case "revoked":
      return { label: "revoked", tone: "default", status }
    case "pending":
      return { label: "pending", tone: "default", status }
    default:
      return { label: "active", tone: "success", status }
  }
}

/** HH:MM in the reader's locale, for "resets 11:55". */
export function formatClock(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  // h23 rather than the locale's default: the wireframe's "at limit → 11:55"
  // is a clock reading, and "→ 11:55 AM" is two words longer in a pill that
  // has room for none.
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })
}

/**
 * "in 212 d", "in 2 d 4 h", "today", "expired", or "—" when unknown.
 *
 * Hours appear only inside the last three days — that is when the difference
 * between "2 d" and "2 d 4 h" decides whether a refresh runs before a run does.
 */
export function formatExpiresIn(iso: string | null | undefined, now = Date.now()): string {
  const t = ms(iso)
  if (t === null) return "—"
  const diff = t - now
  if (diff < 0) return "expired"
  const hours = Math.floor(diff / 3600_000)
  const days = Math.floor(hours / 24)
  if (days === 0) return hours === 0 ? "today" : `in ${hours} h`
  if (days < 3) {
    const rest = hours - days * 24
    return rest > 0 ? `in ${days} d ${rest} h` : `in ${days} d`
  }
  return `in ${days} d`
}

/** "3 agents · 1 crew", "1 agent", or "nobody yet". */
export function paysForLabel(paysFor: ProviderLogin["pays_for"] | null | undefined): string {
  const agents = paysFor?.agents ?? 0
  const crews = paysFor?.crews ?? 0
  if (agents === 0 && crews === 0) return "nobody yet"
  const parts: string[] = []
  if (agents > 0) parts.push(`${agents} agent${agents === 1 ? "" : "s"}`)
  if (crews > 0) parts.push(`${crews} crew${crews === 1 ? "" : "s"}`)
  return parts.join(" · ")
}

export function isUnassigned(c: LoginCredential): boolean {
  const p = c.login?.pays_for
  return !p || (p.agents === 0 && p.crews === 0)
}

export function modeLabel(mode: ProviderLoginMode | string | null | undefined): string {
  return mode === "api_key" ? "API key" : "subscription"
}

/** "Anthropic · subscription · CLAUDE_CODE_OAUTH_TOKEN" — the row's second line. */
export function loginSubtitle(c: LoginCredential): string {
  const login = c.login
  const brand = getBrand(login?.provider ?? c.provider)
  const parts = [brand.label, modeLabel(login?.mode)]
  if (login?.delivery?.target) {
    parts.push(login.delivery.kind === "file" ? `file ${login.delivery.target}` : login.delivery.target)
  }
  if (login?.mode === "api_key") parts.push("metered")
  return parts.join(" · ")
}

/** The plan as the list prints it: the server's label, else its raw code, else unknown. */
export function planLabel(login: ProviderLogin | null | undefined): string {
  if (!login) return "—"
  if (login.plan_label) return login.plan_label
  if (login.plan) return login.plan
  return login.mode === "api_key" ? "pay-as-you-go" : "—"
}

/**
 * The slot an AGENT-scope binding claims for this seat.
 *
 * For env delivery the CLI reads a specific variable, so that is the slot.
 * For file delivery the slot is only a name — the file lands where the CLI
 * looks regardless — and the wizard's own suggestion is the one already in
 * use, so the pool rule (same scope, same slot) keeps working.
 */
export function loginBindingSlot(login: ProviderLogin | null | undefined, fallback: string | null = null): string | null {
  if (login?.delivery?.kind === "env" && login.delivery.target) return login.delivery.target
  if (fallback) return fallback
  const provider = (login?.provider ?? "").toUpperCase()
  const byProvider: Record<string, string> = {
    ANTHROPIC: "CLAUDE_CODE_OAUTH_TOKEN",
    OPENAI: "OPENAI_API_KEY",
    GOOGLE: "GEMINI_API_KEY",
    CURSOR: "CURSOR_API_KEY",
    FACTORY: "FACTORY_API_KEY",
  }
  if (login?.mode === "api_key" && provider === "ANTHROPIC") return "ANTHROPIC_API_KEY"
  return byProvider[provider] ?? null
}

/**
 * Which provider a CLI adapter can pay with. Mirrors the AuthDelivery table of
 * PRD §5.2; OpenCode takes any provider's key, so it maps to nothing here and
 * every seat is offered.
 */
export function adapterProvider(cliAdapter: string | null | undefined): string | null {
  switch ((cliAdapter ?? "").toUpperCase()) {
    case "CLAUDE_CODE":
      return "ANTHROPIC"
    case "CODEX_CLI":
      return "OPENAI"
    case "GEMINI_CLI":
      return "GOOGLE"
    case "CURSOR_CLI":
      return "CURSOR"
    case "FACTORY_DROID":
      return "FACTORY"
    default:
      return null
  }
}

export function adapterLabel(cliAdapter: string | null | undefined): string {
  switch ((cliAdapter ?? "").toUpperCase()) {
    case "CLAUDE_CODE":
      return "Claude Code"
    case "CODEX_CLI":
      return "Codex CLI"
    case "GEMINI_CLI":
      return "Gemini CLI"
    case "CURSOR_CLI":
      return "Cursor CLI"
    case "FACTORY_DROID":
      return "Factory Droid"
    case "OPENCODE":
      return "OpenCode"
    default:
      return cliAdapter ?? "CLI"
  }
}

// ── The rail ────────────────────────────────────────────────────────────────

export type LoginStatusFilter = "all" | "at_limit" | "expiring" | "needs_relogin" | "unassigned"

export interface LoginFilters {
  status: LoginStatusFilter
  provider: string[]
  mode: string[]
  owner: string[]
  search: string
}

export const EMPTY_LOGIN_FILTERS: LoginFilters = {
  status: "all",
  provider: [],
  mode: [],
  owner: [],
  search: "",
}

export interface LoginFacetOption {
  value: string
  label: string
  count: number
}

export interface LoginStatusCounts {
  all: number
  at_limit: number
  expiring: number
  needs_relogin: number
  unassigned: number
}

export function buildLoginStatusCounts(logins: LoginCredential[], now = Date.now()): LoginStatusCounts {
  const out: LoginStatusCounts = { all: logins.length, at_limit: 0, expiring: 0, needs_relogin: 0, unassigned: 0 }
  for (const c of logins) {
    const s = deriveLoginStatus(c, now)
    if (s === "at_limit") out.at_limit++
    if (s === "needs_relogin") out.needs_relogin++
    // Expiring counts every seat inside the window, including one that is
    // also at its limit — the tile asks "what expires", not "what is worst".
    const days = loginDaysUntilExpiry(c, now)
    if (days !== null && days >= 0 && days < EXPIRY_WARNING_DAYS) out.expiring++
    if (isUnassigned(c)) out.unassigned++
  }
  return out
}

function countBy(logins: LoginCredential[], key: (c: LoginCredential) => string | null, label: (v: string) => string): LoginFacetOption[] {
  const counts = new Map<string, number>()
  for (const c of logins) {
    const v = key(c)
    if (!v) continue
    counts.set(v, (counts.get(v) ?? 0) + 1)
  }
  return Array.from(counts.entries())
    .map(([value, count]) => ({ value, label: label(value), count }))
    .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
}

export function buildLoginProviderFacet(logins: LoginCredential[]): LoginFacetOption[] {
  return countBy(logins, (c) => c.login?.provider ?? c.provider, (v) => getBrand(v).label)
}

export function buildLoginModeFacet(logins: LoginCredential[]): LoginFacetOption[] {
  return countBy(logins, (c) => c.login?.mode ?? null, (v) => (v === "api_key" ? "API key" : "Subscription"))
}

/** Owners by email; a seat the server did not attribute is "workspace". */
export const UNOWNED = "__workspace__"

export function buildLoginOwnerFacet(logins: LoginCredential[]): LoginFacetOption[] {
  return countBy(
    logins,
    (c) => c.login?.owner_email || c.login?.owner_user_id || UNOWNED,
    (v) => (v === UNOWNED ? "Workspace (no owner)" : v),
  )
}

function ownerKey(c: LoginCredential): string {
  return c.login?.owner_email || c.login?.owner_user_id || UNOWNED
}

export function applyLoginFilters<T extends LoginCredential>(logins: T[], filters: LoginFilters, now = Date.now()): T[] {
  const q = filters.search.trim().toLowerCase()
  return logins.filter((c) => {
    const status = deriveLoginStatus(c, now)
    if (filters.status === "at_limit" && status !== "at_limit") return false
    if (filters.status === "needs_relogin" && status !== "needs_relogin") return false
    if (filters.status === "expiring") {
      const days = loginDaysUntilExpiry(c, now)
      if (days === null || days < 0 || days >= EXPIRY_WARNING_DAYS) return false
    }
    if (filters.status === "unassigned" && !isUnassigned(c)) return false
    if (filters.provider.length > 0 && !filters.provider.includes(c.login?.provider ?? c.provider)) return false
    if (filters.mode.length > 0 && !filters.mode.includes(c.login?.mode ?? "")) return false
    if (filters.owner.length > 0 && !filters.owner.includes(ownerKey(c))) return false
    if (q) {
      const hay = [c.name, c.login?.owner_email ?? "", planLabel(c.login), getBrand(c.login?.provider ?? c.provider).label]
        .join(" ")
        .toLowerCase()
      if (!hay.includes(q)) return false
    }
    return true
  })
}

// ── The tiles ───────────────────────────────────────────────────────────────

export interface LoginTotals {
  seats: number
  subscription: number
  apiKey: number
  atLimit: number
  /** Agents whose only seat is at its limit — "1 agent waiting". */
  agentsWaiting: number
  /** The earliest reset among the seats at limit, for "resets 11:55". */
  nextReset: string | null
  expiring: number
  /** How many of the expiring seats the server refreshes on its own. */
  expiringAutoRefreshed: number
  unassigned: number
  needsRelogin: number
}

export function loginTotals(logins: LoginCredential[], now = Date.now()): LoginTotals {
  const t: LoginTotals = {
    seats: logins.length,
    subscription: 0,
    apiKey: 0,
    atLimit: 0,
    agentsWaiting: 0,
    nextReset: null,
    expiring: 0,
    expiringAutoRefreshed: 0,
    unassigned: 0,
    needsRelogin: 0,
  }
  for (const c of logins) {
    if (c.login?.mode === "api_key") t.apiKey++
    else t.subscription++
    const status = deriveLoginStatus(c, now)
    if (status === "at_limit") {
      t.atLimit++
      t.agentsWaiting += c.login?.pays_for?.agents ?? 0
      const resets = c.login?.quota?.resets_at ?? null
      if (resets && (!t.nextReset || ms(resets)! < ms(t.nextReset)!)) t.nextReset = resets
    }
    if (status === "needs_relogin") t.needsRelogin++
    const days = loginDaysUntilExpiry(c, now)
    if (days !== null && days >= 0 && days < EXPIRY_WARNING_DAYS) {
      t.expiring++
      if (c.login?.refresh.supported) t.expiringAutoRefreshed++
    }
    if (isUnassigned(c)) t.unassigned++
  }
  return t
}

/** Sort: what needs a hand first, then by name. */
export function sortLogins<T extends LoginCredential>(logins: T[], now = Date.now()): T[] {
  const rank: Record<ProviderLoginStatus, number> = {
    needs_relogin: 0,
    expired: 1,
    at_limit: 2,
    expiring: 3,
    pending: 4,
    revoked: 5,
    active: 6,
  }
  return [...logins].sort((a, b) => {
    const ra = rank[deriveLoginStatus(a, now)]
    const rb = rank[deriveLoginStatus(b, now)]
    if (ra !== rb) return ra - rb
    return a.name.localeCompare(b.name)
  })
}
