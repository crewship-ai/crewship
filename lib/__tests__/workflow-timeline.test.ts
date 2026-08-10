import { describe, expect, it } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"
import type { ChainNode } from "@/lib/trace/build-chain-graph"
import {
  buildWorkflowTimeline,
  chainHeaderDuration,
  formatRowDuration,
  rowStatusToken,
  startedByPhrase,
  workflowRuns,
  type TimelineSource,
} from "@/lib/workflow-timeline"

/* ------------------------------------------------------------------ *
 *  Fixtures
 * ------------------------------------------------------------------ */

const node = (over: Partial<ChainNode> & { id: string }): ChainNode => ({
  kind: "run",
  ref: over.id,
  label: over.id,
  depth: 0,
  ...over,
})

/**
 * A REAL walk, verbatim from a server built at this branch's head and pointed
 * at a copy of the dev database, on 2026-08-10:
 *
 *   crewship chain run_cmsmv0tzx001693a3ed64 -f json
 *
 * Every field is the server's, including the ones that are ABSENT. Two runs
 * carry `occurred_at`/`ended_at`/`duration_ms`; the routine and the rule carry
 * none of the three, because internal/chain dates events and not nouns. That
 * mixture — some rows datable, most not — is the normal case this timeline has
 * to render, not an edge case, so it is the fixture.
 *
 * Note the second run happened TWO MINUTES BEFORE the anchor. The walk returns
 * it last (breadth-first from the anchor); time says it was first.
 */
const LIVE_CHAIN: TimelineSource = {
  nodes: [
    {
      id: "run:run_cmsmv0tzx001693a3ed64",
      kind: "run",
      ref: "run_cmsmv0tzx001693a3ed64",
      key: "on-close-file-followup",
      label: "on-close-file-followup",
      status: "completed",
      depth: 0,
      anchor: true,
      occurred_at: "2026-08-10T06:37:58.366620000Z",
      ended_at: "2026-08-10T06:37:58.379865000Z",
      duration_ms: 13,
    },
    {
      id: "routine:pln_cmsmuxliu0001d886758f",
      kind: "routine",
      ref: "pln_cmsmuxliu0001d886758f",
      key: "on-close-file-followup",
      label: "on-close-file-followup",
      status: "active",
      depth: 1,
    },
    {
      id: "automation:aut_18536c615e644ca4",
      kind: "automation",
      ref: "aut_18536c615e644ca4",
      key: "mission.status_change",
      label: "file a follow-up when an issue closes",
      status: "enabled",
      depth: 1,
    },
    {
      id: "run:run_cmsmuy9f30013ffeaf4f6",
      kind: "run",
      ref: "run_cmsmuy9f30013ffeaf4f6",
      key: "on-close-file-followup",
      label: "on-close-file-followup",
      status: "completed",
      depth: 2,
      occurred_at: "2026-08-10T06:35:58.384405000Z",
      ended_at: "2026-08-10T06:35:58.422991000Z",
      duration_ms: 38,
    },
  ],
  edges: [
    { from: "routine:pln_cmsmuxliu0001d886758f", to: "run:run_cmsmv0tzx001693a3ed64", kind: "runs" },
    { from: "automation:aut_18536c615e644ca4", to: "run:run_cmsmv0tzx001693a3ed64", kind: "triggers" },
    { from: "routine:pln_cmsmuxliu0001d886758f", to: "run:run_cmsmuy9f30013ffeaf4f6", kind: "runs" },
    { from: "automation:aut_18536c615e644ca4", to: "routine:pln_cmsmuxliu0001d886758f", kind: "triggers" },
    { from: "automation:aut_18536c615e644ca4", to: "run:run_cmsmuy9f30013ffeaf4f6", kind: "triggers" },
  ],
}

/**
 * The other real shape, from the same server:
 *   crewship chain run_cmsn6k87n0003f97037f3 -f json
 * A routine, the run of it, the agent assignment that run dispatched, and the
 * agent. It is the only walk on that instance carrying an `executes` edge.
 */
