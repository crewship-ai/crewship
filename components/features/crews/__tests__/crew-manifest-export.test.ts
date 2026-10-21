import { readFileSync } from "node:fs"
import path from "node:path"

import { describe, it, expect } from "vitest"
import { parse } from "yaml"

import { exportCrewManifest } from "@/components/features/crews/crew-manifest-export"

// api.json holds the GET answers the CLI saw for three demo crews (trimmed to
// the fields internal/manifest/export.go reads); the .yaml files are what
// `crewship export crew <slug>` printed for the same crews at the same time.
// The browser composition has to match the CLI byte for byte.
const FX = path.join(__dirname, "fixtures", "crew-export")
const read = (name: string) => readFileSync(path.join(FX, name), "utf8")
const api = JSON.parse(read("api.json")) as Record<string, unknown>

function serve(overrides: Record<string, unknown> = {}, fail: string[] = []) {
  const calls: string[] = []
  const get = async (url: string) => {
    const u = new URL(url, "http://x")
    u.searchParams.delete("workspace_id")
    const key = u.pathname + (u.search ? u.search : "")
    calls.push(key)
    if (fail.some((f) => key.includes(f))) return new Response("{}", { status: 500 })
    const body = key in overrides ? overrides[key] : api[key]
    return body === undefined ? new Response("{}", { status: 404 }) : new Response(JSON.stringify(body))
  }
  return { get, calls }
}

describe("exportCrewManifest: parity with `crewship export crew`", () => {
  for (const slug of ["ops", "engineering", "quality"]) {
    it(`${slug}: identical to the CLI output`, async () => {
      const { yaml } = await exportCrewManifest("ws1", slug, {}, serve().get)
      expect(yaml).toBe(read(`${slug}.yaml`))
    })
  }

  it("--no-credentials drops the credential slots only", async () => {
    const { yaml } = await exportCrewManifest("ws1", "ops", { credentials: false }, serve().get)
    expect(yaml).toBe(read("ops.nocred.yaml"))
  })

  it("--no-skill-bodies keeps the skill slugs but not their bodies", async () => {
    const { yaml } = await exportCrewManifest("ws1", "ops", { skillBodies: false }, serve().get)
    expect(yaml).toBe(read("ops.nobodies.yaml"))
  })
})

describe("exportCrewManifest: behaviour", () => {
  it("names a crew that does not exist", async () => {
    await expect(exportCrewManifest("ws1", "nope", {}, serve().get)).rejects.toThrow('crew "nope" not found')
  })

  it("refuses a crew whose services are private instead of exporting a wrong file", async () => {
    const crews = (api["/api/v1/crews"] as Array<Record<string, unknown>>).map((c) => (c.slug === "ops" ? { ...c, services_json: "[redacted]" } : c))
    await expect(exportCrewManifest("ws1", "ops", {}, serve({ "/api/v1/crews": crews }).get)).rejects.toThrow(/private service configuration cannot be exported/)
  })

  it("a credential's value never reaches the file, even if the list carried one", async () => {
    const creds = (api["/api/v1/credentials"] as Array<Record<string, unknown>>).map((c) => ({ ...c, value: "sk-secret-123", token: "tok-456" }))
    const { yaml } = await exportCrewManifest("ws1", "ops", {}, serve({ "/api/v1/credentials": creds }).get)
    expect(yaml).not.toContain("sk-secret-123")
    expect(yaml).not.toContain("tok-456")
  })

  it("a failed skill or credential lookup for one agent exports the rest and says what is missing", async () => {
    const { yaml, warnings } = await exportCrewManifest("ws1", "ops", {}, serve({}, ["/credentials"]).get)
    expect(yaml).toContain("kind: Crew")
    expect(warnings.some((w) => /credential bindings/i.test(w))).toBe(true)
  })

  it("every call is a GET of an existing read endpoint", async () => {
    const { get, calls } = serve()
    await exportCrewManifest("ws1", "ops", {}, get)
    expect(calls.length).toBeGreaterThan(5)
    expect(calls.every((c) => c.startsWith("/api/v1/"))).toBe(true)
    expect(calls).toContain("/api/v1/crews")
    expect(calls).toContain("/api/v1/credentials")
  })

  it("services round-trip: the stored JSON comes back as services in the file's field order", async () => {
    const services = [{ name: "db", image: "postgres:16", ports: ["5432"], env: { POSTGRES_DB: "app" }, volumes: [{ name: "pg-data", mount: "/var/lib/postgresql/data" }], healthcheck: { test: ["CMD", "pg_isready"], interval: "10s" }, auto_credentials: [{ name: "POSTGRES_PASSWORD" }] }]
    const crews = (api["/api/v1/crews"] as Array<Record<string, unknown>>).map((c) => (c.slug === "ops" ? { ...c, services_json: JSON.stringify(services) } : c))
    const { yaml } = await exportCrewManifest("ws1", "ops", {}, serve({ "/api/v1/crews": crews }).get)
    const block = yaml.slice(yaml.indexOf("    services:"), yaml.indexOf("    services:") + 600)
    expect(block).toContain("- name: db")
    expect(block).toContain("image: postgres:16")
    expect(block.indexOf("name: db")).toBeLessThan(block.indexOf("image: postgres:16"))
    expect(block.indexOf("image: postgres:16")).toBeLessThan(block.indexOf("ports:"))
    expect(block).toContain("auto_credentials:")
  })
})

describe("exportCrewManifest: awkward text survives the round trip", () => {
  const tricky = [
    "yes", "no", "true", "null", "~", "1001", "1e3", "0x1f", "- not a list", "# not a comment", "key: value", "a: b: c",
    " leading space", "trailing space ", "tab\there", "quote \" and ' mixed", "{braces}", "[brackets]", "*star", "&anchor", "!tag", "%percent", "@at", "`tick",
    "multi\nline", "multi\nline\n", "trailing blank\n\n", "  indented first\nsecond", "line with trailing space \nnext", "emoji \u{1F680} and ünïcode", "",
  ]
  it("every prompt comes back exactly when the file is read as YAML", async () => {
    const agents = tricky.map((t, i) => ({ id: `a${i}`, slug: `agent-${String(i).padStart(2, "0")}`, name: `Agent ${i}`, agent_role: "AGENT", cli_adapter: "CLAUDE_CODE", tool_profile: "CODING", timeout_seconds: 60, memory_enabled: true, system_prompt: t, suggested_prompts: t, crew_id: "c1" }))
    const crews = [{ id: "c1", name: "T", slug: "t", description: tricky[10], runtime_image: "x", services_json: null }]
    const over: Record<string, unknown> = { "/api/v1/crews": crews, "/api/v1/agents?crew_id=c1": agents }
    for (const a of agents) { over[`/api/v1/agents/${a.id}/skills`] = []; over[`/api/v1/agents/${a.id}/credentials`] = [] }
    over["/api/v1/crews/c1/integrations"] = []
    const { yaml } = await exportCrewManifest("ws1", "t", {}, serve(over).get)
    const doc = parse(yaml) as { metadata: { description: string }; spec: { agents: Array<{ prompt?: string; suggested_prompts?: string }> } }
    expect(doc.metadata.description).toBe(tricky[10])
    const back = doc.spec.agents.map((a) => a.prompt ?? "")
    expect(back).toEqual(tricky)
    expect(doc.spec.agents.map((a) => a.suggested_prompts ?? "")).toEqual(tricky)
  })
})
