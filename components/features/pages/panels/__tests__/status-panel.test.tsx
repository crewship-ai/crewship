/**
 * `status.v1` — the one schema §3 hands a palette to.
 *
 * *"Status colours are reserved"* is a rule that reserves them FOR this triad,
 * so this is where a page is allowed to be colourful: a grid of a dozen
 * services should be readable at a glance rather than pill by pill. The rail
 * these tests fence is that glance.
 *
 * The constraint it must not break is the same section's other half — *"state
 * carries glyph + text, never colour alone"* — so every assertion about a
 * colour has a sibling assertion that the word is still there.
 */
import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"

import { STATUS_DOT_CLASSES } from "@/lib/colors"
import { PanelRenderer } from "../registry"
import { FIXTURE_NOW, statusFixtures } from "../fixtures"

function items(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('[data-slot="status-item"]'))
}

function railOf(item: HTMLElement): HTMLElement {
  return item.querySelector<HTMLElement>('[data-slot="status-rail"]')!
}

function itemFor(container: HTMLElement, state: string): HTMLElement {
  return items(container).find((el) => el.getAttribute("data-state") === state)!
}

describe("the state rail takes its colour from the reserved triad", () => {
  it("paints ok, warning and critical from the product's status map", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.fresh} now={FIXTURE_NOW} />)

    // Not literals: the same map `StatusBadge` routes through. Pages does not
    // get a second status colour map, and this is how that stays true.
    expect(railOf(itemFor(container, "ok")).className).toContain(STATUS_DOT_CLASSES.COMPLETED)
    expect(railOf(itemFor(container, "warning")).className).toContain(STATUS_DOT_CLASSES.BLOCKED)
    expect(railOf(itemFor(container, "critical")).className).toContain(STATUS_DOT_CLASSES.FAILED)
  })

  it("gives a state the producer invented a neutral rail, not a colour", () => {
    const { container } = render(
      <PanelRenderer {...statusFixtures.unknownState} now={FIXTURE_NOW} />,
    )
    const rail = railOf(itemFor(container, "unknown"))
    expect(rail.className).toMatch(/\bbg-muted-foreground\b/)
    expect(rail.className).not.toMatch(/\bbg-(success|warn|destructive)\b/)
  })

  it("hides the rail from assistive technology — the word is the interface", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.fresh} now={FIXTURE_NOW} />)
    for (const item of items(container)) {
      expect(railOf(item).getAttribute("aria-hidden")).toBe("true")
    }
  })
})

describe("glyph and word, never colour alone (§3)", () => {
  it("still writes the state out on every row", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.fresh} now={FIXTURE_NOW} />)
    const words = items(container).map(
      (el) => el.querySelector('[data-slot="status-word"]')?.textContent,
    )
    expect(words).toEqual(["critical", "ok", "warning"])

    const glyphs = items(container).map(
      (el) => el.querySelector('[data-slot="status-glyph"]')?.textContent,
    )
    expect(glyphs).toEqual(["✕", "✓", "!"])
  })

  it("keeps the machine-readable state on the row, independent of any class", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.fresh} now={FIXTURE_NOW} />)
    expect(items(container).map((el) => el.getAttribute("data-state"))).toEqual([
      "critical",
      "ok",
      "warning",
    ])
  })

  it("names an unnamed item rather than drawing an anonymous coloured row", () => {
    const { container } = render(
      <PanelRenderer {...statusFixtures.unknownState} now={FIXTURE_NOW} />,
    )
    expect(items(container)[0].textContent).toContain("probe")
    expect(items(container)[0].textContent).toContain("unknown")
  })
})
