// The decisions behind the Activity overview, tested away from the DOM.
//
// Every fixture in this file is copied from the Go emit site that produces
// it, cited by file:line. That is deliberate: the previous round of tests on
// this branch passed against payloads the backend never writes (an
// `approval.requested` carrying `state`, a `peer.escalation` at severity
// `error`), which proves the helper handles fiction and nothing else. If a
// shape here stops matching the emitter, this file should go red.

import { describe, expect, it } from "vitest"

import {
  answerRef,
  askRef,
  failureClusters,
  liveSignal,
  openAsks,
  windowSpanDays,
  zeroCopy,
  zeroKind,
} from "@/lib/activity-overview"
import type { JournalEntry } from "@/lib/types/journal"

let seq = 0
function entry(over: Partial<JournalEntry> = {}): JournalEntry {
  seq += 1
  return {
    id: `j${seq}`,
    workspace_id: "ws1",
    ts: "2026-08-07T12:00:00Z",
    entry_type: "run.completed",
    severity: "info",
    actor_type: "agent",
    summary: "done",
    ...over,
  } as JournalEntry
}

/* ------------------------------------------------------------------ *
 *  Fixtures — each one mirrors a real emit site
 * ------------------------------------------------------------------ */

// internal/harbormaster/store_mutate.go:83
const approvalAsk = (id: string) =>
  entry({
    entry_type: "approval.requested",
    severity: "notice",
    summary: "approval requested: exec — deploy to prod",
    payload: { approval_id: id, kind: "exec" },
    refs: { approval_id: id },
  })

// internal/harbormaster/store_mutate.go:232
const approvalGranted = (id: string) =>
  entry({
    entry_type: "approval.granted",
    severity: "notice",
    summary: "approval granted by demo@crewship.ai",
    payload: { approval_id: id, kind: "exec", comment: "" },
    refs: { approval_id: id },
  })

// internal/harbormaster/gate.go:213
const approvalTimeout = (id: string) =>
  entry({
    entry_type: "approval.timeout",
    severity: "warn",
    summary: "approval timed out (sync gate): no answer",
    payload: { approval_id: id, kind: "exec" },
    refs: { approval_id: id },
  })

// internal/api/keeper_request.go:235
const keeperAsk = (id: string) =>
  entry({
    entry_type: "keeper.request",
    severity: "notice",
    summary: "scout requested credential STRIPE_KEY",
    payload: { request_id: id, credential_id: "c1", credential_name: "STRIPE_KEY" },
    refs: { keeper_request_id: id, credential_id: "c1" },
  })

// internal/api/keeper_request.go:381
const keeperDecision = (id: string) =>
  entry({
    entry_type: "keeper.decision",
    severity: "notice",
    summary: "keeper allowed STRIPE_KEY",
    payload: { request_id: id, decision: "allow" },
    refs: { keeper_request_id: id, credential_id: "c1" },
  })

// internal/api/escalation_handler.go:255 — the ask. Note `state: "pending"`.
const escalationAsk = (id: string) =>
  entry({
    entry_type: "peer.escalation",
    severity: "warn",
    summary: "escalation from scout: needs a prod credential",
    payload: {
      reason: "needs a prod credential",
      context: "",
      escalation_type: "CREDENTIAL",
      from_slug: "scout",
      state: "pending",
    },
    refs: { escalation_id: id, chat_id: "chat1" },
  })

// internal/api/escalation_handler.go:607 — same entry_type, resolved.
const escalationResolved = (id: string) =>
  entry({
    entry_type: "peer.escalation",
    severity: "notice",
    summary: `escalation ${id} resolved (approve)`,
    payload: {
      resolution: "***REDACTED:credential***",
      action: "approve",
      state: "resolved",
      escalation_type: "CREDENTIAL",
    },
    refs: { escalation_id: id },
  })

// internal/api/escalation_autoresolve.go:172 — resolved by the system.
const escalationAutoResolved = (id: string) =>
  entry({
    entry_type: "peer.escalation",
    severity: "notice",
    summary: `escalation ${id} auto-resolved: matching credential assigned`,
    payload: {
      resolution: "assigned",
      action: "approve",
      state: "resolved",
      auto_resolved: true,
      credential_name: "STRIPE_KEY",
    },
    refs: { escalation_id: id },
  })

/* ------------------------------------------------------------------ *
 *  Which zero is this?
 * ------------------------------------------------------------------ */

