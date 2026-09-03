import { describe, it, expect } from "vitest"
import { refHref, refLabel } from "@/lib/entity-refs"

describe("refHref", () => {
  it("routes the kind/slug refs pages and routines store", () => {
    expect(refHref("crew/ops")).toBe("/crews?crew=ops")
    expect(refHref("agent/riley")).toBe("/crews?agent=riley")
    expect(refHref("routine/uptime-sweep")).toBe("/routines?slug=uptime-sweep")
    expect(refHref("page/watch")).toBe("/pages/watch")
  })
  it("returns null for anything it cannot route rather than a dead link", () => {
    expect(refHref("script/ops.sh")).toBeNull()
    expect(refHref("crew/")).toBeNull()
    expect(refHref(null)).toBeNull()
  })
  it("labels with the slug alone", () => {
    expect(refLabel("routine/uptime-sweep")).toBe("uptime-sweep")
    expect(refLabel("plain")).toBe("plain")
  })
})
