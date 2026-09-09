import { describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { RoutineRunArtifacts } from "../routine-run-artifacts"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))

describe("compact run artifacts", () => {
  it("shows a compact empty state only after a successful lookup", async () => {
    api.mockResolvedValue({ ok: true, json: async () => ({ artifacts: [], next_cursor: null }) })
    render(<RoutineRunArtifacts workspaceId="ws" runId="run" active={false} compact />)
    expect(await screen.findByText("No saved files or declared outputs.")).toBeInTheDocument()
    expect(screen.queryByText("Saved files and outputs")).not.toBeInTheDocument()
  })
  it("does not turn a failed lookup into a no-files claim", async () => {
    api.mockResolvedValue({ ok: false })
    render(<RoutineRunArtifacts workspaceId="ws" runId="run" active={false} compact />)
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded")
    expect(screen.queryByText(/No saved files|No declared outputs/)).not.toBeInTheDocument()
  })
})
