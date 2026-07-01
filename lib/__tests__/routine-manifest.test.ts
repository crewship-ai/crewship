import { describe, it, expect } from "vitest"
import { manifestGroups, isManifestEmpty } from "@/lib/routine-manifest"
import type { RoutineManifest } from "@/lib/routine-flow"

const empty: RoutineManifest = {
  integrations: [],
  egress: [],
  credentials: [],
  agents: [],
  routines: [],
  datastores: [],
  tools: [],
  has_http: false,
  has_code: false,
}

describe("isManifestEmpty", () => {
  it("treats null / undefined as empty", () => {
    expect(isManifestEmpty(null)).toBe(true)
    expect(isManifestEmpty(undefined)).toBe(true)
  })

  it("treats an all-empty manifest as empty", () => {
    expect(isManifestEmpty(empty)).toBe(true)
  })

  it("is non-empty when any group has a member", () => {
    expect(isManifestEmpty({ ...empty, agents: ["scout"] })).toBe(false)
    expect(isManifestEmpty({ ...empty, routines: ["child"] })).toBe(false)
  })
})

describe("manifestGroups", () => {
  it("returns no groups for an empty / nullish manifest", () => {
    expect(manifestGroups(empty)).toEqual([])
    expect(manifestGroups(null)).toEqual([])
  })

  it("orders groups: integrations, datastores, tools, agents, sub-routines, egress, credentials", () => {
    const m: RoutineManifest = {
      ...empty,
      integrations: ["github"],
      datastores: [{ type: "postgres", name: "main" }],
      tools: [{ type: "ansible" }],
      agents: ["scout"],
      routines: ["child-routine"],
      egress: ["api.example.com"],
      credentials: [{ type: "GITHUB_TOKEN", scope: "repo" }],
    }
    expect(manifestGroups(m).map((g) => g.key)).toEqual([
      "integrations",
      "datastores",
      "tools",
      "agents",
      "routines",
      "egress",
      "credentials",
    ])
  })

  it("labels a named datastore as 'type · name' and an unnamed one as just the type", () => {
    const named = manifestGroups({ ...empty, datastores: [{ type: "postgres", name: "main" }] })
    expect(named[0].chips[0].label).toBe("postgres · main")
    const bare = manifestGroups({ ...empty, datastores: [{ type: "redis" }] })
    expect(bare[0].chips[0].label).toBe("redis")
  })

  it("resolves the brand-icon fallback for SQL stores to a server glyph, others to a db glyph", () => {
    const pg = manifestGroups({ ...empty, datastores: [{ type: "postgres" }] })
    expect(pg[0].chips[0].fallback).toBe("store-server")
    const mongo = manifestGroups({ ...empty, datastores: [{ type: "mongodb" }] })
    expect(mongo[0].chips[0].fallback).toBe("store-db")
  })

  it("marks tools, egress and credentials as risk-toned", () => {
    const m: RoutineManifest = {
      ...empty,
      tools: [{ type: "bash", name: "deploy.sh" }],
      egress: ["evil.example.com"],
      credentials: [{ type: "AWS_KEY" }],
    }
    const groups = manifestGroups(m)
    for (const g of groups) {
      for (const c of g.chips) expect(c.tone).toBe("risk")
    }
  })

  it("prefixes agents with @ and carries no brand-resolution type", () => {
    const g = manifestGroups({ ...empty, agents: ["scout"] })
    expect(g[0].chips[0].label).toBe("@scout")
    expect(g[0].chips[0].type).toBeUndefined()
    expect(g[0].chips[0].fallback).toBe("bot")
  })

  it("renders a sub-routine chip with the routine tone + glyph", () => {
    const g = manifestGroups({ ...empty, routines: ["nightly-report"] })
    expect(g[0].label).toBe("Sub-routines")
    expect(g[0].chips[0]).toMatchObject({ label: "nightly-report", tone: "routine", fallback: "routine" })
  })

  it("uses a scoped credential label and a shield glyph when typed, a key glyph when bare", () => {
    const typed = manifestGroups({ ...empty, credentials: [{ type: "SLACK_TOKEN", scope: "chat:write" }] })
    expect(typed[0].chips[0].label).toBe("SLACK_TOKEN · chat:write")
    expect(typed[0].chips[0].fallback).toBe("shield")
    const bare = manifestGroups({ ...empty, credentials: [{ type: "" }] })
    expect(bare[0].chips[0].fallback).toBe("key")
  })

  it("passes the raw type through for integrations/datastores/tools so a brand logo can resolve", () => {
    const m: RoutineManifest = {
      ...empty,
      integrations: ["slack"],
      datastores: [{ type: "postgres" }],
      tools: [{ type: "ansible" }],
    }
    const [integ, store, tool] = manifestGroups(m)
    expect(integ.chips[0].type).toBe("slack")
    expect(store.chips[0].type).toBe("postgres")
    expect(tool.chips[0].type).toBe("ansible")
  })
})
