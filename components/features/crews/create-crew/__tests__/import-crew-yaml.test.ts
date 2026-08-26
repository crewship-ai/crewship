import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { parseCrewManifest, CrewImportError } from "../import-crew-yaml"
import { INITIAL_STATE, MEMORY_MAX_MB, CPU_MAX } from "../types"

/**
 * The importer is graded against the files that already exist in the repo,
 * not against fixtures written to match it. `crewship apply -f` takes these
 * today; if the browser's reading of one disagrees with the CLI's, the YAML
 * in examples/manifests is the thing that is right.
 */
const EXAMPLES = join(__dirname, "../../../../../examples/manifests")
const readExample = (f: string) => readFileSync(join(EXAMPLES, f), "utf8")

describe("parseCrewManifest — the shapes that are actually on disk", () => {
  it("reads examples/manifests/python-with-features.crew.yaml", () => {
    const { patch, notImported, agentNames } = parseCrewManifest(
      readExample("python-with-features.crew.yaml"),
    )

    expect(patch.name).toBe("Python Backend Crew (features-based DBs)")
    expect(patch.slug).toBe("python-backend-features")
    expect(patch.icon).toBe("code")
    // The file says color: green, which is not one of the wizard's eight
    // palette tokens — asCrewColor falls back rather than writing it through.
    expect(patch.color).toBe("blue")

    const dc = JSON.parse(patch.devcontainerConfig!)
    expect(dc.image).toBe("mcr.microsoft.com/devcontainers/python:3.12")
    expect(Object.keys(dc.features)).toEqual([
      "ghcr.io/itsmechlark/features/postgresql:1",
      "ghcr.io/itsmechlark/features/redis-server:1",
    ])
    // Manifest `env:` is devcontainer.json `containerEnv:`.
    expect(dc.containerEnv.DATABASE_URL).toBe("postgres://postgres@localhost:5432/postgres")

    // The honest half: this file is mostly an agent and a credential, and
    // the wizard's submit path creates neither.
    expect(agentNames).toEqual(["Pavel"])
    expect(notImported).toEqual([
      { label: "agents", count: 1 },
      { label: "credentials", count: 1 },
    ])
  })

  it("reads every other .crew.yaml in the repo without throwing", () => {
    for (const f of ["code-review.crew.yaml", "triage.crew.yaml", "python-with-services.crew.yaml"]) {
      expect(() => parseCrewManifest(readExample(f)), f).not.toThrow()
    }
  })

  it("counts the sidecar services it cannot create", () => {
    const { notImported } = parseCrewManifest(readExample("python-with-services.crew.yaml"))
    expect(notImported.find((b) => b.label === "services")?.count).toBeGreaterThan(0)
  })
})

describe("parseCrewManifest — mapping", () => {
  const base = (spec: string, metadata = "name: Ops\n  slug: ops") => `
apiVersion: crewship/v1
kind: Crew
metadata:
  ${metadata}
spec:
${spec}
`

  it("takes the POST /crews path, never the template deploy", () => {
    // submit.ts branches on mode: "browse" would try to deploy
    // pickedTemplateSlug, which an imported file does not have.
    const { patch } = parseCrewManifest(base("  description: hi"))
    expect(patch.mode).toBe("empty")
    expect(patch.pickedTemplateSlug).toBeNull()
  })

  it("normalises a slug the manifest was allowed to spell freely", () => {
    const { patch } = parseCrewManifest(base("  description: hi", "name: Ops\n  slug: Ops Team!"))
    expect(patch.slug).toBe("ops-team-")
  })

  it("falls back to the name when the file has no slug", () => {
    const { patch } = parseCrewManifest(base("  description: hi", "name: Data Eng"))
    expect(patch.slug).toBe("data-eng")
  })

  it("stringifies mise versions that YAML parsed as numbers", () => {
    // `node: 22` is a YAML int; the mise_config column is map[string]string.
    const { patch } = parseCrewManifest(base("  mise:\n    tools:\n      node: 22\n      python: '3.12'"))
    expect(JSON.parse(patch.miseConfig!)).toEqual({ tools: { node: "22", python: "3.12" } })
  })

  it("lets typed devcontainer fields win over raw, as the Go side does", () => {
    const { patch } = parseCrewManifest(
      base('  devcontainer:\n    image: real:1\n    raw:\n      image: shadow:1\n      remoteUser: agent'),
    )
    const dc = JSON.parse(patch.devcontainerConfig!)
    expect(dc.image).toBe("real:1")
    // A key the manifest does not model still survives the round trip.
    expect(dc.remoteUser).toBe("agent")
  })

  it("clamps sizing into the range the wizard's chips can show", () => {
    const { patch } = parseCrewManifest(base("  devcontainer:\n    memory_mb: 999999\n    cpus: 999"))
    expect(patch.memoryMB).toBe(MEMORY_MAX_MB)
    expect(patch.cpus).toBe(CPU_MAX)
  })

  it("leaves sizing alone when the file says nothing about it", () => {
    const { patch } = parseCrewManifest(base("  description: hi"))
    expect(patch.memoryMB).toBeUndefined()
    expect(patch.cpus).toBeUndefined()
    // …so the wizard keeps its own defaults.
    expect({ ...INITIAL_STATE, ...patch }.memoryMB).toBe(INITIAL_STATE.memoryMB)
  })

  it("emits no devcontainer string for a file that configures nothing", () => {
    const { patch } = parseCrewManifest(base("  description: hi"))
    expect(patch.devcontainerConfig).toBeUndefined()
    expect(patch.miseConfig).toBeUndefined()
  })
})

describe("parseCrewManifest — refusals", () => {
  const expectRefusal = (text: string, match: RegExp) => {
    expect(() => parseCrewManifest(text)).toThrow(CrewImportError)
    expect(() => parseCrewManifest(text)).toThrow(match)
  }

  it("refuses an empty file", () => {
    expectRefusal("   \n  ", /empty/i)
  })

  it("refuses YAML that does not parse", () => {
    expectRefusal("kind: Crew\n  bad: [indent", /not valid YAML/i)
  })

  it("refuses a document that is not a mapping", () => {
    expectRefusal("- one\n- two", /must be a YAML mapping/i)
  })

  it("refuses a file with no kind", () => {
    expectRefusal("apiVersion: crewship/v1\nmetadata:\n  name: Ops", /No `kind:`/)
  })

  it("names the other kind rather than saying it is invalid", () => {
    // kind: CrewTemplate is a real, working manifest — just not this door.
    expectRefusal("kind: CrewTemplate\nmetadata:\n  name: Ops", /already exists on the server/i)
    expectRefusal("kind: Project\nmetadata:\n  name: Ops", /`kind: Project`/)
  })
})
