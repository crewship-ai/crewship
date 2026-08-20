import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"

import {
  ACTIVITY_SOURCES,
  activitySource,
  answeredAsks,
  buildSpine,
  chainElapsedMs,
  entriesInScope,
  formatDurationMs,
  groupIntoBuckets,
  severityTone,
  sourceEntryTypes,
  scopeCounts,
  scopeOf,
  sourceMix,
  dailyCounts,
  NOISE_ENTRY_TYPES,
  narrowToFocus,
  railInventory,
  runIdOf,
  timeBucket,
} from "@/lib/activity-stream"
// The one cross-module import in this file, and the point of it: the rail's
// count and the overview card's list must be the same answer, so the test
// that says so has to see both.
import { openAsks } from "@/lib/activity-overview"
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

/* ------------------------------------------------------------------ *
 *  What is still waiting on a person — #1876
 *
 *  The journal is an EVENT LOG, so an ask stays in it after it was
 *  answered, and `peer.escalation` is emitted for the ask AND for its
 *  resolution. Bucketing by entry type alone therefore reports a queue
 *  that has already been cleared — the rail reading "Waiting 4" beside a
 *  card reading 0.
 *
 *  Every fixture below is copied from the Go emit site that writes it,
 *  cited by file:line, for the same reason activity-overview.test.ts
 *  does it: a helper that only handles payloads the backend never writes
 *  is a helper that handles nothing.
 * ------------------------------------------------------------------ */

// internal/api/escalation_handler.go:255 — the ask. Severity warn, state pending.
const escalationAsk = (id: string) =>
  entry({
    id: `ask-${id}`,
    entry_type: "peer.escalation",
    severity: "warn",
    summary: "escalation from scout: needs a prod credential",
    payload: { reason: "needs a prod credential", escalation_type: "CREDENTIAL", from_slug: "scout", state: "pending" },
    refs: { escalation_id: id, chat_id: "chat1" },
  })

// internal/api/escalation_handler.go:607 — the SAME entry_type, resolved by a person.
const escalationResolved = (id: string) =>
  entry({
    id: `res-${id}`,
    entry_type: "peer.escalation",
    severity: "notice",
    summary: `escalation ${id} resolved (approve)`,
    payload: { resolution: "granted", action: "approve", state: "resolved", escalation_type: "CREDENTIAL" },
    refs: { escalation_id: id },
  })

// internal/api/escalation_autoresolve.go:172 — resolved by the system instead.
const escalationAutoResolved = (id: string) =>
  entry({
    id: `auto-${id}`,
    entry_type: "peer.escalation",
    severity: "notice",
    summary: `escalation ${id} auto-resolved: matching credential assigned`,
    payload: { resolution: "assigned", action: "approve", state: "resolved", auto_resolved: true },
    refs: { escalation_id: id },
  })

// internal/harbormaster/store_mutate.go:83 / :232 — the ask and its grant.
const approvalAsk = (id: string) =>
  entry({
    id: `ask-${id}`,
    entry_type: "approval.requested",
    severity: "notice",
    summary: "approval requested: exec — deploy to prod",
    payload: { approval_id: id, kind: "exec" },
    refs: { approval_id: id },
  })

const approvalGranted = (id: string) =>
  entry({
    id: `grant-${id}`,
    entry_type: "approval.granted",
    severity: "notice",
    summary: "approval granted by demo@crewship.ai",
    payload: { approval_id: id, kind: "exec" },
    refs: { approval_id: id },
  })

// internal/api/keeper_request.go:235 / :381.
const keeperAsk = (id: string) =>
  entry({
    id: `ask-${id}`,
    entry_type: "keeper.request",
    severity: "notice",
    summary: "scout requested credential STRIPE_KEY",
    payload: { request_id: id },
    refs: { keeper_request_id: id },
  })

const keeperDecision = (id: string) =>
  entry({
    id: `dec-${id}`,
    entry_type: "keeper.decision",
    severity: "notice",
    summary: "keeper allowed STRIPE_KEY",
    payload: { request_id: id, decision: "allow" },
    refs: { keeper_request_id: id },
  })

