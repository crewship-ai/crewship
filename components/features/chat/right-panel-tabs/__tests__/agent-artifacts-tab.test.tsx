import { act } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { AgentArtifactsTab } from "../agent-artifacts-tab"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const handlers = new Map<string, (event: { type: string; payload: Record<string, unknown> }) => void>()
const channels: string[] = []
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEventSafe: (type: string, cb: (event: { type: string; payload: Record<string, unknown> }) => void) => { handlers.set(type, cb) },
  useRealtimeChannelSafe: (channel: string | null) => { if (channel) channels.push(channel) },
}))

const file = (name: string) => ({ name, path: `crew-1/jamie/runs/run-1/${name}`, is_dir: false, size: 2048, mod_time: "2026-11-19T10:00:00Z" })
let rows: unknown[]

beforeEach(() => {
  handlers.clear()
  channels.length = 0
  rows = [file("intro.html")]
  apiFetch.mockReset()
  apiFetch.mockImplementation(async () => ({ ok: true, status: 200, json: async () => rows }))
})

async function fire(type: string, payload: Record<string, unknown> = {}) {
  vi.useFakeTimers()
  try {
    act(() => { handlers.get(type)!({ type, payload }) })
    await act(async () => { vi.advanceTimersByTime(1000) })
  } finally { vi.useRealTimers() }
}

describe("AgentArtifactsTab live outputs", () => {
  it("listens on the agent's crew channel and refreshes when this agent writes a file", async () => {
    render(<AgentArtifactsTab agentId="agent-1" workspaceId="ws-1" crewId="crew-1" agentSlug="jamie" />)
    expect(await screen.findByText("intro.html")).toBeInTheDocument()
    expect(channels).toContain("crew:crew-1")
    rows = [file("intro.html"), file("deck.html")]
    await fire("file.event", { event: "write", path: "jamie/runs/run-1/deck.html", agent: "jamie" })
    expect(await screen.findByText("deck.html")).toBeInTheDocument()
    expect(screen.getByText(/Output files/)).toHaveTextContent("2")
  })

  it("ignores file events from other agents in the crew", async () => {
    render(<AgentArtifactsTab agentId="agent-1" workspaceId="ws-1" crewId="crew-1" agentSlug="jamie" />)
    await screen.findByText("intro.html")
    const before = apiFetch.mock.calls.length
    await fire("file.event", { event: "write", path: "other/notes.md", agent: "other" })
    expect(apiFetch.mock.calls.length).toBe(before)
  })

  it("refreshes after a reconnect and when a run ends, keeping the list visible meanwhile", async () => {
    render(<AgentArtifactsTab agentId="agent-1" workspaceId="ws-1" crewId="crew-1" agentSlug="jamie" />)
    await screen.findByText("intro.html")
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => { release = resolve })
    apiFetch.mockImplementation(async () => { await gate; return { ok: true, status: 200, json: async () => [file("intro.html"), file("after.html")] } })
    await fire("realtime.reconnected")
    expect(screen.getByText("intro.html")).toBeInTheDocument()
    expect(screen.queryByText("Loading artifacts…")).not.toBeInTheDocument()
    await act(async () => { release() })
    expect(await screen.findByText("after.html")).toBeInTheDocument()
    expect(handlers.has("run.completed")).toBe(true)
    expect(handlers.has("run.failed")).toBe(true)
  })

  it("clears the list when switching to another agent", async () => {
    const view = render(<AgentArtifactsTab agentId="agent-1" workspaceId="ws-1" crewId="crew-1" agentSlug="jamie" />)
    await screen.findByText("intro.html")
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => { release = resolve })
    apiFetch.mockImplementation(async () => { await gate; return { ok: true, status: 200, json: async () => [] } })
    view.rerender(<AgentArtifactsTab agentId="agent-2" workspaceId="ws-1" crewId="crew-1" agentSlug="riley" />)
    await waitFor(() => expect(screen.queryByText("intro.html")).not.toBeInTheDocument())
    await act(async () => { release() })
  })
})
