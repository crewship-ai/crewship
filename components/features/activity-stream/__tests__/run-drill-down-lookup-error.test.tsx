/**
 * A failed journal lookup is not "no routine claims this run".
 *
 * RunDrillDown asks the journal which routine ran a bare `?run=`; when that
 * request fails it used to fall into the empty-string sentinel and the page
 * said the journal held nothing, with no way to try again (S6).
 */
import * as React from "react"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => <div data-testid="run-timeline" />,
}))
vi.mock("@/hooks/use-pipeline-run-records", () => ({
  usePipelineRunRecords: () => ({ records: [], loading: false, error: null, legacy: false, refresh: vi.fn() }),
}))
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { RunDrillDown } from "../drill-downs"

const journalCalls = () => apiFetch.mock.calls.filter(([url]) => String(url).startsWith("/api/v1/journal?")).length

describe("RunDrillDown routine lookup", () => {
  it("names the failure and offers a retry instead of claiming the journal is empty", async () => {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/journal?")) return { ok: false, status: 503, json: async () => ({}) }
      return { ok: false, status: 404, json: async () => ({}) }
    })
    render(<RunDrillDown workspaceId="ws_1" runID="run_1" />)

    await screen.findByText("Could not ask the journal which routine ran this")
    expect(screen.queryByText("No routine claims this run")).toBeNull()

    const before = journalCalls()
    fireEvent.click(screen.getByTestId("run-routine-lookup-retry"))
    await waitFor(() => expect(journalCalls()).toBe(before + 1))
  })

  it("still says no routine claims the run when the journal answers with nothing", async () => {
    apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/journal?")) return { ok: true, status: 200, json: async () => ({ entries: [] }) }
      return { ok: false, status: 404, json: async () => ({}) }
    })
    render(<RunDrillDown workspaceId="ws_1" runID="run_2" />)
    await screen.findByText("No routine claims this run")
    expect(screen.queryByTestId("run-routine-lookup-retry")).toBeNull()
  })
})
