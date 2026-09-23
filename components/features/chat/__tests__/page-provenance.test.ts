import { describe, expect, it } from "vitest"
import { pageProvenanceForTurn } from "../page-provenance"

describe("Page provenance", () => {
  it("survives a history turn and ignores a client-only slug", () => {
    const page_context = { workspace_id: "ws", page_id: "p1", slug: "fleet", name: "Fleet", snapshot_at: "2026-09-23T09:00:00Z" }
    expect(pageProvenanceForTurn({ metadata: { page_context } })).toEqual({ pageId: "p1", slug: "fleet", name: "Fleet", snapshotAt: page_context.snapshot_at })
    expect(pageProvenanceForTurn({ parts: [{ metadata: { page_context } }] })).not.toBeNull()
    expect(pageProvenanceForTurn({ metadata: { page_context: { slug: "fleet", name: "Forged" } } })).toBeNull()
  })
})
