import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))

import { ActivityWorkPage } from "../activity-work-page"
import type { ChainSummary } from "@/hooks/use-chains"

const work: ChainSummary = {
  origin: "asg_root",
  kind: "assignment",
  task: "Draft the reply to Ava",
  started_by_kind: "agent",
  started_by: "Lead",
  runs: 3,
  max_chain_depth: 2,
  failed_runs: 0,
  failed: false,
  running_runs: 1,
  waiting_runs: 1,
  completed_runs: 2,
  first_activity: "2026-10-08T09:00:00Z",
  last_activity: "2026-10-08T09:05:00Z",
  duration_ms: 300000,
  issue_count: 1,
  issues: [{ id: "m1", identifier: "OPS-1", title: "Reply to Ava" }],
  agent_count: 2,
  agents: [
    { id: "g1", name: "Casey", assignments: 2 },
    { id: "g2", name: "Jordan", assignments: 1 },
  ],
}

const graph = {
  nodes: [
    { id: "assignment:asg_root", kind: "assignment", ref: "asg_root", label: "Draft the reply to Ava", depth: 0 },
    { id: "assignment:asg_2", kind: "assignment", ref: "asg_2", label: "Check the bank feed", status: "COMPLETED", depth: 1 },
    { id: "inbox:x1", kind: "inbox", ref: "x1", label: "Send the reminder?", depth: 1 },
  ],
  edges: [
    { from: "assignment:asg_root", to: "assignment:asg_2", kind: "triggers" },
    { from: "assignment:asg_root", to: "inbox:x1", kind: "produces" },
  ],
}

beforeEach(() => {
  fetchMock.mockReset()
  fetchMock.mockImplementation(async () => new Response(JSON.stringify(graph)))
})
afterEach(cleanup)

describe("ActivityWorkPage (#2989)", () => {
  it("leads with the task, the state in the rail's words and what started it", () => {
    render(<ActivityWorkPage workspaceId="ws" chain={work} onOpenNode={vi.fn()} />)
    expect(screen.getByRole("heading", { name: "Draft the reply to Ava" })).toBeInTheDocument()
    expect(screen.getAllByText("Waiting for you")[0]).toBeInTheDocument()
    expect(screen.getAllByText("from chat · Lead")[0]).toBeInTheDocument()
    expect(fetchMock.mock.calls[0][0]).toContain("/api/v1/chains/asg_root")
  })

  it("nests the delegations and asks the work caused, and opens them", async () => {
    const onOpenNode = vi.fn()
    render(<ActivityWorkPage workspaceId="ws" chain={work} onOpenNode={onOpenNode} />)
    const caused = await screen.findByRole("region", { name: "Delegations and asks" })
    expect(within(caused).getByText("Send the reminder?")).toBeInTheDocument()
    fireEvent.click(within(caused).getByRole("button", { name: /Check the bank feed/ }))
    expect(onOpenNode).toHaveBeenCalledWith("assignment", "asg_2")
  })

  it("links the issue and the agents", () => {
    const onOpenNode = vi.fn()
    render(<ActivityWorkPage workspaceId="ws" chain={work} onOpenNode={onOpenNode} />)
    const linked = screen.getByRole("region", { name: "Linked to" })
    fireEvent.click(within(linked).getByRole("button", { name: /OPS-1/ }))
    expect(onOpenNode).toHaveBeenCalledWith("issue", "m1")
    expect(within(linked).getByText("Casey")).toBeInTheDocument()
  })
})
