// The panel's views beside Overview: Needs you, Running now, All runs and
// Drafts. Each answers one question from data the server already returns.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import { RoutinesViewPane, needsYou } from "../routines-views"
import type { Pipeline } from "@/hooks/use-pipelines"

const h = vi.hoisted(() => ({ active: [] as unknown[], runs: [] as unknown[] }))
vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return { ...mod, useActiveRoutineRuns: () => ({ bySlug: new Map(), runs: h.active, activeCount: h.active.length }) }
})
vi.mock("@/hooks/use-pipeline-runs", () => ({ usePipelineRuns: () => ({ runs: h.runs, loading: false, error: null, refresh: vi.fn() }) }))

const p = (over: Partial<Pipeline>) =>
  ({ id: over.slug, slug: "x", name: "X", invocation_count: 1, head_version: 1, status: "active", last_invocation_status: "completed", ...over }) as Pipeline
const routines = [
  p({ slug: "weekly", name: "Weekly report" }),
  p({ slug: "invoice", name: "Invoice intake", status: "proposed" }),
  p({ slug: "news", name: "News digest", last_invocation_status: "failed" }),
  p({ slug: "cert", name: "Cert check", status: "disabled" }),
]
const run = (over: Record<string, unknown>) => ({ id: "r1", pipeline_slug: "weekly", pipeline_name: "Weekly report", status: "running", started_at: new Date().toISOString(), current_step_id: "draft", triggered_via: "schedule", ...over })

beforeEach(() => {
  h.active = []
  h.runs = []
})
afterEach(cleanup)

const base = { workspaceId: "ws", routines, drafts: [], onSelect: vi.fn(), onOpenRun: vi.fn() }

describe("needsYou", () => {
  it("counts runs waiting for a person, proposed, failing and turned-off routines", () => {
    const items = needsYou(routines, [run({ status: "waiting" }), run({ id: "r2", status: "running" })] as never)
    expect(items.map((i) => [i.slug, i.reason])).toEqual([
      ["weekly", "Waiting for a person"],
      ["invoice", "Proposed · waiting for a Manager review"],
      ["news", "Could not finish"],
      ["cert", "Turned off"],
    ])
  })
})

describe("<RoutinesViewPane>", () => {
  it("Needs you opens the waiting run, and the routine for the rest", () => {
    h.active = [run({ status: "waiting" })]
    const onOpenRun = vi.fn()
    const onSelect = vi.fn()
    render(<RoutinesViewPane {...base} view="needs" onOpenRun={onOpenRun} onSelect={onSelect} />)
    fireEvent.click(screen.getByRole("button", { name: /Weekly report.*Waiting for a person/ }))
    expect(onOpenRun).toHaveBeenCalledWith("weekly", "r1")
    fireEvent.click(screen.getByRole("button", { name: /Invoice intake/ }))
    expect(onSelect).toHaveBeenCalledWith("invoice")
  })

  it("Running now lists live runs and says when there are none", () => {
    render(<RoutinesViewPane {...base} view="live" />)
    expect(screen.getByText("Nothing is running")).toBeInTheDocument()
    cleanup()
    h.active = [run({})]
    render(<RoutinesViewPane {...base} view="live" />)
    expect(screen.getByRole("button", { name: /Weekly report/ })).toHaveTextContent("draft")
  })

  it("All runs lists runs across routines", () => {
    h.runs = [run({ status: "completed", id: "r9" }), run({ id: "r8", status: "failed", pipeline_slug: "news", pipeline_name: "News digest" })]
    const onOpenRun = vi.fn()
    render(<RoutinesViewPane {...base} view="runs" onOpenRun={onOpenRun} />)
    fireEvent.click(screen.getByRole("button", { name: /News digest/ }))
    expect(onOpenRun).toHaveBeenCalledWith("news", "r8")
  })

  it("Drafts tells a new routine from changes to a live one", () => {
    const onSelect = vi.fn()
    render(
      <RoutinesViewPane
        {...base}
        view="drafts"
        onSelect={onSelect}
        drafts={[{ slug: "weekly", revision: 3, updated_at: "2026-10-16T10:00:00Z" }, { slug: "brand-new", revision: 1, updated_at: "2026-10-16T10:00:00Z" }]}
      />,
    )
    expect(screen.getByRole("button", { name: /Weekly report/ })).toHaveTextContent("Changes not live")
    expect(screen.getByRole("button", { name: /brand-new/ })).toHaveTextContent("Not live yet")
    fireEvent.click(screen.getByRole("button", { name: /brand-new/ }))
    expect(onSelect).toHaveBeenCalledWith("brand-new")
  })
})
