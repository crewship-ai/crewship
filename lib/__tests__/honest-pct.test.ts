import { describe, it, expect } from "vitest"
import { honestPct } from "../honest-pct"

describe("honestPct", () => {
  it.each([
    [199, 200, 99],
    [200, 200, 100],
    [1, 300, 1],
    [0, 5, 0],
    [3, 4, 75],
    [0, 0, null],
  ])("%i of %i is %s", (ok, total, want) => {
    expect(honestPct(ok, total)).toBe(want)
  })
})
