import { describe, expect, it } from "vitest"
import {
  EMPTY_SKILL_FILTERS,
  explorerCounts,
  inView,
  matchesSkill,
  skillTrust,
  sortSkills,
  type SkillAgentRef,
  type SkillFilters,
  type SkillRow,
} from "../skills-model"

function agent(id: string, crew: string, missing: string[] = []): SkillAgentRef {
  return {
    agent_id: id, agent_slug: id, agent_name: id[0].toUpperCase() + id.slice(1),
    avatar_seed: null, avatar_style: null, crew_id: crew, crew_slug: crew, crew_name: crew,
    crew_color: null, crew_icon: null, crew_avatar_style: null, missing_credentials: missing,
  }
}

function skill(id: string, over: Partial<SkillRow> = {}): SkillRow {
  return {
    id, name: id, slug: id, display_name: id, description: `${id} description`, version: "1.0.0", author: null,
    category: "CODING", source: "BUNDLED", icon: null, maturity: "OFFICIAL", verification: "VERIFIED", scan_status: "CLEAN",
    installed_on: [], needs_credentials: [], usage: { uses_7d: 0, errors_7d: 0, uses_total: 0, last_used_at: null },
    ...over,
  }
}

const f = (over: Partial<SkillFilters> = {}): SkillFilters => ({ ...EMPTY_SKILL_FILTERS, ...over })

describe("skillTrust", () => {
  it.each([
    [{ scan_status: "FLAGGED", verification: "VERIFIED" }, "flagged", "danger"],
    [{ scan_status: "BLOCKED", verification: "VERIFIED" }, "flagged", "danger"],
    [{ scan_status: "UNSCANNED", verification: "VERIFIED" }, "unscanned", "warn"],
    [{ scan_status: null, verification: null }, "unscanned", "warn"],
    [{ scan_status: "CLEAN", verification: "VERIFIED" }, "verified", "success"],
    [{ scan_status: "CLEAN", verification: "UNVERIFIED" }, "unverified", "muted"],
  ])("%o → %s", (input, level, tone) => {
    const t = skillTrust(input)
    expect(t.level).toBe(level)
    expect(t.tone).toBe(tone)
  })
})

describe("inView", () => {
  const held = skill("held", { installed_on: [agent("ava", "ops")] })
  const idle = skill("idle")
  const flaggedHeld = skill("bad", { scan_status: "FLAGGED", installed_on: [agent("ava", "ops")] })
  const flaggedIdle = skill("bad-idle", { scan_status: "FLAGGED" })
  const lacking = skill("lack", { needs_credentials: ["GH"], installed_on: [agent("ava", "ops", ["GH"])] })
  it.each([
    ["assigned", held, true],
    ["assigned", idle, false],
    ["unassigned", idle, true],
    ["attention", flaggedHeld, true],
    ["attention", flaggedIdle, false],
    ["attention", lacking, true],
    ["attention", held, false],
    ["proposed", held, false],
  ] as const)("%s holds %s → %s", (view, s, want) => {
    expect(inView(s, view)).toBe(want)
  })
})

describe("matchesSkill", () => {
  const rows = [
    skill("a", { installed_on: [agent("ava", "ops")], category: "DEVOPS" }),
    skill("b", { installed_on: [agent("ben", "sales")], source: "CUSTOM", needs_credentials: ["GH"] }),
    skill("c", { maturity: "EXPERIMENTAL", scan_status: "UNSCANNED" }),
  ]
  it.each([
    ["agent", f({ agentId: "ava" }), ["a"]],
    ["crew", f({ crewId: "sales" }), ["b"]],
    ["agent wins over crew", f({ agentId: "ava", crewId: "sales" }), ["a"]],
    ["domain", f({ domain: "DEVOPS" }), ["a"]],
    ["source", f({ sources: ["CUSTOM"] }), ["b"]],
    ["trust", f({ trust: ["unscanned"] }), ["c"]],
    ["maturity", f({ maturities: ["EXPERIMENTAL"] }), ["c"]],
    ["needs a credential", f({ needsCredential: true }), ["b"]],
    ["query by agent name", f({ query: "ben" }), ["b"]],
    ["query all terms", f({ query: "a description" }), ["a"]],
  ])("%s", (_name, filters, want) => {
    expect(rows.filter((s) => matchesSkill(s, filters)).map((s) => s.id)).toEqual(want)
  })
})

describe("explorerCounts", () => {
  it("counts each axis with its own selection left out", () => {
    const rows = [
      skill("a", { installed_on: [agent("ava", "ops"), agent("ben", "sales")] }),
      skill("b", { installed_on: [agent("ben", "sales")], category: "DESIGN" }),
      skill("c"),
    ]
    const c = explorerCounts(rows, f({ agentId: "ben", view: "assigned" }))
    // Picking Ben does not zero Ava's count…
    expect(c.byAgent.get("ava")).toBe(1)
    expect(c.byAgent.get("ben")).toBe(2)
    // …and the views count what Ben holds, whichever view is picked.
    expect(c.views.all).toBe(2)
    expect(c.views.unassigned).toBe(0)
    expect(c.byCrew.get("sales")).toBe(2)
    expect(c.byDomain.get("DESIGN")).toBe(1)
  })
})

describe("sortSkills", () => {
  const rows = [
    skill("beta", { usage: { uses_7d: 2, errors_7d: 0, uses_total: 5, last_used_at: null } }),
    skill("alpha", { installed_on: [agent("ava", "ops"), agent("ben", "ops")] }),
    skill("gamma", { usage: { uses_7d: 9, errors_7d: 0, uses_total: 9, last_used_at: null } }),
  ]
  it.each([
    ["used", ["gamma", "beta", "alpha"]],
    ["agents", ["alpha", "beta", "gamma"]],
    ["name", ["alpha", "beta", "gamma"]],
  ] as const)("%s", (sort, want) => {
    expect(sortSkills(rows, sort).map((s) => s.id)).toEqual(want)
  })
})
