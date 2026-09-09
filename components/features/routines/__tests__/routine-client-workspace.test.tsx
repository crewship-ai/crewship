import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { RoutineRunDetail } from "../routine-run-detail"
import { RoutineResultContent } from "../routine-result-content"
import { RoutineWorkOverview } from "../routine-work-overview"

const h = vi.hoisted(() => ({
  run: {} as Record<string, unknown>, dsl: null as Record<string, unknown> | null,
  api: vi.fn(), refresh: vi.fn(),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.api(...args) }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-trace", () => ({ useTrace: () => ({ run: h.run, dsl: h.dsl, loading: false, error: null, refresh: h.refresh }) }))
vi.mock("@/hooks/use-pending-approval", () => ({ usePendingApproval: () => ({ waitpoint: null, refresh: h.refresh, error: null }) }))
vi.mock("../routine-identity-header", () => ({ RoutineIdentityHeader: ({ children, actions }: { children: React.ReactNode; actions: React.ReactNode }) => <header>{children}{actions}</header> }))
vi.mock("@/components/features/activity/trace-canvas", () => ({ TraceCanvas: ({ dsl }: { dsl: unknown }) => <div data-testid="map">{JSON.stringify(dsl)}</div> }))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({ RunActivityTimeline: () => <div>Activity evidence</div> }))
vi.mock("../routine-execution-history", () => ({ RoutineExecutionHistory: () => <div>Recorded attempts</div> }))
vi.mock("../routine-run-artifacts", () => ({ RoutineRunArtifacts: () => <div>Recorded files</div> }))

beforeEach(() => {
  vi.clearAllMocks()
  h.api.mockResolvedValue({ ok: true, json: async () => ({ slug: "sample", definition: { steps: [{ id: "new-head" }] } }) })
  h.run = { id: "run_1", pipeline_slug: "sample", status: "completed", outcome: "FAILED", error_message: "no outcome reported", output: "# Service report\n\n**Report is available.**", started_at: "2026-09-08T10:00:00Z", triggered_via: "manual", duration_ms: 100, pipeline_version: 2, cost_usd: 0, step_outputs: {} }
  h.dsl = { steps: [{ id: "historical-step", type: "agent_run" }] }
})

describe("client routine workspace", () => {
  it("leads with the recorded report without disguising failed completion", async () => {
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    expect(screen.getByText("A result was recorded, but completion was not confirmed")).toBeInTheDocument()
    expect(screen.getByText("Result failed")).toBeInTheDocument()
    expect(await screen.findByRole("heading", { name: "Service report" })).toBeInTheDocument()
    expect(screen.getByText("Recorded files")).toBeInTheDocument()
    expect(screen.queryByTestId("map")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Workflow", exact: true }))
    expect(screen.getByTestId("map")).toHaveTextContent("historical-step")
    expect(screen.getByTestId("map")).not.toHaveTextContent("new-head")
  })
  it("requires an explicit stop decision and calls the existing cancel endpoint once", async () => {
    h.run = { ...h.run, status: "running", outcome: undefined, output: undefined, error_message: "" }
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    fireEvent.click(screen.getByRole("button", { name: "Stop run", exact: true }))
    expect(screen.getByText(/Actions that already happened are not rolled back/)).toBeInTheDocument()
    expect(h.api.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(0)
    fireEvent.click(screen.getByRole("button", { name: "Stop this run", exact: true }))
    await waitFor(() => expect(h.api).toHaveBeenCalledWith("/api/v1/workspaces/ws/pipelines/runs/run_1/cancel", { method: "POST" }))
    expect(h.refresh).toHaveBeenCalled()
  })
  it("opens historical step details and readable saved inputs, never current defaults", () => {
    h.run = { ...h.run, inputs: { text: "Saved answer" } }
    h.dsl = { inputs: [{ name: "text", label: "Original prompt" }], steps: [{ id: "historical-step", type: "agent_run" }] }
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    fireEvent.click(screen.getByRole("button", { name: "Inputs", exact: true }))
    expect(screen.getByText("Original prompt")).toBeInTheDocument()
    expect(screen.getByText("Saved answer")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Workflow", exact: true }))
    expect(screen.getByRole("combobox", { name: "Explore a step" })).toHaveValue("historical-step")
  })
  it("keeps unavailable step evidence distinct from no output", () => {
    h.run = { ...h.run, step_outputs_available: false, output: undefined }
    h.dsl = null
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    expect(screen.getByText(/historical recipe is unavailable/)).toBeInTheDocument()
    expect(screen.getByText(/Step outputs could not be loaded/)).toBeInTheDocument()
    expect(screen.queryByText("No step outputs recorded yet.")).not.toBeInTheDocument()
  })
  it("describes declared results and conditions without claiming readiness or a fixed order", () => {
    render(<RoutineWorkOverview definition={{ inputs: [{ name: "source", label: "Service URL", required: true, default: "example" }], outputs: [{ name: "report" }], steps: [{ id: "fetch", type: "http", http: { url: "https://example.com" }, if: "inputs.enabled" }, { id: "report", type: "agent_run", agent_slug: "riley", needs: ["fetch"] }] }} />)
    expect(screen.getByText("Service URL")).toBeInTheDocument()
    expect(screen.getByText("Expected result")).toBeInTheDocument()
    expect(screen.getByText("Conditional")).toBeInTheDocument()
    expect(screen.getAllByText("After: fetch").length).toBeGreaterThan(0)
    expect(screen.queryByText(/Ready to run/)).not.toBeInTheDocument()
  })
})


describe("readable recipe", () => {
  it("distinguishes work types and explains required inputs without revealing defaults", () => {
    const { container } = render(<RoutineWorkOverview definition={{ inputs: [{ name: "max_stale_hours", type: "number", required: true, default: 24 }, { name: "token", type: "string", default: "private-value" }], outputs: [{ name: "change_report", type: "string" }], steps: [{ id: "probe", type: "script" }, { id: "red_state", type: "transform", needs: ["probe"] }] }} />)
    expect(screen.getByText("Max stale hours")).toBeInTheDocument()
    expect(screen.getByText("Required")).toBeInTheDocument()
    expect(screen.getByText("Change report")).toBeInTheDocument()
    expect(screen.queryByText("private-value")).not.toBeInTheDocument()
    expect(container.querySelector('[data-step-kind="script"] svg')).not.toBeNull()
    expect(container.querySelector('[data-step-kind="transform"] svg')).not.toBeNull()
    expect(screen.getAllByText("Run a script").length).toBeGreaterThan(0)
  })
})

describe("recorded result rendering", () => {
  it("renders Markdown tables and strips executable HTML", () => {
    const { container } = render(<RoutineResultContent output={"| Service | State |\n| --- | --- |\n| API | Available |\n\n<script>alert('x')</script>"} />)
    expect(screen.getByRole("table")).toBeInTheDocument()
    expect(screen.getByRole("cell", { name: "Available" })).toBeInTheDocument()
    expect(container.querySelector("script")).toBeNull()
  })
  it("preserves structured output and large identifiers without rounding", () => {
    const { container } = render(<RoutineResultContent output={'{"available":false,"count":0,"id":9007199254740993}'} />)
    expect(container.querySelector("pre")?.textContent).toContain('"available":false')
    expect(container.querySelector("pre")?.textContent).toContain('"count":0')
    expect(container.querySelector("pre")?.textContent).toContain('9007199254740993')
  })
})
