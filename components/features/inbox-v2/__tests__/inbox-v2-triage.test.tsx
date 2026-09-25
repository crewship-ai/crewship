import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"

import { groupAdvisories, inboxEntry } from "../inbox-v2-derive"
import { InboxFocusOverview, InboxTriage } from "../inbox-v2-triage"
import type { InboxLookup } from "../inbox-v2-types"

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="avatar">{seed}</span>,
}))
vi.mock("@/components/ui/animated-number", () => ({
  AnimatedNumber: ({ value }: { value: number }) => <span>{value}</span>,
}))

function item(id: string, crew: string | null, overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id, workspace_id: "ws", kind: "escalation", source_id: `esc-${id}`, title: `Agent escalation: q ${id}`,
    state: "unread", priority: "high", blocking: true, sender_type: "agent", sender_name: "riley",
    created_at: "2026-09-03T10:00:00Z", updated_at: "2026-09-03T10:00:00Z",
    payload: { escalation_type: "TEXT", reason: `q ${id}`, ...(crew ? { crew_id: crew } : {}) },
    ...overrides,
  }
}

const lookup: InboxLookup = {
  crewById: new Map([["c-ops", { id: "c-ops", name: "Ops", slug: "ops", color: "emerald" }]]),
  agentBySlug: new Map([["riley", { id: "a-1", name: "Riley", slug: "riley" }]]),
  agentById: new Map([["a-1", { id: "a-1", name: "Riley", slug: "riley" }]]),
  ready: true,
}

describe("InboxTriage", () => {
  it("says what waits, by crew, and narrows to a crew by id", () => {
    const onCrew = vi.fn()
    const action = [inboxEntry(item("1", "c-ops")), inboxEntry(item("2", "c-ops")), inboxEntry(item("3", null))]
    render(<InboxTriage action={action} updates={[]} history={[]} lookup={lookup} onOpen={() => {}} onCrew={onCrew} />)
    expect(screen.getByText("Waiting for you")).toBeInTheDocument()
    expect(screen.getByText("Ops")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Filter inbox by Ops" }))
    expect(onCrew).toHaveBeenCalledWith("c-ops")
    // Workspace-wide items are a static summary, not a dead filter button.
    expect(screen.getByText("Workspace")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Filter inbox by Workspace/ })).toBeNull()
  })

  it("is never blank: the zero form says what lands here", () => {
    render(<InboxTriage action={[]} updates={[]} history={[]} lookup={lookup} onOpen={() => {}} onCrew={() => {}} />)
    expect(screen.getByText(/No decisions are waiting for you/)).toBeInTheDocument()
    expect(screen.getByText(/Nothing has been decided yet/)).toBeInTheDocument()
  })

  it("opens the next decision and lists recent decisions with an outcome word", () => {
    const onOpen = vi.fn()
    const old = inboxEntry(item("old", "c-ops", { created_at: "2026-09-01T10:00:00Z" }))
    const fresh = inboxEntry(item("new", "c-ops"))
    const decided = inboxEntry(item("d", "c-ops", { state: "resolved", resolved_action: "reject", resolved_at: "2026-09-03T11:00:00Z" }))
    render(<InboxTriage action={[fresh, old]} updates={[]} history={[decided]} lookup={lookup} onOpen={onOpen} onCrew={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: /Review next/ }))
    expect(onOpen).toHaveBeenCalledWith(fresh)
    expect(screen.getByText("Rejected")).toBeInTheDocument()
    expect(screen.queryByText("reject")).not.toBeInTheDocument()
  })

  it("separates routine alerts from decisions", () => {
    const onOpen = vi.fn()
    const decision = inboxEntry(item("decision", "c-ops"))
    const missed = inboxEntry(item("missed", "c-ops", { kind: "schedule_missed", blocking: false, payload: { schedule_id: "s1", crew_id: "c-ops" } }))
    render(<InboxTriage action={[missed, decision]} updates={[]} history={[]} lookup={lookup} onOpen={onOpen} onCrew={() => {}} />)
    expect(screen.getByText("1 decision")).toBeInTheDocument()
    expect(screen.getByText("Routine alerts")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Review next/ }))
    expect(onOpen).toHaveBeenCalledWith(decision)
  })
})

describe("InboxFocusOverview", () => {
  it("counts the underlying items when matching notices are grouped", () => {
    const grouped = groupAdvisories([1, 2].map((n) => inboxEntry(item(`skill-${n}`, null, {
      kind: "escalation", blocking: false, sender_name: "Skill Curator",
      title: `Skill check: agent ${n}`, payload: {},
    }))))
    render(<InboxFocusOverview label="Approvals waiting" entries={grouped} lookup={lookup} onOpen={() => {}} onClear={() => {}} />)
    expect(screen.getByText("2 matching active items")).toBeInTheDocument()
    expect(screen.getByText("2 items")).toBeInTheDocument()
  })

  it("shows only the selected dashboard category and offers a way back", () => {
    const onClear = vi.fn()
    render(<InboxFocusOverview label="Approvals waiting" entries={[inboxEntry(item("approval", "c-ops"))]} lookup={lookup} onOpen={() => {}} onClear={onClear} />)
    expect(screen.getByRole("heading", { name: "Approvals waiting" })).toBeInTheDocument()
    expect(screen.getByText("q approval")).toBeInTheDocument()
    expect(screen.queryByText("Routine alerts")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /All inbox/ }))
    expect(onClear).toHaveBeenCalledOnce()
  })
})
