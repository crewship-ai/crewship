import { describe, expect, it } from "vitest"

import {
  ACTIVITY_LENSES,
  agentLens,
  bucketChains,
  chainScopeCounts,
  chainStatus,
  chainsInScope,
  isComposed,
  issueLens,
  matchesQuery,
  routineLens,
  workflowHandle,
  workflowName,
  workflowSentence,
} from "@/lib/activity-lenses"
import type { ChainSummary } from "@/hooks/use-chains"

const chain = (over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin: "run_cmsj1i72g000134a24f6e",
  started_by_kind: "automation",
  started_by: "on issue closed",
  runs: 1,
  max_chain_depth: 0,
  failed_runs: 0,
  running_runs: 0,
  waiting_runs: 0,
  failed: false,
  first_activity: "2026-08-10T10:00:00.000000000Z",
  last_activity: "2026-08-10T10:00:01.000000000Z",
  duration_ms: 1000,
  issue_count: 0,
  agent_count: 0,
  ...over,
})

// 2026-08-10 12:00 UTC — "now" for every bucketing assertion.
const NOW = Date.parse("2026-08-10T12:00:00.000Z")

describe("workflowName", () => {
  it("prefers the routine's human name over its slug", () => {
    expect(workflowName(chain({ routine_slug: "on-close-file-followup" }), "Follow up on close")).toBe(
      "Follow up on close",
    )
  })

  it("falls back to the slug when the routine is not in the loaded list", () => {
    // Reachable: the rail's pipeline list and the chain index are two
    // independent fetches, and a routine deleted since the run still has runs.
    expect(workflowName(chain({ routine_slug: "on-close-file-followup" }))).toBe("on-close-file-followup")
  })

  it("falls back to what started it when no routine ran", () => {
    // The agent-rooted chain: no routine at all, so the cause IS the name.
    expect(workflowName(chain({ routine_slug: undefined, started_by: "Ada" }))).toBe("Ada")
  })

  it("never renders empty", () => {
    // started_by is "" on a chain whose root run was swept by retention.
    expect(workflowName(chain({ routine_slug: undefined, started_by: "" }))).toBe("Workflow")
  })
})

describe("workflowHandle", () => {
  it("is the tail of the origin, without the type prefix", () => {
    expect(workflowHandle("run_cmsj1i72g000134a24f6e")).toBe("34a24f6e")
  })

  it("survives an id with no prefix and an id shorter than the handle", () => {
    expect(workflowHandle("abc")).toBe("abc")
    expect(workflowHandle("")).toBe("")
  })

  it("distinguishes two runs of one routine", () => {
    // The whole point: the name is the same twice, the handle is not.
    expect(workflowHandle("run_aaaaaaaa11112222")).not.toBe(workflowHandle("run_aaaaaaaa11113333"))
  })
})

describe("chainStatus", () => {
  it("puts waiting above everything — it is the only one a person can act on", () => {
    expect(chainStatus(chain({ waiting_runs: 1, running_runs: 2, failed_runs: 3, failed: true }))).toBe("waiting")
  })

  it("reports failed over running: something already went wrong", () => {
    expect(chainStatus(chain({ running_runs: 1, failed_runs: 1, failed: true }))).toBe("failed")
  })

  it("reports running when nothing broke and nothing waits", () => {
    expect(chainStatus(chain({ running_runs: 1 }))).toBe("running")
  })

  it("is done otherwise", () => {
    expect(chainStatus(chain())).toBe("done")
  })

  it("still reports failed when the server sent no live counts", () => {
    // Older server, or a field dropped on the wire: `failed` is the flag that
    // predates the counts, so it must keep working on its own.
    const legacy = chain({ failed: true, failed_runs: 2 })
    delete (legacy as Partial<ChainSummary>).running_runs
    delete (legacy as Partial<ChainSummary>).waiting_runs
    expect(chainStatus(legacy)).toBe("failed")
  })
})