const LIVE_AGENT_CHAIN: TimelineSource = {
  nodes: [
    {
      id: "run:run_cmsn6k87n0003f97037f3",
      kind: "run",
      ref: "run_cmsn6k87n0003f97037f3",
      key: "dispatch-an-agent",
      label: "dispatch-an-agent",
      status: "completed",
      depth: 0,
      anchor: true,
      occurred_at: "2026-08-10T12:00:59.027790000Z",
      ended_at: "2026-08-10T12:00:59.033352000Z",
      duration_ms: 5,
    },
    {
      id: "routine:pln_cmsn6k30g00010250b1b4",
      kind: "routine",
      ref: "pln_cmsn6k30g00010250b1b4",
      key: "dispatch-an-agent",
      label: "dispatch-an-agent",
      status: "active",
      depth: 1,
    },
    {
      id: "assignment:cmsn6k87r00086df18243",
      kind: "assignment",
      ref: "cmsn6k87r00086df18243",
      label: "Summarise what changed on the board today",
      status: "COMPLETED",
      depth: 1,
      occurred_at: "2026-08-10T12:00:59.000000000Z",
      ended_at: "2026-08-10T12:01:37.000000000Z",
      duration_ms: 38000,
    },
    {
      id: "agent:cmsizloci001130145c9a",
      kind: "agent",
      ref: "cmsizloci001130145c9a",
      key: "riley",
      label: "Riley",
      status: "IDLE",
      depth: 2,
    },
  ],
  edges: [
    { from: "routine:pln_cmsn6k30g00010250b1b4", to: "run:run_cmsn6k87n0003f97037f3", kind: "runs" },
    { from: "run:run_cmsn6k87n0003f97037f3", to: "assignment:cmsn6k87r00086df18243", kind: "triggers" },
    { from: "agent:cmsizloci001130145c9a", to: "assignment:cmsn6k87r00086df18243", kind: "executes" },
  ],
}

/** One real row of GET /api/v1/chains, same instance, same day. */
const summary = (over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin: "run_cmsmv0tzx001693a3ed64",
  started_by_kind: "automation",
  started_by: "file a follow-up when an issue closes",
  triggered_via: "automation",
  routine_slug: "on-close-file-followup",
  runs: 1,
  max_chain_depth: 1,
  failed_runs: 0,
  failed: false,
  first_activity: "2026-08-10T06:37:58.366620000Z",
  last_activity: "2026-08-10T06:37:58.379865000Z",
  duration_ms: 13,
  issue_count: 1,
  issues: [
    {
      id: "cmsmv0u0600150dc1a9b0",
      identifier: "ENG-8",
      title: "Follow-up: verify cmsizlp7q009bc81acdb3 in staging",
      created: true,
    },
  ],
  agent_count: 0,
  ...over,
})

const ids = (g: TimelineSource) => buildWorkflowTimeline(g).rows.map((r) => r.id)
const indents = (g: TimelineSource) => buildWorkflowTimeline(g).rows.map((r) => r.indent)

/* ------------------------------------------------------------------ *
 *  The tree — what happened, one under the other
 * ------------------------------------------------------------------ */