describe("zeroKind", () => {
  it("is null when the bucket actually has something in it", () => {
    expect(zeroKind(400, 9)).toBeNull()
  })

  it("calls an empty window 'nothing recorded' — not an all-clear", () => {
    expect(zeroKind(0, 0)).toBe("nothing-recorded")
  })

  it("calls a populated window with an empty bucket 'nothing of this kind'", () => {
    expect(zeroKind(412, 0)).toBe("nothing-of-this-kind")
  })
})

describe("zeroCopy", () => {
  // The bug: a routine was focused, its slice held no failures, and the card
  // said "nothing broke" beside a rail reading Failed 9. The number was
  // right for the question asked; the WORDS claimed a scope the number
  // never had.
  it("never reassures about a window that recorded nothing", () => {
    const copy = zeroCopy("nothing-recorded", 0, "a failure")
    expect(copy.subtitle).toBe("nothing recorded in this window")
    expect(copy.panel).toMatch(/not an all-clear/i)
  })

  it("scopes the all-clear to the events it can actually see", () => {
    const copy = zeroCopy("nothing-of-this-kind", 412, "a failure")
    expect(copy.subtitle).toBe("none of the 412 events shown")
    expect(copy.panel).toContain("412")
    expect(copy.panel).toContain("a failure")
  })

  // Guards the whole point of the fix. No zero state on this page may claim
  // that nothing broke, everything is clear, or anything is nice.
  it("uses none of the reassurance vocabulary that was wrong", () => {
    for (const total of [0, 1, 412]) {
      for (const kind of ["nothing-recorded", "nothing-of-this-kind"] as const) {
        const copy = zeroCopy(kind, total, "a failure")
        const text = `${copy.subtitle} ${copy.panel}`
        expect(text).not.toMatch(/nothing broke|all clear|all clean|Nice\./i)
      }
    }
  })

  it("thousands-separates a big window rather than printing 12000", () => {
    expect(zeroCopy("nothing-of-this-kind", 12000, "a failure").subtitle).toContain("12,000")
  })
})

/* ------------------------------------------------------------------ *
 *  What counts as waiting on a person
 * ------------------------------------------------------------------ */

describe("askRef", () => {
  it("recognises the three things that block a human", () => {
    expect(askRef(approvalAsk("a1"))).toEqual({ kind: "approval", id: "a1" })
    expect(askRef(keeperAsk("k1"))).toEqual({ kind: "keeper", id: "k1" })
    expect(askRef(escalationAsk("e1"))).toEqual({ kind: "escalation", id: "e1" })
  })

  // The trap this whole module exists for: ask and resolution share one
  // entry_type, so `entry_type === "peer.escalation"` counts answered work
  // as pending.
  it("does not read a resolved escalation as an ask", () => {
    expect(askRef(escalationResolved("e1"))).toBeNull()
    expect(askRef(escalationAutoResolved("e2"))).toBeNull()
  })

  it("ignores anything that is not an ask", () => {
    expect(askRef(entry({ entry_type: "run.failed", severity: "error" }))).toBeNull()
    expect(askRef(approvalGranted("a1"))).toBeNull()
    expect(askRef(keeperDecision("k1"))).toBeNull()
  })

  it("falls back to the payload id when refs is missing", () => {
    // Older rows predate the refs column being filled in for keeper asks.
    const e = entry({ entry_type: "keeper.request", payload: { request_id: "k9" } })
    expect(askRef(e)).toEqual({ kind: "keeper", id: "k9" })
  })
})

describe("answerRef", () => {
  it("matches every terminal state an approval can reach", () => {
    expect(answerRef(approvalGranted("a1"))).toEqual({ kind: "approval", id: "a1" })
    expect(answerRef(approvalTimeout("a1"))).toEqual({ kind: "approval", id: "a1" })
    for (const t of ["approval.denied", "approval.cancelled"]) {
      const e = entry({ entry_type: t, refs: { approval_id: "a1" } })
      expect(answerRef(e)).toEqual({ kind: "approval", id: "a1" })
    }
  })

  it("matches a keeper decision to its request", () => {
    expect(answerRef(keeperDecision("k1"))).toEqual({ kind: "keeper", id: "k1" })
  })

  it("treats a resolved escalation as the answer to its own ask", () => {
    expect(answerRef(escalationResolved("e1"))).toEqual({ kind: "escalation", id: "e1" })
    expect(answerRef(escalationAutoResolved("e1"))).toEqual({ kind: "escalation", id: "e1" })
  })

  it("does not treat a pending ask as its own answer", () => {
    expect(answerRef(escalationAsk("e1"))).toBeNull()
    expect(answerRef(approvalAsk("a1"))).toBeNull()
  })
})

