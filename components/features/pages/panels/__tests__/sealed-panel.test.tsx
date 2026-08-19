/**
 * The sealed placeholder (PRD §7.1 rule 2, §2.3, wire shape §11b.14).
 *
 * The bug this pins was live: a panel the viewer may not see arrives with no
 * `schema` — the server strips it — so `resolvePanelComponent("")` fell
 * through to `UnknownSchemaPanel` and told the reader *"This version of
 * Crewship does not render `` panels. Upgrade Crewship."* That is a lie about
 * the cause, it discards the `owner_crew_name` the server takes trouble to
 * send, and it is precisely the Grafana failure mode §2.3 names as the reason
 * the placeholder exists: a dashboard that opens but whose panels fail inside
 * it.
 *
 * §11b.14 fixes the direction the renderer must key on: *"The renderer keys on
 * `sealed`, not on a missing field, so a serialisation bug can never be
 * mistaken for a permission decision."* Both halves are asserted below,
 * because a check that treated "no schema" as "sealed" would be the same bug
 * with the sign flipped — it would hide a broken panel behind a permissions
 * message.
 */
import { describe, it, expect } from "vitest"
import { render, screen, within } from "@testing-library/react"

import { PanelRenderer, resolvePanelComponent } from "../registry"
import { SealedPanel } from "../sealed-panel"
import { UnknownSchemaPanel } from "../fallback-panel"
import { EM_DASH } from "../freshness"
import { FIXTURE_NOW, sealedFixtures } from "../fixtures"

describe("a sealed panel is a permission decision, not an error (§7.1 rule 2)", () => {
  it("names the owning crew so the reader knows who to ask", () => {
    const { container } = render(<PanelRenderer {...sealedFixtures.withCrew} now={FIXTURE_NOW} />)
    const sealed = container.querySelector('[data-slot="panel-sealed"]')
    expect(sealed).toBeTruthy()
    expect(sealed!.textContent).toBe("Hidden · crew Účetní")
    expect(sealed!.getAttribute("data-owner-crew")).toBe("Účetní")
  })

  it("never shows the unknown-schema copy", () => {
    const { container } = render(<PanelRenderer {...sealedFixtures.withCrew} now={FIXTURE_NOW} />)
    expect(within(container).queryByText(/does not render/i)).toBeNull()
    expect(within(container).queryByText(/upgrade crewship/i)).toBeNull()
    expect(within(container).queryByText(/later release/i)).toBeNull()
    expect(within(container).queryByText(/could not be rendered/i)).toBeNull()
  })

  it("still reads as sealed when the crew name did not arrive", () => {
    const { container } = render(<PanelRenderer {...sealedFixtures.withoutCrew} now={FIXTURE_NOW} />)
    const sealed = container.querySelector('[data-slot="panel-sealed"]')!
    expect(sealed.textContent).toMatch(/^Hidden ·/)
    expect(sealed.textContent).not.toMatch(/\bnull\b|\bundefined\b/)
  })

  it("says 'hidden' where every other panel says how fresh it is", () => {
    const { container } = render(<PanelRenderer {...sealedFixtures.withCrew} now={FIXTURE_NOW} />)
    const word = container.querySelector('[data-slot="panel-status-word"]')!
    // NOT "no data yet": there is data, and its absence here is a permission
    // decision rather than a producer that has not run.
    expect(word.textContent).toBe("hidden")
  })
})

