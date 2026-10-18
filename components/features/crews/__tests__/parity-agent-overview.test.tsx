import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, within, fireEvent } from "@testing-library/react"

const h = vi.hoisted(() => ({
  insights: { totals: { succeeded: 19, failed: 1, running: 0 } } as unknown,
  relations: {
    issues: [{ id: "i1", identifier: "CRW-163", title: "Compare pricing", status: "IN_PROGRESS" }],
    pipelines: [{ id: "p1", slug: "weekly", name: "Weekly competitor scan", author_agent_id: "a1" }],
  } as { issues: unknown[]; pipelines: unknown[] },
}))
vi.mock("../memory-workspace", () => ({
  useWorkspaceResource: (url: string) => (url.includes("runs/insights") ? { key: url, data: h.insights } : { key: url }),
}))
vi.mock("../canvas/use-agent-relations", () => ({
  useAgentRelations: () => ({ issues: h.relations.issues, pipelines: h.relations.pipelines, credentials: [], skills: [], readiness: null, loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-paged-list", () => ({
  usePagedList: () => ({ items: [{ id: "a1", name: "Scout", slug: "scout" }, { id: "a2", name: "Writer", slug: "writer" }, { id: "a3", name: "Analyst", slug: "analyst" }], loading: false, error: null }),
}))

import { AgentOverview } from "../workspace-overview"
import type { AgentRecord } from "../agent-canvas-tabs/types"

afterEach(() => { cleanup(); h.relations.issues = [{ id: "i1", identifier: "CRW-163", title: "Compare pricing", status: "IN_PROGRESS" }]; h.relations.pipelines = [{ id: "p1", slug: "weekly", name: "Weekly competitor scan", author_agent_id: "a1" }] })

const agent = { id: "a1", name: "Scout", slug: "scout", crew_id: "c1", crew: { name: "Research", slug: "research" }, timeout_seconds: 1800 } as unknown as AgentRecord
const runs = [{ id: "r1", status: "RUNNING", trigger_type: "SCHEDULE", started_at: "2026-10-18T08:00:00Z", created_at: "2026-10-18T08:00:00Z" }]

function renderOverview(extra: Record<string, unknown> = {}) {
  const props = { workspaceId: "ws", agent, inbox: { count: 0 }, runs: [] as unknown[], chats: [] as unknown[], error: null, onRetry: vi.fn(), onWork: vi.fn(), revision: 0, onStop: vi.fn(), cost30d: 6.4, ...extra }
  render(<AgentOverview {...(props as unknown as React.ComponentProps<typeof AgentOverview>)} />)
  return props
}

// PAR-122: Now, Holds, Talks with and Last 30 days, as v67 draws them.
describe("Agent overview (v67 card set)", () => {
  it("has exactly the v67 cards and none of the old dashboard", () => {
    renderOverview()
    for (const name of ["Now", "Holds", "Talks with", "Last 30 days"]) expect(screen.getByRole("region", { name })).toBeInTheDocument()
    for (const old of ["Run outcomes", "How runs start", "Current work", "Recent outcomes", "Recent conversations", "Completed runs"]) expect(screen.queryByText(old)).toBeNull()
    expect(screen.queryByText(/this month · UTC/)).toBeNull()
  })

  it("Now says Working with Stop while it runs, and says so when it does not", () => {
    const p = renderOverview({ runs })
    const now = screen.getByRole("region", { name: "Now" })
    expect(within(now).getByText("Working")).toBeInTheDocument()
    fireEvent.click(within(now).getByRole("button", { name: /Stop/ }))
    expect(p.onStop).toHaveBeenCalled()
    expect(within(now).getByRole("link", { name: /All in Activity/ })).toHaveAttribute("href", expect.stringContaining("/activity"))
    cleanup()
    renderOverview({ runs: [] })
    const idle = screen.getByRole("region", { name: "Now" })
    expect(within(idle).getByText("Not working right now")).toBeInTheDocument()
    expect(within(idle).queryByRole("button", { name: /Stop/ })).toBeNull()
  })

  it("Holds lists the routines and issues as chips; empty it says so", () => {
    renderOverview()
    const holds = screen.getByRole("region", { name: "Holds" })
    expect(within(holds).getByRole("link", { name: /Weekly competitor scan/ })).toBeInTheDocument()
    expect(within(holds).getByRole("link", { name: /CRW-163/ })).toHaveAttribute("href", expect.stringContaining("/issues/CRW-163"))
    cleanup()
    h.relations.issues = []
    h.relations.pipelines = []
    renderOverview()
    expect(within(screen.getByRole("region", { name: "Holds" })).getByText("Nothing assigned to it.")).toBeInTheDocument()
  })

  it("Talks with has Chats (with Open chat) and Teammates", () => {
    renderOverview({ chats: [{ id: "s1", title: "Pricing", message_count: 4, started_at: "2026-10-16T16:12:00Z" }] })
    const talks = screen.getByRole("region", { name: "Talks with" })
    expect(within(talks).getByText("Chats")).toBeInTheDocument()
    expect(within(talks).getByRole("link", { name: "Open chat" })).toHaveAttribute("href", "/chat/scout")
    expect(within(talks).getByText("Teammates")).toBeInTheDocument()
    expect(within(talks).getByText("Writer, Analyst")).toBeInTheDocument()
  })

  it("Last 30 days reads runs, finished share, cost and the run limit in sans numerals", () => {
    renderOverview()
    const last = screen.getByRole("region", { name: "Last 30 days" })
    expect(within(last).getByText("20")).toBeInTheDocument()
    expect(within(last).getByText("95 %")).toBeInTheDocument()
    expect(within(last).getByText("$6.40")).toBeInTheDocument()
    expect(within(last).getByText("30 min")).toBeInTheDocument()
    expect(last.querySelector(".font-mono")).toBeNull()
  })

  it("keeps the approval notice for an agent with pending decisions", () => {
    renderOverview({ inbox: { count: 2, summary: "2 approvals" } })
    expect(screen.getByRole("link", { name: "Review in Inbox" })).toBeInTheDocument()
  })
})
