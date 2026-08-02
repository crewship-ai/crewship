import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"

import { RuntimeIcon, runtimeBrand, RUNTIME_BRANDS } from "@/components/icons/runtime-icons"

// Crewship advertises seven container runtimes and, until #1690, rendered
// every one of them as the same generic capitalised string next to the same
// green tick. These assertions are about WHICH mark and WHICH colour, because
// "a component mounted" is exactly what would have passed before.

describe("runtimeBrand", () => {
  it.each([
    ["docker", "Docker", true],
    ["podman", "Podman", true],
    ["apple", "Apple Containers", true],
    ["rancher", "Rancher Desktop", true],
    ["containerd", "containerd", true],
    ["nerdctl", "containerd", true],
  ] as const)("%s is %s and has an official mark", (key, label, official) => {
    const brand = runtimeBrand(key)
    expect(brand.label).toBe(label)
    expect(brand.official).toBe(official)
  })

  // Colima and OrbStack have no Simple Icons entry. Inventing a mark or a
  // brand colour for them would be worse than saying nothing: it would be
  // wrong, and confidently so.
  it.each([
    ["colima", "Colima"],
    ["orbstack", "OrbStack"],
  ] as const)("%s keeps its real name but takes a neutral glyph", (key, label) => {
    const brand = runtimeBrand(key)
    expect(brand.label).toBe(label)
    expect(brand.official).toBe(false)
  })

  it("names an unknown runtime after itself rather than guessing", () => {
    const brand = runtimeBrand("some-future-engine")
    expect(brand.label).toBe("some-future-engine")
    expect(brand.official).toBe(false)
  })

  // A brand colour that vanishes on one theme is worse than a neutral glyph.
  // Apple's mark is pure black — invisible on the dark console — so every
  // official mark carries a variant for the background it is drawn on.
  it("gives Apple's mark the colour Apple itself uses per background", () => {
    expect(RUNTIME_BRANDS.apple.light).toBe("#000000")
    expect(RUNTIME_BRANDS.apple.dark).toBe("#FFFFFF")
  })

  it("never leaves an official mark without both colours", () => {
    for (const [key, brand] of Object.entries(RUNTIME_BRANDS)) {
      if (!brand.official) continue
      expect(brand.light, `${key}.light`).toMatch(/^#[0-9A-F]{6}$/)
      expect(brand.dark, `${key}.dark`).toMatch(/^#[0-9A-F]{6}$/)
    }
  })

  // The assertion that actually protects the reader. Eyeballing a colour in
  // one theme is how Docker's whale blue got to 3.02:1 on the light card and
  // Apple's mark to a black square on a black one. WCAG 1.4.11 puts graphical
  // objects at 3:1; every mark is held to it against the card it sits on.
  //
  // Card colours are `--card` from app/globals.css, converted to sRGB: light
  // oklch(0.9851 0 0), dark oklch(0.155 0.010 285). The rows tint them by
  // white/2%, which moves neither enough to matter.
  const CARD = { light: "#FAFAFA", dark: "#0C0C10" } as const

  function contrast(a: string, b: string): number {
    const lum = (hex: string) => {
      const ch = [1, 3, 5]
        .map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
        .map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
      return 0.2126 * ch[0] + 0.7152 * ch[1] + 0.0722 * ch[2]
    }
    const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
    return (hi + 0.05) / (lo + 0.05)
  }

  it("sanity-checks the contrast helper itself", () => {
    // Black on white is the textbook 21:1; a colour on itself is 1:1.
    expect(contrast("#000000", "#FFFFFF")).toBeCloseTo(21, 1)
    expect(contrast("#2496ED", "#2496ED")).toBeCloseTo(1, 5)
  })

  // The floor is 3.5, not the WCAG 3.0 itself. Docker's whale blue measures
  // 3.02:1 on the light card — technically compliant and one adjustment to
  // --card away from not being. A bar set exactly at the minimum passes every
  // colour that is about to fail.
  const FLOOR = 3.5

  it.each(Object.entries(RUNTIME_BRANDS).filter(([, b]) => b.official))(
    "%s's mark clears the contrast floor on both the light and the dark card",
    (_key, brand) => {
      expect(contrast(brand.light, CARD.light)).toBeGreaterThanOrEqual(FLOOR)
      expect(contrast(brand.dark, CARD.dark)).toBeGreaterThanOrEqual(FLOOR)
    },
  )
})

describe("RuntimeIcon", () => {
  it("paints an official mark in its brand colour, with a dark-theme variant", () => {
    const { container } = render(<RuntimeIcon runtime="podman" />)
    const svg = container.querySelector("svg")!

    expect(svg.getAttribute("data-runtime-icon")).toBe("podman")
    expect(svg.getAttribute("data-brand-mark")).toBe("official")
    expect(svg.style.getPropertyValue("--rt-brand-light")).toBe(RUNTIME_BRANDS.podman.light)
    expect(svg.style.getPropertyValue("--rt-brand-dark")).toBe(RUNTIME_BRANDS.podman.dark)
    // Both themes are driven off those variables — a mark that only styles one
    // of them disappears on the other.
    expect(svg.getAttribute("class")).toContain("text-[color:var(--rt-brand-light)]")
    expect(svg.getAttribute("class")).toContain("dark:text-[color:var(--rt-brand-dark)]")
    expect(svg.querySelector("path")?.getAttribute("d")?.length ?? 0).toBeGreaterThan(0)
  })

  it("renders the marks of two different products differently", () => {
    const dockerPath = render(<RuntimeIcon runtime="docker" />)
      .container.querySelector("path")
      ?.getAttribute("d")
    const podmanPath = render(<RuntimeIcon runtime="podman" />)
      .container.querySelector("path")
      ?.getAttribute("d")

    expect(dockerPath).toBeTruthy()
    expect(podmanPath).toBeTruthy()
    expect(dockerPath).not.toBe(podmanPath)
  })

  it("falls back to a neutral glyph, in no brand colour at all, when the product has no mark", () => {
    for (const runtime of ["colima", "orbstack"]) {
      const { container } = render(<RuntimeIcon runtime={runtime} />)
      const svg = container.querySelector("svg")!
      expect(svg.getAttribute("data-runtime-icon")).toBe(runtime)
      expect(svg.getAttribute("data-brand-mark")).toBe("none")
      // No invented brand colour: it inherits the theme's muted foreground,
      // which is legible on both.
      expect(svg.getAttribute("class")).toContain("text-muted-foreground")
      expect(svg.style.getPropertyValue("--rt-brand-light")).toBe("")
    }
  })

  it("forwards className so callers keep control of size", () => {
    const { container } = render(<RuntimeIcon runtime="docker" className="size-5" />)
    expect(container.querySelector("svg")?.getAttribute("class")).toContain("size-5")
  })
})
