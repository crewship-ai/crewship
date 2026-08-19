import { describe, expect, it } from "vitest"

import { toPanelView } from "@/hooks/use-pages"

// The seam C1 fell through.
//
// `SealedPanel` existed and the registry routed to it correctly, but the flag
// never arrived: `toPanelView` put `sealed` beside the spec while
// `page-view.tsx` passes `panel={panel.spec}` to the registry. Two correct
// halves, one wire between them, and the page went on telling readers
// "This version of Crewship does not render `` panels" — the Grafana failure
// mode §2.3 exists to prevent.
//
// This test is the wire. It asserts the flag lands where the renderer looks,
// not merely that it survived the hook.
describe("a sealed panel reaches the component that must key on it", () => {
  it("carries sealed and the crew name on the SPEC, which is what the registry receives", () => {
    const view = toPanelView({ panel_id: "ucetni", span: 4, sealed: true, owner_crew_name: "Účetní" })

    expect(view.spec.sealed).toBe(true)
    expect(view.spec.owner_crew_name).toBe("Účetní")
    // The id survives from panel_id — a sealed panel carries no `id`.
    expect(view.spec.id).toBe("ucetni")
    expect(view.spec.span).toBe(4)
  })

  it("keys on sealed being true, never on the schema being absent (§11b.14)", () => {
    // A panel with no schema that is NOT sealed is a bug and must keep
    // rendering as one, so a serialisation failure can never be mistaken for
    // a permission decision.
    const broken = toPanelView({ id: "oops", span: 12 })
    expect(broken.spec.sealed).toBe(false)

    const sealed = toPanelView({ panel_id: "x", span: 12, sealed: true })
    expect(sealed.spec.sealed).toBe(true)
  })

  it("does not invent a crew name the server did not send", () => {
    const view = toPanelView({ panel_id: "x", span: 12, sealed: true })
    expect(view.spec.owner_crew_name).toBeNull()
  })
})
