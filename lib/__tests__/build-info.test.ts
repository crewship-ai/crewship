import { describe, it, expect } from "vitest"
import { buildLabel, editionName } from "../build-info"

describe("buildLabel", () => {
  it("shows a release tag as is and a dev build by its commit", () => {
    expect(buildLabel({ current: "v0.9.2", commit: "abc" })).toBe("v0.9.2")
    expect(buildLabel({ current: "dev", commit: "d43bfd1f95", dirty: true })).toBe("dev · d43bfd1+")
  })

  it("names the edition", () => {
    expect(editionName("community")).toBe("Community Edition")
    expect(editionName("acme")).toBe("Acme Edition")
  })
})
