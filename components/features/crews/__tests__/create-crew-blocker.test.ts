import { describe, it, expect } from "vitest"
import { stepBlocker } from "@/components/features/crews/create-crew-dialog"
import type { WizardState } from "@/components/features/crews/create-crew/types"

// README §6: every disabled primary states why, next to itself. The
// create-crew Continue used to go grey with nothing to read.
const base = { name: "Engineering", slug: "engineering", mode: "empty", pickedTemplateSlug: null } as unknown as WizardState

describe("stepBlocker", () => {
  it("names the identity rule that is not met yet", () => {
    expect(stepBlocker(1, { ...base, name: "E" })).toBe("Name must be at least 2 characters")
    expect(stepBlocker(1, { ...base, slug: "Eng Team" })).toBe("Slug must use only lowercase letters, digits and hyphens (2+ chars)")
    expect(stepBlocker(1, base)).toBeNull()
  })
  it("asks for a template only when browsing without one", () => {
    expect(stepBlocker(2, { ...base, mode: "browse" } as WizardState)).toBe("Pick a template, or start with an empty lineup")
    expect(stepBlocker(2, { ...base, mode: "browse", pickedTemplateSlug: "eng" } as WizardState)).toBeNull()
    expect(stepBlocker(2, base)).toBeNull()
  })
  it("has nothing to say on the container and review steps", () => {
    expect(stepBlocker(3, base)).toBeNull()
    expect(stepBlocker(4, base)).toBeNull()
  })
})