describe("openAsks", () => {
  it("drops an ask the same window already answered", () => {
    const feed = [approvalGranted("a1"), approvalAsk("a1")]
    expect(openAsks(feed)).toEqual([])
  })

  it("keeps an ask nobody has answered yet", () => {
    const open = approvalAsk("a2")
    const feed = [open, approvalGranted("a1"), approvalAsk("a1")]
    expect(openAsks(feed).map((e) => e.id)).toEqual([open.id])
  })

  // The number this page is judged on. A seeded instance answers most of
  // what it asks, so counting ask-rows reports a queue that is not there.
  it("counts what still needs a person, not how many asks were made", () => {
    const feed = [
      escalationResolved("e1"),
      escalationAsk("e1"),
      keeperDecision("k1"),
      keeperAsk("k1"),
      approvalTimeout("a1"),
      approvalAsk("a1"),
      keeperAsk("k2"), // the only one still open
    ]
    const open = openAsks(feed)
    expect(open).toHaveLength(1)
    expect(open[0].entry_type).toBe("keeper.request")
    expect(open[0].payload?.request_id).toBe("k2")
  })

  it("keeps the feed's order rather than resorting it", () => {
    const a = keeperAsk("k1")
    const b = approvalAsk("a1")
    const c = escalationAsk("e1")
    expect(openAsks([a, b, c]).map((e) => e.id)).toEqual([a.id, b.id, c.id])
  })

  it("keeps an ask whose id is missing instead of silently closing it", () => {
    // Cannot be joined to an answer, so it cannot be PROVEN answered.
    // Over-reporting one row beats hiding a real one.
    const e = entry({ entry_type: "approval.requested", payload: {}, refs: {} })
    expect(openAsks([e])).toHaveLength(1)
  })

  it("does not let one entity's id close another's ask", () => {
    // Same id string, different kind — an approval answer must not close a
    // keeper request that happens to share it.
    const feed = [approvalGranted("x"), keeperAsk("x")]
    expect(openAsks(feed)).toHaveLength(1)
  })

  it("returns nothing for a feed with no asks at all", () => {
    expect(openAsks([entry(), entry({ entry_type: "run.failed", severity: "error" })])).toEqual([])
  })
})

/* ------------------------------------------------------------------ *
 *  What is broken
 * ------------------------------------------------------------------ */

const failedRun = (runID: string, over: Partial<JournalEntry> = {}) =>
  entry({
    entry_type: "run.failed",
    severity: "error",
    summary: "run failed: exit 1",
    payload: { run_id: runID },
    ...over,
  })

const failedStep = (slug: string, over: Partial<JournalEntry> = {}) =>
  entry({
    entry_type: "pipeline.step.failed",
    severity: "error",
    summary: "step failed",
    payload: { pipeline_slug: slug, step: "build" },
    ...over,
  })

describe("failureClusters", () => {
  it("ignores everything that did not fail", () => {
    expect(failureClusters([entry(), entry({ entry_type: "run.completed" })])).toEqual([])
  })

  // Nine rows from one broken routine is ONE thing to fix, and a panel that
  // spends all five of its rows on it hides the other four things.
  it("collapses a routine's repeated failures into one named thing", () => {
    const feed = [failedStep("nightly-sync"), failedStep("nightly-sync"), failedStep("nightly-sync")]
    const clusters = failureClusters(feed)
    expect(clusters).toHaveLength(1)
    expect(clusters[0].count).toBe(3)
    expect(clusters[0].key).toBe("routine:nightly-sync")
  })

  it("shows the newest row as the face of a cluster", () => {
    // The feed arrives newest-first, so the first match is the newest.
    const newest = failedStep("nightly-sync", { ts: "2026-08-07T12:00:00Z" })
    const older = failedStep("nightly-sync", { ts: "2026-08-07T09:00:00Z" })
    expect(failureClusters([newest, older])[0].latest.id).toBe(newest.id)
  })

  it("puts the biggest fire first", () => {
    const feed = [
      failedRun("r1"),
      failedStep("nightly-sync"),
      failedStep("nightly-sync"),
      failedStep("nightly-sync"),
    ]
    const clusters = failureClusters(feed)
    expect(clusters.map((c) => c.key)).toEqual(["routine:nightly-sync", "run:r1"])
  })

  it("keeps unrelated failures apart", () => {
    const clusters = failureClusters([failedRun("r1"), failedRun("r2")])
    expect(clusters).toHaveLength(2)
  })

  it("still names a failure that carries no run or routine", () => {
    // guardrail blocks carry neither, and must not all pile into one bucket
    // with everything else that is idless.
    const g = entry({ entry_type: "guardrail.output_blocked", severity: "error", agent_id: "ag1" })
    const clusters = failureClusters([g])
    expect(clusters).toHaveLength(1)
    expect(clusters[0].key).toBe("agent:ag1")
  })
})

