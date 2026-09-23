import { describe, expect, it } from "vitest"
import fixture from "../__fixtures__/run-evidence.json"
import { buildRunEvidence, formatRunEvidence, RUN_EVIDENCE_MAX_BYTES, RUN_EVIDENCE_MAX_EVENTS, type RunEvidenceInput } from "../run-evidence"
import type { JournalEntry } from "../types/journal"

const base = fixture.input as RunEvidenceInput
const entry = (id: string, type: string, ts = "2026-09-23T07:00:00Z", runId = "run_123"): JournalEntry => ({
  id, workspace_id: "ws_1", ts, entry_type: type, severity: "info", actor_type: "agent",
  summary: "SECRET_IN_SUMMARY", trace_id: runId,
  payload: { run_id: runId, prompt: "SECRET_IN_PROMPT", output: "SECRET_IN_OUTPUT" },
})

describe("run evidence contract", () => {
  it("matches the shared JSON fixture and never exports free text", () => {
    const actual = buildRunEvidence(base)
    expect(actual).toEqual(fixture.expected)
    const text = formatRunEvidence(actual)
    expect(text).not.toMatch(/SECRET_IN_|prompt|error_message|output/i)
  })

  it("drops unknown events and never falls back to their summary or payload", () => {
    const view = buildRunEvidence({ ...base, entries: [entry("evt_unknown", "future.secret_event")] })
    expect(view.evidence).toEqual([])
    expect(view.unavailableReasons).toContain("Some events were not correlated or approved for export")
    expect(formatRunEvidence(view)).not.toContain("SECRET_IN_")
  })

  it("does not mix two runs of the same agent or export an unbound event", () => {
    const view = buildRunEvidence({ ...base, entries: [
      entry("evt_own", "run.started"),
      entry("evt_other", "run.failed", "2026-09-23T07:01:00Z", "run_other"),
      { ...entry("evt_unbound", "run.failed"), trace_id: undefined, payload: undefined },
    ] })
    expect(view.evidence.map((e) => e.reference)).toEqual(["evt_own"])
  })

  it("preserves terminal evidence and enforces event and final UTF-8 byte limits", () => {
    const entries = Array.from({ length: 50 }, (_, i) => entry(`evt_${i.toString().padStart(2, "0")}`, "pipeline.step.completed", `2026-09-23T07:00:${i.toString().padStart(2, "0")}Z`))
    entries[1] = entry("evt_terminal", "pipeline.run.failed", "2026-09-23T07:00:01Z")
    const view = buildRunEvidence({ ...base, entries })
    expect(view.evidence.length).toBeLessThanOrEqual(RUN_EVIDENCE_MAX_EVENTS)
    expect(view.evidence.some((e) => e.reference === "evt_terminal")).toBe(true)
    expect(view.truncated).toBe(true)
    expect(new TextEncoder().encode(formatRunEvidence(view)).length).toBeLessThanOrEqual(RUN_EVIDENCE_MAX_BYTES)
  })

  it("admits only known statuses and reference-shaped identifiers", () => {
    const view = buildRunEvidence({ ...base, status: "SECRET_IN_STATUS", stepId: "SECRET_IN_STEP <token>", failureKind: "SECRET_IN_KIND" })
    expect(view.status).toBe("unknown")
    expect(view.stepId).toBeUndefined()
    expect(view.failureKind).toBeUndefined()
    expect(() => buildRunEvidence({ ...base, runId: "bad?prompt=SECRET_IN_URL" })).toThrow("Invalid run reference")
  })

  it("acknowledges journal gaps without claiming the agent never ran", () => {
    const view = buildRunEvidence({ ...base, entries: [], journalUnavailable: true })
    expect(view.unavailableReasons).toContain("Journal could not be read")
    expect(formatRunEvidence(view)).not.toMatch(/never ran|did not run/)
  })

  it("makes interrupted-versus-running journal contradictions explicit", () => {
    const interrupted = buildRunEvidence({ ...base, status: "interrupted", entries: [entry("evt_start", "pipeline.run.started")] })
    expect(interrupted.unavailableReasons).toContain("Run record is terminal, but no terminal journal event was recorded")
    const stillRunning = buildRunEvidence({ ...base, status: "running", entries: [entry("evt_done", "pipeline.run.completed")] })
    expect(stillRunning.unavailableReasons).toContain("Journal has a terminal event while the run record remains active")
  })

  it("orders whole-second and fractional RFC3339 timestamps by instant", () => {
    const view = buildRunEvidence({ ...base, entries: [
      entry("evt_later", "run.completed", "2026-09-23T07:00:00.500Z"),
      entry("evt_first", "run.started", "2026-09-23T07:00:00Z"),
    ] })
    expect(view.evidence.map((e) => e.reference)).toEqual(["evt_first", "evt_later"])
    expect(view.lastRecordedAt).toBe("2026-09-23T07:00:00.500Z")
  })
})
