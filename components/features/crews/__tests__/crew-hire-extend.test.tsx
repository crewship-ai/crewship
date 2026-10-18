import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within, waitFor } from "@testing-library/react"

import { CrewTeam } from "@/components/features/crews/crew-team"
import { RehireDialog } from "@/components/features/crews/crew-hire-dialog"
import type { AgentSummary } from "@/components/features/crews/crew-canvas-tabs/types"

const NOW = Date.parse("2026-10-17T10:00:00Z")
const base = { status: "IDLE", role_title: null, agent_role: "AGENT" } as const
const agents: AgentSummary[] = [
  { ...base, id: "a1", name: "Ledger", slug: "ledger", agent_role: "LEAD" },
  { ...base, id: "a2", name: "Auditor", slug: "auditor", ephemeral: true, status: "PENDING_REVIEW" },
  { ...base, id: "a3", name: "Pager", slug: "pager", ephemeral: true, expires_at: "2026-10-17T10:38:00Z" },
  { ...base, id: "a4", name: "Night watch", slug: "nightwatch", ephemeral: true, expired_at: "2026-10-16T22:00:00Z" },
]

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function renderTeam(extra: Partial<React.ComponentProps<typeof CrewTeam>> = {}) {
  const props = {
    agents, autonomy: "guided", hireLimit: 10, onSelectAgent: vi.fn(), onNewAgent: vi.fn(), onHire: vi.fn(),
    onApprove: vi.fn(), onRehire: vi.fn(), now: NOW, ...extra,
  }
  render(<CrewTeam {...props} />)
  return props
}

describe("Extend a temporary hire", () => {
  it("a live hire offers Extend, which hands the agent to the caller", () => {
    const onExtend = vi.fn()
    renderTeam({ onExtend })
    const active = document.querySelector('[data-hire-state="active"]') as HTMLElement
    fireEvent.click(within(active).getByRole("button", { name: "Extend" }))
    expect(onExtend).toHaveBeenCalledWith(expect.objectContaining({ id: "a3" }))
  })

  it("only a live hire is extended: pending and expired rows keep their own verbs", () => {
    renderTeam({ onExtend: vi.fn() })
    expect(within(document.querySelector('[data-hire-state="pending"]') as HTMLElement).queryByRole("button", { name: "Extend" })).toBeNull()
    expect(within(document.querySelector('[data-hire-state="expired"]') as HTMLElement).queryByRole("button", { name: "Extend" })).toBeNull()
  })

  it("is not offered to someone who may not hire (no handler)", () => {
    renderTeam()
    expect(screen.queryByRole("button", { name: "Extend" })).toBeNull()
  })

  it("there is no Planned tag and no Release now: the endpoint does not exist", () => {
    renderTeam({ onExtend: vi.fn() })
    expect(screen.queryByText("Planned")).toBeNull()
    expect(screen.queryByRole("button", { name: /Release now/ })).toBeNull()
  })
})

describe("RehireDialog in extend mode", () => {
  it("names the action Extend and posts the new lifetime to rehire", async () => {
    const calls: Array<{ url: string; body: unknown }> = []
    vi.spyOn(global, "fetch").mockImplementation(async (url, init) => {
      calls.push({ url: String(url), body: JSON.parse(String(init?.body ?? "{}")) })
      return new Response("{}", { status: 200 })
    })
    const onRehired = vi.fn()
    render(<RehireDialog open onOpenChange={vi.fn()} workspaceId="ws1" agent={{ id: "a3", name: "Pager" }} extend onRehired={onRehired} />)
    expect(screen.getByText("Extend Pager")).toBeInTheDocument()
    expect(screen.getByText(/from now/i)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/For how long/), { target: { value: "120" } })
    fireEvent.change(screen.getByLabelText(/Why/), { target: { value: "Still paging" } })
    fireEvent.click(screen.getByRole("button", { name: "Extend" }))
    await waitFor(() => expect(onRehired).toHaveBeenCalled())
    expect(calls[0].url).toContain("/api/v1/agents/a3/rehire")
    expect(calls[0].body).toEqual({ ttl_minutes: 120, reason: "Still paging" })
  })

  it("without extend it is still the Rehire dialog", () => {
    render(<RehireDialog open onOpenChange={vi.fn()} workspaceId="ws1" agent={{ id: "a4", name: "Night watch" }} onRehired={vi.fn()} />)
    expect(screen.getByText("Rehire Night watch")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Rehire" })).toBeInTheDocument()
  })
})
