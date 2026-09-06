// The Providers tab reads one object — `login` on a credential row (PRD
// provider-logins §10.1) — and every number it prints comes from these
// functions. What is worth pinning: the precedence of the status ladder, that
// the rail's counts and its filter agree, and that "unknown" stays unknown
// rather than turning into a figure.

import { describe, it, expect } from "vitest"
import {
  adapterProvider,
  applyLoginFilters,
  buildLoginModeFacet,
  buildLoginOwnerFacet,
  buildLoginProviderFacet,
  buildLoginStatusCounts,
  deriveLoginStatus,
  EMPTY_LOGIN_FILTERS,
  formatExpiresIn,
  hasLogin,
  loginBindingSlot,
  loginStatusLabel,
  loginSubtitle,
  loginTotals,
  paysForLabel,
  planLabel,
  sortLogins,
  UNOWNED,
  type LoginCredential,
  type ProviderLogin,
} from "../provider-logins"

const NOW = new Date("2026-09-06T10:00:00Z").getTime()
const DAY = 24 * 3600 * 1000

function login(over: Partial<ProviderLogin> = {}): ProviderLogin {
  return {
    mode: "subscription",
    provider: "ANTHROPIC",
    plan: "max",
    plan_label: "Max 20×",
    owner_user_id: "u1",
    owner_email: "pavel@unify.cz",
    expires_at: new Date(NOW + 212 * DAY).toISOString(),
    refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
    quota: null,
    delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" },
    pays_for: { agents: 3, crews: 1 },
    ...over,
  }
}

function row(name: string, over: Partial<ProviderLogin> = {}, status = "ACTIVE"): LoginCredential {
  return { id: name, name, provider: over.provider ?? "ANTHROPIC", status, login: login(over) }
}

const active = row("Claude Max · pavel")
const expiring = row("Claude Max · jana", { expires_at: new Date(NOW + 18 * DAY).toISOString(), pays_for: { agents: 2, crews: 0 } })
const atLimit = row("ChatGPT Plus · jana", {
  provider: "OPENAI",
  plan: "plus",
  plan_label: "ChatGPT Plus",
  owner_email: "jana@unify.cz",
  expires_at: new Date(NOW + 2 * DAY + 4 * 3600_000).toISOString(),
  refresh: { supported: true, status: "ok", last_at: null, next_at: null, error: null },
  quota: { window_5h_pct: 100, window_weekly_pct: 71, resets_at: new Date(NOW + 2 * 3600_000).toISOString() },
  delivery: { kind: "file", target: ".codex/auth.json" },
  pays_for: { agents: 1, crews: 0 },
})
const unassigned = row("Cursor Pro · pavel", { provider: "CURSOR", plan: "pro", plan_label: "Pro", expires_at: null, pays_for: { agents: 0, crews: 0 } })
const relogin = row("Gemini · jana", {
  provider: "GOOGLE",
  owner_email: "jana@unify.cz",
  refresh: { supported: true, status: "needs_relogin", last_at: null, next_at: null, error: "invalid_grant" },
  pays_for: { agents: 1, crews: 0 },
})
const all = [active, expiring, atLimit, unassigned, relogin]

