import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { IssueCheckpointCard, latestCheckpoint } from "../issue-checkpoint-card"
import type { Mission } from "@/lib/types/mission"

// ODCH-ISS-04: the backend has no structured "Step 3 of 5" plan, but every
// agent session writes free-text checkpoints (done / plan / blockers / next
// step). The card shows the newest one and nothing it cannot back.

const issue = {
  id: "m1",
  identifier: "ENG-4",
  crew_id: "c1",
  workspace_id: "ws",
  title: "FAQ",
  status: "IN_PROGRESS",
  updated_at: "2026-10-18T10:00:00Z",
  delegate: { id: "a1", name: "Scout" },
} as unknown as Mission

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const session = (id: string, agent_id: string) => ({ id, agent_id, agent_name: "Scout", state: "active" })
const cp = (over: Record<string, unknown> = {}) => ({
  id: "k1",
  session_id: "s1",
  seq_at_write: 4,
  done: "Drafted 6 of 10 answers",
  plan: "1. Draft\n2. Review",
  blockers: "Pricing page is not published",
  next_step: "Write the remaining four answers",
  confidence: "medium",
  parsed: true,
  created_at: "2026-10-18T09:00:00Z",
  ...over,
})

function route(map: Record<string, unknown>) {
  apiFetch.mockImplementation(async (url: string) => {
    const hit = Object.keys(map).find((k) => url.includes(k))
    return hit ? json(map[hit]) : json({}, 404)
  })
}

describe("latestCheckpoint", () => {
  it("is null when there is nothing worth showing", () => {
    expect(latestCheckpoint([])).toBeNull()
    expect(latestCheckpoint([cp({ done: "", plan: "", blockers: "", next_step: "", facts: "" })] as never)).toBeNull()
  })
  it("takes the first (newest) checkpoint", () => {
    expect(latestCheckpoint([cp({ id: "new" }), cp({ id: "old" })] as never)?.id).toBe("new")
  })
})

describe("IssueCheckpointCard", () => {
  beforeEach(() => apiFetch.mockReset())

  it("reads the delegate's session and shows next step and blockers from the newest checkpoint", async () => {
    route({
      "/sessions/s2/checkpoints": [cp()],
      "/issues/ENG-4/sessions": [session("s1", "other"), session("s2", "a1")],
    })
    render(<IssueCheckpointCard issue={issue} />)
    await waitFor(() => expect(screen.getByText("Write the remaining four answers")).toBeInTheDocument())
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/crews/c1/issues/ENG-4/sessions?workspace_id=ws")
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/crews/c1/issues/ENG-4/sessions/s2/checkpoints?workspace_id=ws")
    expect(screen.getByText("Next step")).toBeInTheDocument()
    expect(screen.getByText("Pricing page is not published")).toBeInTheDocument()
    expect(screen.getByText("Drafted 6 of 10 answers")).toBeInTheDocument()
    expect(screen.queryByText(/step \d+ of \d+/i)).not.toBeInTheDocument()
  })

  it("renders nothing when the issue has no session", async () => {
    route({ "/issues/ENG-4/sessions": [] })
    const { container } = render(<IssueCheckpointCard issue={issue} />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing and never calls the API for an issue without an agent delegate", () => {
    const { container } = render(<IssueCheckpointCard issue={{ ...issue, delegate: undefined } as Mission} />)
    expect(apiFetch).not.toHaveBeenCalled()
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing when the session has written no checkpoint", async () => {
    route({ "/sessions/s1/checkpoints": [], "/issues/ENG-4/sessions": [session("s1", "a1")] })
    const { container } = render(<IssueCheckpointCard issue={issue} />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
    expect(container).toBeEmptyDOMElement()
  })

  it("compact (peek, phone) leaves out the plan and the done list but keeps next step and blockers", async () => {
    route({ "/sessions/s1/checkpoints": [cp()], "/issues/ENG-4/sessions": [session("s1", "a1")] })
    render(<IssueCheckpointCard issue={issue} compact />)
    await waitFor(() => expect(screen.getByText("Write the remaining four answers")).toBeInTheDocument())
    expect(screen.getByText("Pricing page is not published")).toBeInTheDocument()
    expect(screen.queryByText("Drafted 6 of 10 answers")).not.toBeInTheDocument()
    expect(screen.queryByText("Plan")).not.toBeInTheDocument()
  })
})
