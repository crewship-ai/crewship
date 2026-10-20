import { describe, it, expect, vi, afterEach, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))

import { CrewMemoryCards } from "../crew-memory"

const doc = (name: string, content: string, scope = "crew") => ({ id: `${scope}:${name}`, name, scope, content, state: "available", bytes: content.length, updated_at: "2026-10-10T10:00:00Z", history_path: `crew:research/${name}` })

function stub(documents: unknown[], versions: unknown[] = []) {
  return vi.spyOn(global, "fetch").mockImplementation(async (input) => {
    const url = String(input)
    if (url.includes("/api/v1/inbox")) return new Response(JSON.stringify({ rows: [] }))
    if (url.includes("/memory/versions")) return new Response(JSON.stringify({ entries: versions }))
    if (url.includes("/crews/c1/memory")) return new Response(JSON.stringify({ documents, scopes: { crew: "available" } }))
    return new Response("{}", { status: 404 })
  })
}

const agents = [
  { id: "a1", name: "Scout", slug: "scout", status: "IDLE", role_title: null, agent_role: "LEAD", memory_enabled: true },
  { id: "a2", name: "Writer", slug: "writer", status: "IDLE", role_title: null, agent_role: "AGENT", memory_enabled: false },
]

describe("Crew Memory cards (C-MEM)", () => {
  beforeEach(() => cleanup())
  afterEach(() => vi.restoreAllMocks())

  it("CREW.md shows who wrote it last and opens its version history", async () => {
    stub([doc("CREW.md", "## Conventions\n- Quote prices exactly.")], [
      { id: "v2", sha256: "bbb", written_at: "2026-10-10T10:00:00Z", written_by: "scout" },
      { id: "v1", sha256: "aaa", written_at: "2026-10-09T10:00:00Z", written_by: "pavel" },
    ])
    render(<CrewMemoryCards workspaceId="ws1" crewId="c1" agents={agents} onOpenAgent={vi.fn()} />)
    const card = await screen.findByRole("region", { name: "CREW.md · how this crew works" })
    expect(await within(card).findByText(/Quote prices exactly/)).toBeInTheDocument()
    expect(await within(card).findByText(/Last written by scout/)).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Version history · 2" }))
    expect(await within(card).findByText(/pavel/)).toBeInTheDocument()
  })

  it("has no Waiting for review card while no memory proposal waits", async () => {
    stub([doc("CREW.md", "x")])
    render(<CrewMemoryCards workspaceId="ws1" crewId="c1" agents={agents} onOpenAgent={vi.fn()} />)
    await screen.findByRole("region", { name: "CREW.md · how this crew works" })
    expect(screen.queryByText(/Waiting for review/)).not.toBeInTheDocument()
  })

  it("lists every agent's AGENT.md and opens the agent's Memory tab", async () => {
    stub([doc("CREW.md", "x")])
    const onOpenAgent = vi.fn()
    render(<CrewMemoryCards workspaceId="ws1" crewId="c1" agents={agents} onOpenAgent={onOpenAgent} />)
    const card = await screen.findByRole("region", { name: "Shared notes and agent knowledge" })
    expect(within(card).getByText("Scout · AGENT.md")).toBeInTheDocument()
    expect(within(card).getByText("Memory is off for this agent")).toBeInTheDocument()
    fireEvent.click(within(card).getAllByRole("button", { name: /^Open/ })[0])
    expect(onOpenAgent).toHaveBeenCalledWith("scout")
  })

  it("says so when the crew has no CREW.md yet", async () => {
    stub([])
    render(<CrewMemoryCards workspaceId="ws1" crewId="c1" agents={[]} onOpenAgent={vi.fn()} />)
    expect(await screen.findByText(/No CREW.md yet/)).toBeInTheDocument()
  })
})