describe("status ladder", () => {
  it("reads active, expiring, at limit, needs re-login in that precedence", () => {
    expect(deriveLoginStatus(active, NOW)).toBe("active")
    expect(deriveLoginStatus(expiring, NOW)).toBe("expiring")
    expect(deriveLoginStatus(atLimit, NOW)).toBe("at_limit")
    expect(deriveLoginStatus(relogin, NOW)).toBe("needs_relogin")
  })

  it("a seat at its limit that is also expiring reads as at limit — paused beats soon", () => {
    expect(deriveLoginStatus(atLimit, NOW)).toBe("at_limit")
  })

  it("a window that already reset is not a limit any more", () => {
    const reset = row("x", { quota: { window_5h_pct: 100, window_weekly_pct: 10, resets_at: new Date(NOW - 60_000).toISOString() } })
    expect(deriveLoginStatus(reset, NOW)).toBe("active")
  })

  it("the row's own RATE_LIMITED status counts as at limit even without quota", () => {
    expect(deriveLoginStatus(row("x", {}, "RATE_LIMITED"), NOW)).toBe("at_limit")
  })

  it("expired, revoked and pending win over everything", () => {
    expect(deriveLoginStatus(row("x", { expires_at: new Date(NOW - DAY).toISOString() }), NOW)).toBe("expired")
    expect(deriveLoginStatus(row("x", {}, "REVOKED"), NOW)).toBe("revoked")
    expect(deriveLoginStatus(row("x", {}, "PENDING_APPROVAL"), NOW)).toBe("pending")
  })

  it("prints the wireframe's words — 'at limit → HH:MM'", () => {
    const pill = loginStatusLabel(atLimit, NOW)
    expect(pill.tone).toBe("warn")
    expect(pill.label).toMatch(/^at limit → \d{1,2}:\d{2}/)
    expect(loginStatusLabel(relogin, NOW)).toMatchObject({ label: "needs re-login", tone: "destructive" })
    expect(loginStatusLabel(active, NOW)).toMatchObject({ label: "active", tone: "success" })
  })
})

describe("what the row prints", () => {
  it("expiry: days, days and hours inside three days, dash when unknown", () => {
    expect(formatExpiresIn(active.login!.expires_at, NOW)).toBe("in 212 d")
    expect(formatExpiresIn(atLimit.login!.expires_at, NOW)).toBe("in 2 d 4 h")
    expect(formatExpiresIn(null, NOW)).toBe("—")
    expect(formatExpiresIn(new Date(NOW - 1).toISOString(), NOW)).toBe("expired")
  })

  it("pays for: agents and crews, or nobody", () => {
    expect(paysForLabel({ agents: 3, crews: 1 })).toBe("3 agents · 1 crew")
    expect(paysForLabel({ agents: 1, crews: 0 })).toBe("1 agent")
    expect(paysForLabel({ agents: 0, crews: 0 })).toBe("nobody yet")
    expect(paysForLabel(null)).toBe("nobody yet")
  })

  it("subtitle names the brand, the mode and where it lands", () => {
    expect(loginSubtitle(active)).toBe("Anthropic · subscription · CLAUDE_CODE_OAUTH_TOKEN")
    expect(loginSubtitle(atLimit)).toBe("OpenAI · subscription · file .codex/auth.json")
    const key = row("Gemini API", { provider: "GOOGLE", mode: "api_key", delivery: { kind: "env", target: "GEMINI_API_KEY" } })
    expect(loginSubtitle(key)).toBe("Google AI / Gemini · API key · GEMINI_API_KEY · metered")
  })

  it("plan: the label, else the code, else pay-as-you-go for a key, else a dash", () => {
    expect(planLabel(active.login)).toBe("Max 20×")
    expect(planLabel(login({ plan_label: null, plan: "plus" }))).toBe("plus")
    expect(planLabel(login({ plan_label: null, plan: null, mode: "api_key" }))).toBe("pay-as-you-go")
    expect(planLabel(login({ plan_label: null, plan: null }))).toBe("—")
    expect(planLabel(null)).toBe("—")
  })

  it("hasLogin is a type guard over the optional field", () => {
    expect(hasLogin(active)).toBe(true)
    expect(hasLogin({ id: "s", name: "GH", provider: "GITHUB", status: "ACTIVE" })).toBe(false)
    expect(hasLogin({ id: "s", name: "GH", provider: "GITHUB", status: "ACTIVE", login: null })).toBe(false)
  })
})

