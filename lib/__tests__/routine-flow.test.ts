import { describe, it, expect } from "vitest"
import {
  buildFlowNodes,
  buildPlainSteps,
  stepDeterminism,
  brandIconKey,
  type RoutineManifest,
} from "@/lib/routine-flow"

// A representative "full" routine DSL mirroring the redesign mockup:
// trigger → http fetch → agent → (redis) → ansible → (postgres) → discord out.
const fullDsl = {
  dsl_version: "1.0",
  name: "nightly-config-sync",
  inputs: [{ name: "env", type: "string" }],
  outputs: [{ name: "summary", type: "string" }],
  steps: [
    { id: "fetch", type: "http", http: { method: "GET", url: "https://smartmania.cz/config" } },
    { id: "pick", type: "agent_run", agent_slug: "jordan", prompt: "vyber top 5 zmen" },
    { id: "cache", type: "code", code: { runtime: "bash", code: "redis-cli set ..." } },
    { id: "wait", type: "wait", wait: { kind: "approval", approval_prompt: "ok?" } },
    { id: "shape", type: "transform", transform: { input: "{{ steps.pick.output }}", expression: ".items" } },
    { id: "child", type: "call_pipeline", pipeline_slug: "notify-discord" },
  ],
}

const fullManifest: RoutineManifest = {
  integrations: ["discord"],
  egress: ["smartmania.cz", "discord.com"],
  credentials: [{ type: "PG_DSN", scope: "write" }, { type: "anthropic" }],
  agents: ["jordan"],
  routines: ["notify-discord"],
  datastores: [
    { type: "redis", name: "cache", note: "cfg:items" },
    { type: "postgres", name: "runs", note: "writes table runs" },
  ],
  tools: [{ type: "ansible", name: "deploy.yml" }, { type: "bash" }],
  has_http: true,
  has_code: true,
}

describe("buildFlowNodes", () => {
  it("emits a trigger node first and an output node last", () => {
    const nodes = buildFlowNodes(fullDsl, fullManifest)
    expect(nodes[0].kind).toBe("trigger")
    expect(nodes[0].iconKey).toBe("trigger")
    expect(nodes[nodes.length - 1].kind).toBe("out")
    expect(nodes[nodes.length - 1].iconKey).toBe("out")
  })

  it("derives one node per step with the right kind + icon-key", () => {
    const nodes = buildFlowNodes(fullDsl, null)
    const byId = Object.fromEntries(nodes.map((n) => [n.id, n]))
    expect(byId["fetch"]).toMatchObject({ kind: "step", iconKey: "http", label: "Fetch" })
    expect(byId["fetch"].detail).toBe("smartmania.cz")
    expect(byId["pick"]).toMatchObject({ kind: "agent", iconKey: "agent", detail: "jordan" })
    expect(byId["cache"]).toMatchObject({ kind: "step", iconKey: "code", detail: "bash" })
    expect(byId["wait"]).toMatchObject({ kind: "step", iconKey: "wait", detail: "approval" })
    expect(byId["shape"]).toMatchObject({ kind: "step", iconKey: "transform" })
    expect(byId["child"]).toMatchObject({ kind: "step", iconKey: "call", detail: "notify-discord" })
  })

  it("attaches datastore + tool resource nodes from the manifest", () => {
    const nodes = buildFlowNodes(fullDsl, fullManifest)
    const stores = nodes.filter((n) => n.kind === "store")
    const tools = nodes.filter((n) => n.kind === "tool")
    expect(stores.map((n) => n.iconKey)).toEqual(["store-redis", "store-postgres"])
    expect(stores[0].label).toBe("Redis")
    expect(stores[1].label).toBe("Postgres")
    expect(tools.map((n) => n.label)).toEqual(["ansible", "bash"])
    expect(tools[0].detail).toBe("deploy.yml")
  })

  it("tags datastore + tool nodes with a brand-icon key for real logos", () => {
    const nodes = buildFlowNodes(fullDsl, fullManifest)
    const byId = Object.fromEntries(nodes.map((n) => [n.id, n]))
    const stores = nodes.filter((n) => n.kind === "store")
    const tools = nodes.filter((n) => n.kind === "tool")
    expect(stores.map((n) => n.brandIconKey)).toEqual(["redis", "postgresql"])
    expect(tools.map((n) => n.brandIconKey)).toEqual(["ansible", "bash"])
    // Generic (non-resource) nodes carry no brand key — they stay lucide.
    expect(byId["trigger"].brandIconKey).toBeUndefined()
    expect(byId["fetch"].brandIconKey).toBeUndefined()
    expect(byId["pick"].brandIconKey).toBeUndefined()
  })

  it("leaves brandIconKey undefined for an unbranded datastore/tool", () => {
    const m: RoutineManifest = {
      ...fullManifest,
      datastores: [{ type: "sqlite", name: "local" }],
      tools: [{ type: "make" }],
    }
    const nodes = buildFlowNodes({ steps: [] }, m)
    const store = nodes.find((n) => n.kind === "store")!
    const tool = nodes.find((n) => n.kind === "tool")!
    expect(store.brandIconKey).toBeUndefined()
    expect(tool.brandIconKey).toBeUndefined()
  })

  it("orders resource nodes after the steps and before the output", () => {
    const nodes = buildFlowNodes(fullDsl, fullManifest)
    const kinds = nodes.map((n) => n.kind)
    const lastStep = kinds.lastIndexOf("agent") // agent is a step-ish node
    const firstStore = kinds.indexOf("store")
    const out = kinds.indexOf("out")
    expect(firstStore).toBeGreaterThan(lastStep)
    expect(out).toBe(kinds.length - 1)
  })

  it("skips the host for a templated http url (never throws)", () => {
    const dsl = { steps: [{ id: "h", type: "http", http: { url: "https://{{ inputs.host }}/x" } }] }
    const nodes = buildFlowNodes(dsl)
    const h = nodes.find((n) => n.id === "h")!
    // falls back to the raw url string, not a parsed host
    expect(h.detail).toContain("{{")
  })

  it("is defensive: null / non-object / missing fields never throw", () => {
    expect(() => buildFlowNodes(null)).not.toThrow()
    expect(() => buildFlowNodes(undefined)).not.toThrow()
    expect(() => buildFlowNodes(42)).not.toThrow()
    expect(() => buildFlowNodes({ steps: "nope" })).not.toThrow()
    expect(() => buildFlowNodes({ steps: [null, 1, "x", {}] })).not.toThrow()
    // a null dsl still yields trigger + output bookends
    const bare = buildFlowNodes(null)
    expect(bare[0].kind).toBe("trigger")
    expect(bare[bare.length - 1].kind).toBe("out")
  })

  it("falls back to a positional id when a step has no id", () => {
    const nodes = buildFlowNodes({ steps: [{ type: "http", http: { url: "https://a.test" } }] })
    expect(nodes.some((n) => n.id === "step-0")).toBe(true)
  })

  it("labels the trigger detail from input count", () => {
    expect(buildFlowNodes({ inputs: [{ name: "a" }, { name: "b" }] })[0].detail).toBe("2 inputs")
    expect(buildFlowNodes({})[0].detail).toBe("manual / scheduled")
  })
})

