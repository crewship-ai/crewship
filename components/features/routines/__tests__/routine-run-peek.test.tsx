import * as React from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { RoutinePreviousRuns, RoutineRunPeek, runSentence } from "../routine-run-peek"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

const run = (id: string, over: Partial<PipelineRunRecord> = {}): PipelineRunRecord => ({
  id,
  pipeline_id: "p1",
  pipeline_slug: "weekly",
  status: "completed",
  mode: "run",
  started_at: "2026-10-15T08:00:00Z",
  cost_usd: 0.31,
  duration_ms: 250_000,
  triggered_via: "schedule",
  pipeline_version: 4,
  ...over,
})

// Newest first, as the run list arrives.
const runs = [run("run-3", { status: "waiting" }), run("run-2", { outcome: "FAILED", status: "failed", error_message: "HTTP 401" }), run("run-1", { outcome: "NO_CHANGE" })]

function Harness({ onOpen }: { onOpen: (id: string) => void }) {
  const [id, setId] = React.useState("run-2")
  return <RoutineRunPeek workspaceId="ws" runs={runs} runId={id} onChange={setId} onClose={() => {}} onOpen={onOpen} />
}

describe("run peek", () => {
  it("says what a run did in one sentence", () => {
    expect(runSentence(run("x", { outcome: "NO_CHANGE" }))).toBe("Ran and found nothing new, so nothing was sent.")
    expect(runSentence(run("x", { status: "failed" }))).toBe("Could not finish. Nothing after the failed step ran.")
    expect(runSentence(run("x", { mode: "dry_run" }))).toMatch(/test run/)
  })

  it("J/K moves between runs, Enter opens the run page", () => {
    const onOpen = vi.fn()
    render(<Harness onOpen={onOpen} />)
    const peek = screen.getByTestId("run-peek")
    expect(peek).toHaveTextContent("Could not finish")
    expect(peek).toHaveTextContent("HTTP 401")
    const sheet = document.querySelector('[data-slot="peek"]') as HTMLElement
    fireEvent.keyDown(sheet, { key: "j" })
    expect(screen.getByTestId("run-peek")).toHaveTextContent("Nothing changed")
    fireEvent.keyDown(sheet, { key: "k" })
    fireEvent.keyDown(sheet, { key: "k" })
    expect(screen.getByTestId("run-peek")).toHaveTextContent("Waiting for a person")
    fireEvent.keyDown(sheet, { key: "Enter" })
    expect(onOpen).toHaveBeenCalledWith("run-3")
  })

  it("previous runs: oldest left, the current one marked, a click peeks", () => {
    const onPeek = vi.fn()
    render(<RoutinePreviousRuns runs={runs} current="run-2" onPeek={onPeek} />)
    const dots = screen.getAllByRole("listitem")
    expect(dots.map((d) => d.getAttribute("aria-label"))).toEqual([
      "Peek at run run-1 · Nothing changed",
      "Peek at run run-2 · Could not finish",
      "Peek at run run-3 · Waiting for a person",
    ])
    expect(dots[1]).toHaveAttribute("aria-current", "true")
    fireEvent.click(dots[0])
    expect(onPeek).toHaveBeenCalledWith("run-1")
    expect(screen.getByText(/1 of 3 could not finish/)).toBeInTheDocument()
  })
})
