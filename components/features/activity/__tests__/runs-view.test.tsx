import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
const mocks = vi.hoisted(() => ({ search: new URLSearchParams() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => mocks.search }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => undefined }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
import { apiFetch } from "@/lib/api-fetch"
import { RunsView } from "../runs-view"
const ok = (body: unknown) => new Response(JSON.stringify(body))
function run(overrides: Partial<PipelineRun> = {}): PipelineRun {
  return { id: "run-one", pipeline_id: "pipeline", pipeline_slug: "sample", pipeline_name: "Sample routine", status: "completed", mode: "normal", started_at: "2026-10-03T08:00:00Z", ended_at: "2026-10-03T08:00:01Z", current_step_id: "", cost_usd: 0, duration_ms: 0, triggered_via: "manual", triggered_by_id: "", invoking_crew_id: "", invoking_agent_id: "", invoking_user_id: "", error_message: "", failed_at_step: "", issue_identifier: "", ...overrides }
}
function feeds(rows: PipelineRun[] = [], detail: unknown = rows[0]) {
  vi.mocked(apiFetch).mockImplementation(async input => ok(String(input).includes("?") ? { rows, count: rows.length } : detail))
}
async function expand() { fireEvent.click(await screen.findByRole("button", { name: /Sample routine/ })) }
beforeEach(() => { mocks.search = new URLSearchParams(); vi.mocked(apiFetch).mockReset(); feeds() })
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks() })

