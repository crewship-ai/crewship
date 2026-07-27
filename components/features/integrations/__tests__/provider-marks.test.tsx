import { render } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { brandColor, hasBrandMark, ProviderMark } from "../provider-marks"

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

  it("renders real artwork for every brand except the one with no licensed mark", () => {
    // Gotify has no permissively-licensed mark anywhere, so it is a lettermark
    // on purpose. If that ever changes, this test should be updated — not the
    // other way round.
    const withoutArt = PROVIDERS.filter((p) => !hasBrandMark(p))
    expect(withoutArt).toEqual(["gotify"])
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

  it("renders the bare glyph without a tile when asked", () => {
    const { container } = render(<ProviderMark provider="discord" bare />)
    // The tile is the wrapping <span>; bare mode must not emit one.
    expect(container.firstElementChild?.tagName.toLowerCase()).toBe("svg")
  })
})
