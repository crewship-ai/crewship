import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"

import { RoutinesExplorer } from "../routines-explorer"
import type { Pipeline } from "@/hooks/use-pipelines"

const h = vi.hoisted(() => ({ live: new Map<string, unknown>() }))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })), broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return { ...mod, useActiveRoutineRuns: () => ({ bySlug: h.live, runs: [], activeCount: 0 }) }
})

// The status buckets moved into the Filter panel's "Last run" facet
// (Harbor 2); the panel's own chrome is pinned in routines-panel.test.tsx.

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
  filters: {
    status: "all" as const,
    invocations: "all" as const,
    authorAgentId: null,
    showEphemeral: false,
  },
  onChange: vi.fn(),
  view: "overview" as const,
  onViewChange: vi.fn(),
  counts: { needs: null, live: null, drafts: null },
  crews: new Map(),
}

// The counts and the list disagreed.
//
// The buckets were computed from the live-run feed — a run parked on a
// human is "awaiting", a running one is "running" — but `displayed`
// still filtered on `last_invocation_status`, which reads "running"
// for both. So Awaiting approval could show a count of 1 above a list
// of nothing, and Running quietly included the parked one.
//
// The layout already had the right answer in matchesRoutineFilters;
// the explorer had its own copy that never learned about live runs.
// Found by CodeRabbit on the PR.

describe("<RoutinesExplorer> live buckets filter the list, not just the counts", () => {
  beforeEach(() => {
    h.live = new Map()
  })

  it("shows the parked routine under Awaiting approval", () => {
    h.live = new Map([["nightly", { status: "waiting" }]])
    render(
      <RoutinesExplorer
        {...PROPS}
        filters={{ ...PROPS.filters, status: "awaiting" }}
        routines={[pipeline({ last_invocation_status: "running" })]}
      />,
    )
    expect(screen.getByText("Nightly")).toBeInTheDocument()
  })

  it("keeps the parked routine OUT of Running", () => {
    // last_invocation_status says "running" while it is parked, which
    // is exactly why the row alone cannot answer this.
    h.live = new Map([["nightly", { status: "waiting" }]])
    render(
      <RoutinesExplorer
        {...PROPS}
        filters={{ ...PROPS.filters, status: "running" }}
        routines={[pipeline({ last_invocation_status: "running" })]}
      />,
    )
    expect(screen.queryByText("Nightly")).not.toBeInTheDocument()
  })

  it("shows a genuinely running routine under Running", () => {
    h.live = new Map([["nightly", { status: "running" }]])
    render(
      <RoutinesExplorer
        {...PROPS}
        filters={{ ...PROPS.filters, status: "running" }}
        routines={[pipeline({ last_invocation_status: "completed" })]}
      />,
    )
    expect(screen.getByText("Nightly")).toBeInTheDocument()
  })
})
