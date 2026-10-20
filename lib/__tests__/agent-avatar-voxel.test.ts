import { describe, it, expect, beforeAll, beforeEach } from "vitest"

import {
  AVATAR_ANIMATION_MIN_PX,
  AVATAR_STYLES,
  DEFAULT_AVATAR_STYLE,
  LEGACY_DEFAULT_AVATAR_STYLE,
  getAgentAvatarSVG,
  getAgentAvatarUrl,
  isAnimatableAvatarStyle,
  preloadAvatarStyle,
  shouldAnimateAvatar,
  _resetAvatarCacheForTest,
} from "@/lib/agent-avatar"

// #3789 — DiceBear 10's Voxel Bot and Voxel Art, loaded beside 9.4 for these
// two styles only, and the animation rules that go with them.

function svgOf(uri: string): string {
  const comma = uri.indexOf(",")
  return decodeURIComponent(uri.slice(comma + 1))
}

beforeAll(async () => {
  await Promise.all([preloadAvatarStyle("voxel-bot"), preloadAvatarStyle("voxel-art")])
})

beforeEach(() => {
  _resetAvatarCacheForTest()
})

describe("voxel styles in the catalogue", () => {
  it("offers Voxel Bot and Voxel Art under DiceBear's own names, marked new and animated", () => {
    expect(AVATAR_STYLES["voxel-bot"]).toMatchObject({ label: "Voxel Bot", isNew: true, animated: true })
    expect(AVATAR_STYLES["voxel-art"]).toMatchObject({ label: "Voxel Art", isNew: true, animated: true })
  })

  it("marks only the voxel styles animatable", () => {
    const animatable = Object.keys(AVATAR_STYLES).filter(isAnimatableAvatarStyle)
    expect(animatable.sort()).toEqual(["voxel-art", "voxel-bot"])
    expect(isAnimatableAvatarStyle(null)).toBe(false)
    expect(isAnimatableAvatarStyle("not-a-style")).toBe(false)
  })
})

describe("defaults", () => {
  it("draws an agent with no style (and no crew style) as Voxel Bot", () => {
    expect(DEFAULT_AVATAR_STYLE).toBe("voxel-bot")
    expect(getAgentAvatarUrl("alice", null)).toBe(getAgentAvatarUrl("alice", "voxel-bot"))
    expect(getAgentAvatarUrl("alice", "unknown-style")).toBe(getAgentAvatarUrl("alice", "voxel-bot"))
  })

  it("keeps the pre-#3789 default available by name, because existing agents are pinned to it", () => {
    expect(LEGACY_DEFAULT_AVATAR_STYLE).toBe("bottts-neutral")
    // The reason the database migration exists: an unpinned agent would
    // change face the moment the default moved.
    expect(getAgentAvatarUrl("alice", null)).not.toBe(getAgentAvatarUrl("alice", LEGACY_DEFAULT_AVATAR_STYLE))
  })
})

describe("voxel rendering", () => {
  it("renders a real DiceBear 10 avatar, not the loading placeholder", () => {
    const svg = svgOf(getAgentAvatarUrl("alice", "voxel-bot"))
    expect(svg).toContain("Voxel Bot")
    expect(svg).toContain("CC0 1.0")
  })

  it("is static unless animation is asked for", () => {
    const still = svgOf(getAgentAvatarUrl("alice", "voxel-bot"))
    expect(still).not.toContain("@keyframes")
    const moving = svgOf(getAgentAvatarUrl("alice", "voxel-bot", { animated: true }))
    expect(moving).toContain("@keyframes")
    // The OS setting is honoured inside the SVG itself.
    expect(moving).toContain("@media (prefers-reduced-motion: no-preference)")
  })

  it("animates with the slow variant", () => {
    const moving = svgOf(getAgentAvatarUrl("filip", "voxel-art", { animated: true }))
    expect(moving).toContain("animation-slow")
  })

  it("draws the same face animated and still: the animation only adds a layer", () => {
    const still = svgOf(getAgentAvatarUrl("filip", "voxel-bot"))
    const moving = svgOf(getAgentAvatarUrl("filip", "voxel-bot", { animated: true }))
    const strip = (s: string) =>
      s
        .replace(/<style>[\s\S]*?<\/style>/g, "")
        .replace(/<g id="animation-[a-z]+-[0-9a-f]+">[\s\S]*?<\/g>/g, "")
        .replace(/animation-[a-z]+-/g, "animation-")
    expect(strip(moving)).toBe(strip(still))
  })

  it("ignores the animation flag for styles that cannot move", () => {
    expect(getAgentAvatarUrl("alice", "bottts-neutral", { animated: true })).toBe(
      getAgentAvatarUrl("alice", "bottts-neutral"),
    )
  })

  it("caches still and animated renders separately", () => {
    const a = getAgentAvatarUrl("alice", "voxel-bot")
    const b = getAgentAvatarUrl("alice", "voxel-bot", { animated: true })
    expect(a).not.toBe(b)
    expect(getAgentAvatarUrl("alice", "voxel-bot")).toBe(a)
  })

  it("gives the raw-SVG path the still render", () => {
    const svg = getAgentAvatarSVG("alice", "voxel-bot")
    expect(svg).not.toBeNull()
    expect(svg).not.toContain("@keyframes")
  })
})

describe("shouldAnimateAvatar", () => {
  const base = { style: "voxel-bot", sizePx: 48, motion: "system", avatarAnimation: "on" } as const

  it.each([
    ["voxel at 48 px, defaults", base, true],
    ["exactly the threshold", { ...base, sizePx: AVATAR_ANIMATION_MIN_PX }, true],
    ["below the threshold", { ...base, sizePx: AVATAR_ANIMATION_MIN_PX - 1 }, false],
    ["not measured yet", { ...base, sizePx: 0 }, false],
    ["Reduce motion: On", { ...base, motion: "on" }, false],
    ["Reduce motion: Off still animates", { ...base, motion: "off" }, true],
    ["System, and the OS asks for reduced motion", { ...base, osReducedMotion: true }, false],
    ["Off overrides the OS", { ...base, motion: "off", osReducedMotion: true }, true],
    ["Animated avatars off", { ...base, avatarAnimation: "off" }, false],
    ["a style that cannot move", { ...base, style: "bottts-neutral" }, false],
    ["no style resolves to the default, which moves", { ...base, style: null }, true],
  ] as const)("%s", (_name, input, want) => {
    expect(shouldAnimateAvatar(input)).toBe(want)
  })

  it("draws the line at 32 px", () => {
    expect(AVATAR_ANIMATION_MIN_PX).toBe(32)
  })
})