describe("scopeOf — a resolved ask is not waiting on anyone", () => {
  // Needs no window: the resolution carries its own terminal state, and the
  // old rule ("human source ⇒ waiting") called it a fresh ask anyway.
  it("does not call a resolved escalation waiting", () => {
    expect(scopeOf(escalationResolved("e1"))).not.toBe("waiting")
    expect(scopeOf(escalationResolved("e1"))).toBe("done")
    expect(scopeOf(escalationAutoResolved("e2"))).toBe("done")
  })

  it("still calls a pending escalation waiting", () => {
    expect(scopeOf(escalationAsk("e1"))).toBe("waiting")
  })

  // Needs the window: an approval's grant is a DIFFERENT entry type, so the
  // ask row can only be shown answered by joining it to the answer beside it.
  it("does not call an ask waiting once the same window answered it", () => {
    const feed = [approvalGranted("a1"), approvalAsk("a1"), keeperDecision("k1"), keeperAsk("k1")]
    const answered = answeredAsks(feed)
    expect(scopeOf(approvalAsk("a1"), answered)).toBe("done")
    expect(scopeOf(keeperAsk("k1"), answered)).toBe("done")
  })

  it("keeps an ask nothing in the window answered", () => {
    const feed = [approvalGranted("a1"), approvalAsk("a1"), approvalAsk("a2")]
    expect(scopeOf(approvalAsk("a2"), answeredAsks(feed))).toBe("waiting")
  })

  it("keeps an ask carrying no id rather than silently closing it", () => {
    // It cannot be joined, so it cannot be PROVEN answered. One row too many
    // beats hiding something a person is blocking on.
    const e = entry({ entry_type: "approval.requested", payload: {}, refs: {} })
    expect(scopeOf(e, answeredAsks([e, approvalGranted("a1")]))).toBe("waiting")
  })

  it("does not let one kind's answer close another kind's ask", () => {
    const feed = [approvalGranted("x"), keeperAsk("x")]
    expect(scopeOf(keeperAsk("x"), answeredAsks(feed))).toBe("waiting")
  })

  it("leaves the other three buckets alone", () => {
    const answered = answeredAsks([escalationResolved("e1")])
    expect(scopeOf(entry({ entry_type: "run.started" }), answered)).toBe("active")
    expect(scopeOf(entry({ entry_type: "run.failed", severity: "error" }), answered)).toBe("failed")
    expect(scopeOf(entry({ entry_type: "run.completed" }), answered)).toBe("done")
  })
})