describe("bucketChains", () => {
  it("puts anything non-terminal under Active now whatever its clock says", () => {
    // A run that started three days ago and is STILL waiting on a human is the
    // most urgent row on the page. Bucketing it by last_activity would file it
    // under Earlier, which is where nobody looks.
    const old = chain({ waiting_runs: 1, last_activity: "2026-08-06T10:00:00.000000000Z" })
    const buckets = bucketChains([old], NOW)
    expect(buckets.find((b) => b.key === "active")?.chains).toEqual([old])
  })

  it("separates today from earlier on the reader's calendar day, not on 24 hours", () => {
    // Anchored to LOCAL midnight, not to a UTC string: the boundary is the
    // reader's own day, so a fixture spelled in UTC lands on the wrong side of
    // it in any zone but one. An hour after local midnight is today; an hour
    // before it is yesterday — even though the two are two hours apart and a
    // rolling 24-hour window would call them the same.
    const midnight = new Date(NOW)
    midnight.setHours(0, 0, 0, 0)
    const at = (offsetMs: number) => new Date(midnight.getTime() + offsetMs).toISOString()

    const thisMorning = chain({ origin: "a", last_activity: at(60 * 60 * 1000) })
    const lateYesterday = chain({ origin: "b", last_activity: at(-60 * 60 * 1000) })
    const buckets = bucketChains([thisMorning, lateYesterday], NOW)
    expect(buckets.find((b) => b.key === "today")?.chains.map((c) => c.origin)).toEqual(["a"])
    expect(buckets.find((b) => b.key === "earlier")?.chains.map((c) => c.origin)).toEqual(["b"])
  })

  it("drops empty buckets rather than rendering a header over nothing", () => {
    expect(bucketChains([chain()], NOW).map((b) => b.key)).toEqual(["today"])
  })

  it("keeps an unparseable timestamp visible under Earlier", () => {
    // Absent is not a reason to disappear a row: the chain is real, only its
    // clock is unreadable, and a silently dropped row reads as "it never ran".
    const buckets = bucketChains([chain({ last_activity: "not a date" })], NOW)
    expect(buckets.find((b) => b.key === "earlier")?.chains).toHaveLength(1)
  })

  it("preserves the server's order inside a bucket", () => {
    const a = chain({ origin: "a", last_activity: "2026-08-10T11:00:00.000000000Z" })
    const b = chain({ origin: "b", last_activity: "2026-08-10T09:00:00.000000000Z" })
    expect(bucketChains([a, b], NOW)[0].chains.map((c) => c.origin)).toEqual(["a", "b"])
  })
})

describe("matchesQuery", () => {
  const c = chain({
    routine_slug: "on-close-file-followup",
    started_by: "on issue closed",
    issues: [{ id: "m1", identifier: "ENG-7", title: "Normalize dates" }],
    issue_count: 1,
    agents: [{ id: "ag1", name: "Riley", slug: "riley", assignments: 3 }],
    agent_count: 1,
  })

  it("matches the routine name the row actually renders", () => {
    expect(matchesQuery(c, "follow", "Follow up on close")).toBe(true)
  })

  it("matches the slug, the cause, an issue identifier and an agent name", () => {
    expect(matchesQuery(c, "close-file")).toBe(true)
    expect(matchesQuery(c, "issue closed")).toBe(true)
    expect(matchesQuery(c, "eng-7")).toBe(true)
    expect(matchesQuery(c, "riley")).toBe(true)
  })

  it("matches the handle, which is how two runs of one routine are told apart", () => {
    expect(matchesQuery(c, workflowHandle(c.origin))).toBe(true)
  })

  it("does not match an unrelated term", () => {
    expect(matchesQuery(c, "kubernetes")).toBe(false)
  })

  it("treats a blank query as no query", () => {
    expect(matchesQuery(c, "   ")).toBe(true)
  })
})

