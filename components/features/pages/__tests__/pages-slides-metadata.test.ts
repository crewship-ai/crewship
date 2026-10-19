import { describe, expect, it, vi } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"

vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: null, status: "unauthenticated" }) }))

describe("saved Slides Page metadata", () => {
  it.each([1, 2, 99])("retains the stored schema instead of treating Slides %s as an empty Page", schema => {
    const descriptor = { type_id: "slides", schema_version: schema, supported: schema !== 99 }
    const raw = {
      id: "immutable-slides-page", slug: "presentation", name: "Presentation",
      owner: "user/author", panels: [], has_slides: true, slides_schema: schema,
      content_descriptor: descriptor,
    }
    expect(toPageView(raw as WirePage)).toMatchObject({
      id: raw.id, hasSlides: true, slidesSchema: schema, contentDescriptor: descriptor,
    })
  })

  it("preserves an explicit unsupported descriptor rather than recovering support from a legacy flag", () => {
    const descriptor = { type_id: "conflicting", schema_version: 0, supported: false, reason: "conflicting-storage" }
    expect(toPageView({
      id: "conflicted", slug: "conflicted", panels: [], has_slides: true, slides_schema: 1,
      content_descriptor: descriptor,
    } as WirePage)).toMatchObject({ hasSlides: true, slidesSchema: 1, contentDescriptor: descriptor })
  })

  it("distinguishes an older server with no descriptor from an explicit unavailable descriptor", () => {
    const older = toPageView({ id: "older", slug: "older", panels: [] })
    const unavailable = toPageView({ id: "unavailable", slug: "unavailable", panels: [], content_descriptor: null } as WirePage)
    expect(older).toMatchObject({ hasSlides: false, slidesSchema: null })
    expect(older).not.toHaveProperty("contentDescriptor")
    expect(unavailable).toHaveProperty("contentDescriptor", null)
  })
})
