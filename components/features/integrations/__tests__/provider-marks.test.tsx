import { render } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { brandColor, displayColor, hasBrandMark, ProviderMark } from "../provider-marks"

/** Rec. 709 luma of a #rrggbb string, 0–1. */
function luma(hex: string): number {
  const h = hex.replace("#", "")
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16))
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255
}

/**
 * Every service the catalog can render must resolve to *something* visible.
 *
 * The failure this guards against is silent: an unmapped provider used to fall
 * through to a grey initials tile that looked deliberate, so nobody noticed
 * that Slack had no logo. Asserting per-provider means adding a provider to the
 * Go catalog without artwork fails here rather than shipping a grey square.
 */
const PROVIDERS = [
  "discord",
  "slack",
  "telegram",
  "ntfy",
  "gotify",
  "pushover",
  "mattermost",
  "matrix",
  "teams",
  "googlechat",
  "opsgenie",
]

describe("ProviderMark", () => {
  it.each(PROVIDERS)("renders a mark for %s", (provider) => {
    const { container } = render(<ProviderMark provider={provider} label={provider} />)
    // Either real artwork (an <svg>) or a lettermark (text) — never empty.
    const hasArt = container.querySelector("svg") !== null
    const hasText = (container.textContent ?? "").trim().length > 0
    expect(hasArt || hasText).toBe(true)
  })

  it.each(PROVIDERS)("knows %s's brand colour", (provider) => {
    expect(brandColor(provider)).toMatch(/^#[0-9A-Fa-f]{6}$/)
  })

  it("renders real artwork for every service — no lettermark fallbacks left", () => {
    // Gotify was the last holdout (nothing under a permissive licence); it is
    // now the Arcticons mark, CC BY-SA. A provider added to the Go catalog
    // without artwork fails here rather than shipping a grey initials tile
    // that looks deliberate.
    expect(PROVIDERS.filter((p) => !hasBrandMark(p))).toEqual([])
  })

  it.each(PROVIDERS)("draws %s in something visible on a dark surface", (provider) => {
    // Matrix's official brand colour is #000000. Rendering it faithfully means
    // rendering it invisible on a dark-first UI — which is what shipped once,
    // as a smudge on a near-black tile. Any brand whose real hex is near-black
    // must be lifted for display; this asserts the LIFT, not the brand value.
    const drawn = displayColor(provider)
    expect(drawn).toBeDefined()
    expect(luma(drawn!)).toBeGreaterThan(0.12)
  })

  it("keeps every other brand's real hex for display", () => {
    // Only the near-black case may diverge; a lift applied more widely would
    // quietly repaint brands we went to the trouble of vendoring accurately.
    const diverging = PROVIDERS.filter((p) => brandColor(p) !== displayColor(p))
    expect(diverging).toEqual(["matrix"])
  })

  it("tints the built-in transports rather than leaving them grey", () => {
    for (const key of ["email", "webhook", "composio"]) {
      expect(brandColor(key)).toMatch(/^#[0-9A-Fa-f]{6}$/)
    }
  })

  it("falls back to a lettermark for a provider it has never heard of", () => {
    const { container } = render(<ProviderMark provider="brand-new-thing" label="Brand New" />)
    expect(container.textContent).toBe("BN")
  })

  it("prefers a remote logo over the vendored marks", () => {
    // Composio's catalog is 1000+ apps; gmail and googledrive cannot come from
    // the eleven marks bundled here. Without this the panel showed two-letter
    // tiles while the column beside it showed the real icon for the same row.
    const { container } = render(
      <ProviderMark provider="gmail" label="gmail" logoUrl="https://logos.example/gmail" />,
    )
    const img = container.querySelector("img")
    expect(img).not.toBeNull()
    expect(img?.getAttribute("src")).toBe("https://logos.example/gmail")
  })

  it("still uses the vendored mark when a brand has one and no logo is passed", () => {
    // The remote logo is an addition, not a replacement: Discord's vendored
    // full-colour mark must not start depending on a network fetch.
    const { container } = render(<ProviderMark provider="discord" />)
    expect(container.querySelector("img")).toBeNull()
    expect(container.querySelector("svg")).not.toBeNull()
  })

  it("renders the bare glyph without a tile when asked", () => {
    const { container } = render(<ProviderMark provider="discord" bare />)
    // The tile is the wrapping <span>; bare mode must not emit one.
    expect(container.firstElementChild?.tagName.toLowerCase()).toBe("svg")
  })
})
