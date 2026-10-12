import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

const mock = vi.hoisted(() => ({
  fetch: vi.fn(),
  decide: vi.fn(),
  waitpoints: [] as unknown[],
  refreshWaitpoints: vi.fn(),
  role: "OWNER" as string | null,
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: mock.fetch }))
vi.mock("@/lib/api/waitpoints", () => ({ waitpointDecide: mock.decide }))
vi.mock("@/hooks/use-run-waitpoints", () => ({
  useWorkspaceWaitpoints: () => ({ waitpoints: mock.waitpoints, refresh: mock.refreshWaitpoints }),
}))
vi.mock("@/hooks/use-journal-spend", () => ({ useJournalSpend: () => ({ data: { total_cost_usd: 0.42 } }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: mock.role }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { ActivityHome } from "../activity-home"

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
const runs = [
  { id: "r_live", pipeline_slug: "check", pipeline_name: "Check deliveries", status: "running", started_at: ago(1), duration_ms: 0, current_step_id: "query" },
  { id: "r_fail", pipeline_slug: "repair", pipeline_name: "Repair demo delivery", status: "failed", started_at: ago(22), duration_ms: 12000, error_message: "HTTP 502 Bad Gateway", failed_at_step: "post", triggered_via: "webhook" },
  { id: "r_ok", pipeline_slug: "telemetry", pipeline_name: "Refresh telemetry", status: "completed", started_at: ago(120), duration_ms: 4700, triggered_via: "schedule" },
]
const groups = [
  { fingerprint: "now", count: 9, pipeline_slug: "repair", failed_at_step: "post", sample_error: "HTTP 502 Bad Gateway", run_ids: ["r_fail"] },
  { fingerprint: "fixed", count: 30, pipeline_slug: "legacy", failed_at_step: "x", sample_error: "old", run_ids: ["r_ancient"] },
]

beforeEach(() => {
  vi.clearAllMocks()
  mock.role = "OWNER"
  mock.waitpoints = [
    { token: "tok", pipeline_run_id: "r_wait", step_id: "s4", kind: "approval", prompt: "Send the payment reminder for INV-2041?", timeout_at: "", created_at: ago(4) },
  ]
  mock.decide.mockResolvedValue({ ok: true })
  mock.fetch.mockImplementation(async (url: string) => {
    if (url.includes("/pipeline-runs?")) return new Response(JSON.stringify({ rows: runs }))
    if (url.includes("/runs/errors")) return new Response(JSON.stringify({ groups }))
    if (url.includes("/pipelines/calendar")) {
      return new Response(JSON.stringify({ events: [{ kind: "planned", at: new Date(Date.now() + 12 * 60_000).toISOString(), slug: "telemetry", name: "Refresh telemetry" }] }))
    }
    return new Response("{}")
  })
})
afterEach(cleanup)

const mount = (over: Partial<React.ComponentProps<typeof ActivityHome>> = {}) =>
  render(<ActivityHome workspaceId="ws" chains={[]} onOpenRun={vi.fn()} onOpenIssue={vi.fn()} {...over} />)

describe("ActivityHome", () => {
  it("sums the day up in one sentence", async () => {
    mount()
    const summary = screen.getByLabelText("Summary")
    await waitFor(() => expect(summary).toHaveTextContent("3 runs·1 running·1 needs you·1 could not finish·$0.42"))
  })

  it("lets a manager approve a plain decision from the card, as in Inbox", async () => {
    mount()
    const needs = screen.getByRole("region", { name: "Needs you" })
    fireEvent.click(within(needs).getByRole("button", { name: "Approve" }))
    await waitFor(() => expect(mock.decide).toHaveBeenCalledWith("ws", "tok", true))
    expect(mock.refreshWaitpoints).toHaveBeenCalled()
  })

  it("sends a member, and any decision with a form, to the run instead", () => {
    mock.role = "MEMBER"
    const onOpenRun = vi.fn()
    mount({ onOpenRun })
    const needs = screen.getByRole("region", { name: "Needs you" })
    expect(within(needs).queryByRole("button", { name: "Approve" })).toBeNull()
    fireEvent.click(within(needs).getByRole("button", { name: "Open run" }))
    expect(onOpenRun).toHaveBeenCalledWith("r_wait")
  })

  it("draws every run as a bar that opens it", async () => {
    const onOpenRun = vi.fn()
    mount({ onOpenRun })
    const ran = screen.getByRole("region", { name: "What ran" })
    fireEvent.click(await within(ran).findByRole("button", { name: /Repair demo delivery: Could not finish/ }))
    expect(onOpenRun).toHaveBeenCalledWith("r_fail")
  })

  it("lists only problems still happening, and opens the latest failure", async () => {
    const onOpenRun = vi.fn()
    mount({ onOpenRun })
    const problems = screen.getByRole("region", { name: "Problems" })
    const row = await within(problems).findByRole("button", { name: /Repair demo delivery/ })
    expect(within(problems).queryByText(/legacy/)).toBeNull()
    fireEvent.click(row)
    expect(onOpenRun).toHaveBeenCalledWith("r_fail")
  })

  it("tells each run's outcome in one line and shows what fires next", async () => {
    mount()
    const latest = screen.getByRole("region", { name: "Latest runs" })
    expect(await within(latest).findByText("HTTP 502 Bad Gateway")).toBeInTheDocument()
    expect(within(latest).getByText("Finished in 4.7s")).toBeInTheDocument()
    const now = screen.getByRole("region", { name: "Right now" })
    expect(await within(now).findByText("in 12 min")).toBeInTheDocument()
  })

  it("narrows to the rail's chains and says so in the heading (#3002)", async () => {
    mount({ scope: { origins: new Set(["r_ok"]), label: "Operations" } })
    expect(screen.getByRole("heading", { name: /Today · Operations/ })).toBeInTheDocument()
    // The workspace spend ($0.42) is not this scope's cost.
    expect(screen.getByLabelText("Summary")).not.toHaveTextContent("$0.42")
    const latest = screen.getByRole("region", { name: "Latest runs" })
    expect(await within(latest).findByText("Finished in 4.7s")).toBeInTheDocument()
    expect(within(latest).queryByText("HTTP 502 Bad Gateway")).toBeNull()
    // The waiting ask belongs to a run outside the scope.
    expect(within(screen.getByRole("region", { name: "Needs you" })).getByText("Nothing is waiting for you.")).toBeInTheDocument()
  })
})