/* ------------------------------------------------------------------ *
 *  How far back the window can actually speak for
 * ------------------------------------------------------------------ */

describe("windowSpanDays", () => {
  it("has no span at all for an empty window", () => {
    expect(windowSpanDays([])).toBe(0)
  })

  // The default range is 24 hours, so this is the COMMON case — and the
  // fixed 7-day chart drew six empty columns for it, which read as six
  // quiet days rather than six days nobody asked about.
  it("reports one day for a window that sits inside one day", () => {
    // Kept mid-day so the pair stays on one LOCAL date whatever zone the
    // test runs in — the span is calendar arithmetic in the viewer's zone,
    // exactly like the buckets in activity-stream.
    const feed = [
      entry({ ts: "2026-08-07T14:00:00Z" }),
      entry({ ts: "2026-08-07T10:00:00Z" }),
    ]
    expect(windowSpanDays(feed)).toBe(1)
  })

  it("counts both end days, not the gap between them", () => {
    const feed = [entry({ ts: "2026-08-07T12:00:00Z" }), entry({ ts: "2026-08-05T12:00:00Z" })]
    expect(windowSpanDays(feed)).toBe(3)
  })

  it("caps at the number of columns the chart has", () => {
    const feed = [entry({ ts: "2026-08-07T12:00:00Z" }), entry({ ts: "2026-01-01T12:00:00Z" })]
    expect(windowSpanDays(feed)).toBe(7)
    expect(windowSpanDays(feed, 3)).toBe(3)
  })

  it("ignores a row whose timestamp will not parse", () => {
    const feed = [entry({ ts: "2026-08-07T12:00:00Z" }), entry({ ts: "not a date" })]
    expect(windowSpanDays(feed)).toBe(1)
  })
})

/* ------------------------------------------------------------------ *
 *  The small "right now" signal
 * ------------------------------------------------------------------ */

describe("liveSignal", () => {
  it("reports nothing in flight for an empty window", () => {
    expect(liveSignal([])).toEqual({ running: 0, agents: 0, spendUSD: 0, slowestMs: null })
  })

  it("counts runs in flight, not runs that finished", () => {
    const feed = [
      entry({ entry_type: "run.started", agent_id: "ag1" }),
      entry({ entry_type: "assignment.running", agent_id: "ag2" }),
      entry({ entry_type: "run.completed", agent_id: "ag3" }),
    ]
    expect(liveSignal(feed).running).toBe(2)
  })

  // scopeOf puts severity first, so a failed run is broken, never live.
  it("does not call a failed run live", () => {
    const feed = [entry({ entry_type: "run.started", severity: "error", agent_id: "ag1" })]
    expect(liveSignal(feed).running).toBe(0)
  })

  it("counts each agent once however many rows it wrote", () => {
    const feed = [
      entry({ agent_id: "ag1" }),
      entry({ agent_id: "ag1" }),
      entry({ agent_id: "ag2" }),
      entry({}),
    ]
    expect(liveSignal(feed).agents).toBe(2)
  })

  it("adds up spend and finds the slowest thing", () => {
    const feed = [
      entry({ entry_type: "llm.call", payload: { cost_usd: 0.25, duration_ms: 1200 } }),
      entry({ entry_type: "llm.call", payload: { cost_usd: 0.5, duration_ms: 9000 } }),
      entry({ entry_type: "run.completed" }),
    ]
    const s = liveSignal(feed)
    expect(s.spendUSD).toBeCloseTo(0.75, 6)
    expect(s.slowestMs).toBe(9000)
  })

  it("reports no slowest rather than 0ms when nothing was timed", () => {
    // 0 would render as "0ms", which asserts an instant run that never ran.
    expect(liveSignal([entry()]).slowestMs).toBeNull()
  })
})
