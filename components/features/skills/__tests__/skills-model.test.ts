import { describe, expect, it } from "vitest"
import {
  EMPTY_SKILL_FILTERS,
  attentionReasons,
  groupSkills,
  inView,
  matchesSkill,
  panelCounts,
  repoFromUrl,
  skillSource,
  skillTrust,
  sortSkills,
  tabFromParam,
  viewFromParam,
  visibleSkills,
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
    lifecycle_state: "active", installed_on: [], needs_credentials: [], usage: { uses_7d: 0, errors_7d: 0, uses_total: 0, last_used_at: null },
    ...over,
  }
}

const f = (over: Partial<SkillFilters> = {}): SkillFilters => ({ ...EMPTY_SKILL_FILTERS, ...over })

describe("skillTrust", () => {
  it.each([
    [{ scan_status: "FLAGGED", verification: "VERIFIED" }, "flagged", "Flagged", "danger"],
    [{ scan_status: "BLOCKED", verification: "VERIFIED" }, "flagged", "Flagged", "danger"],
    [{ scan_status: "UNSCANNED", verification: "VERIFIED" }, "unreviewed", "Not reviewed", "muted"],
    [{ scan_status: null, verification: null }, "unreviewed", "Not reviewed", "muted"],
    [{ scan_status: "CLEAN", verification: "VERIFIED" }, "verified", "Verified", "success"],
    [{ scan_status: "CLEAN", verification: "UNVERIFIED" }, "unreviewed", "Not reviewed", "muted"],
  ])("%o → %s", (input, level, label, tone) => {
    const t = skillTrust(input)
    expect(t).toEqual({ level, label, tone })
  })
})

describe("skillSource", () => {
  it.each([
    [{ source: "BUNDLED", vendor: "crewship", homepage: null }, "builtin", "Crewship built-in"],
    [{ source: "CUSTOM", vendor: "community", homepage: "https://github.com/anthropics/skills.git" }, "anthropics/skills", "anthropics/skills"],
    [{ source: "GENERATED", vendor: null, homepage: null }, "workspace", "Made in this workspace"],
    [{ source: "CUSTOM", vendor: "community", homepage: null }, "workspace", "Made in this workspace"],
    [{ source: "CUSTOM", vendor: "vercel", homepage: null }, "vendor:vercel", "vercel"],
  ])("%o → %s", (input, key, label) => {
    const s = skillSource(input)
    expect(s.key).toBe(key)
    expect(s.label).toBe(label)
  })

  it("reads owner/repo and the web address from a recorded git URL", () => {
    expect(repoFromUrl("https://gitlab.com/acme/skills")).toEqual({ name: "acme/skills", url: "https://gitlab.com/acme/skills" })
    expect(repoFromUrl("not a url")).toBeNull()
  })
})

describe("viewFromParam", () => {
  it("legacy view values map to new ones", () => {
    expect(viewFromParam("all")).toBe("library")
    expect(viewFromParam("assigned")).toBe("used")
    expect(viewFromParam("unassigned")).toBe("unused")
    expect(viewFromParam("attention")).toBe("attention")
    expect(viewFromParam("archived")).toBe("archived")
    expect(viewFromParam("nonsense")).toBeNull()
  })

  it("legacy tab values map to the new tabs", () => {
    expect(tabFromParam("content")).toBe("md")
    expect(tabFromParam("agents")).toBe("used")
    expect(tabFromParam("activity")).toBe("used")
    expect(tabFromParam("security")).toBe("security")
    expect(tabFromParam(null)).toBe("overview")
  })
})

