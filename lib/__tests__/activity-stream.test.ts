import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"

import {
  ACTIVITY_SOURCES,
  activitySource,
  buildSpine,
  formatDurationMs,
  groupIntoBuckets,
  severityTone,
  sourceEntryTypes,
  scopeOf,
  sourceMix,
  dailyCounts,
  NOISE_ENTRY_TYPES,
  runIdOf,
  timeBucket,
} from "@/lib/activity-stream"
import { JOURNAL_ENTRY_TYPES, type JournalEntry } from "@/lib/types/journal"

function entry(over: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "j1",
    workspace_id: "ws1",
    ts: "2026-08-07T12:00:00Z",
    entry_type: "run.completed",
    severity: "info",
    actor_type: "agent",
    summary: "done",
    ...over,
  } as JournalEntry
}

describe("activitySource", () => {
  it("routes each family to its own source", () => {
    expect(activitySource("run.failed")).toBe("run")
    expect(activitySource("mission.comment")).toBe("issue")
    expect(activitySource("agent.mentioned")).toBe("issue")
    expect(activitySource("approval.requested")).toBe("human")
    expect(activitySource("keeper.decision")).toBe("security")
    expect(activitySource("llm.call")).toBe("cost")
    expect(activitySource("memory.updated")).toBe("memory")
    expect(activitySource("message.broadcast")).toBe("comms")
  })

  it("falls back to system for an unknown type rather than throwing", () => {
    expect(activitySource("something.invented.later")).toBe("system")
  })

  // The sidebar filters by entry_type server-side, so a type that no source
  // claims would be unreachable from the UI — invisible, not just uncategorised.
  it("claims every entry type the backend can emit", () => {
    const unclaimed = JOURNAL_ENTRY_TYPES.filter(
      (t) => activitySource(t) === "system" && !sourceEntryTypes("system").includes(t),
    )
    expect(unclaimed).toEqual([])
  })

  it("never lets two sources claim the same type", () => {
    const seen = new Map<string, string>()
    for (const s of ACTIVITY_SOURCES) {
      for (const t of s.types) {
        expect(seen.has(t), `${t} claimed by ${seen.get(t)} and ${s.key}`).toBe(false)
        seen.set(t, s.key)
      }
    }
  })

  it("names a globals.css token, never a literal colour", () => {
    for (const s of ACTIVITY_SOURCES) {
      expect(s.token).toMatch(/^--[a-z-]+$/)
    }
  })
})

describe("timeBucket", () => {
  const now = new Date("2026-08-07T12:00:00Z")

  it("splits by recency then by calendar day", () => {
    expect(timeBucket("2026-08-07T11:59:30Z", now)).toBe("now")
    expect(timeBucket("2026-08-07T11:20:00Z", now)).toBe("hour")
    expect(timeBucket("2026-08-07T03:00:00Z", now)).toBe("today")
    // Midday the day before, so the assertion holds in any timezone the
    // suite might run in — day buckets are LOCAL, because "today" is a
    // human word, not a UTC one.
    expect(timeBucket("2026-08-06T12:00:00Z", now)).toBe("yesterday")
    expect(timeBucket("2026-08-01T10:00:00Z", now)).toBe("earlier")
  })

  it("treats a future timestamp as now instead of dropping it", () => {
    // Clock skew between the agent container and the host is real; an entry
    // stamped slightly ahead must still land at the top, not vanish.
    expect(timeBucket("2026-08-07T12:00:30Z", now)).toBe("now")
  })
})

describe("groupIntoBuckets", () => {
  const now = new Date("2026-08-07T12:00:00Z")

  it("keeps buckets in newest-first order and drops empty ones", () => {
    const groups = groupIntoBuckets(
      [
        entry({ id: "a", ts: "2026-08-07T11:59:50Z" }),
        entry({ id: "b", ts: "2026-08-07T03:00:00Z" }),
        entry({ id: "c", ts: "2026-08-01T10:00:00Z" }),
      ],
      now,
    )
    expect(groups.map((g) => g.bucket)).toEqual(["now", "today", "earlier"])
    expect(groups.map((g) => g.entries.length)).toEqual([1, 1, 1])
  })

  it("preserves every entry", () => {
    const entries = [
      entry({ id: "a", ts: "2026-08-07T11:59:50Z" }),
      entry({ id: "b", ts: "2026-08-07T11:59:51Z" }),
      entry({ id: "c", ts: "2026-08-06T09:00:00Z" }),
    ]
    const total = groupIntoBuckets(entries, now).reduce((n, g) => n + g.entries.length, 0)
    expect(total).toBe(entries.length)
  })

  it("returns nothing for an empty feed", () => {
    expect(groupIntoBuckets([], now)).toEqual([])
  })
})

