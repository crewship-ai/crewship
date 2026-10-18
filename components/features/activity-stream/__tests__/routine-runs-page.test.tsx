/**
 * The page, at the level only the page can be wrong at.
 *
 * The bucketing, the labels, the median and the failure vocabulary are asserted
 * against the pure functions in lib/__tests__/run-digest.test.ts. What is left
 * for this file is the thing the pure layer cannot see: that the two places
 * this page counts the same fact — the hour header and the stat strip — reach
 * the screen agreeing, and that a routine on a daily cron reaches it as one
 * header per day rather than as one header repeated.
 *
 * Both defects were invisible to a unit test by construction. "2 runs · 1
 * failed" over "Failed 0" is only wrong when the two are rendered together, and
 * a duplicate React key is only wrong when React sees it.
 */

import * as React from "react"
import { render, screen, within } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

// Only the fetching hook is replaced. isActiveRunStatus, which the page also
// takes from this module, is the real one — it mirrors the backend's in-flight
// set, and a stubbed copy of it here would be this test agreeing with itself.
const hookState = vi.fn()
vi.mock("@/hooks/use-pipeline-run-records", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-pipeline-run-records")>()
  return { ...actual, usePipelineRunRecords: () => hookState() }
})

import { RoutineRunsPage } from "../routine-runs-page"

/**
 * A run record, minimal but of the real wire shape.
 *
 * Stamps are built from LOCAL components rather than parsed from a UTC string,
 * because the page's whole subject is the reader's clock: "09:00Z" is the
 * previous day west of Greenwich, which would make the multi-day assertions
 * pass or fail on where CI happens to run.
 */
const record = (over: Partial<PipelineRunRecord> = {}): PipelineRunRecord =>
  ({
    id: "run_a",
    pipeline_id: "pln_1",
    pipeline_slug: "triage",
    status: "completed",
    mode: "run",
    started_at: at(2026, 8, 10, 9, 0),
    cost_usd: 0,
    duration_ms: 200,
    triggered_via: "schedule",
    ...over,
  }) as PipelineRunRecord

const at = (y: number, m: number, d: number, h: number, min = 0) =>
  new Date(y, m - 1, d, h, min, 0, 0).toISOString()

/** The cell of the strip under a given label — label and value are siblings. */
const stat = (label: string) =>
  within(screen.getByText(label).parentElement as HTMLElement)

const renderPage = (records: PipelineRunRecord[]) => {
  hookState.mockReturnValue({
    records,
    loading: false,
    error: null,
    legacy: false,
    refresh: vi.fn(),
  })
  render(
    <RoutineRunsPage
      workspaceId="ws_1"
      slug="triage"
      label="Triage inbox"
      onOpenRun={vi.fn()}
    />,
  )
}

beforeEach(() => {
  hookState.mockReset()
})

describe("RoutineRunsPage", () => {
  it("counts the same failures in the header and in the strip", () => {
    // The defect, in one screen: the header folded `cancelled` in and the strip
    // matched `failed` alone, so the page said "1 failed" and "Failed 0" six
    // pixels apart. Asserted together because apart they were each defensible.
    renderPage([
      record({ id: "a", status: "completed", started_at: at(2026, 8, 10, 9, 1) }),
      record({ id: "b", status: "cancelled", started_at: at(2026, 8, 10, 9, 2) }),
    ])
    expect(screen.getByText("2 runs · 1 failed")).toBeInTheDocument()
    expect(stat("Failed").getByText("1")).toBeInTheDocument()
    expect(stat("Pass rate").getByText("50%")).toBeInTheDocument()
  })

  it("does not count a run that has not finished as a pass", () => {
    // One passed, one failed, one still going. The rate is 50% — one of the two
    // runs that reached a verdict — and NOT 67%, which is what counting the
    // in-flight run as a pass gives: a page crediting work that has not
    // happened, with a number that falls when it does.
    //
    // The third run is what gives this teeth. With only a pass and an in-flight
    // run both readings are 100% and the assertion proves nothing; a failure
    // has to be present for the denominators to disagree.
    renderPage([
      record({ id: "a", status: "completed", started_at: at(2026, 8, 10, 9, 1) }),
      record({ id: "b", status: "failed", started_at: at(2026, 8, 10, 9, 2) }),
      record({ id: "c", status: "running", started_at: at(2026, 8, 10, 9, 3), duration_ms: 40_000 }),
    ])
    expect(stat("Pass rate").getByText("50%")).toBeInTheDocument()
    // …and the in-flight run's partial duration is not the record either: 40s
    // is how far it has GOT. Reporting it as "Slowest" is a number that shrinks
    // when the page reloads.
    expect(stat("Slowest").getByText("200ms")).toBeInTheDocument()
  })

  it("gives a daily cron one header per day, each naming its own", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {})
    renderPage([
      record({ id: "mon", started_at: at(2026, 8, 10, 9, 0) }),
      record({ id: "sun", started_at: at(2026, 8, 9, 9, 0) }),
      record({ id: "sat", started_at: at(2026, 8, 8, 9, 0) }),
    ])
    // Three mornings, one time. The clock alone cannot tell them apart, which
    // is why the date is on the header at all.
    expect(screen.getAllByText("09:00")).toHaveLength(3)
    const days = [8, 9, 10].map((d) =>
      new Date(2026, 7, d).toLocaleDateString(undefined, {
        weekday: "short",
        day: "numeric",
        month: "short",
      }),
    )
    for (const day of days) expect(screen.getByText(day)).toBeInTheDocument()
    // And React saw three hours, not one hour three times. A duplicate key is
    // not a rendering failure — it is a warning and a reconciler that reuses
    // the wrong subtree — so the console is where the evidence is.
    const dupes = spy.mock.calls.filter((c) => String(c[0]).includes("same key"))
    expect(dupes).toEqual([])
    spy.mockRestore()
  })

  it("leaves the date off when every run is from the same day", () => {
    // The per-minute case, and the reason the label is bare: the date on thirty
    // headers is thirty repetitions of what the reader already knew.
    renderPage([
      record({ id: "a", started_at: at(2026, 8, 10, 14, 51) }),
      record({ id: "b", started_at: at(2026, 8, 10, 13, 2) }),
    ])
    const day = new Date(2026, 7, 10).toLocaleDateString(undefined, {
      weekday: "short",
      day: "numeric",
      month: "short",
    })
    expect(screen.queryByText(day)).toBeNull()
  })

  it("reports the newest run even when the server hands them over oldest first", () => {
    // The list arrives newest-first today. "The first element" is an assertion
    // about somebody else's ORDER BY, and when it changes the page does not
    // break — it quietly labels the oldest run "Newest".
    renderPage([
      record({ id: "old", started_at: at(2026, 8, 10, 9, 0) }),
      record({ id: "new", started_at: at(2026, 8, 10, 17, 30) }),
    ])
    const expected = new Date(2026, 7, 10, 17, 30).toLocaleTimeString(undefined, { hour12: false })
    expect(stat("Newest").getByText(expected)).toBeInTheDocument()
  })
})
