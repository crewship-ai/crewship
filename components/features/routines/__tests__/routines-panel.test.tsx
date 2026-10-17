// The Routines secondary panel (Harbor 2): Views first, then the routines
// grouped by their crew, one status pill per row, a pencil for changes that
// are not live, and Filter facets that agree with the groups.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, within, cleanup, waitFor } from "@testing-library/react"

import { RoutinesExplorer, routineRowState } from "../routines-explorer"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { RoutineFilterState } from "@/lib/routine-filters"

const h = vi.hoisted(() => ({ live: new Map<string, unknown>(), prefs: {} as Record<string, unknown> }))
vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return { ...mod, useActiveRoutineRuns: () => ({ bySlug: h.live, runs: [], activeCount: 0 }) }
})
// GET /api/v1/me/preferences answers with h.prefs: the panel's group and sort
// are user preferences (the test setup stubs localStorage out).
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => ({ ok: true, status: 200, json: async () => (url === "/api/v1/me/preferences" ? h.prefs : {}) })),
  broadcastSessionExpired: vi.fn(),
}))

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

function baseProps(): React.ComponentProps<typeof RoutinesExplorer> {
  return {
    routines,
    search: "",
    onSearchChange: vi.fn(),
    selectedSlug: null,
    onSelectRoutine: vi.fn(),
    filters,
    onChange: vi.fn(),
    view: "overview",
    onViewChange: vi.fn(),
    counts: { needs: 4, live: 1, drafts: 2 },
    crews,
  }
}

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
  h.prefs = {}
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

describe("<RoutinesExplorer> triggers, next run and spend (RT-02)", () => {
  // Composed in the browser from the schedule, webhook and budget lists and
  // the chat command catalog (lib/routine-panel-index).
  const index = {
    triggers: new Map<string, ("schedule" | "webhook" | "issue" | "chat")[]>([
      ["weekly", ["schedule", "chat"]],
      ["news", ["schedule"]],
      ["invoice", ["webhook", "issue"]],
    ]),
    nextRun: new Map([
      ["weekly", "2026-10-22T08:00:00Z"],
      ["news", "2026-10-18T07:30:00Z"],
    ]),
    spend: new Map([
      ["invoice", 4.5],
      ["weekly", 12],
    ]),
  }

  it("the Trigger facet counts Schedule, Webhook, Issue start and Chat command", () => {
    const props = renderPanel({ index })
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    const panel = screen.getByRole("group", { name: "Filter routines" })
    expect(within(panel).getByRole("button", { name: /Schedule\s*2/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Webhook\s*1/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Issue start\s*1/ })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /Chat command\s*1/ })).toBeInTheDocument()
    fireEvent.click(within(panel).getByRole("button", { name: /Webhook\s*1/ }))
    expect(props.onChange).toHaveBeenCalledWith({ ...filters, trigger: "webhook" })
  })

  it("a Trigger filter keeps only the routines that start that way, and shows as a chip", () => {
    const props = renderPanel({ index, filters: { ...filters, trigger: "schedule" } })
    expect(screen.getByText("Weekly report")).toBeInTheDocument()
    expect(screen.getByText("News digest")).toBeInTheDocument()
    expect(screen.queryByText("Invoice intake")).not.toBeInTheDocument()
    expect(screen.queryByText("Orphan")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Schedule" }))
    expect(props.onChange).toHaveBeenCalledWith({ ...filters, trigger: null })
  })

  it("View offers Group by Trigger and Sort by Next run and Spend", async () => {
    renderPanel({ index })
    fireEvent.pointerDown(screen.getByRole("button", { name: "View: group and sort" }), { button: 0, ctrlKey: false })
    expect(await screen.findByRole("menuitemradio", { name: "Trigger" })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: "Next run" })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: "Spend" })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: "Last run" })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: "Name" })).toBeInTheDocument()
  })

  it("Group by Trigger puts each routine under how it starts", async () => {
    h.prefs = { "routines.panel.group": "trigger" }
    renderPanel({ index })
    expect(within(await screen.findByRole("group", { name: "Schedule" })).getAllByText(/Weekly report|News digest/)).toHaveLength(2)
    expect(within(screen.getByRole("group", { name: "Webhook" })).getByText("Invoice intake")).toBeInTheDocument()
    const manual = screen.getByRole("group", { name: "Only when someone presses Run" })
    expect(within(manual).getByText("Cert check")).toBeInTheDocument()
    expect(within(manual).getByText("Orphan")).toBeInTheDocument()
  })

  it("Sort by Next run puts the soonest start first; Spend puts the most spent first", async () => {
    h.prefs = { "routines.panel.group": "none", "routines.panel.sort": "next" }
    const { unmount } = render(<RoutinesExplorer {...baseProps()} index={index} />)
    const order = () => Array.from(document.querySelectorAll("[data-routine-row]")).map((el) => el.textContent?.replace(/(Failed|Proposed|Off)$/, ""))
    await waitFor(() => expect(order().slice(0, 2)).toEqual(["News digest", "Weekly report"]))
    unmount()
    h.prefs = { "routines.panel.group": "none", "routines.panel.sort": "spend" }
    render(<RoutinesExplorer {...baseProps()} index={index} />)
    await waitFor(() => expect(order().slice(0, 2)).toEqual(["Weekly report", "Invoice intake"]))
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
