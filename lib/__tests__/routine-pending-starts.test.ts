import { describe, expect, it } from "vitest"
import { didNotRun, isWaitingForCapacity, pendingStartPresentation, pendingStartStatus, type PendingStart } from "@/lib/routine-pending-starts"

const NOW = Date.parse("2026-10-08T12:00:00Z")
const at = (minutes: number) => new Date(NOW + minutes * 60_000).toISOString()
const start = (extra: Partial<PendingStart> = {}): PendingStart => ({ id: "p1", pipeline_slug: "invoice", fire_at: at(30), ...extra })

describe("pendingStartPresentation", () => {
  it("reads an older server's row, with no status, as a planned start", () => {
    expect(pendingStartStatus(start())).toBe("pending")
    expect(pendingStartPresentation(start(), NOW)).toMatchObject({ label: "Planned", detail: "Starts in 30 min." })
  })

  it("says a due start is being picked up, not that it is late", () => {
    expect(pendingStartPresentation(start({ fire_at: at(-1) }), NOW)).toMatchObject({ label: "Starting", tone: "blue" })
  })

  // next_attempt_at is eligibility, not a promise; the wording must not turn
  // it into an ETA or a queue position.
  it("names capacity as the reason it waits, with the next try as an earliest time", () => {
    const p = pendingStartPresentation(
      start({ fire_at: at(-5), dispatch_attempts: 3, next_attempt_at: at(1), expires_at: at(60) }),
      NOW,
    )
    expect(p).toMatchObject({ label: "Waiting for capacity", tone: "warn" })
    expect(p.detail).toBe("Another run holds its slot; tried 3 times. Next try in 1 min at the earliest. Gives up in 1 h if no slot frees.")
    expect(p.detail).not.toMatch(/position|ETA|will start/i)
  })

  // A fired start's run link is written once the run is recorded; until then
  // the run may still be executing, so this is not a failure.
  it("never calls a fired start without a run link a failure", () => {
    const p = pendingStartPresentation(start({ status: "fired", run_id: "" }), NOW)
    expect(p.tone).toBe("blue")
    expect(p.detail).toMatch(/run link appears once the run is recorded/)
    expect(didNotRun(start({ status: "fired" }))).toBe(false)
  })

  it("shows the server's reason for a start that did not run", () => {
    const failed = start({ status: "failed", last_error: "The accepted recipe version is no longer available." })
    expect(pendingStartPresentation(failed, NOW)).toEqual({ label: "Did not run", tone: "danger", detail: "The accepted recipe version is no longer available." })
    const expired = start({ status: "expired", last_error: "Deferred start expired before execution capacity became available." })
    expect(pendingStartPresentation(expired, NOW)).toMatchObject({ label: "Expired", detail: expired.last_error })
    expect(didNotRun(failed) && didNotRun(expired)).toBe(true)
  })

  it("tells capacity waiting apart from waiting for its time", () => {
    expect(isWaitingForCapacity(start({ dispatch_attempts: 1 }))).toBe(true)
    expect(isWaitingForCapacity(start())).toBe(false)
    expect(isWaitingForCapacity(start({ status: "failed", dispatch_attempts: 10 }))).toBe(false)
  })
})
