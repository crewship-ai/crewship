import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// ---------------------------------------------------------------- stubs

let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { AgentFilesPane } from "../agent-files-pane"

// ---------------------------------------------------------------- helpers

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

function file(name: string, extra: Record<string, unknown> = {}) {
  return {
    path: `crew-1/casey/workspace/${name}`,
    name,
    size: 1024,
    is_dir: false,
    mod_time: "2026-08-01T10:00:00Z",
    ...extra,
  }
}

function url(args: unknown[]): string {
  return String(args[0])
}

beforeEach(() => {
  workspaceId = "ws-1"
  apiFetch.mockReset()
})

// ---------------------------------------------------------------- tests

describe("AgentFilesPane", () => {
  it("shows a loading state while the agent's files are in flight", async () => {
    apiFetch.mockImplementation(() => new Promise(() => {}))
    render(<AgentFilesPane agentId="agent-1" agentSlug="casey" crewId="crew-1" />)
    expect(await screen.findByTestId("files-pane-loading")).toBeInTheDocument()
  })

  it("reads the agent files endpoint and renders the reused three-scope browser", async () => {
    apiFetch.mockImplementation((...args: unknown[]) => {
      if (url(args).includes("/api/v1/agents/agent-1/files")) return ok([file("report.md"), file("notes.txt")])
      return ok([])
    })
    render(<AgentFilesPane agentId="agent-1" agentSlug="casey" crewId="crew-1" />)

    expect(await screen.findByText("report.md")).toBeInTheDocument()
    expect(screen.getByText("notes.txt")).toBeInTheDocument()

    const agentCall = apiFetch.mock.calls.find((c) => url(c).includes("/api/v1/agents/agent-1/files"))
    expect(agentCall).toBeTruthy()
    expect(url(agentCall!)).toContain("workspace_id=ws-1")

    // The three scopes come from the existing browser, not a second one.
    expect(screen.getByText("Agent")).toBeInTheDocument()
    expect(screen.getByText("Crew")).toBeInTheDocument()
    expect(screen.getByText("Workspace")).toBeInTheDocument()
  })

  it("explains the crewless case rather than showing an empty tree", async () => {
    // GET /api/v1/agents/{id}/files returns [] for an agent with no crew_id —
    // there is no workspace on disk at all. That is not "no files yet".
    apiFetch.mockImplementation(() => ok([]))
    render(<AgentFilesPane agentId="agent-1" agentSlug="casey" crewId={null} />)

    const empty = await screen.findByTestId("files-empty-no-crew")
    expect(empty).toHaveTextContent(/crew/i)
    expect(screen.queryByText("Agent")).not.toBeInTheDocument()
  })

  it("shows an empty state for a crewed agent whose workspace is still bare", async () => {
    apiFetch.mockImplementation(() => ok([]))
    render(<AgentFilesPane agentId="agent-1" agentSlug="casey" crewId="crew-1" />)

    expect(await screen.findByTestId("files-empty-agent-scope")).toBeInTheDocument()
    // The crew scope stays reachable — the browser is still mounted.
    expect(screen.getByText("Crew")).toBeInTheDocument()
  })

  it("shows an error state naming the failure, and retries on demand", async () => {
    apiFetch.mockImplementationOnce(() => Promise.resolve({ ok: false, status: 502, json: () => Promise.resolve({}) }))
    render(<AgentFilesPane agentId="agent-1" agentSlug="casey" crewId="crew-1" />)

    const err = await screen.findByTestId("files-error")
    expect(err).toHaveTextContent(/502/)

    apiFetch.mockImplementation((...args: unknown[]) => {
      if (url(args).includes("/api/v1/agents/agent-1/files")) return ok([file("recovered.md")])
      return ok([])
    })
    fireEvent.click(screen.getByRole("button", { name: /retry/i }))

    await waitFor(() => expect(screen.getByText("recovered.md")).toBeInTheDocument())
    expect(screen.queryByTestId("files-error")).not.toBeInTheDocument()
  })
})
