import { describe, expect, it } from "vitest"

import { applyCredentialFilters, type CredentialLike } from "../facets"
import { buildVaultQuery, hasExplicitVaultState, parseVaultUrl, resolveCrewTokens } from "../vault-url-state"

const NOW = Date.parse("2026-10-17T12:00:00Z")
const DAY = 24 * 3600 * 1000

function cred(id: string, over: Partial<CredentialLike> = {}): CredentialLike {
  return { id, name: id, provider: "GITHUB", type: "API_KEY", status: "ACTIVE", scope: "WORKSPACE", ...over }
}

const ROWS: CredentialLike[] = [
  cred("gh", { agent_ids: ["a-research"], security_level: 2 }),
  cred("stripe", { provider: "STRIPE", sensitivity: "SEALED", security_level: 4, scope: "CREW", crew_ids: ["crew-pay"] }),
  cred("ssh", { type: "SSH_KEY", sensitivity: "RESTRICTED", security_level: 3 }),
  cred("old", { last_used_at: new Date(NOW - 40 * DAY).toISOString() }),
  cred("fresh", { last_used_at: new Date(NOW - 2 * DAY).toISOString() }),
  cred("dead", { status: "REVOKED" }),
  cred("soon", { token_expires_at: new Date(NOW + 5 * DAY).toISOString() }),
]
const AGENT_CREW = new Map([["a-research", "crew-research"]])

function filtered(query: string) {
  const state = parseVaultUrl(new URLSearchParams(query))
  return applyCredentialFilters(ROWS, state.filters, new Set(), { agentCrew: AGENT_CREW, now: NOW }).map((c) => c.id)
}

describe("vault URL state", () => {
  it("?crew ?class ?type ?tier ?scope filter the table and survive reload", () => {
    expect(filtered("crew=crew-research")).toEqual(["gh"])
    expect(filtered("crew=crew-pay")).toEqual(["stripe"])
    expect(filtered("class=SEALED")).toEqual(["stripe"])
    expect(filtered("class=sealed,restricted")).toEqual(["stripe", "ssh"])
    expect(filtered("type=SSH_KEY")).toEqual(["ssh"])
    expect(filtered("tier=4")).toEqual(["stripe"])
    expect(filtered("scope=crew:crew-pay")).toEqual(["stripe"])

    for (const q of ["crew=crew-research&class=STANDARD", "type=SSH_KEY&tier=3", "view=unused&brand=GITHUB&q=ol"]) {
      const once = parseVaultUrl(new URLSearchParams(q))
      const again = parseVaultUrl(new URLSearchParams(buildVaultQuery(once).slice(1)))
      expect(again).toEqual(once)
    }
  })

  it("reads the views the panel and the health tiles open", () => {
    expect(filtered("view=rejected")).toEqual(["dead"])
    expect(filtered("view=expiring")).toEqual(["soon"])
    expect(filtered("view=recent")).toEqual(["fresh"])
    expect(filtered("view=unused")).not.toContain("fresh")
    expect(filtered("view=nonsense")).toHaveLength(ROWS.length)
  })

  it("keeps ?tab=providers as the AI providers space and ?tab= as the secret page tab", () => {
    expect(parseVaultUrl(new URLSearchParams("tab=providers")).space).toBe("providers")
    expect(parseVaultUrl(new URLSearchParams("space=providers")).space).toBe("providers")
    const page = parseVaultUrl(new URLSearchParams("id=c1&tab=history"))
    expect(page).toMatchObject({ space: "secrets", id: "c1", tab: "history" })
    expect(buildVaultQuery(page)).toBe("?id=c1&tab=history")
    expect(parseVaultUrl(new URLSearchParams("id=c1&tab=overview")).tab).toBeNull()
  })

  it("drops values it does not know instead of filtering everything away", () => {
    const s = parseVaultUrl(new URLSearchParams("class=TOPSECRET&tier=9&group=folder"))
    expect(s.filters.classification).toEqual([])
    expect(s.filters.tier).toBeNull()
    expect(s.group).toBe("none")
    expect(buildVaultQuery(s)).toBe("")
  })

  it("reads the AI providers space: views, facets, layout, grouping and the group page", () => {
    const s = parseVaultUrl(new URLSearchParams("space=providers&view=attention&provider=anthropic&mode=api_key&mode=bogus&owner=jana@acme.dev&layout=list&group=owner&q=max"))
    expect(s.space).toBe("providers")
    expect(s.providers.filters).toMatchObject({ status: "attention", provider: ["ANTHROPIC"], mode: ["api_key"], owner: ["jana@acme.dev"], search: "max" })
    expect(s.providers).toMatchObject({ layout: "list", group: "owner", pool: null })
    // A secrets view name means nothing here.
    expect(parseVaultUrl(new URLSearchParams("space=providers&view=recent")).providers.filters.status).toBe("all")
    const again = parseVaultUrl(new URLSearchParams(buildVaultQuery(s).slice(1)))
    expect(again).toEqual(s)

    const pool = parseVaultUrl(new URLSearchParams("pool=p1&tab=order"))
    expect(pool).toMatchObject({ space: "providers", id: null, tab: null, providers: { pool: "p1", poolTab: "order" } })
    expect(buildVaultQuery(pool)).toBe("?space=providers&pool=p1&tab=order")
    // An account page inside the space keeps its own tab.
    const acc = parseVaultUrl(new URLSearchParams("space=providers&id=c1&tab=access"))
    expect(acc).toMatchObject({ id: "c1", tab: "access" })
    expect(buildVaultQuery(acc)).toBe("?space=providers&id=c1&tab=access")
  })

  it("a bare address names nothing, so the remembered space may apply", () => {
    expect(hasExplicitVaultState(new URLSearchParams(""))).toBe(false)
    expect(hasExplicitVaultState(new URLSearchParams("space=secrets"))).toBe(true)
    expect(hasExplicitVaultState(new URLSearchParams("id=c1"))).toBe(true)
    expect(hasExplicitVaultState(new URLSearchParams("crew=research"))).toBe(true)
  })

  it("resolves a crew slug from an entity link to the crew id", () => {
    const crews = { "crew-research": { id: "crew-research", slug: "research" } }
    expect(resolveCrewTokens(["research", "crew-research", "unknown"], crews)).toEqual(["crew-research", "crew-research", "unknown"])
  })
})
