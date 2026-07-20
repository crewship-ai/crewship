import { describe, it, expect, beforeAll } from "vitest"
import { createHash } from "node:crypto"
import {
  AVATAR_STYLES,
  preloadAvatarStyle,
  getAgentAvatarUrl,
  _resetAvatarCacheForTest,
} from "@/lib/agent-avatar"

/**
 * Golden-hash pin on the rendered avatar output.
 *
 * Agent avatars are derived from (seed, style) at render time, so the
 * DiceBear version in package.json decides what an agent looks like. That
 * makes an avatar library upgrade a user-visible product change disguised as
 * a dependency bump: nothing in the type system, the build, or the existing
 * cache tests notices when the same seed starts drawing a different face.
 *
 * Agents CAN now keep a stored render (#1297), which pins their face against
 * exactly this drift — but only once one has been stored for them. Freshly
 * created agents, agents nobody with edit rights has viewed yet, and every
 * agent on an instance that has not backfilled are still live-generated, and
 * this generator is also what produces the bytes that get stored. So the
 * tripwire still matters; its blast radius is just bounded to the
 * not-yet-persisted population rather than every agent everywhere.
 *
 * This is not hypothetical. A @dicebear/core 9 -> 10 spike (2026-07-20)
 * re-rendered 10 styles x 5 seeds and found ZERO identical outputs — new
 * background colours, different feature variants for the same seed, SVG
 * payloads changing by 3x in both directions. Shipping it would silently
 * repaint every agent in every workspace.
 *
 * So: these hashes are a deliberate tripwire, not a correctness assertion.
 * There is no "right" hash — there is only "the same as what users are
 * already looking at".
 *
 * IF THIS TEST FAILS, the rendering changed. Do not just refresh the
 * hashes. Decide, explicitly, whether this is an intended visual refresh
 * that gets communicated — and remember that agents with a stored render
 * will NOT follow it, so an upgrade now splits the roster into old faces
 * (persisted) and new ones (not), which is its own product decision.
 * Regenerate the hashes only once that decision is made, in the same
 * commit as the change that justifies it.
 *
 * A version bump also has to regenerate the Go-side fixtures that pin the
 * generator/validator contract: `node scripts/gen-avatar-fixtures.mjs`.
 */
const GOLDEN_AVATAR_HASHES: Record<string, string> = {
  "bottts-neutral__alice": "4bba430f9df07414",
  "bottts-neutral__bob-the-builder": "92edb18e04e8cde5",
  "bottts-neutral__42": "ae09fbc9e84c92cd",
  "adventurer__alice": "1e128daabe4d48d8",
  "adventurer__bob-the-builder": "494079c0bdded4d0",
  "adventurer__42": "2e6e1495dc516719",
  "fun-emoji__alice": "e003dfb8bdb053f1",
  "fun-emoji__bob-the-builder": "32ca87580ced17e4",
  "fun-emoji__42": "c651603338d18035",
  "pixel-art__alice": "fd95500c990e5c49",
  "pixel-art__bob-the-builder": "2479f90248abcdda",
  "pixel-art__42": "756e6feb91e51bfa",
  "micah__alice": "2a37186e4bfa3189",
  "micah__bob-the-builder": "18fe32e1ed8051fd",
  "micah__42": "4f4bd221afa72ffb",
  "notionists__alice": "2a7b161a761e226a",
  "notionists__bob-the-builder": "ed36c15de44db983",
  "notionists__42": "1b20e13a21a01b46",
  "thumbs__alice": "1763b4a2c01193ec",
  "thumbs__bob-the-builder": "3ebeffb86e30fe1f",
  "thumbs__42": "2357291af12a888c",
  "lorelei__alice": "e7cafffb31822008",
  "lorelei__bob-the-builder": "25caed1697415e11",
  "lorelei__42": "ee4c4a3c07bc1a5d",
  "big-smile__alice": "0c41e1ac9193204e",
  "big-smile__bob-the-builder": "d0c90f01411cbbae",
  "big-smile__42": "5d5e884068c701bf",
  "avataaars__alice": "489f421f0ee84c1a",
  "avataaars__bob-the-builder": "a5465172e3c94cbe",
  "avataaars__42": "ecef057d5cc87751",
}

const SEEDS = ["alice", "bob-the-builder", "42"]

const hashOf = (uri: string) => createHash("sha256").update(uri).digest("hex").slice(0, 16)

describe("avatar rendering stability", () => {
  beforeAll(async () => {
    _resetAvatarCacheForTest()
    // Styles load lazily; getAgentAvatarUrl returns a placeholder until the
    // collection lands, so every style has to be resident before hashing or
    // the pin would lock in placeholders instead of real avatars.
    await Promise.all(Object.keys(AVATAR_STYLES).map((s) => preloadAvatarStyle(s)))
  })

  it("covers every style the picker offers", () => {
    // Guards the pin against silently going stale: a style added to
    // AVATAR_STYLES without a golden entry would otherwise be unpinned.
    const pinned = new Set(Object.keys(GOLDEN_AVATAR_HASHES).map((k) => k.split("__")[0]))
    expect([...pinned].sort()).toEqual(Object.keys(AVATAR_STYLES).sort())
    expect(Object.keys(GOLDEN_AVATAR_HASHES)).toHaveLength(
      Object.keys(AVATAR_STYLES).length * SEEDS.length,
    )
  })

  it.each(Object.keys(AVATAR_STYLES))("renders %s identically to the pinned output", (style) => {
    for (const seed of SEEDS) {
      const key = `${style}__${seed}`
      const uri = getAgentAvatarUrl(seed, style)
      // A placeholder here means the style never resolved — that is a
      // broken test setup, not a rendering change, so fail loudly on it
      // rather than pinning the placeholder's hash.
      expect(uri, `${key} rendered a placeholder — style failed to load`).toMatch(
        /^data:image\/svg\+xml/,
      )
      expect(hashOf(uri), `avatar rendering changed for ${key} — see this file's header`).toBe(
        GOLDEN_AVATAR_HASHES[key],
      )
    }
  })

  it("is deterministic across repeated renders of the same seed", () => {
    _resetAvatarCacheForTest()
    const first = getAgentAvatarUrl("alice", "bottts-neutral")
    _resetAvatarCacheForTest()
    const second = getAgentAvatarUrl("alice", "bottts-neutral")
    expect(second).toBe(first)
  })
})