describe("brandIconKey", () => {
  it("maps datastores and their aliases to a Simple-Icon key", () => {
    expect(brandIconKey("postgres")).toBe("postgresql")
    expect(brandIconKey("postgresql")).toBe("postgresql")
    expect(brandIconKey("redis")).toBe("redis")
    expect(brandIconKey("mysql")).toBe("mysql")
    expect(brandIconKey("mariadb")).toBe("mysql")
    expect(brandIconKey("mongodb")).toBe("mongodb")
    expect(brandIconKey("mongo")).toBe("mongodb")
  })

  it("maps tools / runtimes to a Simple-Icon key", () => {
    expect(brandIconKey("ansible")).toBe("ansible")
    expect(brandIconKey("terraform")).toBe("terraform")
    expect(brandIconKey("kubectl")).toBe("kubernetes")
    expect(brandIconKey("kubernetes")).toBe("kubernetes")
    expect(brandIconKey("docker")).toBe("docker")
    expect(brandIconKey("python")).toBe("python")
    expect(brandIconKey("bash")).toBe("bash")
    expect(brandIconKey("git")).toBe("git")
  })

  it("maps integration slugs to a Simple-Icon key", () => {
    expect(brandIconKey("slack")).toBe("slack")
    expect(brandIconKey("discord")).toBe("discord")
    expect(brandIconKey("github")).toBe("github")
    expect(brandIconKey("notion")).toBe("notion")
    expect(brandIconKey("gcal")).toBe("googlecalendar")
    expect(brandIconKey("zapier")).toBe("zapier")
  })

  it("is case / whitespace insensitive and returns null when unknown", () => {
    expect(brandIconKey("  Postgres ")).toBe("postgresql")
    expect(brandIconKey("REDIS")).toBe("redis")
    expect(brandIconKey("sqlite")).toBeNull()
    expect(brandIconKey("make")).toBeNull()
    expect(brandIconKey("")).toBeNull()
  })
})

describe("stepDeterminism", () => {
  it("classifies agent_run as AI, everything else as script", () => {
    expect(stepDeterminism("agent_run")).toBe("ai")
    expect(stepDeterminism("http")).toBe("script")
    expect(stepDeterminism("code")).toBe("script")
    expect(stepDeterminism("transform")).toBe("script")
    expect(stepDeterminism("")).toBe("script")
  })
})

describe("buildPlainSteps", () => {
  it("opens with a trigger line then one line per step, numbered", () => {
    const lines = buildPlainSteps(fullDsl)
    expect(lines[0].determinism).toBe("trigger")
    expect(lines[0].index).toBe(0)
    const steps = lines.slice(1)
    expect(steps.map((s) => s.index)).toEqual([1, 2, 3, 4, 5, 6])
    expect(steps[1].determinism).toBe("ai") // the agent_run step
    expect(steps[0].determinism).toBe("script")
  })

  it("produces a human title + detail per step type", () => {
    // Prose now comes from the shared canonical renderer
    // (lib/routine-step-describe), so it reads identically to the readable
    // summary: "Fetch from <host>", "Ask <agent>", etc.
    const lines = buildPlainSteps(fullDsl)
    const fetch = lines.find((l) => l.id === "fetch")!
    expect(fetch.title.toLowerCase()).toContain("fetch")
    expect(fetch.detail).toBeTruthy()
    const agent = lines.find((l) => l.id === "pick")!
    expect(agent.title).toBe("Ask jordan")
    // http carries no plain-language detail, so the mono line falls back
    // to the technical one-liner.
    expect(fetch.detail).toContain("http GET")
  })

  it("is defensive on malformed input", () => {
    expect(() => buildPlainSteps(null)).not.toThrow()
    expect(buildPlainSteps(null)[0].determinism).toBe("trigger")
    expect(() => buildPlainSteps({ steps: [null, 3] })).not.toThrow()
  })
})
