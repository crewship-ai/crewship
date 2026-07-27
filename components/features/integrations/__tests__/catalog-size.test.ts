import { describe, expect, it } from "vitest"

import { CATALOG_EXTRA_ENTRIES, catalogSize } from "../views/catalog-view"

/**
 * The sub-bar, the Catalog tab badge and the Catalog heading all state how
 * many services exist. They must state the SAME number.
 *
 * They did not, twice: the bar counted the shoutrrr provider registry (11)
 * while the catalog listed 11 providers plus e-mail, webhook and the managed
 * tools card (14). Two contradicting counts a centimetre apart is how a page
 * stops being believed. This pins the arithmetic so the next entry added to
 * the catalog has to update the constant, not just the JSX.
 */
describe("catalogSize", () => {
  it("counts the built-in transports and the tools card on top of the registry", () => {
    expect(catalogSize(11)).toBe(11 + CATALOG_EXTRA_ENTRIES)
    expect(catalogSize(11)).toBe(14)
  })

  it("still reports the extras when the registry is empty", () => {
    // A server with every provider disabled still offers e-mail and webhook,
    // so "0 services available" would be wrong.
    expect(catalogSize(0)).toBe(CATALOG_EXTRA_ENTRIES)
  })
})
