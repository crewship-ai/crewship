import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

import { CrewTeam, hireState } from "@/components/features/crews/crew-team"
import type { AgentSummary } from "@/components/features/crews/crew-canvas-tabs/types"

const NOW = Date.parse("2026-10-17T10:00:00Z")
const base = { status: "IDLE", role_title: null, agent_role: "AGENT" } as const
const agents: AgentSummary[] = [
  { ...base, id: "a1", name: "Ledger", slug: "ledger", agent_role: "LEAD", cli_adapter: "CLAUDE_CODE", llm_model: "claude-sonnet-4-5", tool_profile: "CODING", _count: { skills: 1, credentials: 0 } },
  { ...base, id: "a2", name: "Auditor", slug: "auditor", ephemeral: true, status: "PENDING_REVIEW", hire_reason: "[2026-10-17] Cross-check September invoices" },
  { ...base, id: "a3", name: "Pager", slug: "pager", ephemeral: true, expires_at: "2026-10-17T10:38:00Z" },
  { ...base, id: "a4", name: "Night watch", slug: "nightwatch", ephemeral: true, expired_at: "2026-10-16T22:00:00Z" },
]

afterEach(cleanup)

function renderTeam(extra: Partial<React.ComponentProps<typeof CrewTeam>> = {}) {
  const props = {
    agents,
    autonomy: "guided",
    hireLimit: 10,
    leadName: "Ledger",
    runsByAgent: new Map([["a1", 5]]),
    onSelectAgent: vi.fn(),
    onNewAgent: vi.fn(),
    onHire: vi.fn(),
    onApprove: vi.fn(),
    onRehire: vi.fn(),
    now: NOW,
    ...extra,
  }
  render(<CrewTeam {...props} />)
  return props
}

describe("CrewTeam", () => {
  it("temporary hires: pending shows Approve, active shows time left, expired shows Rehire", () => {
    const p = renderTeam()
    const pending = document.querySelector('[data-hire-state="pending"]') as HTMLElement
    const active = document.querySelector('[data-hire-state="active"]') as HTMLElement
    const expired = document.querySelector('[data-hire-state="expired"]') as HTMLElement
    expect(within(pending).getByText(/Waiting for approval/)).toBeInTheDocument()
    expect(within(pending).getByText(/Cross-check September invoices/)).toBeInTheDocument()
    fireEvent.click(within(pending).getByRole("button", { name: "Approve" }))
    expect(p.onApprove).toHaveBeenCalledWith(expect.objectContaining({ id: "a2" }))
    expect(within(pending).getByRole("link", { name: "Review in Inbox" })).toHaveAttribute("href", expect.stringContaining("/inbox"))
    expect(within(active).getByText(/Expires in 38 min/)).toBeInTheDocument()
    fireEvent.click(within(expired).getByRole("button", { name: /Rehire/ }))
    expect(p.onRehire).toHaveBeenCalledWith(expect.objectContaining({ id: "a4" }))
    // Release now has no endpoint yet (#3454): never offered, on any row.
    expect(screen.queryByRole("button", { name: /Release now/ })).toBeNull()
  })

  it("counts hires in use against the crew's limit", () => {
    renderTeam()
    expect(screen.getByRole("region", { name: "Temporary hires · 2 of 10 in use" })).toBeInTheDocument()
  })

  it("the table lists permanent agents with tool access, credentials and runs over 30 days", () => {
    renderTeam()
    const row = document.querySelector('[data-agent-row="ledger"]') as HTMLElement
    expect(within(row).getByText("LEAD")).toBeInTheDocument()
    expect(within(row).getByText("Workspace work")).toBeInTheDocument()
    expect(within(row).getByText("none")).toBeInTheDocument()
    expect(within(row).getByText("5")).toBeInTheDocument()
    expect(document.querySelector('[data-agent-row="pager"]')).toBeNull()
  })

  it("a strict crew cannot hire and says so", () => {
    renderTeam({ agents: [agents[0]], autonomy: "strict" })
    expect(screen.getByRole("button", { name: /Hire a temporary agent/ })).toBeDisabled()
    expect(screen.getByText(/hires are not allowed/)).toBeInTheDocument()
  })

  it("hireState names the three states", () => {
    expect(hireState(agents[0])).toBeNull()
    expect(hireState(agents[1])).toBe("pending")
    expect(hireState(agents[2])).toBe("active")
    expect(hireState(agents[3])).toBe("expired")
  })
})
