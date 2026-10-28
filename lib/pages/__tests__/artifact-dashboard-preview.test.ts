import { describe, expect, it } from "vitest"
import { dashboardName, parseDashboardCollection, parseDashboardManifest } from "../artifact-dashboard"
const manifest = (content: string) => [{ path: "dashboard.json", media_type: "application/json", content }]
describe("Dashboard manifest declarations", () => {
  it("reads only closed flat collection declarations from the selected code manifest", () => {
    expect(parseDashboardManifest(manifest('{"schema_version":1,"collections":["sales"]}'))).toEqual({ schema_version: 1, collections: ["sales"] })
    expect(parseDashboardManifest(manifest('{"collections":["sales"],"schema_version":1}'))?.collections).toEqual(["sales"])
  })
  it.each(['{}', '{"schema_version":2,"collections":["sales"]}', '{"schema_version":1,"collections":[]}', '{"schema_version":1,"collections":["sales","sales"]}', '{"schema_version":1,"collections":["https://evil.test"]}', '{"schema_version":1,"collections":["constructor"]}', '{"schema_version":1,"collections":["sales"],"collections":["private"]}', '{"schema_version":1,"collections":["sales"],"script":"evil"}'])("rejects %s without fetching", content => expect(parseDashboardManifest(manifest(content))).toBeNull())
  it("rejects absent or duplicate manifest files", () => { expect(parseDashboardManifest([])).toBeNull(); const files = manifest('{"schema_version":1,"collections":["sales"]}'); expect(parseDashboardManifest([...files, ...files])).toBeNull() })
})
describe("reader data projection", () => {
  const doc = { id: "north", version: 1, value: { revenue: 12 }, updated_at: "2026-10-28T08:00:00Z", updated_by: "private-run-id", source_label: "Agent", source_kind: "agent" }
  const body = { collection: "sales", code_version: 2, documents: [doc] }
  it("binds the exact code pin, removing private actor metadata", () => {
    const data = parseDashboardCollection(body, "sales", 2)!
    expect(data.documents[0]).not.toHaveProperty("updated_by")
    expect(data.documents[0].source_label).toBe("Agent")
    expect(parseDashboardCollection(body, "sales", 99)).toBeNull()
    expect(parseDashboardCollection(body, "private", 2)).toBeNull()
  })
  it("rejects duplicate documents, invalid counters and unsafe identifiers", () => {
    expect(parseDashboardCollection({ ...body, documents: [doc, doc] }, "sales", 2)).toBeNull()
    expect(parseDashboardCollection({ ...body, documents: [{ ...doc, version: 9007199254740992 }] }, "sales", 2)).toBeNull()
    expect(parseDashboardCollection({ ...body, documents: [{ ...doc, value: [] }] }, "sales", 2)).toBeNull()
    for (const name of ["__proto__", "constructor", "prototype", "a.b", "../a"]) expect(dashboardName(name)).toBe(false)
  })
})

it("accepts the store's complete 4096-document boundary and rejects overflow", () => {
 const documents = Array.from({ length: 4096 }, (_, index) => ({ id: `row${index}`, version: 1, value: { amount: index }, updated_at: "2026-10-28T08:00:00Z" }))
 expect(parseDashboardCollection({ collection: "sales", code_version: 1, documents }, "sales", 1)?.documents).toHaveLength(4096)
 expect(parseDashboardCollection({ collection: "sales", code_version: 1, documents: [...documents, { ...documents[0], id: "overflow" }] }, "sales", 1)).toBeNull()
})
