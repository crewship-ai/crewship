// A row of four zeros says "nothing here" four times at 32px. When every
// value a strip can show is zero, the overview collapses it to one
// InlineEmpty line; one real number keeps the tiles.

import { describe, it, expect } from "vitest"
import { kpiStripIsEmpty } from "../kpi-strip"

describe("kpiStripIsEmpty", () => {
  it.each([
    [[0, 0, 0, 0], true],
    [[0, 0, 1, 0], false],
    [[3], false],
    // A tile the reader may not see (null) is not a reason to keep the strip…
    [[0, null, null, null], true],
    // …but a strip of nothing but hidden tiles says nothing either way.
    [[null, null], false],
    [[], false],
  ] as const)("%j → %s", (values, empty) => {
    expect(kpiStripIsEmpty(values)).toBe(empty)
  })
})