describe("issueLens", () => {
  it("collapses one issue touched by two chains into one row", () => {
    const rows = issueLens([
      chain({ origin: "a", issues: [{ id: "m1", identifier: "ENG-7", title: "T" }], issue_count: 1 }),
      chain({ origin: "b", issues: [{ id: "m1", identifier: "ENG-7", title: "T" }], issue_count: 1 }),
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0].chains).toEqual(["a", "b"])
  })

  it("carries created through from any chain that authored it", () => {
    const rows = issueLens([
      chain({ origin: "a", issues: [{ id: "m1", identifier: "ENG-7", created: false }], issue_count: 1 }),
      chain({ origin: "b", issues: [{ id: "m1", identifier: "ENG-7", created: true }], issue_count: 1 }),
    ])
    expect(rows[0].created).toBe(true)
  })

  it("orders by how many chains touched it, then by identifier", () => {
    const rows = issueLens([
      chain({ origin: "a", issues: [{ id: "m1", identifier: "ENG-1" }, { id: "m2", identifier: "ENG-2" }], issue_count: 2 }),
      chain({ origin: "b", issues: [{ id: "m2", identifier: "ENG-2" }], issue_count: 1 }),
    ])
    expect(rows.map((r) => r.identifier)).toEqual(["ENG-2", "ENG-1"])
  })
})

describe("agentLens", () => {
  it("sums the assignments an agent took across chains", () => {
    const rows = agentLens([
      chain({ origin: "a", agents: [{ id: "ag1", name: "Riley", assignments: 3 }], agent_count: 1 }),
      chain({ origin: "b", agents: [{ id: "ag1", name: "Riley", assignments: 2 }], agent_count: 1 }),
    ])
    expect(rows[0].assignments).toBe(5)
    expect(rows[0].chains).toEqual(["a", "b"])
  })

  it("orders by work taken, busiest first", () => {
    const rows = agentLens([
      chain({
        agents: [
          { id: "ag1", name: "Riley", assignments: 1 },
          { id: "ag2", name: "Ada", assignments: 9 },
        ],
        agent_count: 2,
      }),
    ])
    expect(rows.map((r) => r.name)).toEqual(["Ada", "Riley"])
  })
})

describe("routineLens", () => {
  it("groups chains by the routine that ran, counting runs not chains", () => {
    const rows = routineLens([
      chain({ origin: "a", routine_slug: "sweep", runs: 3 }),
      chain({ origin: "b", routine_slug: "sweep", runs: 2 }),
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0].runs).toBe(5)
    expect(rows[0].chains).toEqual(["a", "b"])
  })

  it("omits chains that no routine ran, rather than inventing a row for them", () => {
    // The agent-rooted chain belongs in the Agents lens; giving it a blank
    // routine row would put an unnamed entry in a catalogue of names.
    expect(routineLens([chain({ routine_slug: undefined })])).toEqual([])
  })

  it("flags a routine as failing when any of its chains failed", () => {
    const rows = routineLens([
      chain({ origin: "a", routine_slug: "sweep" }),
      chain({ origin: "b", routine_slug: "sweep", failed: true, failed_runs: 1 }),
    ])
    expect(rows[0].failed).toBe(true)
  })
})

describe("ACTIVITY_LENSES", () => {
  it("leads with workflows — the causal run is the unit the page is about", () => {
    expect(ACTIVITY_LENSES[0].key).toBe("workflows")
  })

  it("names every lens the rail can render", () => {
    expect(ACTIVITY_LENSES.map((l) => l.key)).toEqual(["workflows", "issues", "agents", "routines"])
  })
})

