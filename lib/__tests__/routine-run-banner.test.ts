import { describe, expect, it } from "vitest"
import { formatAgo, formatUntil, routineRunBanner } from "../routine-run-presentation"

const steps = [
  { id: "extract", name: "Read the invoice", type: "agent_run" },
  { id: "verify", name: "Check the extraction", type: "agent_run" },
  { id: "decide", name: "Ask Finance when over the limit", type: "wait", if: "total > limit" },
  { id: "post", name: "Post to the ledger", type: "script" },
  { id: "notify", name: "Tell #finance", type: "notify", notify: { to: "#finance" } },
]
const NOW = Date.parse("2026-09-15T10:36:00Z")

describe("formatAgo / formatUntil", () => {
  it("speaks in minutes, hours and days — never seconds", () => {
    expect(formatAgo("2026-09-15T10:35:40Z", NOW)).toBe("just now")
    expect(formatAgo("2026-09-15T10:32:00Z", NOW)).toBe("4 min ago")
    expect(formatAgo("2026-09-15T08:00:00Z", NOW)).toBe("2 h ago")
    expect(formatAgo("2026-09-12T10:00:00Z", NOW)).toBe("3 d ago")
    expect(formatAgo(undefined, NOW)).toBe("")
    expect(formatUntil("2026-09-16T10:32:00Z", NOW)).toBe("23 h 56 min")
    expect(formatUntil("2026-09-15T11:00:00Z", NOW)).toBe("24 min")
    expect(formatUntil("2026-09-15T10:00:00Z", NOW)).toBe("")
  })
})

describe("routineRunBanner", () => {
  it("names who needs to decide, why, and when the decision expires", () => {
    const banner = routineRunBanner({
      run: { status: "waiting", current_step_id: "decide" },
      steps,
      waitKind: "approval",
      waiting: { who: "Finance", why: "The total is over the limit.", expiresAt: "2026-09-16T10:32:00Z" },
      now: NOW,
    })
    expect(banner.tone).toBe("warn")
    expect(banner.title).toBe("Finance needs to decide")
    expect(banner.detail).toBe(
      "The total is over the limit. Nothing after this step has happened yet. This is the same decision shown in Inbox. Expires in 23 h 56 min.",
    )
  })

  it("falls back to 'A person' and no expiry when the waitpoint says nothing", () => {
    const banner = routineRunBanner({ run: { status: "waiting" }, steps, waitKind: "approval", now: NOW })
    expect(banner.title).toBe("A person needs to decide")
    expect(banner.detail).not.toMatch(/Expires/)
  })

  it("says where the work is while it runs", () => {
    const banner = routineRunBanner({ run: { status: "running", current_step_id: "verify" }, steps })
    expect(banner.tone).toBe("blue")
    expect(banner.title).toBe("Work in progress · step 2 of 5, “Check the extraction”")
    expect(routineRunBanner({ run: { status: "running" } }).title).toBe("Work in progress")
  })

  it("uses the server's failure projection: step, plain reason, kept and not done", () => {
    const banner = routineRunBanner({
      run: {
        status: "failed",
        error_message: 'step verify: checker rejected: criterion "total_equals_lines"',
        failure: {
          kind: "checker_rejected",
          step_id: "verify",
          step_name: "Check the extraction",
          summary: "The checker rejected the result after exhausting the allowed model tiers: total_equals_lines.",
          kept_step_ids: ["extract"],
          not_done_step_ids: ["decide", "post", "notify"],
        },
      },
      steps,
    })
    expect(banner.tone).toBe("destructive")
    expect(banner.title).toBe("Stopped at step 2, “Check the extraction”")
    expect(banner.detail).toBe("The checker rejected the result after exhausting the allowed model tiers: total_equals_lines.")
    expect(banner.kept).toBe("Read the invoice")
    expect(banner.notDone).toBe("Ask Finance when over the limit, Post to the ledger, Tell #finance")
    // The raw engine message never leads the banner.
    expect(banner.detail).not.toMatch(/criterion/)
  })

  it("keeps today's words for a failure the server did not classify", () => {
    const banner = routineRunBanner({
      run: { status: "failed", failed_at_step: "verify", error_message: "boom" },
      steps,
    })
    expect(banner.title).toBe("This run could not finish")
    expect(banner.kept).toBeUndefined()
    expect(routineRunBanner({ run: { status: "interrupted" } }).title).toBe("Run interrupted")
  })

  it("says what a completed run did, skipped and notified", () => {
    const banner = routineRunBanner({
      run: {
        status: "completed",
        step_outputs: { extract: {}, verify: {}, post: "LE-1", notify: "ok" },
        step_outputs_available: true,
      },
      steps,
      resultLabel: "Posted to the ledger",
    })
    expect(banner.tone).toBe("success")
    expect(banner.title).toBe("Done · Posted to the ledger")
    expect(banner.detail).toBe(
      "Skipped: Ask Finance when over the limit (its condition was not met). Notified #finance.",
    )
    expect(routineRunBanner({ run: { status: "completed" } }).title).toBe("Done · Completed")
  })

  it("calls a stopped run stopped and keeps the odd outcomes' explanations", () => {
    expect(routineRunBanner({ run: { status: "cancelled" } }).title).toBe("Run stopped")
    expect(routineRunBanner({ run: { status: "completed", outcome: "PARTIAL" } }).title).toBe(
      "Part of the work is complete",
    )
    expect(
      routineRunBanner({ run: { status: "completed", outcome: "FAILED", error_message: "no outcome reported" } })
        .title,
    ).toBe("Completion was not confirmed")
  })
})