describe("buildWorkflowTimeline — the causal tree", () => {
  // The whole point of indentation: a run belongs UNDER the routine that
  // defines it, and the routine under the rule that fired it. The walk gives
  // the anchor run two parents (routine via `runs`, automation via
  // `triggers`); picking the automation would flatten all three children into
  // one fan and lose the routine→run relation from the picture, while picking
  // the routine keeps the automation as an ancestor anyway, because
  // automation→routine is an edge in its own right.
  it("nests a run under its routine and the routine under the rule that fired it", () => {
    expect(ids(LIVE_CHAIN)).toEqual([
      "automation:aut_18536c615e644ca4",
      "routine:pln_cmsmuxliu0001d886758f",
      // Two minutes earlier than the anchor, and the walk returned it last.
      "run:run_cmsmuy9f30013ffeaf4f6",
      "run:run_cmsmv0tzx001693a3ed64",
    ])
    expect(indents(LIVE_CHAIN)).toEqual([0, 1, 2, 2])
  })

  it("marks the anchor so the page can say which row you came from", () => {
    const anchored = buildWorkflowTimeline(LIVE_CHAIN).rows.filter((r) => r.anchor)
    expect(anchored.map((r) => r.id)).toEqual(["run:run_cmsmv0tzx001693a3ed64"])
  })

  it("names the relation that put each row where it is", () => {
    const byId = new Map(buildWorkflowTimeline(LIVE_CHAIN).rows.map((r) => [r.id, r]))
    expect(byId.get("automation:aut_18536c615e644ca4")?.via).toBeUndefined()
    expect(byId.get("routine:pln_cmsmuxliu0001d886758f")?.via).toBe("triggers")
    expect(byId.get("run:run_cmsmv0tzx001693a3ed64")?.via).toBe("runs")
  })

  // An agent EXECUTES work it did not cause. Nesting the assignment under the
  // agent would group the timeline by actor instead of by cause, so the causal
  // parent wins and the actor rides on the row.
  it("keeps the executing agent on the row rather than nesting the work under it", () => {
    const rows = buildWorkflowTimeline(LIVE_AGENT_CHAIN).rows
    const work = rows.find((r) => r.id === "assignment:cmsn6k87r00086df18243")
    expect(work?.parentId).toBe("run:run_cmsn6k87n0003f97037f3")
    expect(work?.via).toBe("triggers")
    expect(work?.executedBy?.label).toBe("Riley")
    expect(work?.executedBy?.ref).toBe("cmsizloci001130145c9a")
  })

  // The agent has no incoming edge, so it is a root of its own. It stays on
  // the page: dropping a node from a surface whose job is completeness is a
  // worse answer than an oddly-placed one, and it is the only way to click
  // through to the agent when the assignment row is the one that names it.
  it("still lists an agent that nothing caused", () => {
    expect(ids(LIVE_AGENT_CHAIN)).toEqual([
      "routine:pln_cmsn6k30g00010250b1b4",
      "run:run_cmsn6k87n0003f97037f3",
      "assignment:cmsn6k87r00086df18243",
      "agent:cmsizloci001130145c9a",
    ])
    expect(indents(LIVE_AGENT_CHAIN)).toEqual([0, 1, 2, 0])
  })

  it("draws every node exactly once even when the edges form a cycle", () => {
    const g: TimelineSource = {
      nodes: [node({ id: "a" }), node({ id: "b" })],
      edges: [
        { from: "a", to: "b", kind: "triggers" },
        { from: "b", to: "a", kind: "triggers" },
      ],
    }
    expect(ids(g).slice().sort()).toEqual(["a", "b"])
  })

  it("ignores an edge whose endpoint is not in the response", () => {
    const g: TimelineSource = {
      nodes: [node({ id: "a" })],
      edges: [{ from: "ghost", to: "a", kind: "triggers" }],
    }
    const t = buildWorkflowTimeline(g)
    expect(t.rows.map((r) => r.id)).toEqual(["a"])
    expect(t.rows[0].indent).toBe(0)
  })
})

/* ------------------------------------------------------------------ *
 *  Time — and the honest absence of it
 * ------------------------------------------------------------------ */

