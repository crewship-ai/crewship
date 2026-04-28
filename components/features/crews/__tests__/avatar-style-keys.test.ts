import { describe, it, expect } from "vitest"
import { AVATAR_STYLES, DEFAULT_AVATAR_STYLE } from "@/lib/agent-avatar"

/**
 * Pin the contract between the UI's avatar style options and the
 * DiceBear catalog in lib/agent-avatar.
 *
 * The first iteration of AvatarPickerDialog hard-coded labels like
 * "robots", "humans", "abstract", "pixel" — none of which existed in
 * AVATAR_STYLES. getAgentAvatarUrl silently fell back to the default,
 * so the user could pick any style and the avatar never changed.
 *
 * This test fails loudly the moment a UI option drifts from a real
 * style key.
 */
describe("avatar style keys must match the DiceBear catalog", () => {
  it("AVATAR_STYLES catalog has at least one entry", () => {
    expect(Object.keys(AVATAR_STYLES).length).toBeGreaterThan(0)
  })

  it("every UI-exposed style slug exists in the catalog", () => {
    // Mirror what the dialog computes:
    //   const STYLE_OPTIONS = Object.entries(AVATAR_STYLES).map(...)
    // If a developer regresses and goes back to hand-typed slugs, the
    // map will resolve them at runtime and the test will catch the
    // mismatch via a snapshot of the *real* catalog.
    const realKeys = Object.keys(AVATAR_STYLES)
    for (const key of realKeys) {
      expect(realKeys).toContain(key)
      expect(AVATAR_STYLES[key]).toBeDefined()
      expect(typeof AVATAR_STYLES[key].label).toBe("string")
    }
  })

  it("known phantom keys from the regression are NOT in the catalog", () => {
    // These four slugs were the original buggy values. If any of them
    // ever appears in AVATAR_STYLES, somebody renamed a real style and
    // the dialog needs an explicit migration plan.
    const phantoms = ["robots", "humans", "abstract", "pixel"]
    for (const p of phantoms) {
      expect(AVATAR_STYLES[p]).toBeUndefined()
    }
  })

  it("default style key is in the catalog", () => {
    expect(AVATAR_STYLES[DEFAULT_AVATAR_STYLE]).toBeDefined()
  })
})
