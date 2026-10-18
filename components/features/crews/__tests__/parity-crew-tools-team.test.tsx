import { describe, it, expect, vi, afterEach, beforeEach } from "vitest"
import { render, screen, cleanup, within, waitFor, fireEvent } from "@testing-library/react"

const api = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api.fetch(...a) }))

import { CrewTools } from "../crew-tools"
import { CrewTeam } from "../crew-team"
import type { AgentSummary } from "../crew-canvas-tabs/types"

afterEach(cleanup)

const json = (v: unknown, ok = true) => Promise.resolve({ ok, status: ok ? 200 : 500, json: () => Promise.resolve(v) })
const agents = [
  { id: "a1", name: "Scout", slug: "scout", status: "IDLE", agent_role: "LEAD" },
  { id: "a2", name: "Writer", slug: "writer", status: "IDLE", agent_role: "AGENT" },
] as unknown as AgentSummary[]

beforeEach(() => {
  api.fetch.mockReset()
  api.fetch.mockImplementation((url: string, init?: { method?: string }) => {
    if (init?.method === "POST" || init?.method === "DELETE") return json({ id: "asg-new" })
    if (url.includes("/agents/a1/skills")) return json([{ id: "asg1", skill_id: "s1", enabled: true, skill: { id: "s1", name: "web-search", slug: "web-search" } }])
    if (url.includes("/agents/a2/skills")) return json([])
    if (url.includes("/agents/a1/credential-bindings")) return json({ slots: [{ credential_id: "k1", credential_name: "Anthropic production" }] })
    if (url.includes("/agents/a2/credential-bindings")) return json({ slots: [] })
    return json([])
  })
})

// PAR-120: two cards, one per kind, each a matrix of checkbox cells per agent.
describe("Crew Tools and access", () => {
  const renderTools = () => render(<CrewTools workspaceId="ws" crewSlug="research" agents={agents} integrations={[]} />)

  it("has a Skills card and a Credentials card with their own add actions", async () => {
    renderTools()
    const skills = await screen.findByRole("region", { name: "Skills" })
    const creds = screen.getByRole("region", { name: "Credentials" })
    expect(within(skills).getByRole("link", { name: /Add skill/ })).toHaveAttribute("href", expect.stringContaining("/skills"))
    expect(within(creds).getByRole("link", { name: /Share a credential/ })).toHaveAttribute("href", expect.stringContaining("/credentials"))
    expect(screen.queryByRole("region", { name: "Tools and access" })).toBeNull()
  })

  it("skill cells are checkboxes per agent and toggle through the agent's skills", async () => {
    renderTools()
    const skills = await screen.findByRole("region", { name: "Skills" })
    const has = within(skills).getByRole("checkbox", { name: /Scout uses web-search/ })
    const lacks = within(skills).getByRole("checkbox", { name: /Writer does not use web-search/ })
    expect(has).toBeChecked()
    expect(lacks).not.toBeChecked()
    fireEvent.click(lacks)
    await waitFor(() => expect(api.fetch).toHaveBeenCalledWith("/api/v1/agents/a2/skills", expect.objectContaining({ method: "POST" })))
    fireEvent.click(has)
    await waitFor(() => expect(api.fetch).toHaveBeenCalledWith("/api/v1/agents/a1/skills/asg1", expect.objectContaining({ method: "DELETE" })))
  })

  it("credential cells are checkboxes and the filter, search and the info note follow v67", async () => {
    renderTools()
    const creds = await screen.findByRole("region", { name: "Credentials" })
    expect(within(creds).getByRole("checkbox", { name: /Scout uses Anthropic production/ })).toBeChecked()
    expect(within(creds).getByRole("columnheader", { name: "State" })).toBeInTheDocument()
    expect(screen.getByRole("radio", { name: /^Skills/ })).toBeInTheDocument()
    // The banner is the last thing on the page, not above the cards.
    const note = screen.getByText("Credentials never reach an agent as text")
    const last = screen.getByTestId("crew-tools").lastElementChild as HTMLElement
    expect(last.contains(note)).toBe(true)
  })
})

// PAR-121: Cost · 30 d is a column even when the reader cannot see spend.
describe("Crew team table", () => {
  const props = { agents, autonomy: "guided", hireLimit: 10, onSelectAgent: vi.fn(), onNewAgent: vi.fn(), onHire: vi.fn(), onApprove: vi.fn(), onRehire: vi.fn() }
  it("always has the Cost · 30 d column; unknown cost reads a dash", () => {
    render(<CrewTeam {...props} runsByAgent={new Map([["a1", 5]])} costByAgent={null} />)
    expect(screen.getByRole("columnheader", { name: "Cost · 30 d" })).toBeInTheDocument()
    const row = document.querySelector('[data-agent-row="scout"]') as HTMLElement
    expect(within(row).getAllByText("—").length).toBeGreaterThan(0)
  })
  it("shows the cost when it is known, in sans tabular numerals", () => {
    render(<CrewTeam {...props} runsByAgent={new Map([["a1", 5]])} costByAgent={new Map([["a1", 6.4]])} />)
    const row = document.querySelector('[data-agent-row="scout"]') as HTMLElement
    const cost = within(row).getByText("$6.40")
    expect(cost.className).toMatch(/tabular-nums/)
    expect(cost.className).not.toMatch(/font-mono/)
  })
  it("status pills are sans", () => {
    render(<CrewTeam {...props} />)
    const row = document.querySelector('[data-agent-row="scout"]') as HTMLElement
    expect(row.querySelector("[data-slot=status-pill]")?.className).toMatch(/font-sans/)
  })
})
