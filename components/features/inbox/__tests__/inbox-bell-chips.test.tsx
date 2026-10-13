import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

import {
  InboxBellView,
  filterInboxByKind,
  inboxBadge,
  inboxKindChips,
  inlineDecisionFor,
  type InboxKindChip,
} from "../inbox-bell-new"
import type { WorkspaceRole } from "../inbox-derive"

afterEach(cleanup)

// The bell's kind chips, badge tone and inline-decision gate, as pure helpers
// first (table-driven) and then once through the view to pin the wiring.

const now = Date.now()

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind">): InboxItem {
  return {
    title: over.id,
    workspace_id: "ws",
    source_id: `s-${over.id}`,
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: new Date(now - 60_000).toISOString(),
    updated_at: new Date(now - 60_000).toISOString(),
    ...over,
  } as InboxItem
}

const wp = (over: Partial<InboxItem> = {}) =>
  item({ id: "wp", kind: "waitpoint", blocking: true, attention_class: "decision", ...over })

describe("inboxKindChips", () => {
  it.each<{ name: string; classes: (InboxItem["attention_class"])[]; want: string[] }>([
    { name: "nothing", classes: [], want: ["all:0"] },
    { name: "unclassified rows count under All only", classes: [undefined, undefined], want: ["all:2"] },
    {
      name: "one chip per present class, fixed order, zero-count kinds hidden",
      classes: ["repair", "decision", undefined, "decision", "review"],
      want: ["all:5", "decision", "review", "repair"],
    },
    { name: "input alone", classes: ["input"], want: ["all:1", "input"] },
  ])("$name", ({ classes, want }) => {
    const items = classes.map((c, i) => item({ id: `i${i}`, kind: "message", attention_class: c }))
    const got = inboxKindChips(items).map((c) => (c.count != null ? `${c.id}:${c.count}` : c.id))
    expect(got).toEqual(want)
  })

  it("labels the chips in the wireframe's words", () => {
    const items = (["decision", "input", "review", "repair"] as const).map((c) =>
      item({ id: c, kind: "message", attention_class: c }))
    expect(inboxKindChips(items).map((c) => c.label)).toEqual(["All", "Decisions", "Input", "Review", "Repair"])
  })
})

describe("filterInboxByKind", () => {
  const items = [
    item({ id: "d", kind: "waitpoint", attention_class: "decision" }),
    item({ id: "r", kind: "failed_run", attention_class: "repair" }),
    item({ id: "n", kind: "message" }),
  ]
  it.each<{ chip: InboxKindChip; want: string[] }>([
    { chip: "all", want: ["d", "r", "n"] },
    { chip: "decision", want: ["d"] },
    { chip: "repair", want: ["r"] },
    { chip: "input", want: [] },
  ])("$chip → $want", ({ chip, want }) => {
    expect(filterInboxByKind(items, chip).map((i) => i.id)).toEqual(want)
  })
})

describe("inboxBadge", () => {
  it.each([
    { name: "empty", items: [], want: { count: 0, tone: "active" } },
    {
      name: "unread mail only → primary, counts unread",
      items: [item({ id: "m1", kind: "message" }), item({ id: "m2", kind: "message", state: "read" })],
      want: { count: 1, tone: "active" },
    },
    {
      name: "a blocking waitpoint → warn, counts decisions",
      items: [wp(), item({ id: "m1", kind: "message" }), item({ id: "m2", kind: "message" })],
      want: { count: 1, tone: "urgent" },
    },
    {
      name: "a read blocking escalation still turns it warn",
      items: [item({ id: "e", kind: "escalation", blocking: true, state: "read", payload: { escalation_type: "TEXT" } })],
      want: { count: 1, tone: "urgent" },
    },
    {
      // Actionable but nothing parked on it — isBlockingInboxItem says no.
      name: "a tripped breaker alone stays primary",
      items: [item({ id: "b", kind: "schedule_circuit_breaker_tripped", payload: { schedule_id: "s" } })],
      want: { count: 1, tone: "active" },
    },
    {
      name: "a resolved waitpoint is not waiting",
      items: [wp({ state: "resolved" })],
      want: { count: 0, tone: "active" },
    },
  ])("$name", ({ items, want }) => {
    expect(inboxBadge(items as InboxItem[])).toEqual(want)
  })
})