describe("buildSpine", () => {
  it("reads the chain out of ids the entry already carries", () => {
    const spine = buildSpine(
      entry({
        mission_id: "m1",
        trace_id: "tr1",
        payload: { pipeline_slug: "nightly-triage", run_id: "r9", step_id: "step-4" },
      }),
      { issues: { m1: "ENG-3" } },
    )
    expect(spine.map((l) => l.kind)).toEqual(["issue", "routine", "run", "step"])
    expect(spine[0].label).toBe("ENG-3")
    expect(spine[1].label).toBe("nightly-triage")
  })

  it("falls back to a short id when the label is not resolved yet", () => {
    const spine = buildSpine(entry({ mission_id: "cmsj0awf80064108807fc" }), {})
    expect(spine).toHaveLength(1)
    expect(spine[0].label).not.toContain("cmsj0awf80064108807fc")
    expect(spine[0].id).toBe("cmsj0awf80064108807fc")
  })

  it("reads refs when payload does not carry the link", () => {
    const spine = buildSpine(entry({ refs: { pipeline_slug: "classify-ticket" } }), {})
    expect(spine.map((l) => l.label)).toEqual(["classify-ticket"])
  })

  it("returns an empty chain rather than null when nothing links", () => {
    expect(buildSpine(entry(), {})).toEqual([])
  })

  it("ignores a non-string id instead of rendering [object Object]", () => {
    const spine = buildSpine(entry({ payload: { pipeline_slug: { nested: true } } }), {})
    expect(spine).toEqual([])
  })
})

describe("severityTone", () => {
  it("maps backend severities onto the shared detail tones", () => {
    expect(severityTone("error")).toBe("destructive")
    expect(severityTone("warn")).toBe("warn")
    expect(severityTone("notice")).toBe("blue")
    expect(severityTone("info")).toBe("default")
    expect(severityTone("unheard-of")).toBe("default")
  })
})

describe("formatDurationMs", () => {
  it("keeps columns narrow and readable", () => {
    expect(formatDurationMs(420)).toBe("420ms")
    expect(formatDurationMs(8_100)).toBe("8.1s")
    expect(formatDurationMs(68_000)).toBe("1m 08s")
    expect(formatDurationMs(3_725_000)).toBe("1h 02m")
  })

  it("renders an em dash for a missing or nonsense duration", () => {
    expect(formatDurationMs(undefined)).toBe("—")
    expect(formatDurationMs(-5)).toBe("—")
  })
})

/* ------------------------------------------------------------------ *
 *  Overview shaping — what the dashboard cards read
 * ------------------------------------------------------------------ */

describe("scopeOf", () => {
  it("separates what is live, what blocks a person, and what broke", () => {
    expect(scopeOf(entry({ entry_type: "run.started" }))).toBe("active")
    expect(scopeOf(entry({ entry_type: "assignment.running" }))).toBe("active")
    expect(scopeOf(entry({ entry_type: "approval.requested" }))).toBe("waiting")
    expect(scopeOf(entry({ entry_type: "peer.escalation" }))).toBe("waiting")
    expect(scopeOf(entry({ entry_type: "run.failed", severity: "error" }))).toBe("failed")
    expect(scopeOf(entry({ entry_type: "run.completed" }))).toBe("done")
  })

  it("treats any error severity as failed, whatever emitted it", () => {
    // A guardrail block is not a run, but it is still something that broke.
    expect(scopeOf(entry({ entry_type: "guardrail.output_blocked", severity: "error" }))).toBe("failed")
  })

  it("does not let a failed run be counted as active as well", () => {
    const e = entry({ entry_type: "run.failed", severity: "error" })
    expect(scopeOf(e)).not.toBe("active")
  })
})

describe("sourceMix", () => {
  it("counts per source and drops sources with nothing in them", () => {
    const mix = sourceMix([
      entry({ id: "1", entry_type: "run.completed" }),
      entry({ id: "2", entry_type: "run.failed" }),
      entry({ id: "3", entry_type: "mission.comment" }),
    ])
    expect(mix.map((m) => [m.key, m.count])).toEqual([
      ["run", 2],
      ["issue", 1],
    ])
  })

  it("names a token so the donut cannot invent a colour", () => {
    const mix = sourceMix([entry({ entry_type: "llm.call" })])
    expect(mix[0].token).toBe("--gold")
  })

  it("is empty for an empty feed rather than a ring of zeroes", () => {
    expect(sourceMix([])).toEqual([])
  })
})

