import { describe, expect, it } from "vitest"

import { buildChainGraph, type ChainGraph } from "@/lib/trace/build-chain-graph"
import { OVERVIEW_NODE_TYPES, OVERVIEW_NODE_WIDTH } from "@/lib/trace/build-overview-graph"

function chain(over: Partial<ChainGraph> = {}): ChainGraph {
  return {
    anchor: "ENG-1",
    nodes: [],
    edges: [],
    gaps: [],
    truncated: false,
    ...over,
  }
}

describe("buildChainGraph", () => {
  it("renders every node kind the walker can return", () => {
    // The walker's kinds and the canvas's node types were built by two
    // people from two bases: chain returns `assignment`, which had no
    // component, and a component existed for `automation`, which the
    // walker did not yet return. A kind with no mapping renders as
    // nothing — a silently missing node in a picture whose whole job is
    // to be complete.
    const kinds = ["issue", "routine", "run", "assignment", "agent", "inbox", "automation"] as const
    const g = buildChainGraph(
      chain({
        nodes: kinds.map((k, i) => ({
          id: `${k}:${i}`,
          kind: k,
          ref: String(i),
          label: `a ${k}`,
          depth: i,
        })),
      }),
    )
    expect(g.nodes).toHaveLength(kinds.length)
    for (const n of g.nodes) {
      expect(OVERVIEW_NODE_TYPES).toContain(n.type)
    }
  })

  it("gives every node type it emits a registered width", () => {
    // Same footgun the overview builder had: an unwidthed type lays out
    // with the wrong x-offset and nothing catches it.
    const g = buildChainGraph(
      chain({
        nodes: [
          { id: "issue:1", kind: "issue", ref: "1", label: "i", depth: 0 },
          { id: "assignment:2", kind: "assignment", ref: "2", label: "a", depth: 1 },
        ],
      }),
    )
    for (const n of g.nodes) {
      expect(OVERVIEW_NODE_WIDTH[n.type as keyof typeof OVERVIEW_NODE_WIDTH]).toBeGreaterThan(0)
    }
  })

  it("lays every node out — a node dagre never saw would stack at the origin", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          { id: "issue:1", kind: "issue", ref: "1", label: "ENG-1", depth: 0 },
          { id: "routine:2", kind: "routine", ref: "2", label: "triage", depth: 1 },
        ],
        edges: [{ from: "issue:1", to: "routine:2", kind: "triggers" }],
      }),
    )
    const at = new Set(g.nodes.map((n) => `${n.position.x},${n.position.y}`))
    expect(at.size).toBe(2)
    expect(at.has("0,0")).toBe(false)
  })

  it("drops an edge whose endpoint is not on the graph", () => {
    // The walker promises never to emit one, but a dangling edge renders
    // as a node the client failed to draw — worth refusing here too.
    const g = buildChainGraph(
      chain({
        nodes: [{ id: "issue:1", kind: "issue", ref: "1", label: "ENG-1", depth: 0 }],
        edges: [{ from: "issue:1", to: "run:99", kind: "runs" }],
      }),
    )
    expect(g.edges).toHaveLength(0)
  })

  it("marks the anchor so the reader can find where they came in", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          { id: "issue:1", kind: "issue", ref: "1", label: "ENG-1", depth: 0, anchor: true },
          { id: "routine:2", kind: "routine", ref: "2", label: "triage", depth: 1 },
        ],
      }),
    )
    expect(g.nodes.find((n) => n.id === "issue:1")?.data.isAnchor).toBe(true)
    expect(g.nodes.find((n) => n.id === "routine:2")?.data.isAnchor).toBeFalsy()
  })

  it("carries a partial node's reason through instead of hiding it", () => {
    // A node the walk could not expand is the honest edge of the picture.
    // Dropping the reason turns "we cannot see past here" into "nothing
    // is past here", which is the one lie this graph must not tell.
    const g = buildChainGraph(
      chain({
        nodes: [
          {
            id: "inbox:1",
            kind: "inbox",
            ref: "1",
            label: "approval",
            depth: 0,
            partial: true,
            partial_reason: "inbox_items carries no issue pointer",
          },
        ],
      }),
    )
    expect(g.nodes[0].data.partial).toBe(true)
    expect(g.nodes[0].data.partialReason).toContain("no issue pointer")
  })

  it("returns an empty graph for an empty chain rather than throwing", () => {
    const g = buildChainGraph(chain())
    expect(g.nodes).toEqual([])
    expect(g.edges).toEqual([])
  })

  it("maps each kind to its own card, not to the fallback", () => {
    // The fallback exists so nothing vanishes, which also means a missing
    // or wrong mapping is invisible to a test that only counts nodes. This
    // pins the pairs, so drawing an agent as a run card fails.
    const want: Record<string, string> = {
      issue: "overviewIssue",
      routine: "overviewRoutine",
      run: "overviewRun",
      agent: "overviewAgent",
      inbox: "overviewInbox",
      automation: "overviewAutomation",
    }
    const g = buildChainGraph(
      chain({
        nodes: Object.keys(want).map((k, i) => ({
          id: `${k}:${i}`,
          kind: k,
          ref: String(i),
          label: k,
          depth: i,
        })),
      }),
    )
    for (const n of g.nodes) {
      const kind = n.id.split(":")[0]
      expect(n.type, `${kind} must draw as its own card`).toBe(want[kind])
    }
  })

  it("never names a colour a literal", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          { id: "issue:1", kind: "issue", ref: "1", label: "ENG-1", depth: 0 },
          { id: "run:2", kind: "run", ref: "2", label: "run", depth: 1 },
        ],
        edges: [{ from: "issue:1", to: "run:2", kind: "runs" }],
      }),
    )
    const serialised = JSON.stringify(g.edges)
    expect(serialised).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(serialised).not.toMatch(/rgba?\(/)
  })
})

