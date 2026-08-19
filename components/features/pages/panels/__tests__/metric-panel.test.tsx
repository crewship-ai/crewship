/**
 * `metric.v1` — where colour is allowed to say something, and where it is not.
 *
 * §11b.9 settled this for the delta: *"green-up on an error rate would be a
 * lie, so the payload has to say which way is good."* A target is the same
 * shape of claim — 128 of 150 invoices reaching 150 is an achievement, 128 of
 * 150 open incidents reaching 150 is a fire, and `{value, target}` alone cannot
 * tell them apart. These tests hold the meter to the delta's rule rather than
 * letting it invent a second one.
 *
 * The other half is §3's *"never colour alone"*: every coloured state here also
 * carries a word, so the panel survives a monochrome print.
 */
import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"

import { PanelRenderer } from "../registry"
import { FIXTURE_NOW, metricFixtures } from "../fixtures"
import type { MetricPayload, PanelSnapshot, PanelSpec } from "../types"

const panel: PanelSpec = {
  id: "faktury",
  schema: "metric.v1",
  title: "Invoices closed",
  span: 4,
  sla_seconds: 3600,
}

function fixture(payload: MetricPayload): { panel: PanelSpec; data: PanelSnapshot } {
  return {
    panel,
    data: {
      state: "fresh",
      payload,
      provenance: { producer: "routine/nightly-close", produced_at: FIXTURE_NOW },
    },
  }
}

function meter(container: HTMLElement): HTMLElement {
  return container.querySelector<HTMLElement>('[data-slot="panel-target"]')!
}

/** The filled bar: the meter's only element with a width style. */
function bar(container: HTMLElement): HTMLElement {
  return meter(container).querySelector<HTMLElement>("[style]")!
}

function caption(container: HTMLElement): HTMLElement {
  return meter(container).querySelector<HTMLElement>("span")!
}

describe("the target meter colours only what the payload declared (§11b.9, §3)", () => {
  it("stays neutral below the target, whatever delta_good says", () => {
    const { container } = render(
      <PanelRenderer {...fixture({ value: 128, target: 150, delta_good: "up" })} now={FIXTURE_NOW} />,
    )
    expect(meter(container).getAttribute("data-reached")).toBe("false")
    expect(bar(container).className).toMatch(/\bbg-primary\b/)
    expect(bar(container).className).not.toMatch(/\bbg-(success|destructive)\b/)
    expect(caption(container).textContent).toBe("of 150 target")
  })

  it("goes to success when the target is reached and up is good", () => {
    const { container } = render(
      <PanelRenderer {...fixture({ value: 150, target: 150, delta_good: "up" })} now={FIXTURE_NOW} />,
    )
    expect(meter(container).getAttribute("data-reached")).toBe("true")
    expect(bar(container).className).toMatch(/\bbg-success\b/)
    expect(caption(container).className).toMatch(/\btext-success\b/)
  })

  /**
   * The case that makes the opt-in worth having: an error count with a ceiling
   * of 150. Reaching it is not an achievement, and a green bar would say it is.
   */
  it("goes to destructive when the target is reached and down is good", () => {
    const { container } = render(
      <PanelRenderer
        {...fixture({ value: 181, target: 150, delta_good: "down" })}
        now={FIXTURE_NOW}
      />,
    )
    expect(meter(container).getAttribute("data-reached")).toBe("true")
    expect(bar(container).className).toMatch(/\bbg-destructive\b/)
    expect(bar(container).className).not.toMatch(/\bbg-success\b/)
    expect(caption(container).className).toMatch(/\btext-destructive\b/)
  })

  it("reports the fact but claims nothing when the payload never said which way is good", () => {
    const { container } = render(
      <PanelRenderer {...fixture({ value: 150, target: 150 })} now={FIXTURE_NOW} />,
    )
    expect(meter(container).getAttribute("data-reached")).toBe("true")
    expect(bar(container).className).toMatch(/\bbg-primary\b/)
    expect(caption(container).className).not.toMatch(/\btext-(success|destructive)\b/)
    expect(caption(container).textContent).toContain("reached")
  })

  it("never leaves colour as the only carrier", () => {
    const { container } = render(
      <PanelRenderer {...fixture({ value: 150, target: 150, delta_good: "up" })} now={FIXTURE_NOW} />,
    )
    expect(caption(container).textContent).toContain("reached")
    expect(meter(container).getAttribute("aria-label")).toContain("target reached")
  })

  it("clamps the fill and keeps the meter's ARIA honest past 100 % (§11b.10)", () => {
    const { container } = render(
      <PanelRenderer {...fixture({ value: 300, target: 150, delta_good: "up" })} now={FIXTURE_NOW} />,
    )
    expect(bar(container).getAttribute("style")).toContain("100.0%")
    expect(meter(container).getAttribute("aria-valuenow")).toBe("300")
    expect(meter(container).getAttribute("aria-valuemax")).toBe("150")
  })

  it("draws no meter at all without a target", () => {
    const { container } = render(
      <PanelRenderer {...fixture({ value: 128, delta_good: "up" })} now={FIXTURE_NOW} />,
    )
    expect(container.querySelector('[data-slot="panel-target"]')).toBeNull()
  })

  /** A measured zero is a zero (§9b.4) — including as a meter of 0 %. */
  it("draws a zero-length bar for a measured zero rather than none", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.zero} now={FIXTURE_NOW} />)
    expect(meter(container)).toBeTruthy()
    expect(bar(container).getAttribute("style")).toContain("0.0%")
    expect(meter(container).getAttribute("data-reached")).toBe("false")
  })
})

/**
 * Unchanged by the colour pass, and asserted so it stays that way: the delta's
 * own opt-in is the precedent the meter now follows, and the two must not drift.
 */
describe("the delta's opt-in is untouched (§11b.9)", () => {
  it("is muted without delta_good, success with up, destructive against it", () => {
    const muted = render(<PanelRenderer {...metricFixtures.fresh} now={FIXTURE_NOW} />)
    expect(
      muted.container.querySelector('[data-slot="panel-delta"]')!.className,
    ).toMatch(/\btext-muted-foreground\b/)

    const up = render(<PanelRenderer {...metricFixtures.deltaGoodUp} now={FIXTURE_NOW} />)
    expect(up.container.querySelector('[data-slot="panel-delta"]')!.className).toMatch(
      /\btext-success\b/,
    )

    const down = render(<PanelRenderer {...metricFixtures.deltaGoodDown} now={FIXTURE_NOW} />)
    expect(down.container.querySelector('[data-slot="panel-delta"]')!.className).toMatch(
      /\btext-destructive\b/,
    )
  })

  it("carries an arrow and a sign, so direction never depends on colour", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.deltaGoodUp} now={FIXTURE_NOW} />)
    expect(container.querySelector('[data-slot="panel-delta"]')!.textContent).toContain("▲")
    expect(container.querySelector('[data-slot="panel-delta"]')!.textContent).toContain("+12")
  })
})
