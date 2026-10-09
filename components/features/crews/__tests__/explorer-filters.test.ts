import { describe, it, expect } from "vitest"
import {
  EMPTY_FILTERS,
  DEFAULT_VIEW,
  activeFilterCount,
  agentActivity,
  agentBucket,
  agentMatches,
  bucketCounts,
  crewHealth,
  crewSubline,
  explorerFacets,
  explorerStateFromParams,
  explorerStateToParams,
  filterChips,
  isNarrowing,
  shortAgo,
  sortAgents,
  type ExplorerFilterAgent,
  type ExplorerFilterContext,
} from "@/components/features/crews/explorer-filters"

const NOW = new Date("2026-10-09T12:00:00Z").getTime()
const hoursAgo = (h: number) => new Date(NOW - h * 3600_000).toISOString()

const agent = (id: string, extra: Partial<ExplorerFilterAgent> = {}): ExplorerFilterAgent => ({
  id, name: id[0].toUpperCase() + id.slice(1), slug: id, status: "IDLE", role_title: null, agent_role: "AGENT",
  crew_id: "sales", llm_model: "claude-haiku-4-5", cli_adapter: "CLAUDE_CODE", last_active_at: null, ...extra,
})

const ctx: ExplorerFilterContext = {
  now: NOW,
  provisioningByCrew: new Map([["ops", "needs_provision"]]),
  gapsByCrew: new Map([["fin", 2]]),
}

// dev4's roster plus the states a busy day adds.
const roster = [
  agent("alex", { agent_role: "LEAD", llm_model: "claude-sonnet-5", status: "RUNNING", last_active_at: hoursAgo(0.1) }),
  agent("jamie", { last_active_at: hoursAgo(24 * 9) }),
  agent("taylor", { last_active_at: hoursAgo(30) }),
  agent("jordan", { crew_id: "fin", agent_role: "LEAD", llm_model: "claude-sonnet-5", status: "WAITING" }),
  agent("riley", { crew_id: "ops", status: "ERROR", cli_adapter: "CODEX_CLI", llm_model: "gpt-5-codex", last_active_at: hoursAgo(1) }),
  agent("quinn", { crew_id: "ops", ephemeral: true, expires_at: hoursAgo(-3) }),
  agent("drew", { crew_id: "fin", ephemeral: true, expired_at: hoursAgo(2), status: "ERROR" }),
]

describe("agentBucket", () => {
  it("files every agent under exactly one status", () => {
    expect(roster.map((a) => [a.id, agentBucket(a)])).toEqual([
      ["alex", "working"], ["jamie", "idle"], ["taylor", "idle"], ["jordan", "needs"],
      ["riley", "needs"], ["quinn", "idle"], ["drew", "expired"],
    ])
  })
  it("treats review and approval waits as needing a person", () => {
    expect(agentBucket(agent("a", { status: "PENDING_REVIEW" }))).toBe("needs")
    expect(agentBucket(agent("a", { status: "AWAITING_APPROVAL" }))).toBe("needs")
  })
})

describe("agentActivity", () => {
  it("reads last_active_at as today, this week, quiet or never", () => {
    expect(agentActivity(roster[0], NOW)).toBe("today")
    expect(agentActivity(roster[2], NOW)).toBe("week")
    expect(agentActivity(roster[1], NOW)).toBe("quiet")
    expect(agentActivity(roster[3], NOW)).toBe("never")
    expect(agentActivity(agent("x", { last_active_at: "garbage" }), NOW)).toBe("never")
  })
})

describe("crewHealth", () => {
  it("reports a rebuild for a stale or failed image and gaps for missing credentials", () => {
    expect(crewHealth("ops", ctx)).toEqual(["rebuild"])
    expect(crewHealth("fin", ctx)).toEqual(["gaps"])
    expect(crewHealth("sales", ctx)).toEqual([])
    expect(crewHealth(null, ctx)).toEqual([])
    expect(crewHealth("x", { ...ctx, provisioningByCrew: new Map([["x", "failed"]]) })).toEqual(["rebuild"])
  })
})

describe("agentMatches", () => {
  it("combines facets with AND and values within a facet with OR", () => {
    const f = { ...EMPTY_FILTERS, role: ["lead"], model: ["claude-sonnet-5", "gpt-5-codex"] }
    expect(roster.filter((a) => agentMatches(a, f, ctx)).map((a) => a.id)).toEqual(["alex", "jordan"])
  })
  it("filters by status bucket, hire type, activity and crew health", () => {
    const ids = (f: Partial<typeof EMPTY_FILTERS>) => roster.filter((a) => agentMatches(a, { ...EMPTY_FILTERS, ...f }, ctx)).map((a) => a.id)
    expect(ids({ bucket: "needs" })).toEqual(["jordan", "riley"])
    expect(ids({ hire: ["temporary"] })).toEqual(["quinn", "drew"])
    expect(ids({ activity: ["quiet", "never"] })).toEqual(["jamie", "jordan", "quinn", "drew"])
    expect(ids({ health: ["rebuild"] })).toEqual(["riley", "quinn"])
    expect(ids({ runtime: ["CODEX_CLI"] })).toEqual(["riley"])
  })
  it("can leave the bucket out, which is how the bucket counts are made", () => {
    expect(agentMatches(roster[0], { ...EMPTY_FILTERS, bucket: "idle" }, ctx, { ignoreBucket: true })).toBe(true)
  })
})

