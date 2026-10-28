import { describe, expect, it } from "vitest"
import { dashboardField, dashboardName, parseDashboardCollection, parseDashboardManifest } from "../artifact-dashboard"
const manifest = (content: string) => [{ path: "dashboard.json", media_type: "application/json", content }]
describe("Dashboard manifest declarations", () => {
  it("reads only closed flat collection declarations from the selected code manifest", () => {
    expect(parseDashboardManifest(manifest('{"schema_version":1,"collections":["sales"]}'))).toEqual({ schema_version: 1, collections: ["sales"] })
    expect(parseDashboardManifest(manifest('{"collections":["sales"],"schema_version":1}'))?.collections).toEqual(["sales"])
  })
  it.each(['{}', '{"schema_version":2,"collections":["sales"]}', '{"schema_version":1,"collections":[]}', '{"schema_version":1,"collections":["sales","sales"]}', '{"schema_version":1,"collections":["https://evil.test"]}', '{"schema_version":1,"collections":["__proto__"]}', '{"schema_version":1,"collections":["sales"],"collections":["private"]}', '{"schema_version":1,"collections":["sales"],"script":"evil"}'])("rejects %s without fetching", content => expect(parseDashboardManifest(manifest(content))).toBeNull())
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
    for (const name of ["__proto__", "a.b", "../a"]) expect(dashboardName(name)).toBe(false)
  })
})

it("accepts the store's complete 4096-document boundary and rejects overflow", () => {
 const documents = Array.from({ length: 4096 }, (_, index) => ({ id: `row${index}`, version: 1, value: { amount: index }, updated_at: "2026-10-28T08:00:00Z" }))
 expect(parseDashboardCollection({ collection: "sales", code_version: 1, documents }, "sales", 1)?.documents).toHaveLength(4096)
 expect(parseDashboardCollection({ collection: "sales", code_version: 1, documents: [...documents, { ...documents[0], id: "overflow" }] }, "sales", 1)).toBeNull()
})


it("admits backend collection and row identifiers without inventing reserved words", () => {
  expect(parseDashboardManifest(manifest('{"schema_version":1,"collections":["constructor","prototype"]}'))?.collections).toEqual(["constructor", "prototype"])
  for (const id of ["constructor", "prototype"]) {
    expect(dashboardName(id)).toBe(true)
    const value = JSON.parse('{"__proto__":"own text","constructor":"constructor text","prototype":"prototype text","DisplayName":"Business name","a.b":"flat dot key"}')
    const result = parseDashboardCollection({ collection: id, code_version: 1, documents: [{ id, version: 1, value, updated_at: "2026-10-28T08:00:00Z" }] }, id, 1)
    expect(result?.documents[0].value).toHaveProperty("DisplayName", "Business name")
    expect(Object.hasOwn(result!.documents[0].value, "__proto__")).toBe(true)
    expect(result!.documents[0].value["__proto__"]).toBe("own text")
    expect(Object.hasOwn(result!.documents[0].value, "toString")).toBe(false)
  }
  expect(Object.getPrototypeOf({})).toBe(Object.prototype)
})

it("treats bounded business field keys as flat text, never identifier paths", () => {
 for (const key of ["__proto__", "constructor", "prototype", "DisplayName", "a.b", "Sales total", "Žluťoučký", "x".repeat(128)]) expect(dashboardField(key)).toBe(true)
 for (const key of ["", "x".repeat(129), "a\nkey", "a\u007fkey", "a\u0085key"]) expect(dashboardField(key)).toBe(false)
})
