// The Routines secondary panel (Harbor 2): Views first, then the routines
// grouped by their crew, one status pill per row, a pencil for changes that
// are not live, and Filter facets that agree with the groups.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, within, cleanup } from "@testing-library/react"

import { RoutinesExplorer, routineRowState } from "../routines-explorer"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { RoutineFilterState } from "@/lib/routine-filters"

const h = vi.hoisted(() => ({ live: new Map<string, unknown>() }))
vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return { ...mod, useActiveRoutineRuns: () => ({ bySlug: h.live, runs: [], activeCount: 0 }) }
})
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })), broadcastSessionExpired: vi.fn() }))

function pipeline(over: Partial<Pipeline>): Pipeline {
  return {
    id: over.slug ?? "p",
    slug: "p",
    name: "P",
    dsl_version: "1.0",
    definition_hash: "h",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 1,
    last_invocation_status: "completed",
    authored_via: "user_api",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    status: "active",
    ...over,
  } as Pipeline
}

const routines = [
  pipeline({ slug: "weekly", name: "Weekly report", author_crew_id: "crew_res", draft: { id: "d", revision: 2, updated_at: "" } }),
  pipeline({ slug: "news", name: "News digest", author_crew_id: "crew_res", last_invocation_status: "failed" }),
  pipeline({ slug: "invoice", name: "Invoice intake", author_crew_id: "crew_fin", status: "proposed" }),
  pipeline({ slug: "cert", name: "Cert check", author_crew_id: "crew_ops", status: "disabled" }),
  pipeline({ slug: "orphan", name: "Orphan", author_crew_id: undefined, invocation_count: 0, last_invocation_status: undefined }),
]
const crews = new Map([
  ["crew_res", { id: "crew_res", name: "Research" }],
  ["crew_fin", { id: "crew_fin", name: "Finance" }],
  ["crew_ops", { id: "crew_ops", name: "Ops" }],
])
const filters: RoutineFilterState = { status: "all", invocations: "all", authorAgentId: null, showEphemeral: false }

function renderPanel(over: Partial<React.ComponentProps<typeof RoutinesExplorer>> = {}) {
  const props = {
    routines,
    search: "",
    onSearchChange: vi.fn(),
    selectedSlug: null,
    onSelectRoutine: vi.fn(),
    filters,
    onChange: vi.fn(),
    view: "overview" as const,
    onViewChange: vi.fn(),
    counts: { needs: 4, live: 1, drafts: 2 },
    crews,
    ...over,
  }
  render(<RoutinesExplorer {...props} />)
  return props
}

beforeEach(() => {
  h.live = new Map()
  window.localStorage.clear()
})
afterEach(cleanup)

describe("<RoutinesExplorer> Harbor 2 panel", () => {
  it("puts the Views first, in order, with their counts", () => {
    const props = renderPanel()
    const views = screen.getByRole("group", { name: "Views" })
    const rows = within(views).getAllByRole("button")
    expect(rows.map((r) => r.textContent)).toEqual(["Overview", "Needs you4", "Running now1", "All runs", "Drafts2"])
    expect(rows[0]).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(rows[1])
    expect(props.onViewChange).toHaveBeenCalledWith("needs")
  })

  it("groups routines by crew and shows draft and state badges", () => {
    renderPanel()
    const research = screen.getByRole("group", { name: "Research" })
    expect(within(research).getByText("Weekly report")).toBeInTheDocument()
    expect(within(research).getByText("News digest")).toBeInTheDocument()
    expect(within(research).getByLabelText("Changes not live")).toBeInTheDocument()
    expect(within(research).getByText("Failed")).toBeInTheDocument()
    expect(within(screen.getByRole("group", { name: "Finance" })).getByText("Proposed")).toBeInTheDocument()
    expect(within(screen.getByRole("group", { name: "Ops" })).getByText("Off")).toBeInTheDocument()
    expect(within(screen.getByRole("group", { name: "No crew" })).getByText("Orphan")).toBeInTheDocument()
    // One pill per row, never a dot plus a chip.
    for (const pill of document.querySelectorAll('[data-slot="status-pill"]')) expect(pill.closest("[data-routine-row]")).toBeTruthy()
  })

  it("a live run outranks the last result", () => {
    h.live = new Map([["news", { status: "waiting" }], ["weekly", { status: "running" }]])
    renderPanel()
    expect(within(screen.getByRole("group", { name: "Research" })).getByText("Waiting")).toBeInTheDocument()
    expect(within(screen.getByRole("group", { name: "Research" })).getByText("Running")).toBeInTheDocument()
  })

  it("filter facets count Status, Last run and Crew", () => {
    const props = renderPanel()
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    const panel = screen.getByRole("group", { name: "Filter routines" })
    expect(within(panel).getByRole("button", { name: /Active\s*3/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Has draft changes\s*1/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Proposed\s*1/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Turned off\s*1/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Could not finish\s*1/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Never run\s*1/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Research\s*2/ })).toBeInTheDocument()
    fireEvent.click(within(panel).getByRole("button", { name: /Proposed\s*1/ }))
    expect(props.onChange).toHaveBeenCalledWith({ ...filters, lifecycle: "proposed" })
    fireEvent.click(within(panel).getByRole("button", { name: /Finance\s*1/ }))
    expect(props.onChange).toHaveBeenCalledWith({ ...filters, crewId: "crew_fin" })
  })

  it("an active facet shows as a removable chip named after it", () => {
    const props = renderPanel({ filters: { ...filters, lifecycle: "proposed", crewId: "crew_fin" } })
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Proposed" }))
    expect(props.onChange).toHaveBeenCalledWith({ ...filters, lifecycle: "all", crewId: "crew_fin" })
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Finance" }))
    expect(props.onChange).toHaveBeenCalledWith({ ...filters, lifecycle: "proposed", crewId: null })
  })

  it("selecting a routine reports its slug and marks the row", () => {
    const props = renderPanel({ selectedSlug: "invoice", view: null })
    expect(within(screen.getByRole("group", { name: "Views" })).getByRole("button", { name: "Overview" })).toHaveAttribute("aria-pressed", "false")
    fireEvent.click(screen.getByText("News digest"))
    expect(props.onSelectRoutine).toHaveBeenCalledWith("news")
  })
})

describe("routineRowState", () => {
  it.each([
    [{ status: "proposed" }, null, "Proposed"],
    [{ status: "disabled" }, null, "Off"],
    [{ last_invocation_status: "failed" }, null, "Failed"],
    [{ last_run_outcome: "FAILED" }, null, "Failed"],
    [{}, { status: "waiting" }, "Waiting"],
    [{ status: "proposed" }, { status: "running" }, "Running"],
    [{}, null, null],
  ] as const)("%j live=%j → %s", (row, live, label) => {
    expect(routineRowState(pipeline(row as Partial<Pipeline>), live)?.label ?? null).toBe(label)
  })
})