describe("inlineDecisionFor", () => {
  it.each<{ name: string; it: InboxItem; role: WorkspaceRole | null; want: "waitpoint" | null }>([
    { name: "plain waitpoint, manager", it: wp(), role: "MANAGER", want: "waitpoint" },
    { name: "plain waitpoint, owner", it: wp(), role: "OWNER", want: "waitpoint" },
    { name: "plain waitpoint, member", it: wp(), role: "MEMBER", want: null },
    { name: "plain waitpoint, viewer", it: wp(), role: "VIEWER", want: null },
    { name: "no role", it: wp(), role: null, want: null },
    { name: "resolved", it: wp({ state: "resolved" }), role: "OWNER", want: null },
    { name: "no token", it: wp({ source_id: "" }), role: "OWNER", want: null },
    { name: "autonomy gate", it: wp({ payload: { kind: "autonomy_gate", approval_id: "a1" } }), role: "OWNER", want: null },
    { name: "hire", it: wp({ payload: { kind: "hire" } }), role: "OWNER", want: null },
    {
      name: "decision form",
      it: wp({
        payload: {
          decision_form: {
            fields: [{ name: "why", type: "string" }],
            actions: [
              { id: "ok", label: "Ship", approved: true },
              { id: "no", label: "Hold", approved: false },
            ],
          },
        },
      }),
      role: "OWNER",
      want: null,
    },
    { name: "destructive", it: wp({ payload: { risk_level: "destructive" } }), role: "OWNER", want: null },
    {
      name: "escalation",
      it: item({ id: "e", kind: "escalation", blocking: true, payload: { escalation_type: "TEXT" } }),
      role: "OWNER",
      want: null,
    },
    { name: "message", it: item({ id: "m", kind: "message" }), role: "OWNER", want: null },
  ])("$name → $want", ({ it: row, role, want }) => {
    expect(inlineDecisionFor(row, role)).toBe(want)
  })
})

describe("InboxBellView chips", () => {
  const ITEMS = [
    wp({ title: "Approve promote" }),
    item({ id: "fix", kind: "failed_run", title: "Nightly failed", attention_class: "repair" }),
    item({ id: "msg", kind: "message", title: "Atlas replied" }),
  ]

  function open(items = ITEMS) {
    render(<InboxBellView items={items} role="OWNER" onOpenItem={vi.fn()} onOpenInbox={vi.fn()} />)
    fireEvent.click(screen.getByTestId("bell-trigger"))
  }

  it("shows All with its count and only the kinds present", () => {
    open()
    expect(screen.getByTestId("bell-chip-all")).toHaveTextContent("All (3)")
    expect(screen.getByTestId("bell-chip-all")).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("bell-chip-decision")).toBeInTheDocument()
    expect(screen.getByTestId("bell-chip-repair")).toBeInTheDocument()
    expect(screen.queryByTestId("bell-chip-input")).not.toBeInTheDocument()
    expect(screen.queryByTestId("bell-chip-review")).not.toBeInTheDocument()
  })

  it("filters both sections to the chosen kind", () => {
    open()
    fireEvent.click(screen.getByTestId("bell-chip-repair"))
    expect(screen.getByTestId("bell-row-fix")).toBeInTheDocument()
    expect(screen.queryByTestId("bell-row-wp")).not.toBeInTheDocument()
    expect(screen.queryByTestId("bell-row-msg")).not.toBeInTheDocument()
    expect(screen.queryByText("Needs a decision")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("bell-chip-decision"))
    expect(screen.getByTestId("bell-row-wp")).toBeInTheDocument()
    expect(screen.queryByTestId("bell-row-fix")).not.toBeInTheDocument()
  })

  it("hides the chip strip when All would be the only chip", () => {
    open([item({ id: "msg", kind: "message", title: "Atlas replied" })])
    expect(screen.queryByTestId("bell-chip")).not.toBeInTheDocument()
  })

  it("renders inline Approve / Deny only when the caller wires a decide", async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined)
    render(
      <InboxBellView
        items={ITEMS} role="OWNER" onOpenItem={vi.fn()} onOpenInbox={vi.fn()} onDecideWaitpoint={onDecide}
      />,
    )
    fireEvent.click(screen.getByTestId("bell-trigger"))
    fireEvent.click(screen.getByRole("button", { name: "Deny Approve promote" }))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: "wp" }), false)
    // The row's busy state settles once the decide resolves.
    await waitFor(() => expect(screen.getByRole("button", { name: "Deny Approve promote" })).not.toBeDisabled())
  })

  it("does not offer inline buttons to a role the server would refuse", () => {
    render(
      <InboxBellView
        items={ITEMS} role="MEMBER" onOpenItem={vi.fn()} onOpenInbox={vi.fn()} onDecideWaitpoint={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByTestId("bell-trigger"))
    expect(screen.queryByRole("button", { name: /Approve Approve promote/ })).not.toBeInTheDocument()
  })
})
