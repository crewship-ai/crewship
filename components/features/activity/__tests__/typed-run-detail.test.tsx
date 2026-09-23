import { beforeEach, describe, expect, it, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { apiFetch } from "@/lib/api-fetch"
import { TypedRunDetail } from "../typed-run-detail"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/components/features/routines/routine-run-detail", () => ({ RoutineRunDetail: () => <div data-testid="routine-detail" /> }))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({ RunActivityTimeline: ({ workspaceId, params }: { workspaceId: string; params: { run_id?: string } }) => <div data-testid="agent-timeline" data-workspace-id={workspaceId} data-run-id={params.run_id} /> }))
vi.mock("@/components/features/activity/run-evidence-panel", () => ({ RunEvidencePanel: () => <div data-testid="agent-evidence" /> }))

beforeEach(() => vi.mocked(apiFetch).mockReset())

describe("TypedRunDetail", () => {
  it("loads the existing agent run API and shows agent detail, not a routine", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ ok: true, json: async () => ({ id: "run_a", kind: "agent", status: "COMPLETED", agent_slug: "marta", mission_identifier: "ENG-1", exit_code: 0 }) } as Response)
    render(<TypedRunDetail workspaceId="ws" runId="run_a" />)
    await waitFor(() => expect(screen.getByText("Agent run")).toBeInTheDocument())
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/runs/run_a?workspace_id=ws", expect.any(Object))
    expect(screen.getByText("ENG-1")).toHaveAttribute("href", "/issues?issue=ENG-1")
    expect(screen.getByTestId("agent-evidence")).toBeInTheDocument()
    expect(screen.getByTestId("agent-timeline")).toBeInTheDocument()
    expect(screen.queryByTestId("routine-detail")).toBeNull()
  })

  it("falls back to the routine detail only on 404", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce({ ok: false, status: 404 } as Response).mockResolvedValueOnce({ ok: true, status: 200 } as Response)
    render(<TypedRunDetail workspaceId="ws" runId="run_old" />)
    await waitFor(() => expect(screen.getByTestId("routine-detail")).toBeInTheDocument())
  })

  it("does not claim an agent never ran when both execution records are absent", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ ok: false, status: 404 } as Response)
    render(<TypedRunDetail workspaceId="ws" runId="run_missing" />)
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("does not prove"))
    expect(screen.queryByTestId("routine-detail")).toBeNull()
    expect(screen.getByTestId("agent-timeline")).toHaveAttribute("data-workspace-id", "ws")
    expect(screen.getByTestId("agent-timeline")).toHaveAttribute("data-run-id", "run_missing")
  })

  it.each([403, 500])("does not try a broader fallback after %i", async (status) => {
    vi.mocked(apiFetch).mockResolvedValue({ ok: false, status } as Response)
    render(<TypedRunDetail workspaceId="ws" runId="run_hidden" />)
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(`(${status})`))
    expect(screen.queryByTestId("routine-detail")).toBeNull()
    expect(screen.queryByTestId("agent-timeline")).toBeNull()
  })
})
