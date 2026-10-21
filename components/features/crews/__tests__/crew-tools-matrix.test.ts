import { describe, it, expect } from "vitest"

import { buildMatrix } from "@/components/features/crews/crew-tools"

const skills = new Map([
  ["lead", [{ id: "s1", name: "web-search" }, { id: "s2", name: "pdf-reader" }]],
  ["writer", [{ id: "s1", name: "web-search" }]],
])
const credentials = new Map([["lead", [{ id: "k1", name: "Anthropic production" }]]])
const integrations = [
  { id: "i1", name: "gdrive", display_name: "Google Drive", transport: "http", endpoint: null, enabled: true, agent_binding_count: 2, auth_status: "connected" },
  { id: "i2", name: "linear", display_name: "Linear", transport: "http", endpoint: null, enabled: true, agent_binding_count: 1, auth_status: "missing" },
]

describe("Tools and access matrix", () => {
  it("one row per resource, holders per agent; integrations carry a count", () => {
    const rows = buildMatrix({ skills, credentials, integrations })
    expect(rows.map((r) => `${r.kind}:${r.name}`)).toEqual([
      "skill:pdf-reader", "skill:web-search", "credential:Anthropic production", "integration:Google Drive", "integration:Linear",
    ])
    expect([...(rows[1].holders ?? [])].sort()).toEqual(["lead", "writer"])
    expect(rows[3]).toMatchObject({ holders: null, count: 2, status: "ready" })
    expect(rows[4].status).toBe("missing")
  })

  it("filters by type and searches across types", () => {
    expect(buildMatrix({ skills, credentials, integrations, filter: "credential" }).map((r) => r.name)).toEqual(["Anthropic production"])
    expect(buildMatrix({ skills, credentials, integrations, query: "li" }).map((r) => r.name)).toEqual(["Linear"])
  })
})
