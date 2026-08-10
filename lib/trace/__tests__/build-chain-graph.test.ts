import { describe, expect, it } from "vitest"

import { buildChainGraph, CHAIN_FIT_PADDING, type ChainGraph } from "@/lib/trace/build-chain-graph"
import {
  OVERVIEW_NODE_HEIGHT,
  OVERVIEW_NODE_TYPES,
  OVERVIEW_NODE_WIDTH,
} from "@/lib/trace/build-overview-graph"

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

describe("the laid-out extent", () => {
  // The card used to be a fixed 380px box whatever it held, so a two-node
  // chain sat in a third of the height it was given and the rest was dot
  // background. The builder is the only thing that knows how tall the
  // picture actually is; reporting it is what lets the card be that tall.
  function ranked(runs: number): ChainGraph {
    return chain({
      nodes: [
        { id: "routine:r", kind: "routine", ref: "r", label: "triage", depth: 0 },
        ...Array.from({ length: runs }, (_, i) => ({
          id: `run:${i}`,
          kind: "run",
          ref: String(i),
          label: `run ${i}`,
          depth: 1,
        })),
      ],
      edges: Array.from({ length: runs }, (_, i) => ({
        from: "routine:r",
        to: `run:${i}`,
        kind: "runs",
      })),
    })
  }

  it("measures the box the nodes actually occupy", () => {
    const g = buildChainGraph(ranked(2))
    const widthOf = (t: unknown) => OVERVIEW_NODE_WIDTH[t as keyof typeof OVERVIEW_NODE_WIDTH]
    const left = Math.min(...g.nodes.map((n) => n.position.x))
    const right = Math.max(...g.nodes.map((n) => n.position.x + widthOf(n.type)))
    const top = Math.min(...g.nodes.map((n) => n.position.y))
    const bottom = Math.max(...g.nodes.map((n) => n.position.y + OVERVIEW_NODE_HEIGHT))
    expect(g.bounds.width).toBe(right - left)
    expect(g.bounds.height).toBe(bottom - top)
  })

  it("grows when the chain branches — two runs are taller than one", () => {
    // A height that ignores the shape is the fixed 380 by another name.
    expect(buildChainGraph(ranked(2)).bounds.height).toBeGreaterThan(
      buildChainGraph(ranked(1)).bounds.height,
    )
  })

  it("is zero for an empty chain, not NaN or -Infinity", () => {
    // Math.min of nothing is Infinity, and a card sized from that renders
    // nothing at all. The empty case has to be spelled, not derived.
    expect(buildChainGraph(chain()).bounds).toEqual({ width: 0, height: 0 })
  })

  it("publishes the fit padding as a number a caller can size against", () => {
    // The card sizes its frame from the graph, and fitView then insets by
    // this fraction on each side. Two copies of it drift, and the drift
    // shows as either a clipped node or the dead space it replaced.
    expect(CHAIN_FIT_PADDING).toBeGreaterThan(0)
    expect(CHAIN_FIT_PADDING).toBeLessThan(0.5)
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

describe("buildChainGraph — when a node happened", () => {
  // The run card has always accepted a startedAt and this adapter has always
  // passed "". The walk carries the answer now (occurred_at), and a card that
  // keeps hard-coding the empty string is a timeline with no time on it.
  it("passes a run's occurred_at through to the card", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          {
            id: "run:r1",
            kind: "run",
            ref: "r1",
            key: "deploy",
            label: "deploy",
            status: "completed",
            depth: 0,
            occurred_at: "2026-08-07T09:41:02.000000000Z",
            ended_at: "2026-08-07T09:42:32.000000000Z",
            duration_ms: 90000,
          },
        ],
      }),
    )
    expect(g.nodes[0].data.startedAt).toBe("2026-08-07T09:41:02.000000000Z")
    expect(g.nodes[0].data.endedAt).toBe("2026-08-07T09:42:32.000000000Z")
    expect(g.nodes[0].data.durationMs).toBe(90000)
  })

  it("passes an assignment's occurred_at through the card it borrows", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          {
            id: "assignment:a1",
            kind: "assignment",
            ref: "a1",
            label: "summarise the thread",
            status: "COMPLETED",
            depth: 1,
            occurred_at: "2026-08-07T10:00:00.000000000Z",
            ended_at: "2026-08-07T10:00:45.000000000Z",
            duration_ms: 45000,
          },
        ],
      }),
    )
    expect(g.nodes[0].data.startedAt).toBe("2026-08-07T10:00:00.000000000Z")
    expect(g.nodes[0].data.durationMs).toBe(45000)
  })

  // The negative, which is the whole point of the server returning nothing for
  // a kind that cannot answer. If absent became 0 or "" -> Date(0) anywhere on
  // the way to a card, every noun in the graph stacks up on 1 January 1970.
  it("leaves a kind that carries no time undated rather than dating it at zero", () => {
    const g = buildChainGraph(
      chain({
        nodes: [
          { id: "issue:m1", kind: "issue", ref: "m1", key: "ENG-1", label: "an issue", depth: 0 },
          { id: "routine:p1", kind: "routine", ref: "p1", key: "triage", label: "triage", depth: 1 },
          { id: "agent:ag1", kind: "agent", ref: "ag1", key: "ada", label: "Ada", depth: 1 },
          {
            id: "automation:a1",
            kind: "automation",
            ref: "a1",
            key: "run.failed",
            label: "rule",
            status: "enabled",
            depth: 1,
          },
          // A run that has not finished: it has a beginning and no end.
          {
            id: "run:r1",
            kind: "run",
            ref: "r1",
            key: "deploy",
            label: "deploy",
            status: "running",
            depth: 1,
            occurred_at: "2026-08-07T09:41:02.000000000Z",
          },
        ],
      }),
    )
    for (const n of g.nodes) {
      expect(n.data.durationMs, `${n.id} duration`).toBeUndefined()
      if (n.id !== "run:r1") {
        expect(n.data.startedAt ?? "", `${n.id} startedAt`).toBe("")
      }
      expect(n.data.endedAt ?? "", `${n.id} endedAt`).toBe("")
    }
  })
})
