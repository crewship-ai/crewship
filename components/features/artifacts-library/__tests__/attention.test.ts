import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { libraryItems } from "../library-items"
import { ATTENTION_LIMIT, attentionItems, attentionReason } from "../attention"

const wire = (over: Partial<WirePage> & Record<string, unknown>) =>
  ({ id: over.slug, owner: "user/u1", reach: ["owner"], updated_at: "2026-11-08T10:00:00Z", ...over }) as WirePage

describe("Needs attention rule", () => {
  it("flags nothing on a fresh seed: unshared, never produced and someone else's stale data are not signals", () => {
    // The dev seed (#5132): five typed artifacts never shared, six ops Pages with never-produced or stale panels the caller does not own.
    const seed = libraryItems([
      wire({ slug: "landing-page", artifact_kind: "design" }),
      wire({ slug: "deck", artifact_kind: "slides" }),
      wire({ slug: "roi", artifact_kind: "project" }),
      wire({ slug: "vote", artifact_kind: "dashboard" }),
      wire({ slug: "sales", artifact_kind: "dashboard" }),
      wire({ slug: "demo-live", owner: "crew/ops", reach: ["role", "crew:ops"], has_application: true, panel_count: 2, panel_states: { never_produced: 1 } }),
      wire({ slug: "custom-operations", owner: "crew/ops", reach: ["role", "crew:ops"], has_application: true, panel_count: 3, panel_states: { stale: 3 } }),
      ...["shipping", "marketing", "finance", "sales-ops"].map(name => wire({ slug: `demo-${name}`, owner: "crew/ops", reach: ["role"], has_application: true, panel_count: 7, panel_states: { never_produced: 6 } })),
    ].map(toPageView))
    expect(attentionItems(seed)).toEqual([])
  })

  it("orders draft, failed, stale and names each reason", () => {
    const items = libraryItems([
      wire({ slug: "stale", name: "My board", panel_count: 2, stale_panels: 2, updated_at: "2026-11-08T12:00:00Z" }),
      wire({ slug: "failed", name: "Ops board", owner: "crew/ops", reach: ["crew:ops"], panel_count: 3, failed_panels: 1 }),
      wire({ slug: "brief", name: "Partner brief", artifact_kind: "word" }),
    ].map(toPageView))
    const entries = attentionItems(items, { drafts: new Set(["brief"]) })
    expect(entries.map(entry => [entry.item.slug, entry.reason, entry.title])).toEqual([
      ["brief", "draft", "Your recovered draft"],
      ["failed", "failed", "Refresh failed"],
      ["stale", "stale", "Data is stale"],
    ])
    expect(entries[1].detail).toBe("Ops board · 1 panel kept the last good data")
  })

  it("ignores archived and test items and caps the list", () => {
    const many = libraryItems(Array.from({ length: 12 }, (_, n) => wire({ slug: `b${n}`, panel_count: 1, failed_panels: 1 })).map(toPageView))
    expect(attentionItems(many)).toHaveLength(ATTENTION_LIMIT)
    expect(attentionReason({ ...many[0], archived: true })).toBeNull()
    expect(attentionReason({ ...many[0], test: true })).toBeNull()
  })
})