describe("bucketCounts", () => {
  it("counts each bucket under the other filters, never under the bucket itself", () => {
    const counts = bucketCounts(roster, { ...EMPTY_FILTERS, bucket: "needs" }, ctx)
    expect(counts).toEqual({ all: 7, needs: 2, working: 1, idle: 3, expired: 1 })
    expect(bucketCounts(roster, { ...EMPTY_FILTERS, role: ["lead"] }, ctx)).toEqual({ all: 2, needs: 1, working: 1, idle: 0, expired: 0 })
  })
})

describe("explorerFacets", () => {
  it("lists only facets with a choice to make, with counts", () => {
    const facets = explorerFacets(roster, EMPTY_FILTERS, ctx)
    expect(facets.map((f) => f.key)).toEqual(["role", "model", "runtime", "hire", "activity", "health"])
    expect(facets.find((f) => f.key === "model")?.options).toEqual([
      { value: "claude-haiku-4-5", label: "claude-haiku-4-5", count: 4 },
      { value: "claude-sonnet-5", label: "claude-sonnet-5", count: 2 },
      { value: "gpt-5-codex", label: "gpt-5-codex", count: 1 },
    ].map((o) => ({ ...o, label: expect.any(String) })))
    expect(facets.find((f) => f.key === "runtime")?.options.map((o) => o.label)).toEqual(["Claude Code", "Codex CLI"])
  })
  it("hides a facet where every agent has the same value, unless it is in use", () => {
    const sameModel = roster.map((a) => ({ ...a, llm_model: "claude-haiku-4-5", cli_adapter: "CLAUDE_CODE" }))
    const keys = explorerFacets(sameModel, EMPTY_FILTERS, ctx).map((f) => f.key)
    expect(keys).not.toContain("model")
    expect(keys).not.toContain("runtime")
    const inUse = explorerFacets(sameModel, { ...EMPTY_FILTERS, model: ["claude-haiku-4-5"] }, ctx).map((f) => f.key)
    expect(inUse).toContain("model")
  })
  it("keeps Crew health with a single value: it splits the roster in two", () => {
    const facets = explorerFacets(roster, EMPTY_FILTERS, { ...ctx, gapsByCrew: new Map() })
    expect(facets.find((f) => f.key === "health")?.options.map((o) => o.value)).toEqual(["rebuild"])
  })
})

describe("filters in the URL", () => {
  it("round-trips filters and view, and leaves defaults out", () => {
    const filters = { ...EMPTY_FILTERS, bucket: "needs" as const, model: ["claude-sonnet-5", "gpt 5"], health: ["gaps" as const] }
    const view = { group: "status" as const, sort: "active" as const, details: false }
    const params = explorerStateToParams(filters, view)
    expect(params).toMatchObject({ status: "needs", model: "claude-sonnet-5,gpt 5", health: "gaps", group: "status", sort: "active", details: "off", role: null })
    const back = explorerStateFromParams(new URLSearchParams(Object.entries(params).filter(([, v]) => v != null) as [string, string][]))
    expect(back).toEqual({ filters, view })
    expect(Object.values(explorerStateToParams(EMPTY_FILTERS, DEFAULT_VIEW)).every((v) => v === null)).toBe(true)
  })
  it("ignores values it does not know", () => {
    const { filters, view } = explorerStateFromParams(new URLSearchParams("status=bogus&group=x&sort=y&activity=today,forever&role=lead,boss"))
    expect(filters.bucket).toBe("all")
    expect(filters.activity).toEqual(["today"])
    expect(filters.role).toEqual(["lead"])
    expect(view).toEqual(DEFAULT_VIEW)
  })
})

describe("chips and counts", () => {
  it("counts facet picks, not the status bucket, which has its own section", () => {
    const f = { ...EMPTY_FILTERS, bucket: "needs" as const, role: ["lead"], activity: ["today" as const, "week" as const] }
    expect(activeFilterCount(f)).toBe(3)
    expect(isNarrowing(f, "")).toBe(true)
    expect(isNarrowing(EMPTY_FILTERS, "  ")).toBe(false)
    expect(filterChips(f).map((c) => c.label)).toEqual(["Needs you", "Lead", "Active in the last 24 h", "Active this week"])
  })
})

describe("sortAgents", () => {
  it("puts the lead first, then sorts by name or by the latest activity", () => {
    const crew = [roster[1], roster[2], roster[0]]
    expect(sortAgents(crew, "name").map((a) => a.id)).toEqual(["alex", "jamie", "taylor"])
    expect(sortAgents([roster[1], roster[2], roster[3]], "active").map((a) => a.id)).toEqual(["jordan", "taylor", "jamie"])
    expect(sortAgents([roster[1], roster[2]], "active", { leadFirst: false }).map((a) => a.id)).toEqual(["taylor", "jamie"])
  })
})

describe("crewSubline", () => {
  it("names the running mission, else the last activity, else that nothing ran", () => {
    expect(crewSubline(roster.slice(0, 3), 1, NOW)).toBe("1 mission running")
    expect(crewSubline(roster.slice(0, 3), 2, NOW)).toBe("2 missions running")
    expect(crewSubline(roster.slice(1, 3), 0, NOW)).toBe("Active 1d ago")
    expect(crewSubline([roster[3]], 0, NOW)).toBe("No work yet")
  })
})

describe("shortAgo", () => {
  it("writes minutes, hours and days", () => {
    expect(shortAgo(hoursAgo(0.01), NOW)).toBe("now")
    expect(shortAgo(hoursAgo(0.5), NOW)).toBe("30m")
    expect(shortAgo(hoursAgo(5), NOW)).toBe("5h")
    expect(shortAgo(hoursAgo(24 * 9), NOW)).toBe("9d")
    expect(shortAgo(null, NOW)).toBeNull()
  })
})
