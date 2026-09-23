import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { RunEvidencePanel } from "../run-evidence-panel"

const h = vi.hoisted(() => ({
  entries: [] as Record<string, unknown>[],
  loading: false,
  error: null as string | null,
  nextCursor: null as string | null,
  refresh: vi.fn(),
  copied: vi.fn(),
}))
vi.mock("@/hooks/use-journal-list", () => ({ useJournalList: () => ({ ...h }) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const run = { kind: "routine" as const, runId: "run_123", status: "failed", failureKind: "checker_rejected" }

beforeEach(() => {
  h.entries = [{ id: "evt_a", workspace_id: "ws", ts: "2026-09-23T07:00:00Z", entry_type: "pipeline.run.failed", severity: "error", actor_type: "orchestrator", actor_id: "run_123", summary: "SECRET_IN_SUMMARY", payload: { run_id: "run_123", error: "SECRET_IN_ERROR" } }]
  h.loading = false; h.error = null; h.nextCursor = null
  h.copied.mockReset()
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: h.copied.mockResolvedValue(undefined) } })
})

describe("RunEvidencePanel", () => {
  it("previews exactly the safe text copied to clipboard without starting work", async () => {
    render(<RunEvidencePanel workspaceId="ws" run={run} />)
    fireEvent.click(screen.getByText("Preview evidence"))
    const preview = screen.getByTestId("run-evidence-preview").textContent ?? ""
    expect(preview).toContain("evt_a")
    expect(preview).not.toContain("SECRET_IN_")
    fireEvent.click(screen.getByRole("button", { name: /copy evidence/i }))
    await waitFor(() => expect(h.copied).toHaveBeenCalledWith(preview))
  })

  it("acknowledges journal failure and does not reuse old entries", () => {
    h.error = "Failed to load journal"
    render(<RunEvidencePanel workspaceId="ws" run={run} />)
    expect(screen.getByRole("alert")).toHaveTextContent("Journal unavailable")
    fireEvent.click(screen.getByText("Preview evidence"))
    expect(screen.getByTestId("run-evidence-preview").textContent).not.toContain("evt_a")
    expect(screen.getByTestId("run-evidence-preview").textContent).toContain("Journal could not be read")
  })

  it("does not preview or copy cached events from the previous workspace while switching", async () => {
    const { rerender } = render(<RunEvidencePanel workspaceId="ws" run={run} />)
    fireEvent.click(screen.getByText("Preview evidence"))
    expect(screen.getByTestId("run-evidence-preview").textContent).toContain("evt_a")

    // The journal hook has not yet replaced its old page after the prop switch.
    rerender(<RunEvidencePanel workspaceId="other_ws" run={run} />)
    expect(screen.getByTestId("run-evidence-preview").textContent).not.toContain("evt_a")
    fireEvent.click(screen.getByRole("button", { name: /copy evidence/i }))
    await waitFor(() => expect(h.copied).toHaveBeenCalledTimes(1))
    expect(h.copied.mock.calls[0]?.[0]).not.toContain("evt_a")
  })

  it("reports clipboard refusal", async () => {
    h.copied.mockRejectedValueOnce(new Error("denied"))
    const { toast } = await import("sonner")
    render(<RunEvidencePanel workspaceId="ws" run={run} />)
    fireEvent.click(screen.getByRole("button", { name: /copy evidence/i }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Could not copy evidence"))
  })
})