describe("inView", () => {
  const held = skill("held", { installed_on: [agent("ava", "ops")] })
  const idle = skill("idle")
  const archived = skill("old", { lifecycle_state: "archived" })
  const flaggedIdle = skill("bad-idle", { scan_status: "FLAGGED" })
  const lacking = skill("lack", { needs_credentials: ["GH"], installed_on: [agent("ava", "ops", ["GH"])] })
  const unscannedIdle = skill("unscanned", { scan_status: null })
  it.each([
    ["library", held, true],
    ["library", archived, false],
    ["used", held, true],
    ["used", idle, false],
    ["unused", idle, true],
    ["unused", archived, false],
    ["archived", archived, true],
    ["archived", held, false],
    ["attention", flaggedIdle, true],
    ["attention", lacking, true],
    ["attention", unscannedIdle, false],
    ["attention", held, false],
    ["proposed", held, false],
  ] as const)("%s holds %s → %s", (view, s, want) => {
    expect(inView(s, view)).toBe(want)
  })

  it("says why a skill needs attention", () => {
    expect(attentionReasons(lacking).map((r) => r.kind)).toEqual(["credential"])
    expect(attentionReasons(flaggedIdle).map((r) => r.kind)).toEqual(["flagged"])
  })
})

describe("matchesSkill", () => {
  const rows = [
    skill("a", { installed_on: [agent("ava", "ops")], category: "DEVOPS" }),
    skill("b", { installed_on: [agent("ben", "sales")], source: "CUSTOM", vendor: "community", homepage: "https://github.com/acme/skills" }),
    skill("c", { source: "GENERATED", scan_status: "UNSCANNED", lifecycle_state: "stale" }),
  ]
  it.each([
    ["agent", f({ agentId: "ava" }), ["a"]],
    ["used by crew", f({ crewId: "sales" }), ["b"]],
    ["domain", f({ domain: "DEVOPS" }), ["a"]],
    ["source", f({ source: "acme/skills" }), ["b"]],
    ["trust", f({ trust: "unreviewed" }), ["c"]],
    ["status unused 30 days", f({ status: "stale" }), ["c"]],
    ["status made here", f({ status: "made" }), ["c"]],
    ["query by agent name", f({ query: "ben" }), ["b"]],
    ["query by source", f({ query: "acme" }), ["b"]],
  ])("%s", (_name, filters, want) => {
    expect(rows.filter((s) => matchesSkill(s, filters)).map((s) => s.id)).toEqual(want)
  })

  it("Attention and Proposed views list library rows for the counts", () => {
    expect(visibleSkills(rows, f({ view: "proposed" })).length).toBe(3)
  })
})

describe("panelCounts", () => {
  it("counts each facet with its own selection left out", () => {
    const rows = [
      skill("a", { installed_on: [agent("ava", "ops"), agent("ben", "sales")] }),
      skill("b", { installed_on: [agent("ben", "sales")], category: "DESIGN" }),
      skill("c"),
      skill("d", { lifecycle_state: "archived" }),
    ]
    const c = panelCounts(rows, f({ crewId: "sales" }))
    expect(c.crew.get("ops")).toBe(1)
    expect(c.crew.get("sales")).toBe(2)
    expect(c.domain.get("DESIGN")).toBe(1)
    expect(c.views).toMatchObject({ library: 3, used: 2, unused: 1, archived: 1 })
    expect(c.sources[0]).toEqual({ key: "builtin", label: "Crewship built-in", count: 2 })
  })
})

describe("sortSkills and groupSkills", () => {
  const rows = [
    skill("beta", { usage: { uses_7d: 2, errors_7d: 0, uses_total: 5, last_used_at: null }, updated_at: "2026-10-03" }),
    skill("alpha", { category: "DATA", updated_at: "2026-10-09" }),
    skill("gamma", { usage: { uses_7d: 9, errors_7d: 0, uses_total: 9, last_used_at: null }, updated_at: "2026-10-01" }),
  ]
  it.each([
    ["used", ["gamma", "beta", "alpha"]],
    ["updated", ["alpha", "beta", "gamma"]],
    ["name", ["alpha", "beta", "gamma"]],
  ] as const)("%s", (sort, want) => {
    expect(sortSkills(rows, sort).map((s) => s.id)).toEqual(want)
  })

  it("groups by domain, largest group first, keeping the order inside", () => {
    const g = groupSkills(sortSkills(rows, "name"), "domain")
    expect(g.map((x) => [x.label, x.rows.map((r) => r.id)])).toEqual([
      ["Coding", ["beta", "gamma"]],
      ["Data", ["alpha"]],
    ])
  })
})