describe("buildWorkflowTimeline — time, and its absence", () => {
  // Four kinds are nouns and the server dates none of them: an issue, a
  // routine, an agent, a rule. Half a real chain is therefore undatable, and
  // the count is what the page says out loud instead of implying a sequence.
  it("counts the rows the server could not date", () => {
    const t = buildWorkflowTimeline(LIVE_CHAIN)
    expect(t.timed).toBe(true)
    expect(t.untimedCount).toBe(2)
    expect(t.rows.filter((r) => !r.occurredAt).map((r) => r.kind)).toEqual([
      "automation",
      "routine",
    ])
  })

  it("gives an undated noun no duration rather than a zero", () => {
    const routine = buildWorkflowTimeline(LIVE_CHAIN).rows.find((r) => r.kind === "routine")
    expect(routine?.timing).toEqual({ state: "unknown" })
    expect(formatRowDuration(routine!.timing)).toBe("—")
    expect(formatRowDuration(routine!.timing)).not.toBe("0ms")
  })

  it("reports each real run's own duration, exactly as the server derived it", () => {
    const byId = new Map(buildWorkflowTimeline(LIVE_CHAIN).rows.map((r) => [r.id, r]))
    expect(byId.get("run:run_cmsmv0tzx001693a3ed64")?.timing).toEqual({ state: "measured", ms: 13 })
    expect(formatRowDuration(byId.get("run:run_cmsmuy9f30013ffeaf4f6")!.timing)).toBe("38ms")
  })

  // The counterpart to the rule above, and the reason it is stated as ABSENT
  // rather than falsy. chain.spanMS returns a pointer precisely so that 0 can
  // mean "both stamps parsed and the interval rounded under a millisecond";
  // treating it as missing would delete the only fact that pointer carries.
  it("honours a zero the server actually sent", () => {
    const g: TimelineSource = {
      nodes: [
        node({
          id: "a",
          status: "completed",
          occurred_at: "2026-08-10T06:37:58.366620000Z",
          ended_at: "2026-08-10T06:37:58.366620000Z",
          duration_ms: 0,
        }),
      ],
      edges: [],
    }
    const [row] = buildWorkflowTimeline(g).rows
    expect(row.timing).toEqual({ state: "measured", ms: 0 })
    expect(formatRowDuration(row.timing)).toBe("0ms")
  })

  // withSpan withholds ended_at and duration_ms together while a run is in
  // flight, so this is exactly the shape a live run arrives in.
  it("says running for a run that started, has no end, and says it is running", () => {
    const g: TimelineSource = {
      nodes: [node({ id: "a", status: "running", occurred_at: "2026-08-10T06:37:58.366620000Z" })],
      edges: [],
    }
    const [row] = buildWorkflowTimeline(g).rows
    expect(row.timing.state).toBe("running")
    expect(formatRowDuration(row.timing)).toBe("running")
  })

  it("reads an assignment's upper-case status the same way", () => {
    const g: TimelineSource = {
      nodes: [
        node({
          id: "a",
          kind: "assignment",
          status: "RUNNING",
          occurred_at: "2026-08-10T12:00:59.000000000Z",
        }),
      ],
      edges: [],
    }
    expect(buildWorkflowTimeline(g).rows[0].timing.state).toBe("running")
  })

  // "No end recorded" is not the same claim as "still going". A completed run
  // whose end never landed is unknown, not live.
  it("does not call finished work running just because its end is missing", () => {
    const g: TimelineSource = {
      nodes: [node({ id: "a", status: "completed", occurred_at: "2026-08-10T06:37:58.366620000Z" })],
      edges: [],
    }
    expect(buildWorkflowTimeline(g).rows[0].timing.state).toBe("unknown")
  })

  // The rule chainElapsedMs settled: wall clock across the chain, NOT the sum
  // of the rows' own durations. On this real chain the sum is 51ms; the two
  // runs are two minutes apart, and two minutes is how long the workflow took.
  it("spans the chain by wall clock, not by summing the rows", () => {
    const t = buildWorkflowTimeline(LIVE_CHAIN)
    const sum = t.rows.reduce((n, r) => n + (r.timing.state === "measured" ? r.timing.ms : 0), 0)
    expect(sum).toBe(51)
    // 06:35:58.384 (the earlier run starting) to 06:37:58.379 (the anchor
    // ending) — just under two minutes, against a 51ms sum.
    expect(t.elapsedMs).toBe(119_995)
  })

  it("reports no span when there is only one datable moment", () => {
    const g: TimelineSource = {
      nodes: [node({ id: "a", occurred_at: "2026-08-10T06:35:00.000Z" })],
      edges: [],
    }
    expect(buildWorkflowTimeline(g).elapsedMs).toBeNull()
  })

  it("reports no span when every moment is the same moment", () => {
    const g: TimelineSource = {
      nodes: [
        node({ id: "a", occurred_at: "2026-08-10T06:35:00.000Z", ended_at: "2026-08-10T06:35:00.000Z" }),
      ],
      edges: [],
    }
    expect(buildWorkflowTimeline(g).elapsedMs).toBeNull()
  })

  it("skips an unparseable timestamp rather than poisoning the span with NaN", () => {
    const g: TimelineSource = {
      nodes: [
        node({ id: "a", occurred_at: "not-a-date" }),
        node({ id: "b", occurred_at: "2026-08-10T06:35:00.000Z", ended_at: "2026-08-10T06:36:00.000Z" }),
      ],
      edges: [],
    }
    const t = buildWorkflowTimeline(g)
    expect(t.elapsedMs).toBe(60_000)
    expect(t.untimedCount).toBe(1)
  })
})

