import { readFileSync } from "node:fs"
import path from "node:path"

import { describe, it, expect } from "vitest"

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
    expect(warnings.some((w) => /credential bindings/.test(w))).toBe(true)
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
