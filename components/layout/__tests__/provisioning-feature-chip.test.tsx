import { describe, it, expect, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { FeatureChip } from "../app-toolbar-provisioning"

/**
 * The Builder popover listed what the crew ASKED for. That is not what it is
 * running: `common-utils:2` builds some 2.x and the tag keeps moving, and
 * because the cache key is the ref as written, a moved tag is reused in
 * silence. Showing the resolved version — and which refs can still drift —
 * turns that into something the operator can actually see (#1779).
 */
describe("FeatureChip", () => {
  beforeEach(() => cleanup())

  it("shows the feature's leaf name", () => {
    render(<FeatureChip featureRef="ghcr.io/devcontainers/features/python:1" />)
    expect(screen.getByText("python")).toBeTruthy()
  })

  it("shows the resolved version when the build recorded one", () => {
    render(
      <FeatureChip
        featureRef="ghcr.io/devcontainers/features/github-cli:1"
        resolved={{ version: "1.0.14", digest: "sha256:abc", pinned: false }}
      />,
    )
    expect(screen.getByText(/1\.0\.14/)).toBeTruthy()
  })

  it("marks a floating ref so a silent drift is visible", () => {
    render(
      <FeatureChip
        featureRef="ghcr.io/devcontainers/features/github-cli:1"
        resolved={{ version: "1.0.14", digest: "sha256:abc", pinned: false }}
      />,
    )
    const chip = screen.getByTestId("feature-chip")
    expect(chip.getAttribute("title")).toMatch(/can change|floating/i)
  })

  it("does not warn about a pinned ref", () => {
    render(
      <FeatureChip
        featureRef="ghcr.io/devcontainers/features/github-cli@sha256:abc"
        resolved={{ version: "1.0.14", digest: "sha256:abc", pinned: true }}
      />,
    )
    const chip = screen.getByTestId("feature-chip")
    expect(chip.getAttribute("title")).not.toMatch(/can change|floating/i)
  })

  // A crew provisioned before provenance existed has no record. That must read
  // as "unknown", never as a claim about the version.
  it("stays quiet when nothing was recorded", () => {
    render(<FeatureChip featureRef="ghcr.io/devcontainers/features/python:1" />)
    const chip = screen.getByTestId("feature-chip")
    expect(chip.textContent).toBe("python")
  })
})

/**
 * The floating-ref hazard was carried by two things a screen-reader user never
 * receives: a `warn` colour, and a `title` attribute. `title` is not announced
 * by most screen readers and never appears on keyboard focus, since the chip is
 * a non-interactive span — so the warning reached sighted mouse users only.
 * Colour alone carries no meaning either. The state has to exist as text.
 */
describe("FeatureChip accessibility", () => {
  beforeEach(() => cleanup())

  it("states the floating hazard as text, not only as colour and a tooltip", () => {
    render(
      <FeatureChip
        featureRef="ghcr.io/devcontainers/features/github-cli:1"
        resolved={{ version: "1.0.14", digest: "sha256:abc", pinned: false }}
      />,
    )
    const chip = screen.getByTestId("feature-chip")
    // Remove every styling hook and the tooltip; what a screen reader is left
    // with must still say the ref can drift.
    expect(chip.textContent ?? "").toMatch(/floating|can change/i)
  })

  it("says a pinned ref is pinned rather than saying nothing", () => {
    render(
      <FeatureChip
        featureRef="ghcr.io/devcontainers/features/github-cli@sha256:abc"
        resolved={{ version: "1.0.14", digest: "sha256:abc", pinned: true }}
      />,
    )
    const chip = screen.getByTestId("feature-chip")
    expect(chip.textContent ?? "").toMatch(/pinned/i)
    expect(chip.textContent ?? "").not.toMatch(/floating/i)
  })
})
