import { act } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { AgentMemoryTab } from "../agent-memory-tab"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
const handlers = new Map<string, () => void>()
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (type: string, cb: () => void) => { handlers.set(type, cb) } }))
vi.mock("../../chat-agent-context", () => ({ useChatAgent: () => ({ name: "Jamie", slug: "jamie", crewId: "crew-1" }) }))
vi.mock("../preferences-tab", () => ({ PreferencesTab: () => <section aria-label="Your preferences for this agent">prefs</section> }))
vi.mock("@/components/features/issues/markdown-content", () => ({ MarkdownContent: ({ children }: { children: string }) => <div>{children}</div> }))

let documents: unknown[]
beforeEach(() => {
  handlers.clear()
  documents = [
    { id: "agent:jamie/AGENT.md", name: "AGENT.md", scope: "agent", state: "available", content: "Prefers short release notes.", bytes: 30 },
    { id: "agent:jamie/daily/2026-11-19.md", name: "daily/2026-11-19.md", scope: "agent", state: "available", content: "Wrote intro deck.", bytes: 20 },
    { id: "crew:crew-1/CREW.md", name: "CREW.md", scope: "crew", state: "available", content: "Ship on Fridays.", bytes: 20 },
  ]
  apiFetch.mockReset()
  apiFetch.mockImplementation(async () => ({ ok: true, status: 200, json: async () => ({ documents, scopes: { agent: "available", crew: "available" } }) }))
})

describe("AgentMemoryTab", () => {
  it("shows what the agent and its crew remember, read-only, with the viewer's own preferences kept", async () => {
    render(<AgentMemoryTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByText("Prefers short release notes.")).toBeInTheDocument()
    expect(screen.getByText("Ship on Fridays.")).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "What Jamie remembers" })).toHaveTextContent("Wrote intro deck.")
    expect(screen.getByRole("region", { name: "Your preferences for this agent" })).toBeInTheDocument()
    for (const name of [/Forget/, /History/, /Export/, /Remember between runs/]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument()
      expect(screen.queryByRole("switch", { name })).not.toBeInTheDocument()
    }
    expect(screen.getByRole("link", { name: /Manage memory/ })).toHaveAttribute("href", "/crews?agent=jamie&tab=memory")
    expect(apiFetch.mock.calls[0][0]).toBe("/api/v1/agents/agent-1/memory?workspace_id=ws-1")
  })

  it("says plainly when nothing is remembered yet and when memory cannot be read", async () => {
    documents = []
    const view = render(<AgentMemoryTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByText("Jamie has not saved anything yet.")).toBeInTheDocument()
    apiFetch.mockImplementation(async () => ({ ok: false, status: 500, json: async () => ({}) }))
    view.rerender(<AgentMemoryTab agentId="agent-2" workspaceId="ws-1" />)
    expect(await screen.findByRole("alert")).toHaveTextContent("Memory could not be loaded.")
  })

  it("rereads memory when a run ends or the connection comes back", async () => {
    render(<AgentMemoryTab agentId="agent-1" workspaceId="ws-1" />)
    await screen.findByText("Prefers short release notes.")
    documents = [{ id: "agent:jamie/AGENT.md", name: "AGENT.md", scope: "agent", state: "available", content: "Now prefers bullet points.", bytes: 30 }]
    await act(async () => { handlers.get("run.completed")!() })
    await waitFor(() => expect(screen.getByText("Now prefers bullet points.")).toBeInTheDocument())
    expect(handlers.has("realtime.reconnected")).toBe(true)
  })
})
