import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// Harbor 2 (#3385): one detail layout for every kind — head (sender +
// attention), title, message, the decision block with its dock, then "Where
// this came from". The decision is always in the same place.

vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import { InboxDetail, inboxArchivable } from "../inbox-detail"

afterEach(cleanup)

function item(over: Partial<InboxItem> & Pick<InboxItem, "kind" | "title">): InboxItem {
  return {
    id: "i", workspace_id: "ws", source_id: "src", state: "unread", priority: "medium", blocking: false,
    body_md: "The message the agent wrote.",
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(), ...over,
  } as InboxItem
}

const KINDS: [string, InboxItem][] = [
  ["waitpoint", item({ kind: "waitpoint", title: "Publish weekly report?", blocking: true, payload: { pipeline_run_id: "r1", pipeline_slug: "scan" } })],
  ["hire", item({ kind: "waitpoint", title: "Hire ephemeral agent: Analyst", blocking: true, payload: { kind: "hire" } })],
  ["question", item({ kind: "escalation", title: "Merge with failing lint?", blocking: true, payload: { escalation_type: "TEXT", chat_url: "/chat/rev" } })],
  ["skill proposal", item({ kind: "escalation", title: "Skill proposed: pricing-diff", payload: { kind: "skill_proposal", crew_id: "c" } })],
  ["memory", item({ kind: "memory_consolidation", title: "6 rules pending review", payload: { proposal_id: "p1" } })],
  ["missed", item({ kind: "schedule_missed", title: "Schedule missed 2 occurrences", payload: { schedule_id: "s1", pipeline_slug: "digest" } })],
  ["paused", item({ kind: "schedule_circuit_breaker_tripped", title: "Routine paused", payload: { schedule_id: "s1", pipeline_slug: "triage" } })],
  ["failed run", item({ kind: "failed_run", title: "Scheduled routine failed", payload: { pipeline_slug: "triage", pipeline_run_id: "r2" } })],
  ["run needs human", item({ kind: "run_needs_human", title: "Scout needs your input", blocking: true, actions: [{ id: "answer", label: "Answer", effect: "", irreversible: false }], payload: { issue_identifier: "CRW-151" } })],
  ["message", item({ kind: "message", title: "CRW-142 ready for review", payload: { issue_identifier: "CRW-142" } })],
]

describe("one detail layout for every kind", () => {
  it.each(KINDS)("%s renders head, title, message, then the dock", (_, it) => {
    render(<InboxDetail item={it} role="OWNER" onResolve={vi.fn()} onArchive={vi.fn()} onMarkUnread={vi.fn()} onRefresh={vi.fn()} menu={false} />)
    const frame = screen.getByTestId("inbox-detail-frame")
    const head = within(frame).getByTestId("inbox-detail-head")
    const title = within(frame).getByRole("heading", { level: 1, name: it.title })
    const message = within(frame).getByText("The message the agent wrote.")
    const dock = within(frame).getByTestId("decision-dock")
    expect(within(dock).getAllByRole("button").length + within(dock).queryAllByRole("link").length).toBeGreaterThan(0)
    // Document order: head → title → message → dock.
    const follows = (a: Element, b: Element) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)
    expect(follows(head, title)).toBe(true)
    expect(follows(title, message)).toBe(true)
    expect(follows(message, dock)).toBe(true)
  })

  it("names where it came from as chips after the decision", () => {
    render(<InboxDetail item={KINDS[0][1]} role="OWNER" onResolve={vi.fn()} onArchive={vi.fn()} onMarkUnread={vi.fn()} onRefresh={vi.fn()} menu={false} />)
    const origin = screen.getByRole("navigation", { name: "Where this came from" })
    expect(within(origin).getByRole("link", { name: /Open run/ })).toBeInTheDocument()
    const dock = screen.getByTestId("decision-dock")
    expect(Boolean(dock.compareDocumentPosition(origin) & Node.DOCUMENT_POSITION_FOLLOWING)).toBe(true)
  })

  it("Archive disabled on undecided waitpoint", () => {
    expect(inboxArchivable(KINDS[0][1])).toBe(false)
    expect(inboxArchivable({ ...KINDS[0][1], source_missing: true })).toBe(true)
    expect(inboxArchivable(KINDS[9][1])).toBe(true)
    expect(inboxArchivable({ ...KINDS[9][1], state: "resolved" })).toBe(false)
  })

  it("names who decides beside the buttons it disables", () => {
    render(<InboxDetail item={KINDS[3][1]} role="MANAGER" onResolve={vi.fn()} onArchive={vi.fn()} onMarkUnread={vi.fn()} onRefresh={vi.fn()} menu={false} />)
    const dock = screen.getByTestId("decision-dock")
    expect(within(dock).getByRole("button", { name: /Approve/ })).toBeDisabled()
    expect(within(dock).getByTestId("inbox-role-note")).toHaveTextContent("An owner or admin decides this")
  })
})
