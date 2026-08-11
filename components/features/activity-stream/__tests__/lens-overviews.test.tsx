/**
 * The lens dashboards, at the level only they can be wrong at.
 *
 * What a lens GROUPS is asserted against the pure functions in
 * lib/__tests__/activity-lenses.test.ts. What is left here is the arithmetic
 * these files do on top of those groups — every KPI is a ratio or a count over
 * some population, and picking the wrong population is a defect no amount of
 * correct grouping underneath prevents.
 */

import * as React from "react"
import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"

import { IssuesOverview, RoutinesLensOverview } from "../lens-overviews"

const chain = (over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin: "run_a",
  started_by_kind: "schedule",
  started_by: "nightly",
  runs: 1,
  max_chain_depth: 0,
  failed_runs: 0,
  failed: false,
  first_activity: "2026-08-10T10:00:00.000Z",
  last_activity: "2026-08-10T10:00:01.000Z",
  duration_ms: 1000,
  issue_count: 0,
  agent_count: 0,
  ...over,
})

/** The text of the KPI card carrying `label`, value included. */
function kpi(label: string): string {
  const el = screen.getByText(label).closest("div")
  return el?.parentElement?.textContent ?? el?.textContent ?? ""
}

describe("RoutinesLensOverview — the success ratio", () => {
  it("counts both sides of the ratio over the same population", () => {
    // routineLens keys on `routine_slug` and skips a chain without one — a
    // chain whose root run retention swept. The numerator used to be summed
    // over EVERY chain, so that orphan's failures were subtracted from a total
    // its runs were never counted in: four clean routine runs beside one
    // orphaned chain with two failures rendered "Success 50%", a red number
    // over a routine that had not failed once.
    render(
      <RoutinesLensOverview
        chains={[
          chain({ origin: "c1", routine_slug: "triage", runs: 4, failed_runs: 0 }),
          chain({ origin: "c2", routine_slug: undefined, runs: 2, failed_runs: 2, failed: true }),
        ]}
        routines={[{ id: "p1", slug: "triage", name: "Triage" }]}
        rangeLabel="Past 24 hours"
        catalogueCount={1}
        schedules={[]}
        onOpenRoutine={vi.fn()}
      />,
    )
    expect(kpi("Success")).toContain("100%")
    expect(kpi("Success")).toContain("4 of 4 runs")
  })

  it("still reports a real failure of a real routine", () => {
    // The fix must not be "ignore failures"; a mutation that dropped the
    // numerator entirely would pass the test above and fail this one.
    render(
      <RoutinesLensOverview
        chains={[chain({ origin: "c1", routine_slug: "triage", runs: 4, failed_runs: 1, failed: true })]}
        routines={[{ id: "p1", slug: "triage", name: "Triage" }]}
        rangeLabel="Past 24 hours"
        catalogueCount={1}
        schedules={[]}
        onOpenRoutine={vi.fn()}
      />,
    )
    expect(kpi("Success")).toContain("75%")
    expect(kpi("Failing")).toContain("1")
  })

  it("does not count a routine-less chain among the workflows routines affected", () => {
    render(
      <RoutinesLensOverview
        chains={[
          chain({ origin: "c1", routine_slug: "triage", runs: 1, failed_runs: 0 }),
          chain({ origin: "c2", routine_slug: undefined, runs: 1, failed_runs: 1, failed: true }),
        ]}
        routines={[{ id: "p1", slug: "triage", name: "Triage" }]}
        rangeLabel="Past 24 hours"
        catalogueCount={1}
        schedules={[]}
        onOpenRoutine={vi.fn()}
      />,
    )
    expect(kpi("Failing")).toContain("0 workflows affected")
  })
})

describe("IssuesOverview — the Touched denominator", () => {
  it("counts the workflows that reached an issue, not every workflow loaded", () => {
    // "12 issues touched by 40 workflows" on a window where five of the forty
    // went near an issue. A subtitle on a KPI is read as its explanation, so a
    // number answering a different question is a wrong one.
    render(
      <IssuesOverview
        chains={[
          chain({ origin: "c1", issue_count: 1, issues: [{ id: "m1", identifier: "ENG-1" }] }),
          chain({ origin: "c2" }),
          chain({ origin: "c3" }),
        ]}
        rangeLabel="Past 24 hours"
        issueMeta={new Map()}
        onOpenEntity={vi.fn()}
        onOpenWorkflow={vi.fn()}
      />,
    )
    expect(kpi("Touched")).toContain("by 1 workflow")
  })

  it("counts a workflow once however many issues it reached", () => {
    render(
      <IssuesOverview
        chains={[
          chain({
            origin: "c1",
            issue_count: 2,
            issues: [{ id: "m1", identifier: "ENG-1" }, { id: "m2", identifier: "ENG-2" }],
          }),
        ]}
        rangeLabel="Past 24 hours"
        issueMeta={new Map()}
        onOpenEntity={vi.fn()}
        onOpenWorkflow={vi.fn()}
      />,
    )
    expect(kpi("Touched")).toContain("by 1 workflow")
  })
})