/* ------------------------------------------------------------------ *
 *  Sibling order — the only place time may move a row
 * ------------------------------------------------------------------ */

describe("buildWorkflowTimeline — sibling order", () => {
  const siblings = (occurred: (string | undefined)[]): TimelineSource => ({
    nodes: [
      node({ id: "root", kind: "routine" }),
      node({ id: "a", occurred_at: occurred[0] }),
      node({ id: "b", occurred_at: occurred[1] }),
      node({ id: "c", occurred_at: occurred[2] }),
    ],
    edges: [
      { from: "root", to: "a", kind: "runs" },
      { from: "root", to: "b", kind: "runs" },
      { from: "root", to: "c", kind: "runs" },
    ],
  })

  it("orders siblings by when they happened, once every one of them is dated", () => {
    const g = siblings([
      "2026-08-10T06:36:00.000Z", // a — second
      "2026-08-10T06:37:00.000Z", // b — third
      "2026-08-10T06:35:00.000Z", // c — first
    ])
    expect(ids(g)).toEqual(["root", "c", "a", "b"])
  })

  // The dishonest alternative is to sort the ones that have a time and drop
  // the rest at one end: that PLACES an undatable row, and the reader cannot
  // tell the placement was a guess. One undated sibling therefore takes the
  // whole group back to the order the walk returned.
  it("keeps the walk's order for the whole group as soon as one sibling is undated", () => {
    const g = siblings(["2026-08-10T06:36:00.000Z", undefined, "2026-08-10T06:35:00.000Z"])
    expect(ids(g)).toEqual(["root", "a", "b", "c"])
  })

  it("orders roots by the same rule", () => {
    const g: TimelineSource = {
      nodes: [
        node({ id: "late", occurred_at: "2026-08-10T06:38:00.000Z" }),
        node({ id: "early", occurred_at: "2026-08-10T06:35:00.000Z" }),
      ],
      edges: [],
    }
    expect(ids(g)).toEqual(["early", "late"])
  })
})

/* ------------------------------------------------------------------ *
 *  Header
 * ------------------------------------------------------------------ */

describe("chainHeaderDuration", () => {
  it("formats the measured span of a real row", () => {
    expect(chainHeaderDuration(summary())).toEqual({
      text: "13ms",
      note: "wall clock, first to last",
    })
  })

  // duration_ms is null when the server found no span between the chain's
  // first and last activity — which, per chainElapsedMS, is the single run
  // that has not ended yet, because last_activity falls back to started_at.
  it("renders an unmeasurable span on a healthy chain as running, never as 0", () => {
    const d = chainHeaderDuration(summary({ duration_ms: null }))
    expect(d.text).toBe("running")
    expect(d.text).not.toBe("0ms")
  })

  // It cannot be running if something in it already failed, so this refuses to
  // say so and reports the absence instead.
  it("refuses to call a failed chain running", () => {
    const d = chainHeaderDuration(summary({ duration_ms: null, failed: true, failed_runs: 1 }))
    expect(d.text).toBe("—")
    expect(d.note).toBe("no span recorded")
  })
})

