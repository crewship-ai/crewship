import { describe, it, expect, vi, afterEach, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), loading: vi.fn() }) }))

import { AgentMemoryCards } from "../agent-memory"

const agentDoc = { id: "agent:AGENT.md", name: "AGENT.md", scope: "agent", content: "## What I know\n- Pricing pages change monthly.", state: "available", bytes: 40, history_path: "agent:scout/AGENT.md" }

function stub({ cards = [{ id: "pc1", agent_id: "a1", agent_slug: "scout", content: "Prefers tables; reviews on Thursdays" }] as unknown[] } = {}) {
  return vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? "GET"
    if (url.includes("/users/me/peer-cards")) return new Response(JSON.stringify({ peers: cards }))
    if (url.includes("/agents/a1/peers/u1") && method === "DELETE") return new Response(null, { status: 204 })
    if (url.includes("/agents/a1/memory")) return new Response(JSON.stringify({ documents: [agentDoc, { ...agentDoc, id: "agent:daily", name: "daily/2026-10-01.md" }], scopes: { agent: "available" } }))
    if (url.includes("/agents/a1/learning")) return new Response(JSON.stringify({ agent_id: "a1", enabled: false }))
    if (url.includes("/memory/versions")) return new Response(JSON.stringify({ entries: [] }))
    return new Response("{}", { status: 404 })
  })
}

const base = { workspaceId: "ws1", agentId: "a1", agentSlug: "scout", agentName: "Scout", crewId: "c1", userId: "u1", userName: "Pavel Srba", memoryEnabled: true, canEdit: true, canExport: false }

describe("Agent Memory cards (A-MEM)", () => {
  beforeEach(() => cleanup())
  afterEach(() => vi.restoreAllMocks())

  it("Remember between runs is a switch that saves on the spot", async () => {
    stub()
    const onMemoryEnabled = vi.fn().mockResolvedValue(undefined)
    render(<AgentMemoryCards {...base} onMemoryEnabled={onMemoryEnabled} />)
    const sw = await screen.findByRole("switch", { name: "Remember between runs" })
    expect(sw).toHaveAttribute("aria-checked", "true")
    fireEvent.click(sw)
    await waitFor(() => expect(onMemoryEnabled).toHaveBeenCalledWith(false))
  })

  it("What it knows about you: Forget asks a question title and a verb button", async () => {
    const spy = stub()
    render(<AgentMemoryCards {...base} onMemoryEnabled={vi.fn()} />)
    const card = await screen.findByRole("region", { name: "What it knows about you" })
    expect(await within(card).findByText(/Prefers tables/)).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Forget" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText("Forget what Scout knows about you?")).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole("button", { name: "Forget" }))
    await waitFor(() => expect(spy.mock.calls.some(([u, i]) => String(u).includes("/agents/a1/peers/u1") && i?.method === "DELETE")).toBe(true))
  })

  it("AGENT.md card shows the note, History and the count of all notes", async () => {
    stub({ cards: [] })
    render(<AgentMemoryCards {...base} onMemoryEnabled={vi.fn()} />)
    const card = await screen.findByRole("region", { name: "AGENT.md" })
    expect(await within(card).findByText(/Pricing pages change monthly/)).toBeInTheDocument()
    expect(within(card).getByRole("button", { name: "History" })).toBeInTheDocument()
    expect(within(card).getByRole("button", { name: "All notes · 2" })).toBeInTheDocument()
    expect(screen.getByText("Nothing saved about you yet.")).toBeInTheDocument()
  })
})
