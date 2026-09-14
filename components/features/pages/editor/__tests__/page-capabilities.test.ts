import { describe, expect, it } from "vitest"

import { derivePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * The regression these tests exist for: one boolean used to gate Edit, App
 * preview, Source history and Publications together, and it went false the
 * moment a Page carried a single panel the viewer could not see. Three of
 * those four the server would have answered.
 */

function page(panels: unknown[], extra: Partial<WirePageDetail> = {}): WirePageDetail {
  return { slug: "ops", name: "Operations Lab", panels: panels as never, ...extra } as WirePageDetail
}

const plain = { id: "services", schema: "status.v1", owner: "crew/ops", producer: "routine/nightly", sla: "5m" }
const sealed = { id: "hidden", sealed: true }

describe("derivePageCapabilities", () => {
  it("has nothing to offer before the page loads", () => {
    const caps = derivePageCapabilities(null)
    expect(caps.loaded).toBe(false)
    expect(caps.mayEditMetadata).toBe(false)
    expect(caps.mayEditDocument).toBe(false)
    expect(caps.mayManageAccess).toBe(false)
  })

  it("opens every section on an ordinary page", () => {
    const caps = derivePageCapabilities(page([plain]))
    expect(caps.loaded).toBe(true)
    expect(caps.mayEditMetadata).toBe(true)
    expect(caps.mayEditDocument).toBe(true)
    expect(caps.documentRefusal).toBeNull()
    expect(caps.mayManageAccess).toBe(true)
    expect(caps.mayViewSourceHistory).toBe(true)
  })

  it("a sealed panel closes the document editor and nothing else", () => {
    const caps = derivePageCapabilities(page([plain, sealed]))
    expect(caps.mayEditDocument).toBe(false)
    expect(caps.documentRefusal).toContain("panel you may not see")
    // The point of the split: these three stay open, because the server
    // answers them and a sealed panel is irrelevant to all of them.
    expect(caps.mayEditMetadata).toBe(true)
    expect(caps.mayManageAccess).toBe(true)
    expect(caps.mayViewSourceHistory).toBe(true)
  })

  it("counts more than one sealed panel in the refusal", () => {
    const caps = derivePageCapabilities(page([sealed, { id: "other", sealed: true }]))
    expect(caps.documentRefusal).toContain("2 panels you may not see")
  })

  it("the refusal says what is still possible, so it cannot read as 'this product cannot'", () => {
    const caps = derivePageCapabilities(page([plain, sealed]))
    expect(caps.documentRefusal).toMatch(/name, description and access can still be changed/i)
  })

  it("an application is only claimed when the record says so", () => {
    expect(derivePageCapabilities(page([plain], { has_application: false })).hasApplication).toBe(false)
    expect(derivePageCapabilities(page([plain], { has_application: true })).hasApplication).toBe(true)
    // The field has no `omitempty` on the wire, so the server always states
    // it. Absent therefore means a fixture, and guessing "yes" would route an
    // ordinary panel Page into the application review surface.
    expect(derivePageCapabilities(page([plain])).hasApplication).toBe(false)
  })

  it("never offers publishing on a Page with no application", () => {
    const caps = derivePageCapabilities(page([plain], { has_application: false }))
    expect(caps.mayPublishApplication).toBe(false)
  })

  it("a server refusal lowers exactly one capability", () => {
    const caps = derivePageCapabilities(page([plain]), { mayManageAccess: false })
    expect(caps.mayManageAccess).toBe(false)
    expect(caps.mayEditDocument).toBe(true)
    expect(caps.mayViewSourceHistory).toBe(true)
  })

  it("the review snapshot can lower publishing without touching the rest", () => {
    const caps = derivePageCapabilities(page([plain]), { mayPublishApplication: false })
    expect(caps.mayPublishApplication).toBe(false)
    expect(caps.mayManageAccess).toBe(true)
    expect(caps.mayEditDocument).toBe(true)
  })
})