describe("Activity run list and step details", () => {
  it("renders each empty filter and sends its filter to the API", async () => {
    render(<RunsView workspaceId="ws" />)
    expect(await screen.findByText(/No routines running/)).toBeInTheDocument()
    for (const [label, filter, text] of [["All", "all", "No runs in the workspace yet."], ["Completed", "completed", "No completed runs yet."], ["Failed", "failed", "No failed runs — workspace is clean."], ["Active", "active", "No routines running."]]) {
      fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${label}`) }))
      await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([input]) => (new URL(String(input), "http://localhost").searchParams.get("status") ?? "all") === filter)).toBe(true))
      expect(await screen.findByText(new RegExp(text))).toBeInTheDocument()
    }
  })
  it("reports a refused list rather than claiming it is empty", async () => {
    vi.mocked(apiFetch).mockResolvedValue(new Response(null, { status: 503 }))
    render(<RunsView workspaceId="ws" />)
    expect(await screen.findByText("Runs unavailable: runs: 503")).toBeInTheDocument()
    expect(screen.queryByText("Nothing here")).not.toBeInTheDocument()
  })
  it("counts all active and failed states including parked approvals", async () => {
    feeds(["running", "queued", "paused", "waiting", "completed", "failed", "cancelled"].map((status, i) => run({ id: `run-${i}`, status, pipeline_name: "", duration_ms: i * 1000, cost_usd: i / 100 })))
    render(<RunsView workspaceId="ws" />)
    expect(await screen.findByRole("button", { name: "Active 4" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "All 7" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Completed 1" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Failed 2" })).toBeInTheDocument()
    expect(screen.getByText("$0.0600")).toBeInTheDocument()
  })
  it("loads details lazily, presents all DSL steps and expands actual string and structured outputs", async () => {
    const row = run({ current_step_id: "approve", status: "running", triggered_by_id: "user-one" })
    feeds([row], { ...row, step_outputs: { first: "Readable output", second: { result: true }, empty: "" }, definition: { steps: [ { id: "first", type: "agent", agent_slug: "writer" }, { id: "second", type: "transform" }, { id: "empty", type: "transform" }, { id: "approve", type: "wait", wait: { approval_prompt: "Approve this result" } }, { id: "later", type: "agent" } ] } })
    render(<RunsView workspaceId="ws" />)
    const header = await screen.findByRole("button", { name: /Sample routine/ })
    expect(apiFetch).toHaveBeenCalledTimes(1)
    fireEvent.click(header)
    const first = await screen.findByRole("button", { name: /1\. first/ })
    expect(screen.getByText("user-one")).toBeInTheDocument()
    expect(screen.getByText("— writer")).toBeInTheDocument()
    expect(screen.getByText("— Approve this result")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Resolve in Inbox" })).toHaveAttribute("href", "/inbox")
    fireEvent.click(first); expect(screen.getByText("Readable output")).toBeInTheDocument()
    fireEvent.click(first); expect(screen.queryByText("Readable output")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /2\. second/ })); expect(screen.getByText(/"result": true/)).toBeInTheDocument()
    for (const name of [/3\. empty/, /4\. approve/, /5\. later/]) { const step = screen.getByRole("button", { name }); expect(step).toHaveAttribute("aria-disabled", "true"); fireEvent.click(step); expect(step).not.toHaveAttribute("aria-expanded") }
    fireEvent.click(header); expect(header).toHaveAttribute("aria-expanded", "false")
  })
  it.each(["waiting", "paused"])("offers the approval inbox for %s even when the saved definition is missing", async status => {
    const row = run({ status, current_step_id: "approval" }); feeds([row], { ...row, step_outputs: { first: 0 } })
    render(<RunsView workspaceId="ws" />); await expand()
    expect(await screen.findByText("— in flight")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Resolve in Inbox" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /1\. first/ })); expect(screen.getByText("0", { selector: "pre" })).toBeInTheDocument()
  })
  it.each(["", "first"])("shows output fallback and failure context without a DSL (failed step %s)", async failed_at_step => {
    const row = run({ status: "failed", failed_at_step, error_message: "Worker refused", current_step_id: "first" }); feeds([row], { ...row, step_outputs: { first: null } })
    render(<RunsView workspaceId="ws" />); await expand()
    expect(await screen.findByText("Worker refused")).toBeInTheDocument()
    expect(screen.getByText(failed_at_step ? "Failed at first" : "Failed", { selector: "span.font-medium" })).toBeInTheDocument()
    expect(screen.queryByText("— in flight")).not.toBeInTheDocument()
    expect(screen.queryByRole("link", { name: "Resolve in Inbox" })).not.toBeInTheDocument()
  })
  it.each(["", "working"])("explains an empty output set with current step %s", async current_step_id => {
    const row = run({ current_step_id }); feeds([row], { ...row, step_outputs: {} })
    render(<RunsView workspaceId="ws" />); await expand()
    expect(await screen.findByText(current_step_id ? "— in flight, no outputs yet" : "No step outputs recorded.")).toBeInTheDocument()
  })
  it("keeps the issue source readable without nesting a link inside the expand button", async () => {
    feeds([run({ triggered_via: "issue", issue_identifier: "ISS-1" })]); render(<RunsView workspaceId="ws" />)
    const header = await screen.findByRole("button", { name: /Sample routine/ })
    expect(header).toHaveTextContent("ISS-1")
    expect(header.querySelector("a")).toBeNull()
  })
  it("cancels a pending deep-link scroll when the view unmounts", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {})
    mocks.search = new URLSearchParams("run=run-one"); feeds([run()])
    const view = render(<RunsView workspaceId="ws" />)
    await act(async () => {})
    expect(screen.getByRole("button", { name: /Sample routine/ })).toHaveAttribute("aria-expanded", "true")
    view.unmount()
    act(() => vi.advanceTimersByTime(200))
    expect(scroll).not.toHaveBeenCalled()
  })
  it("announces a failed detail read instead of presenting it as an empty output set", async () => {
    vi.mocked(apiFetch).mockImplementation(async input => String(input).includes("?") ? ok({ rows: [run()], count: 1 }) : new Response(null, { status: 403 }))
    render(<RunsView workspaceId="ws" />); await expand()
    expect(await screen.findByRole("alert")).toHaveTextContent("Run details unavailable: run: 403")
    expect(screen.queryByText("No step outputs recorded.")).not.toBeInTheDocument()
  })
  it("scrolls a loaded deep-linked run into view", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {})
    mocks.search = new URLSearchParams("run=run-one"); feeds([run()])
    render(<RunsView workspaceId="ws" />)
    await act(async () => {})
    act(() => vi.advanceTimersByTime(100))
    expect(scroll).toHaveBeenCalledWith({ behavior: "smooth", block: "center" })
  })

})