describe("scopeCounts / entriesInScope — the rail counts what the card counts", () => {
  // The screen this fixes: five human-source rows, of which exactly one is
  // still an open ask. Counting rows gives "Waiting 5" beside a card reading 1.
  const feed = [
    escalationResolved("e1"),
    escalationAsk("e1"),
    keeperDecision("k1"),
    keeperAsk("k1"),
    approvalGranted("a1"),
    approvalAsk("a1"),
    keeperAsk("k2"), // the only one still open
    entry({ entry_type: "run.completed" }),
    entry({ entry_type: "run.started" }),
    entry({ entry_type: "run.failed", severity: "error" }),
  ]

  it("counts open asks, not ask-shaped rows", () => {
    expect(scopeCounts(feed).waiting).toBe(1)
  })

  it("agrees with the overview card, which is the number that was right", () => {
    // One question, one answer. This equality IS the bug's absence.
    expect(scopeCounts(feed).waiting).toBe(openAsks(feed).length)
  })

  it("keeps the four buckets mutually exclusive and complete", () => {
    const c = scopeCounts(feed)
    expect(c.active + c.waiting + c.failed + c.done).toBe(feed.length)
  })

  it("puts an answered ask in done rather than dropping it from the feed", () => {
    const done = entriesInScope(feed, "done")
    expect(done.map((e) => e.id)).toContain("ask-a1")
    expect(done.map((e) => e.id)).toContain("res-e1")
  })

  it("lists exactly the open asks under the waiting scope, in feed order", () => {
    expect(entriesInScope(feed, "waiting").map((e) => e.id)).toEqual(["ask-k2"])
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

describe("narrowToFocus — the rail and the cards must count the same set", () => {
  // The screen showed both answers at once: the FAILED card read "0 — nothing
  // broke" while the rail beside it read "Failed 9". The cards were built from
  // the routine-focused set and the rail from the whole loaded window, so one
  // screen gave two answers to "did anything break", and the reassuring one
  // was the wrong one.
  //
  // The rail's STATUS list is a filter CONTROL: each count answers "how many
  // would I get if I also clicked this". That is only true when it is counted
  // over the same focus the cards use — so the focus narrowing is extracted
  // here, applied once, and shared.
  const entry = (over: Partial<JournalEntry>): JournalEntry =>
    ({
      id: "j1",
      workspace_id: "ws",
      entry_type: "pipeline.run.completed",
      severity: "info",
      ts: new Date().toISOString(),
      ...over,
    }) as JournalEntry

  const probe = entry({ id: "a", payload: { pipeline_slug: "cost-spike-probe" } })
  const probeFailed = entry({
    id: "b",
    entry_type: "pipeline.run.failed",
    severity: "error",
    payload: { pipeline_slug: "cost-spike-probe" },
  })
  const otherFailed = entry({
    id: "c",
    entry_type: "pipeline.run.failed",
    severity: "error",
    payload: { pipeline_slug: "normalize-dates" },
  })

  it("narrows to the focused routine", () => {
    const out = narrowToFocus([probe, probeFailed, otherFailed], {
      kind: "routine",
      id: "cost-spike-probe",
    })
    expect(out.map((e) => e.id)).toEqual(["a", "b"])
  })

  it("returns everything when nothing is focused", () => {
    const all = [probe, probeFailed, otherFailed]
    expect(narrowToFocus(all, null)).toHaveLength(3)
  })

  it("counts the focused set, not the window — the contradiction, pinned", () => {
    const focused = narrowToFocus([probe, probeFailed, otherFailed], {
      kind: "routine",
      id: "cost-spike-probe",
    })
    const failed = focused.filter((e) => scopeOf(e) === "failed").length
    // One failure belongs to this routine; the other does not. A rail reading
    // 2 here is the screen contradicting its own cards.
    expect(failed).toBe(1)
  })

  it("matches routine_slug as well as pipeline_slug", () => {
    // Two producers spell it differently and both reach the journal; matching
    // only one silently drops half the routine's events from the count.
    const viaRoutineSlug = entry({ id: "d", refs: { routine_slug: "cost-spike-probe" } })
    const out = narrowToFocus([viaRoutineSlug], { kind: "routine", id: "cost-spike-probe" })
    expect(out).toHaveLength(1)
  })
})

describe("chainElapsedMs — how long the chain actually took", () => {
  // "Chain duration —" beside "4 events" tells a reader nothing. It was the
  // SUM of per-entry durations, which is 0 for an agentless routine whose
  // steps report 0ms, and which double-counts a step nested inside the run
  // that contains it. Neither is what "duration" says.
  //
  // Wall clock between the first and last entry is what a person means, and it
  // is the one number the journal can always answer.
  const at = (iso: string, over: Partial<JournalEntry> = {}): JournalEntry =>
    ({
      id: iso,
      workspace_id: "ws",
      entry_type: "pipeline.step.completed",
      severity: "info",
      ts: iso,
      ...over,
    }) as JournalEntry

  it("measures first to last, not the sum of the parts", () => {
    const chain = [
      at("2026-08-10T08:34:00.000Z"),
      at("2026-08-10T08:34:03.500Z"),
      at("2026-08-10T08:34:08.000Z"),
    ]
    expect(chainElapsedMs(chain)).toBe(8000)
  })

  it("is order-independent — the journal returns newest first", () => {
    const chain = [at("2026-08-10T08:34:08.000Z"), at("2026-08-10T08:34:00.000Z")]
    expect(chainElapsedMs(chain)).toBe(8000)
  })

  it("returns null for a single entry rather than a confident zero", () => {
    // One event has no span. Reporting 0ms would say "it was instant"; the
    // truth is "there is nothing to measure between".
    expect(chainElapsedMs([at("2026-08-10T08:34:00.000Z")])).toBeNull()
    expect(chainElapsedMs([])).toBeNull()
  })

  it("survives an unparseable timestamp instead of returning NaN", () => {
    const chain = [at("not-a-date"), at("2026-08-10T08:34:00.000Z"), at("2026-08-10T08:34:05.000Z")]
    expect(chainElapsedMs(chain)).toBe(5000)
  })

  it("reports a real span even when every step reported 0ms", () => {
    // The agentless case that produced the dash: token-zero steps carry no
    // duration, but the chain still occupied wall-clock time.
    const chain = [
      at("2026-08-10T08:34:00.000Z", { payload: { duration_ms: 0 } }),
      at("2026-08-10T08:34:02.000Z", { payload: { duration_ms: 0 } }),
    ]
    expect(chainElapsedMs(chain)).toBe(2000)
  })
})

describe("railInventory — the rail must stay navigable while focused", () => {
  // Unfocused, the rail answers "where is the activity": only entities with
  // events, busiest first. A rail listing 38 routines of which 3 are live is a
  // rail you scroll past.
  //
  // Focused, that same rule removes every OTHER entity — because an issue
  // focus narrows the fetch server-side, so the window holds only that issue's
  // events and every other issue and routine counts zero. The rail then has
  // exactly one row and there is no way to click to a different issue: the
  // list stopped being navigation and became a label for the current filter.
  const items = [
    { id: "a", label: "A" },
    { id: "b", label: "B" },
    { id: "c", label: "C" },
  ]
  const key = (i: { id: string }) => i.id

  it("unfocused: only entities with activity, busiest first", () => {
    const out = railInventory(items, { a: 2, c: 9 }, key, false)
    expect(out.map((i) => i.id)).toEqual(["c", "a"])
  })

  it("focused: the whole inventory, so you can navigate away", () => {
    const out = railInventory(items, { a: 2 }, key, true)
    expect(out.map((i) => i.id).sort()).toEqual(["a", "b", "c"])
  })

  it("focused: the ones with activity still lead", () => {
    // Order still carries information — the quiet ones simply stop vanishing.
    const out = railInventory(items, { c: 9, a: 2 }, key, true)
    expect(out.slice(0, 2).map((i) => i.id)).toEqual(["c", "a"])
  })

  it("unfocused with no activity at all yields an empty rail, not the inventory", () => {
    // "Nothing happened" is a real answer and must not be dressed up as a
    // catalogue of everything that exists.
    expect(railInventory(items, {}, key, false)).toEqual([])
  })
})
