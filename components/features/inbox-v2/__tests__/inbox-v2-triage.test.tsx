import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"

import { inboxEntry } from "../inbox-v2-derive"
import { InboxTriage } from "../inbox-v2-triage"
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
    render(<InboxTriage action={action} updates={[]} history={[]} lookup={lookup} live={false} onOpen={() => {}} onCrew={onCrew} />)
    expect(screen.getByText("Waiting for you")).toBeInTheDocument()
    expect(screen.getByText("Ops")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Ops/ }))
    expect(onCrew).toHaveBeenCalledWith("c-ops")
    // A row without a crew is a bucket you cannot narrow to.
    expect(screen.getByRole("button", { name: /No crew/ })).toBeDisabled()
  })

  it("is never blank: the zero form says what lands here", () => {
    render(<InboxTriage action={[]} updates={[]} history={[]} lookup={lookup} live={false} onOpen={() => {}} onCrew={() => {}} />)
    expect(screen.getByText(/questions from agents, failed runs and missed schedules land here/)).toBeInTheDocument()
    expect(screen.getByText(/Nothing has been decided yet/)).toBeInTheDocument()
  })

  it("opens the oldest item and lists recent decisions with an outcome word", () => {
    const onOpen = vi.fn()
    const old = inboxEntry(item("old", "c-ops", { created_at: "2026-09-01T10:00:00Z" }))
    const fresh = inboxEntry(item("new", "c-ops"))
    const decided = inboxEntry(item("d", "c-ops", { state: "resolved", resolved_action: "reject", resolved_at: "2026-09-03T11:00:00Z" }))
    render(<InboxTriage action={[fresh, old]} updates={[]} history={[decided]} lookup={lookup} live={true} onOpen={onOpen} onCrew={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: /Open oldest/ }))
    expect(onOpen).toHaveBeenCalledWith(old)
    expect(screen.getByText("Rejected")).toBeInTheDocument()
    expect(screen.queryByText("reject")).not.toBeInTheDocument()
  })
})
