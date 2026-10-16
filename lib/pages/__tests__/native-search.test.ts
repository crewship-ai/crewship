import { describe, expect, it } from "vitest"
import { nativePageSearchResponseSchema } from "../native-search"

const match = { page_id: "page", slug: "page", name: "Page", content_type: "document", schema_version: 4, source_revision: 0, source_generation: 1, snippet: "Plain text" }
const response = { scope: "native-docs-slides", matches: [match], has_more: false }
describe("matched native search response", () => {
  it("accepts Docs1..4 and preserves the actual source version", () => {
    for (const version of [1, 2, 3, 4]) {
      expect(nativePageSearchResponseSchema.parse({ ...response, matches: [{ ...match, schema_version: version }] }).matches[0].schema_version).toBe(version)
    }
  })
  it("rejects unknown Docs5 and Slides3 without normalizing them", () => {
    for (const candidate of [{ ...match, schema_version: 5 }, { ...match, content_type: "slides", schema_version: 3 }]) {
      expect(nativePageSearchResponseSchema.safeParse({ ...response, matches: [candidate] }).success).toBe(false)
    }
  })
  it("keeps text, response fields and continuation bounded and closed", () => {
    for (const invalid of [
      { ...response, matches: [{ ...match, snippet: "é".repeat(257) }] },
      { ...response, matches: [{ ...match, asset_ref: "private" }] },
      { ...response, matches: Array.from({ length: 51 }, () => match) },
      { ...response, has_more: true },
      { ...response, cursor: "a".repeat(43) },
      { ...response, has_more: true, cursor: "a".repeat(43), matches: [] },
      { ...response, hidden_count: 1 },
    ]) expect(nativePageSearchResponseSchema.safeParse(invalid).success).toBe(false)
    expect(nativePageSearchResponseSchema.safeParse({ ...response, has_more: true, cursor: "a".repeat(43) }).success).toBe(true)
  })
})
