import { describe, it, expect } from "vitest"

import { AVATAR_STYLES, DEFAULT_AVATAR_STYLE, loadableStyleSlugs } from "@/lib/agent-avatar"

// =============================================================================
// A style in the catalogue with no way to load it is a silent lie.
//
// AVATAR_STYLES is what the picker enumerates; STYLE_LOADERS is what can
// actually be rendered. If a slug reaches the first without reaching the
// second, getAgentAvatarUrl falls through to the default — so the user picks
// "Open Peeps", saves, and keeps the same face with no error anywhere. That is
// the exact failure the original avatar-style-keys test was written about, one
// level down.
//
// Labels are DiceBear's own names so the list can be diffed against
// dicebear.com by eye. "Robots" was a local invention for bottts-neutral and
// became actively wrong the moment the sibling collection bottts arrived.
// =============================================================================

describe("avatar catalogue", () => {
  it("can load every style it offers", () => {
    const loadable = new Set([...loadableStyleSlugs(), DEFAULT_AVATAR_STYLE])
    for (const slug of Object.keys(AVATAR_STYLES)) {
      expect(loadable.has(slug), `"${slug}" is offered but has no loader`).toBe(true)
    }
  })

  it("offers every style it can load", () => {
    for (const slug of loadableStyleSlugs()) {
      expect(AVATAR_STYLES[slug], `"${slug}" can load but is not offered`).toBeDefined()
    }
  })

  it("uses DiceBear's own names", () => {
    // A label that is not the collection's real name cannot be checked against
    // the source, and drifts the moment a sibling collection appears.
    expect(AVATAR_STYLES["bottts-neutral"].label).toBe("Bottts Neutral")
    expect(AVATAR_STYLES.bottts.label).toBe("Bottts")
    expect(Object.values(AVATAR_STYLES).map((s) => s.label)).not.toContain("Robots")
  })

  it("carries the full catalogue, not a sample of it", () => {
    for (const slug of [
      "adventurer", "adventurer-neutral", "avataaars", "avataaars-neutral",
      "big-ears", "big-ears-neutral", "big-smile", "bottts", "bottts-neutral",
      "croodles", "croodles-neutral", "dylan", "fun-emoji", "lorelei",
      "lorelei-neutral", "micah", "miniavs", "notionists", "notionists-neutral",
      "open-peeps", "personas", "pixel-art", "pixel-art-neutral", "thumbs",
      "toon-head",
    ]) {
      expect(AVATAR_STYLES[slug], `missing "${slug}"`).toBeDefined()
    }
  })

  it("has no duplicate labels", () => {
    const labels = Object.values(AVATAR_STYLES).map((s) => s.label)
    expect(new Set(labels).size).toBe(labels.length)
  })
})