describe("startedByPhrase", () => {
  it("names a rule as the rule it is", () => {
    expect(startedByPhrase(summary())).toBe("Rule · file a follow-up when an issue closes")
  })

  it("names a person", () => {
    expect(startedByPhrase(summary({ started_by_kind: "user", started_by: "Demo User" }))).toBe(
      "Person · Demo User",
    )
  })

  // The server sends this kind deliberately: the trigger was not recorded.
  it("does not pretend to know an unknown starter", () => {
    expect(startedByPhrase(summary({ started_by_kind: "unknown", started_by: "" }))).toBe(
      "Unrecorded",
    )
  })
})

describe("rowStatusToken", () => {
  it("uses the same vocabulary the rail already speaks", () => {
    expect(rowStatusToken("completed")).toBe("--success")
    expect(rowStatusToken("FAILED")).toBe("--destructive")
    expect(rowStatusToken("running")).toBe("--primary")
    expect(rowStatusToken("waiting")).toBe("--warn")
  })

  it("falls back to a neutral token rather than inventing a colour", () => {
    // "IDLE" is what a real agent node carries, and it is not a run status.
    expect(rowStatusToken("IDLE")).toBe("--muted-foreground-soft")
    expect(rowStatusToken(undefined)).toBe("--muted-foreground-soft")
  })
})

/* ------------------------------------------------------------------ *
 *  workflowRuns — the Runs card
 * ------------------------------------------------------------------ */

describe("workflowRuns", () => {
  const graph = (nodes: ChainNode[]): TimelineSource => ({ nodes, edges: [] })

  it("keeps only run nodes — a routine and an agent are not runs", () => {
    const rows = workflowRuns(
      graph([
        node({ id: "run_a", kind: "run", occurred_at: "2026-08-10T10:00:00.000000000Z" }),
        node({ id: "pln_1", kind: "routine" }),
        node({ id: "agt_1", kind: "agent" }),
      ]),
    )
    expect(rows.map((r) => r.id)).toEqual(["run_a"])
  })

  it("orders oldest first — this is a sequence, not a recency list", () => {
    const rows = workflowRuns(
      graph([
        node({ id: "run_late", occurred_at: "2026-08-10T10:00:05.000000000Z" }),
        node({ id: "run_early", occurred_at: "2026-08-10T10:00:01.000000000Z" }),
      ]),
    )
    expect(rows.map((r) => r.id)).toEqual(["run_early", "run_late"])
  })

  it("sorts an undated run last, not first", () => {
    // Absent is the least certain row on the card, and the top is where the
    // reader looks first. A missing stamp sorting to the top is the same class
    // of bug as a zero timestamp rendering as 1970.
    const rows = workflowRuns(
      graph([
        node({ id: "run_undated" }),
        node({ id: "run_dated", occurred_at: "2026-08-10T10:00:01.000000000Z" }),
      ]),
    )
    expect(rows.map((r) => r.id)).toEqual(["run_dated", "run_undated"])
  })

  it("is deterministic when two runs share an instant", () => {
    const rows = workflowRuns(
      graph([
        node({ id: "run_b", occurred_at: "2026-08-10T10:00:01.000000000Z" }),
        node({ id: "run_a", occurred_at: "2026-08-10T10:00:01.000000000Z" }),
      ]),
    )
    expect(rows.map((r) => r.id)).toEqual(["run_a", "run_b"])
  })

  it("drops a node with no ref rather than rendering a link to nowhere", () => {
    expect(workflowRuns(graph([{ kind: "run", ref: "", label: "x", depth: 0 } as ChainNode]))).toEqual([])
  })

  it("carries the status and the timing the row renders", () => {
    const [row] = workflowRuns(
      graph([
        node({
          id: "run_a",
          status: "failed",
          occurred_at: "2026-08-10T10:00:01.000000000Z",
          duration_ms: 38,
        }),
      ]),
    )
    expect(row.status).toBe("failed")
    expect(formatRowDuration(row.timing)).toBe("38ms")
  })
})

