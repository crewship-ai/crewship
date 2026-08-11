/**
 * The card's frame is derived from the chain, not fixed.
 *
 * It used to be `h-[380px]` for everything. A two-node chain is 64px tall,
 * so it sat in a sixth of the box with a band of dot background above and
 * below it, while a branching chain was still clipped sideways. Only the
 * layout knows how big the picture is, and this is the arithmetic that turns
 * that into a frame.
 */

import { describe, expect, it } from "vitest"

import { canvasHeightFor } from "../topology-card"
import { buildChainGraph, CHAIN_FIT_PADDING, type ChainGraph } from "@/lib/trace/build-chain-graph"

describe("canvasHeightFor", () => {
  it("gives a single-rank chain far less than the old fixed 380", () => {
    // The complaint, in one number: one routine and one run is 64px of
    // picture, and it was given 380px of card.
    expect(canvasHeightFor(64)).toBeLessThan(380)
  })

  it("grows with the chain", () => {
    expect(canvasHeightFor(240)).toBeGreaterThan(canvasHeightFor(120))
  })

  it("leaves room for the inset fitView will take out", () => {
    // Sized to the graph exactly, fitView's padding would shrink the graph
    // to fit the padding — the frame has to be bigger by that fraction.
    const graph = 200
    expect(canvasHeightFor(graph)).toBeGreaterThan(graph)
    expect(canvasHeightFor(graph)).toBe(Math.round(graph / (1 - 2 * CHAIN_FIT_PADDING)))
  })

  it("clamps a deep chain rather than pushing the page below the fold", () => {
    expect(canvasHeightFor(5000)).toBeLessThanOrEqual(420)
  })

  it("survives a graph that reported no size", () => {
    // bounds is {0,0} for an empty chain, and a frame of 0 renders nothing
    // at all — including the reason it rendered nothing.
    for (const bad of [0, -10, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(canvasHeightFor(bad)).toBeGreaterThan(0)
    }
  })

  it("sizes a real branching chain smaller than the fixed box it replaced", () => {
    // Shaped like the card in the report: one rule, one routine, two runs.
    const chain: ChainGraph = {
      anchor: "run:a",
      nodes: [
        { id: "automation:x", kind: "automation", ref: "x", label: "on close", depth: 0 },
        { id: "routine:r", kind: "routine", ref: "r", label: "on-close-file-followup", depth: 1 },
        { id: "run:a", kind: "run", ref: "a", label: "run a", depth: 2 },
        { id: "run:b", kind: "run", ref: "b", label: "run b", depth: 2 },
      ],
      edges: [
        { from: "automation:x", to: "routine:r", kind: "triggers" },
        { from: "routine:r", to: "run:a", kind: "runs" },
        { from: "routine:r", to: "run:b", kind: "runs" },
      ],
      gaps: [],
      truncated: false,
    }
    const h = canvasHeightFor(buildChainGraph(chain).bounds.height)
    expect(h).toBeGreaterThan(0)
    expect(h).toBeLessThan(380)
  })
})