describe("dailyCounts", () => {
  const now = new Date("2026-08-07T12:00:00Z")

  it("returns one bucket per day including days with nothing", () => {
    const days = dailyCounts([entry({ ts: "2026-08-07T09:00:00Z" })], 7, now)
    expect(days).toHaveLength(7)
    expect(days[days.length - 1].total).toBe(1)
    expect(days[0].total).toBe(0)
  })

  it("splits errors out of the total so a bar can show both", () => {
    const days = dailyCounts(
      [
        entry({ id: "a", ts: "2026-08-07T09:00:00Z" }),
        entry({ id: "b", ts: "2026-08-07T10:00:00Z", severity: "error" }),
      ],
      7,
      now,
    )
    const today = days[days.length - 1]
    expect(today.total).toBe(2)
    expect(today.errors).toBe(1)
  })

  it("ignores entries older than the window instead of piling them on day 0", () => {
    const days = dailyCounts([entry({ ts: "2026-01-01T00:00:00Z" })], 7, now)
    expect(days.reduce((n, d) => n + d.total, 0)).toBe(0)
  })
})

describe("NOISE_ENTRY_TYPES", () => {
  it("hides the high-frequency telemetry that drowns a feed", () => {
    // The seeded dev instance emits container.metrics per crew per minute;
    // eight of those is what "Latest activity" showed before this existed.
    // The five loudest types measured on a seeded dev instance over one
    // hour. Between them they were 86% of the feed.
    for (const t of [
      "container.metrics",
      "file.written",
      "network.egress",
      "network.port_opened",
      "agent.status_change",
    ]) {
      expect(NOISE_ENTRY_TYPES, `${t} was measured as feed-dominating noise`).toContain(t)
    }
  })

  it("never hides anything a person is waiting on", () => {
    for (const t of sourceEntryTypes("human")) {
      expect(NOISE_ENTRY_TYPES).not.toContain(t)
    }
  })

  it("never hides a failure or a run outcome", () => {
    for (const t of ["run.failed", "run.completed", "assignment.failed", "budget.exceeded"]) {
      expect(NOISE_ENTRY_TYPES).not.toContain(t)
    }
  })

  it("only names types the backend actually emits", () => {
    for (const t of NOISE_ENTRY_TYPES) {
      expect(JOURNAL_ENTRY_TYPES as readonly string[]).toContain(t)
    }
  })
})

/* ------------------------------------------------------------------ *
 *  Drift ratchet
 *
 *  The earlier "claims every entry type" test compared the frontend list
 *  against itself, so it passed while the backend had grown 50 types the
 *  UI had never heard of — the whole pipeline.* family (routines!), chat.*,
 *  provisioning.*, credential.*, skill.*, audit.*. Everything unknown fell
 *  into System, so routine activity was unreachable from the Routines
 *  facet. This reads the Go source instead, which is the only version of
 *  this test that can fail when it should.
 * ------------------------------------------------------------------ */

describe("backend parity", () => {
  const goTypes = (() => {
    const src = readFileSync(resolve(process.cwd(), "internal/journal/types.go"), "utf8")
    return [...src.matchAll(/EntryType\s*=\s*"([^"]+)"/g)].map((m) => m[1])
  })()

  it("finds the Go constants at all (guards against a moved file)", () => {
    expect(goTypes.length).toBeGreaterThan(80)
  })

  it("mirrors every backend EntryType in the frontend union", () => {
    const front = new Set<string>(JOURNAL_ENTRY_TYPES as readonly string[])
    expect(goTypes.filter((t) => !front.has(t))).toEqual([])
  })

  it("routes every backend EntryType to a source that claims it explicitly", () => {
    const unclaimed = goTypes.filter(
      (t) => activitySource(t) === "system" && !sourceEntryTypes("system").includes(t),
    )
    expect(unclaimed).toEqual([])
  })

  it("keeps routine activity out of the System bucket", () => {
    // The whole point: pipeline.* IS routines, and a person filtering by
    // "Routines" must see it.
    expect(activitySource("pipeline.run.started")).toBe("routine")
    expect(activitySource("pipeline.step.failed")).toBe("routine")
  })
})

describe("runIdOf", () => {
  // internal/api/pipeline_runs.go:452 states the rule verbatim: "Pipeline
  // runs tag their journal entries with the run id in the payload
  // (payload.run_id) — NOT the trace_id column. Agent-driven runs use
  // trace_id instead. Match either." Reading only trace_id means routine
  // runs — the case that matters most — resolve to no graph at all.
  it("prefers trace_id, which is what agent-driven runs carry", () => {
    expect(runIdOf(entry({ trace_id: "run-agent-1" }))).toBe("run-agent-1")
  })

  it("falls back to payload.run_id, which is what routine runs carry", () => {
    expect(runIdOf(entry({ payload: { run_id: "pl-run-9" } }))).toBe("pl-run-9")
  })

  it("reads refs when neither trace_id nor payload has it", () => {
    expect(runIdOf(entry({ refs: { run_id: "pl-run-7" } }))).toBe("pl-run-7")
  })

  it("returns null when the event belongs to no run", () => {
    expect(runIdOf(entry())).toBeNull()
  })

  it("ignores a non-string run id rather than rendering an object", () => {
    expect(runIdOf(entry({ payload: { run_id: { id: 1 } } }))).toBeNull()
  })
})