describe("workflowRuns — the chain's runs, not the routine's", () => {
  const graph = (nodes: ChainNode[], edges: { from: string; to: string; kind: string }[], anchor?: string) =>
    ({ nodes, edges, anchor_node: anchor }) as unknown as TimelineSource

  it("drops the routine's OTHER runs, which the walk reaches through it", () => {
    // The walk goes run → its routine → every run of that routine. So a chain
    // with ONE run comes back carrying eight, and a card built from the node
    // list said "8" beside a header saying "1 run". Two numbers on one page
    // describing different things.
    const rows = workflowRuns(
      graph(
        [
          node({ id: "run_mine", occurred_at: "2026-08-10T10:00:00.000000000Z" }),
          node({ id: "run_sibling_a", occurred_at: "2026-08-10T09:00:00.000000000Z" }),
          node({ id: "run_sibling_b", occurred_at: "2026-08-10T08:00:00.000000000Z" }),
          node({ id: "pln_1", kind: "routine" }),
        ],
        [
          { from: "routine:pln_1", to: "run:run_mine", kind: "runs" },
          { from: "routine:pln_1", to: "run:run_sibling_a", kind: "runs" },
          { from: "routine:pln_1", to: "run:run_sibling_b", kind: "runs" },
        ],
        "run:run_mine",
      ),
    )
    expect(rows.map((r) => r.id)).toEqual(["run_mine"])
  })

  it("keeps a run this chain actually caused", () => {
    // A composed chain: the anchor triggered a second run. That edge is not a
    // `runs` edge, so the run is a member rather than a sibling.
    const rows = workflowRuns(
      graph(
        [
          node({ id: "run_root", occurred_at: "2026-08-10T10:00:00.000000000Z" }),
          node({ id: "run_child", occurred_at: "2026-08-10T10:00:01.000000000Z" }),
          node({ id: "run_sibling", occurred_at: "2026-08-10T09:00:00.000000000Z" }),
          node({ id: "pln_1", kind: "routine" }),
        ],
        [
          { from: "routine:pln_1", to: "run:run_root", kind: "runs" },
          { from: "routine:pln_1", to: "run:run_sibling", kind: "runs" },
          { from: "run:run_root", to: "run:run_child", kind: "triggers" },
        ],
        "run:run_root",
      ),
    )
    expect(rows.map((r) => r.id)).toEqual(["run_root", "run_child"])
  })

  it("keeps every run when the walk names no anchor", () => {
    // Older server, or a caller handing over a graph-shaped object without the
    // field. Dropping rows on a guess would hide real runs; showing them all is
    // what this card did before and is the safe direction to be wrong in.
    const rows = workflowRuns(
      graph(
        [node({ id: "run_a" }), node({ id: "run_b" })],
        [
          { from: "routine:pln_1", to: "run:run_a", kind: "runs" },
          { from: "routine:pln_1", to: "run:run_b", kind: "runs" },
        ],
      ),
    )
    expect(rows).toHaveLength(2)
  })

  it("keeps the anchor even when it is only reachable as a sibling", () => {
    // The anchor is reached by the same `runs` edge as its siblings — that is
    // how the walk renders it — so it must be kept by identity, not by edge.
    const rows = workflowRuns(
      graph(
        [node({ id: "run_mine" })],
        [{ from: "routine:pln_1", to: "run:run_mine", kind: "runs" }],
        "run:run_mine",
      ),
    )
    expect(rows.map((r) => r.id)).toEqual(["run_mine"])
  })
})
