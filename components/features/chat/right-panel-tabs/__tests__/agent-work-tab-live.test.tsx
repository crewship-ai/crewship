import { act } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { AgentWorkTab } from "../agent-work-tab"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const handlers = new Map<string, (event: { type: string; payload: Record<string, unknown> }) => void>()
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEventSafe: (type: string, cb: (event: { type: string; payload: Record<string, unknown> }) => void) => { handlers.set(type, cb) },
}))
vi.mock("../../chat-agent-context", () => ({ useChatAgent: () => ({ name: "Jamie", slug: "jamie", crewId: "crew-1" }) }))

type Reply = { ok: boolean; body: unknown; total?: number }
let replies: Record<string, Reply>

function route(url: string): Reply {
  if (url.includes("/issues?")) return replies.issues
  if (url.includes("/pipelines?")) return replies.routines
  if (url.includes("/api/v1/runs?")) return replies.runs
  if (url.includes("/assignments")) return replies.assignments
  if (url.includes("/escalations")) return replies.escalations
  return { ok: false, body: null }
}

function respond(reply: Reply) {
  return { ok: reply.ok, status: reply.ok ? 200 : 500, headers: new Headers(reply.total === undefined ? {} : { "X-Total-Count": String(reply.total) }), json: async () => reply.body }
}

const openIssue = { id: "i1", identifier: "COPY-1", title: "Review headline", status: "IN_PROGRESS" }

beforeEach(() => {
  handlers.clear()
  apiFetch.mockReset()
  replies = {
    issues: { ok: true, body: [openIssue], total: 7 },
    routines: { ok: true, body: [] },
    runs: { ok: true, body: { data: [{ id: "run-1", agent_id: "agent-1", status: "RUNNING", trigger_type: "chat", mission_identifier: "COPY-1", started_at: "2026-11-19T10:00:00Z" }] } },
    assignments: { ok: true, body: [
      { id: "a1", task: "Draft the release notes", status: "QUEUED", queued_reason: "agent_busy", assigned_to_slug: "jamie", assigned_by_name: "Lead", created_at: "2026-11-19T09:00:00Z" },
      { id: "a2", task: "Someone else's task", status: "QUEUED", assigned_to_slug: "other", created_at: "2026-11-19T09:00:00Z" },
      { id: "a3", task: "Finished task", status: "COMPLETED", assigned_to_slug: "jamie", created_at: "2026-11-18T09:00:00Z" },
    ] },
    escalations: { ok: true, body: [
      { id: "e1", type: "approval", reason: "Publish the pricing page?", from_slug: "jamie", status: "PENDING", created_at: "2026-11-19T10:01:00Z" },
      { id: "e2", type: "approval", reason: "Other agent question", from_slug: "other", status: "PENDING", created_at: "2026-11-19T10:01:00Z" },
    ] },
  }
  apiFetch.mockImplementation(async (url: string) => respond(route(url)))
})

const urls = () => apiFetch.mock.calls.map(([url]) => String(url))