describe("the rail", () => {
  it("counts and filters from the same predicate", () => {
    const counts = buildLoginStatusCounts(all, NOW)
    expect(counts).toEqual({ all: 5, at_limit: 1, expiring: 2, needs_relogin: 1, unassigned: 1 })
    for (const status of ["at_limit", "expiring", "needs_relogin", "unassigned"] as const) {
      expect(applyLoginFilters(all, { ...EMPTY_LOGIN_FILTERS, status }, NOW)).toHaveLength(counts[status])
    }
  })

  it("provider, mode and owner facets carry counts and readable labels", () => {
    expect(buildLoginProviderFacet(all)[0]).toEqual({ value: "ANTHROPIC", label: "Anthropic", count: 2 })
    expect(buildLoginModeFacet(all)).toEqual([{ value: "subscription", label: "Subscription", count: 5 }])
    const owners = buildLoginOwnerFacet(all)
    expect(owners).toEqual([
      { value: "pavel@unify.cz", label: "pavel@unify.cz", count: 3 },
      { value: "jana@unify.cz", label: "jana@unify.cz", count: 2 },
    ])
  })

  it("a seat with no owner files under the workspace, not under nobody", () => {
    const orphan = row("k", { owner_email: null, owner_user_id: null })
    expect(buildLoginOwnerFacet([orphan])).toEqual([{ value: UNOWNED, label: "Workspace (no owner)", count: 1 }])
    expect(applyLoginFilters([orphan, active], { ...EMPTY_LOGIN_FILTERS, owner: [UNOWNED] }, NOW)).toEqual([orphan])
  })

  it("facets AND, values inside a facet OR, search reads name owner and plan", () => {
    expect(applyLoginFilters(all, { ...EMPTY_LOGIN_FILTERS, provider: ["OPENAI", "CURSOR"] }, NOW).map((c) => c.name))
      .toEqual(["ChatGPT Plus · jana", "Cursor Pro · pavel"])
    expect(applyLoginFilters(all, { ...EMPTY_LOGIN_FILTERS, provider: ["OPENAI"], owner: ["pavel@unify.cz"] }, NOW)).toEqual([])
    expect(applyLoginFilters(all, { ...EMPTY_LOGIN_FILTERS, search: "plus" }, NOW).map((c) => c.name)).toEqual(["ChatGPT Plus · jana"])
    expect(applyLoginFilters(all, { ...EMPTY_LOGIN_FILTERS, search: "jana@" }, NOW)).toHaveLength(2)
  })

  it("sorts what needs a hand to the top", () => {
    expect(sortLogins(all, NOW).map((c) => deriveLoginStatus(c, NOW))).toEqual([
      "needs_relogin", "at_limit", "expiring", "active", "active",
    ])
  })
})

describe("the tiles", () => {
  it("seats by mode, at limit with the earliest reset and waiting agents, expiring with auto-refresh, unassigned", () => {
    const t = loginTotals(all, NOW)
    expect(t).toMatchObject({
      seats: 5, subscription: 5, apiKey: 0,
      atLimit: 1, agentsWaiting: 1, nextReset: atLimit.login!.quota!.resets_at,
      expiring: 2, expiringAutoRefreshed: 1,
      unassigned: 1, needsRelogin: 1,
    })
  })

  it("an empty tab is all zeroes, not a crash", () => {
    expect(loginTotals([], NOW)).toMatchObject({ seats: 0, atLimit: 0, expiring: 0, unassigned: 0, nextReset: null })
  })
})

describe("bindings", () => {
  it("env delivery binds under the variable the CLI reads; file delivery under the seat's name slot", () => {
    expect(loginBindingSlot(active.login)).toBe("CLAUDE_CODE_OAUTH_TOKEN")
    expect(loginBindingSlot(atLimit.login)).toBe("OPENAI_API_KEY")
    expect(loginBindingSlot(atLimit.login, "CODEX_SEAT")).toBe("CODEX_SEAT")
    expect(loginBindingSlot(login({ provider: "ANTHROPIC", mode: "api_key", delivery: { kind: "env", target: "" } }))).toBe("ANTHROPIC_API_KEY")
    expect(loginBindingSlot(null)).toBeNull()
  })

  it("maps an adapter to the provider it can pay with; OpenCode takes any", () => {
    expect(adapterProvider("CODEX_CLI")).toBe("OPENAI")
    expect(adapterProvider("CLAUDE_CODE")).toBe("ANTHROPIC")
    expect(adapterProvider("OPENCODE")).toBeNull()
    expect(adapterProvider(undefined)).toBeNull()
  })
})
