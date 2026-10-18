import { describe, expect, it } from "vitest"
import { outcomeLabel } from "../inbox-detail-frame"

describe("outcomeLabel", () => {
  // A supplied credential is not an approval: the receipt names what happened.
  it("says a credential was supplied", () => {
    expect(outcomeLabel("supplied")).toBe("Credential supplied")
  })

  it("keeps the approval words", () => {
    expect(outcomeLabel("approved")).toBe("Approved")
    expect(outcomeLabel(null)).toBe("Decision saved")
  })
})