describe("AgentWorkTab live work", () => {
  it("asks only for open assigned issues and counts them from the same query", async () => {
    render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    await screen.findByRole("link", { name: /^Issue COPY-1/ })
    const issueUrl = urls().find((url) => url.includes("/issues?"))!
    const statuses = new URL(issueUrl, "http://x").searchParams.get("status")!.split(",")
    expect(statuses).toEqual(expect.arrayContaining(["BACKLOG", "TODO", "IN_PROGRESS", "REVIEW"]))
    expect(statuses).not.toEqual(expect.arrayContaining(["DONE"]))
    expect(statuses.some((s) => ["DONE", "COMPLETED", "CANCELLED", "FAILED", "DUPLICATE"].includes(s))).toBe(false)
    expect(screen.getByRole("tab", { name: /Issues/ })).toHaveTextContent("7")
  })

  it("shows what the agent is doing now: running work, decisions it waits for and its queue", async () => {
    render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    const now = await screen.findByRole("region", { name: "Working now" })
    expect(now).toHaveTextContent("Running")
    expect(now).toHaveTextContent("COPY-1")
    expect(now).toHaveTextContent("Waiting for a decision")
    expect(now).toHaveTextContent("Publish the pricing page?")
    expect(now).toHaveTextContent("Queued")
    expect(now).toHaveTextContent("Draft the release notes")
    expect(now).not.toHaveTextContent("Someone else's task")
    expect(now).not.toHaveTextContent("Finished task")
    expect(now).not.toHaveTextContent("Other agent question")
    expect(urls().some((url) => url.includes("agent_id=agent-1") && url.includes("status=RUNNING"))).toBe(true)
  })

  it("refreshes on realtime events without blanking the list, and drops an issue reassigned away", async () => {
    render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    await screen.findByRole("link", { name: /^Issue COPY-1/ })
    const before = apiFetch.mock.calls.length
    replies.issues = { ok: true, body: [], total: 0 }
    let resolveIssues: () => void = () => {}
    const gate = new Promise<void>((resolve) => { resolveIssues = resolve })
    apiFetch.mockImplementation(async (url: string) => { if (url.includes("/issues?")) await gate; return respond(route(url)) })
    vi.useFakeTimers()
    try {
      act(() => { handlers.get("issue.updated")!({ type: "issue.updated", payload: { id: "i1" } }) })
      await act(async () => { vi.advanceTimersByTime(1000) })
    } finally { vi.useRealTimers() }
    expect(apiFetch.mock.calls.length).toBeGreaterThan(before)
    // Still showing the previous list while the refresh is in flight.
    expect(screen.getByRole("link", { name: /^Issue COPY-1/ })).toBeInTheDocument()
    expect(screen.queryByText("Loading agent work…")).not.toBeInTheDocument()
    await act(async () => { resolveIssues() })
    await waitFor(() => expect(screen.queryByRole("link", { name: /^Issue COPY-1/ })).not.toBeInTheDocument())
    expect(screen.getByText("No open issues assigned to this agent.")).toBeInTheDocument()
  })

  it("refreshes after a reconnect and on run, assignment and decision events", async () => {
    render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    await screen.findByRole("link", { name: /^Issue COPY-1/ })
    for (const type of ["realtime.reconnected", "run.started", "run.completed", "run.failed", "assignment.updated", "task.updated", "escalation.created", "escalation.resolved", "issue.created", "issue.status_changed"]) {
      expect(handlers.has(type), type).toBe(true)
    }
  })

  it("keeps the other sources when one fails", async () => {
    replies.runs = { ok: false, body: null }
    replies.escalations = { ok: false, body: null }
    render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByRole("link", { name: /^Issue COPY-1/ })).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Working now" })).toHaveTextContent("Draft the release notes")
    expect(screen.queryByText("Work could not be loaded.")).not.toBeInTheDocument()
  })

  it("never paints a previous agent's work after switching agents mid-request", async () => {
    let releaseFirst: () => void = () => {}
    const gate = new Promise<void>((resolve) => { releaseFirst = resolve })
    apiFetch.mockImplementation(async (url: string) => {
      if (url.includes("assignee_id=agent-1")) { await gate; return respond({ ok: true, body: [{ ...openIssue, title: "Stale from agent 1" }], total: 1 }) }
      if (url.includes("/issues?")) return respond({ ok: true, body: [{ id: "i2", identifier: "COPY-2", title: "Agent 2 issue", status: "TODO" }], total: 1 })
      return respond(route(url))
    })
    const view = render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    view.rerender(<AgentWorkTab agentId="agent-2" workspaceId="ws-1" />)
    expect(await screen.findByRole("link", { name: /^Issue COPY-2/ })).toBeInTheDocument()
    await act(async () => { releaseFirst() })
    expect(screen.queryByText("Stale from agent 1")).not.toBeInTheDocument()
  })
})
