import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { apiFetch } from "@/lib/api-fetch"
import { RoutineExecutionHistory } from "../routine-execution-history"
import { RoutineRunArtifacts } from "../routine-run-artifacts"

// "Lists are paginated; execution and artifact content is loaded on demand."
// The cheapest way for that claim to be quietly false is a list request that
// already carries every transcript, so these tests assert on the REQUESTS the
// two panels make, not only on what they render. Both are rendered with
// active=false: the 3s poll is not what is under test here.

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

const ok = (body: unknown) => ({ ok: true, json: async () => body }) as Response

function execution(overrides: Record<string, unknown> = {}) {
  return {
    id: "exec_1",
    parent_execution_id: "",
    step_id: "write",
    execution_path: "/write",
    attempt: 1,
    kind: "script",
    status: "completed",
    agent_slug: "",
    model: "",
    started_at: "2026-09-08T08:00:00Z",
    ended_at: "2026-09-08T08:00:05Z",
    error: "",
    output_bytes: 4096,
    ...overrides,
  }
}

function artifact(overrides: Record<string, unknown> = {}) {
  return {
    id: "art_1",
    step_execution_id: "exec_1",
    kind: "json",
    label: "Summary",
    state: "available",
    media_type: "",
    sha256: "",
    content_bytes: 120,
    source: "",
    error: "",
    created_at: "2026-09-08T08:00:05Z",
    execution_path: "/write",
    attempt: 1,
    ...overrides,
  }
}

beforeEach(() => vi.clearAllMocks())

describe("recorded executions", () => {
  it("lists attempts without downloading their outputs, then fetches one on demand", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(
      ok({
        rows: [
          execution(),
          execution({
            id: "exec_2",
            step_id: "agent",
            execution_path: "/write/agent",
            kind: "agent_attempt",
            agent_slug: "casey",
            model: "claude-sonnet-5",
            attempt: 2,
          }),
        ],
        next_cursor: null,
      }),
    )
    render(<RoutineExecutionHistory workspaceId="ws" runId="run_1" active={false} />)

    await screen.findByText(/Agent invocation · Attempt 2/)
    expect(vi.mocked(apiFetch)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(apiFetch).mock.calls[0][0]).not.toContain("execution_id=")
    // Only the agent invocation claims a requested model.
    expect(screen.getByText(/requested claude-sonnet-5/)).toBeTruthy()
    expect(screen.getByText(/^\/write · /)).toBeTruthy()

    vi.mocked(apiFetch).mockResolvedValueOnce(
      ok({ id: "exec_2", output: "the recorded transcript" }),
    )
    fireEvent.click(screen.getByRole("button", { name: /Agent invocation · Attempt 2/ }))

    expect(await screen.findByText("the recorded transcript")).toBeTruthy()
    expect(vi.mocked(apiFetch).mock.calls[1][0]).toContain("execution_id=exec_2")
  })

  it("says an output is unavailable rather than showing it as empty", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(ok({ rows: [execution()], next_cursor: null }))
    render(<RoutineExecutionHistory workspaceId="ws" runId="run_1" active={false} />)
    await screen.findByText(/write · Attempt 1/)

    vi.mocked(apiFetch).mockResolvedValueOnce({ ok: false, json: async () => ({}) } as Response)
    fireEvent.click(screen.getByRole("button", { name: /write · Attempt 1/ }))
    expect(await screen.findByText("This output is unavailable.")).toBeTruthy()
  })

  it("pages forward with the server's cursor and can return to the first page", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(ok({ rows: [execution()], next_cursor: "42" }))
    render(<RoutineExecutionHistory workspaceId="ws" runId="run_1" active={false} />)
    await screen.findByText(/write · Attempt 1/)
    expect(vi.mocked(apiFetch).mock.calls[0][0]).not.toContain("after=")

    vi.mocked(apiFetch).mockResolvedValueOnce(
      ok({ rows: [execution({ id: "exec_9", step_id: "publish" })], next_cursor: null }),
    )
    fireEvent.click(screen.getByRole("button", { name: "Next executions" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls[1][0]).toContain("after=42"))
    await screen.findByText(/publish · Attempt 1/)

    vi.mocked(apiFetch).mockResolvedValueOnce(ok({ rows: [execution()], next_cursor: "42" }))
    fireEvent.click(screen.getByRole("button", { name: "First executions" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls[2][0]).not.toContain("after="))
  })

  it("does not present a finished run's missing history as work still to come", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(ok({ rows: [], next_cursor: null }))
    render(<RoutineExecutionHistory workspaceId="ws" runId="run_old" active={false} />)
    expect(
      await screen.findByText(/Detailed attempt history is unavailable for this run/),
    ).toBeTruthy()
  })
})

describe("declared outputs", () => {
  it("loads a text output's content only when it is opened", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(ok({ artifacts: [artifact()], next_cursor: null }))
    render(<RoutineRunArtifacts workspaceId="ws" runId="run_1" active={false} />)

    await screen.findByText("Summary")
    expect(vi.mocked(apiFetch)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(apiFetch).mock.calls[0][0]).not.toContain("artifact_id=")
    expect(screen.getByText(/json · available/)).toBeTruthy()

    vi.mocked(apiFetch).mockResolvedValueOnce(ok({ id: "art_1", content: '{"count":4}' }))
    const disclosure = screen.getByText("View content").closest("details")!
    disclosure.open = true
    fireEvent(disclosure, new Event("toggle"))

    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls[1][0]).toContain("artifact_id=art_1"))
    expect(await screen.findByText('{"count":4}')).toBeTruthy()
  })

  it("offers a download only for a file whose bytes were actually stored", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(
      ok({
        artifacts: [
          artifact({
            id: "art_file",
            kind: "file",
            label: "report.txt",
            sha256: "abc123",
            source: "/crew/shared/report.txt",
          }),
          artifact({
            id: "art_missing",
            kind: "file",
            label: "gone.txt",
            state: "unavailable",
            sha256: "",
            source: "/crew/shared/gone.txt",
            error: "Declared file could not be snapshotted.",
          }),
        ],
        next_cursor: null,
      }),
    )
    render(<RoutineRunArtifacts workspaceId="ws" runId="run_1" active={false} />)

    await screen.findByText("report.txt")
    expect(screen.getAllByRole("button", { name: "Download saved version" })).toHaveLength(1)
    expect(screen.getByText(/file · unavailable/)).toBeTruthy()
    expect(screen.getByText("Declared file could not be snapshotted.")).toBeTruthy()
  })

  it("says a run declared no outputs rather than failing silently", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(ok({ artifacts: [], next_cursor: null }))
    render(<RoutineRunArtifacts workspaceId="ws" runId="run_1" active={false} />)
    expect(await screen.findByText(/No declared outputs recorded/)).toBeTruthy()
  })
})