describe("buildChainGraph over a real response", () => {
  // Captured from a live instance: GET /api/v1/chains/ENG-2 with seeded
  // demo data. Hand-written fixtures agree with whatever the adapter
  // expects; only a recorded response can disagree with it.
  //
  // Refresh with:  crewship chain ENG-2 -f json > fixtures/chain-eng2.json
  it("lays out a chain the server actually returned", async () => {
    const real = (await import("./fixtures/chain-eng2.json")).default as unknown as ChainGraph

    const g = buildChainGraph(real)

    expect(g.nodes).toHaveLength(real.nodes.length)
    // Every edge survives: the walker promises both endpoints are present,
    // and if that ever stops being true the filter would silently thin the
    // picture rather than fail.
    expect(g.edges).toHaveLength(real.edges.length)

    const origin = g.nodes.filter((n) => n.position.x === 0 && n.position.y === 0)
    expect(origin.length, "nodes stacked at the origin mean dagre never saw them").toBe(0)

    const anchor = g.nodes.filter((n) => n.data.isAnchor === true)
    expect(anchor).toHaveLength(1)

    // The response carries kinds the fixtures did not: `assignment` is what
    // the mission engine produces, and it had no node component of its own.
    expect(real.nodes.some((n) => n.kind === "assignment")).toBe(true)
    for (const n of g.nodes) {
      expect(n.type, "every real node must resolve to a card").toBeTruthy()
    }
  })

  it("keeps the gaps the server declared", async () => {
    const real = (await import("./fixtures/chain-eng2.json")).default as unknown as ChainGraph
    // Not an adapter behaviour — a pin on the contract. If the walker ever
    // stops reporting gaps, the card silently starts implying completeness.
    expect(real.gaps.length).toBeGreaterThan(0)
    for (const gap of real.gaps) {
      expect(gap.reason).not.toBe("")
    }
  })
})

describe("the contract with the walker", () => {
  // internal/chain spells an automation's state into `status` as the literal
  // "enabled"/"disabled", and this adapter derives the boolean from it. That
  // is a string agreement across a language boundary with nothing checking
  // it: rename the spelling on either side and every rule silently renders
  // as enabled, which is the wrong way round for a safety-relevant flag.
  it("reads a disabled rule as disabled, not as enabled-by-default", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          {
            id: "automation:a1",
            kind: "automation",
            ref: "a1",
            key: "mission.status_change",
            label: "triage on close",
            status: "disabled",
            depth: 0,
          },
        ],
      }),
    )
    expect(g.nodes[0].data.enabled).toBe(false)
  })

  // The third spelling. `enabled` was derived as `status !== "disabled"`, so a
  // tombstone — status "deleted" — read as ENABLED: the card drew a live green
  // "on" badge for a rule that no longer exists and cannot be switched off.
  // Deriving one boolean from a three-state string is what made that possible,
  // so the tombstone gets its own field rather than a cleverer comparison.
  it("reads a deleted rule as deleted, and never as enabled", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          {
            id: "automation:a1",
            kind: "automation",
            ref: "a1",
            key: "mission.status_change",
            label: "triage on close",
            status: "deleted",
            depth: 0,
          },
        ],
      }),
    )
    expect(g.nodes[0].data.deleted).toBe(true)
    expect(g.nodes[0].data.enabled).toBe(false)
  })

  it("does not mark a live rule as deleted", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          {
            id: "automation:a1",
            kind: "automation",
            ref: "a1",
            key: "mission.status_change",
            label: "triage on close",
            status: "enabled",
            depth: 0,
          },
        ],
      }),
    )
    expect(g.nodes[0].data.deleted).toBe(false)
  })

  it("reads an enabled rule as enabled", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          {
            id: "automation:a1",
            kind: "automation",
            ref: "a1",
            key: "mission.status_change",
            label: "triage on close",
            status: "enabled",
            depth: 0,
          },
        ],
      }),
    )
    expect(g.nodes[0].data.enabled).toBe(true)
  })
})