describe("a sealed panel reveals nothing (§11b.14)", () => {
  it("renders no payload, no value, no provenance and no producer", () => {
    for (const fixture of [sealedFixtures.withCrew, sealedFixtures.withoutCrew]) {
      const { container, unmount } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      expect(container.querySelector('[data-slot="panel-value"]')).toBeNull()
      expect(container.querySelector('[data-slot="panel-provenance"]')).toBeNull()
      expect(container.querySelector('[data-slot="panel-age"]')).toBeNull()
      expect(container.querySelector("table, svg[data-slot='series-chart']")).toBeNull()
      unmount()
    }
  })

  it("offers no instruction, because there is nothing the viewer can do", () => {
    render(<PanelRenderer {...sealedFixtures.withCrew} now={FIXTURE_NOW} />)
    // §9b.3's "empty states are instructions" assumes the reader can act. Here
    // they cannot, and "push a first payload" would be advice to someone with
    // no access to the panel at all.
    expect(screen.queryByText(/crewship page set/i)).toBeNull()
    expect(screen.queryByText(/run the producer/i)).toBeNull()
  })

  it("does not borrow the em dash", () => {
    const { container } = render(<PanelRenderer {...sealedFixtures.withCrew} now={FIXTURE_NOW} />)
    // `—` means "no basis to compute" (§9b.4). This is not a missing
    // measurement — it is a measurement that exists and is not ours to read.
    expect(container.textContent).not.toContain(EM_DASH)
  })

  it("keeps its grid slot, so the page has the same shape for every viewer", () => {
    const { container } = render(<PanelRenderer {...sealedFixtures.withCrew} now={FIXTURE_NOW} />)
    // The same card idiom as every other panel (§9b.2): the caller applies the
    // span, and this renders a real panel into it rather than a hole.
    const panel = container.querySelector('[data-slot="panel"]')!
    expect(panel.getAttribute("data-panel-id")).toBe("mzdy")
    expect(container.querySelector('[data-slot="panel-label"]')).toBeTruthy()
    expect(container.querySelector('[data-slot="panel-status-word"]')).toBeTruthy()
    expect(sealedFixtures.withCrew.panel.span).toBe(6)
  })
})

describe("sealed is keyed on the flag, never on a missing field (§11b.14)", () => {
  it("routes on `sealed` before the schema is looked up at all", () => {
    // The flag wins even when the schema WOULD have resolved: a serialisation
    // that leaked a schema onto a sealed panel must not start rendering it.
    const { container } = render(
      <PanelRenderer
        panel={{ id: "mzdy", schema: "metric.v1", sealed: true, owner_crew_name: "Finance" }}
        data={{ state: "fresh", payload: { value: 999999 } }}
        now={FIXTURE_NOW}
      />,
    )
    expect(container.querySelector('[data-slot="panel-sealed"]')).toBeTruthy()
    expect(container.textContent).not.toContain("999999")
  })

  it("leaves an unsealed panel with no schema on the unknown-schema fallback", () => {
    // The inverse of the bug. A missing schema that is NOT sealed is a
    // serialisation fault and must go on reading as one — treating it as
    // sealed would hide a broken panel behind a permissions message.
    const { container } = render(
      <PanelRenderer {...sealedFixtures.unsealedWithoutSchema} now={FIXTURE_NOW} />,
    )
    expect(container.querySelector('[data-slot="panel-sealed"]')).toBeNull()
    expect(within(container).getByText(/does not render/i)).toBeInTheDocument()
    expect(resolvePanelComponent("")).toBe(UnknownSchemaPanel)
  })

  it("treats only a literal true as sealed", () => {
    // `sealed: "false"`, `sealed: 1` or a stray truthy value from a loose
    // normaliser must not seal a panel a viewer is entitled to see — that
    // would withhold data the server chose to send.
    for (const value of [undefined, null, false, 0, "", "false", "true", 1]) {
      const { container, unmount } = render(
        <PanelRenderer
          panel={{ id: "p", schema: "metric.v1", sealed: value as unknown as boolean }}
          data={{ state: "fresh", payload: { value: 42 } }}
          now={FIXTURE_NOW}
        />,
      )
      expect(container.querySelector('[data-slot="panel-sealed"]'), String(value)).toBeNull()
      expect(container.textContent).toContain("42")
      unmount()
    }
  })
})

describe("the component is exported and renders standalone", () => {
  it("renders without going through the registry", () => {
    const { container } = render(<SealedPanel {...sealedFixtures.withCrew} now={FIXTURE_NOW} />)
    expect(container.querySelector('[data-slot="panel-sealed"]')).toBeTruthy()
  })
})
