import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { RoutinesExplorer } from "../routines-explorer"
import type { Pipeline } from "@/hooks/use-pipelines"

const h = vi.hoisted(() => ({ live: new Map<string, unknown>() }))

vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return { ...mod, useActiveRoutineRuns: () => ({ bySlug: h.live, runs: [], activeCount: 0 }) }
})

// The sidebar chrome, which read as flat: six status buckets of equal
// weight with four of them at zero, and a filter menu whose chosen
// option was distinguished by text colour alone.

function pipeline(over: Partial<Pipeline> = {}): Pipeline {
  return {
    id: "p1",
    slug: "nightly",
    name: "Nightly",
    dsl_version: "1.0",
    definition_hash: "h",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 3,
    last_invocation_status: "completed",
    authored_via: "user_api",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...over,
  } as Pipeline
}

const PROPS = {
  search: "",
  onSearchChange: vi.fn(),
  selectedSlug: null,
  onSelectRoutine: vi.fn(),
  filters: { status: "all" as const, invocations: "all" as const, authorAgentId: null, showEphemeral: false },
  onChange: vi.fn(),
}

describe("<RoutinesExplorer> chrome", () => {
  beforeEach(() => {
    h.live = new Map()
  })

  it("dims a status bucket that holds nothing", () => {
    // Four of six buckets sit at zero on a fresh workspace. Rendering
    // them at the same weight as the ones with content is what made
    // the column a wall of identical rows.
    render(<RoutinesExplorer {...PROPS} routines={[pipeline({})]} />)
    const empty = screen.getByText("Failed").className
    const filled = screen.getByText("Completed").className
    expect(empty).toContain("text-foreground/40")
    expect(filled).toContain("text-foreground/80")
  })

  it("ticks the chosen filter instead of only recolouring it", () => {
    render(<RoutinesExplorer {...PROPS} routines={[pipeline({})]} />)
    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    const menu = screen.getByRole("menu")
    const chosen = within(menu).getByText("All usage").closest("button")!
    // A colour difference alone is not a state a reader can name.
    expect(chosen.className).toContain("bg-primary/10")
    expect(chosen.querySelector("svg")).toBeTruthy()
  })

  it("keeps the live sub-line on a running routine", () => {
    h.live = new Map([["nightly", { status: "running", current_step_id: "draft", started_at: new Date().toISOString() }]])
    render(<RoutinesExplorer {...PROPS} routines={[pipeline({})]} />)
    expect(screen.getByText(/draft/)).toBeInTheDocument()
  })
})
