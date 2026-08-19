import { describe, expect, it } from "vitest"

import { toPanelView } from "@/hooks/use-pages"

// The last hop of the icon, and the one that was missing.
//
// The server, the validator, the renderer and the docs all shipped together;
// `toPanelView` builds `spec` field by field, so the icon was dropped one step
// before the component that resolves it. Panels rendered their schema defaults
// and nothing failed — which is exactly why this needs a test rather than a
// glance. It is the same seam the `sealed` flag fell through earlier: two
// correct halves and no wire between them.
describe("a panel's chosen icon reaches the frame", () => {
  it("carries the icon onto the spec, which is what the renderer receives", () => {
    const view = toPanelView({ id: "pamet", schema: "metric.v1", icon: "memory", span: 4 })
    expect(view.spec.icon).toBe("memory")
  })

  it("does not invent one when the author chose none", () => {
    const view = toPanelView({ id: "pamet", schema: "metric.v1", span: 4 })
    expect(view.spec.icon).toBeNull()
  })

  it("passes an unknown name through rather than filtering it here", () => {
    // Narrowing belongs to PanelFrame, which owns the closed Set and the
    // schema fallback. Dropping it in the hook would mean two places decide
    // what a valid icon is, and they would eventually disagree.
    const view = toPanelView({ id: "x", schema: "metric.v1", icon: "definitely-not-an-icon" })
    expect(view.spec.icon).toBe("definitely-not-an-icon")
  })

  it("treats blank as absent, not as a name", () => {
    const view = toPanelView({ id: "x", schema: "metric.v1", icon: "   " })
    expect(view.spec.icon).toBeNull()
  })
})