describe("chainScopeCounts / chainsInScope", () => {
  const set = () => [
    chain({ origin: "w", waiting_runs: 1 }),
    chain({ origin: "r", running_runs: 1 }),
    chain({ origin: "f", failed: true, failed_runs: 1 }),
    chain({ origin: "d" }),
  ]

  it("counts each chain exactly once, under its one status", () => {
    expect(chainScopeCounts(set())).toEqual({ active: 1, waiting: 1, failed: 1, done: 1 })
  })

  it("maps running onto the page's 'active' scope word", () => {
    expect(chainScopeCounts([chain({ running_runs: 2 })]).active).toBe(1)
  })

  it("narrows the list to the picked status", () => {
    expect(chainsInScope(set(), "waiting").map((c) => c.origin)).toEqual(["w"])
    expect(chainsInScope(set(), "active").map((c) => c.origin)).toEqual(["r"])
    expect(chainsInScope(set(), "failed").map((c) => c.origin)).toEqual(["f"])
    expect(chainsInScope(set(), "done").map((c) => c.origin)).toEqual(["d"])
  })

  it("keeps everything under 'all', including what finished cleanly", () => {
    expect(chainsInScope(set(), "all")).toHaveLength(4)
  })

  it("agrees with the segment counts it sits beside", () => {
    // The bug this pairing exists to prevent: a count above a list that
    // describes a different set. Whatever a segment claims, the filter must
    // produce exactly that many rows.
    const chains = set()
    const counts = chainScopeCounts(chains)
    for (const scope of ["active", "waiting", "failed", "done"] as const) {
      expect(chainsInScope(chains, scope)).toHaveLength(counts[scope])
    }
  })
})

describe("isComposed — what earns the word workflow", () => {
  it("is false for one manual run that touched nothing", () => {
    // 12 of 21 rows on the live instance were this: `crewship routine run X`,
    // one run, depth 0, no issue, no agent. Nothing was composed, so calling it
    // a workflow makes the word mean "a run" — and then the Workflows lens is
    // the Routines lens with worse naming.
    expect(isComposed(chain({ runs: 1, max_chain_depth: 0 }))).toBe(false)
  })

  it("is true when something caused something else", () => {
    expect(isComposed(chain({ max_chain_depth: 1 }))).toBe(true)
    expect(isComposed(chain({ runs: 2 }))).toBe(true)
  })

  it("is true when it put an agent to work", () => {
    expect(isComposed(chain({ agent_count: 1 }))).toBe(true)
  })

  it("is true when it reached an issue", () => {
    expect(isComposed(chain({ issue_count: 1 }))).toBe(true)
  })

  it("is true for a failed single run — a failure crosses into what a person does", () => {
    // The one exception to "one run is not a workflow". A run that broke is the
    // reason somebody opened this page, and filing it away under its routine
    // would hide the thing the rail exists to surface.
    expect(isComposed(chain({ runs: 1, failed: true, failed_runs: 1 }))).toBe(true)
  })

  it("is true for a run still going or still asking", () => {
    expect(isComposed(chain({ runs: 1, running_runs: 1 }))).toBe(true)
    expect(isComposed(chain({ runs: 1, waiting_runs: 1 }))).toBe(true)
  })
})

describe("workflowName — the sentence that is not the routine's name", () => {
  it("names what set it off and what it reached", () => {
    const c = chain({
      routine_slug: "on-close-file-followup",
      started_by: "file a follow-up when an issue closes",
      started_by_kind: "automation",
      issues: [{ id: "m1", identifier: "ENG-7" }],
      issue_count: 1,
      agents: [{ id: "a1", name: "riley", assignments: 1 }],
      agent_count: 1,
    })
    expect(workflowSentence(c, "Follow up on close")).toBe(
      "file a follow-up when an issue closes → Follow up on close → riley → ENG-7",
    )
  })

  it("falls back to the routine alone when nothing else is known", () => {
    expect(workflowSentence(chain({ routine_slug: "sweep", started_by: "" }), "Sweep")).toBe("Sweep")
  })

  it("does not repeat the cause when it IS the routine", () => {
    // A manual run's cause is a person, not a rule; naming "Demo User → Sweep"
    // adds nothing a reader is looking for in a list of processes.
    const c = chain({ routine_slug: "sweep", started_by: "Demo User", started_by_kind: "user" })
    expect(workflowSentence(c, "Sweep")).toBe("Sweep")
  })

  it("elides a long reach rather than printing every noun", () => {
    const c = chain({
      routine_slug: "sweep",
      started_by_kind: "schedule",
      started_by: "nightly",
      issues: [
        { id: "1", identifier: "ENG-1" },
        { id: "2", identifier: "ENG-2" },
        { id: "3", identifier: "ENG-3" },
      ],
      issue_count: 9,
    })
    expect(workflowSentence(c, "Sweep")).toContain("+6")
  })
})
