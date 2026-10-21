import { describe, it, expect } from "vitest"
import { CHANGE_KIND_LABEL } from "../activity-feed"

// ODCH-19: one concept, one name. The sidebar and the palette call it
// Artifacts; so does the Activity "what changed" filter.
describe("CHANGE_KIND_LABEL", () => {
  it("calls the artifact kind Artifacts", () => {
    expect(CHANGE_KIND_LABEL.artifact).toBe("Artifacts")
  })
})
