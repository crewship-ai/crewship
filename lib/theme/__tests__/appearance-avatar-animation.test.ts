import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  AVATAR_ANIMATION_STORAGE_KEY,
  DEFAULT_AVATAR_ANIMATION,
  parseAppearance,
  readAvatarAnimation,
  setAvatarAnimation,
  subscribeAppearance,
} from "@/lib/theme/appearance"

// #3789 — "Animated avatars", a per-viewer appearance choice beside Reduce
// motion. On by default; synced with the other appearance choices.

let store: Map<string, string>
function stubStorage() {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
}
beforeEach(stubStorage)
afterEach(() => vi.unstubAllGlobals())

describe("Animated avatars", () => {
  it("is on by default", () => {
    expect(DEFAULT_AVATAR_ANIMATION).toBe("on")
    expect(readAvatarAnimation()).toBe("on")
  })

  it("remembers the choice on this device and tells every reader", () => {
    const l = vi.fn()
    const off = subscribeAppearance(l)
    setAvatarAnimation("off")
    expect(localStorage.getItem(AVATAR_ANIMATION_STORAGE_KEY)).toBe("off")
    expect(readAvatarAnimation()).toBe("off")
    expect(l).toHaveBeenCalled()
    off()
  })

  it("ignores a stored value it does not know", () => {
    localStorage.setItem(AVATAR_ANIMATION_STORAGE_KEY, "sometimes")
    expect(readAvatarAnimation()).toBe("on")
  })

  it("travels with the synced appearance, and is dropped when invalid", () => {
    expect(parseAppearance({ avatarAnimation: "off" })).toEqual({ avatarAnimation: "off" })
    expect(parseAppearance({ avatarAnimation: "maybe" })).toEqual({})
  })
})
