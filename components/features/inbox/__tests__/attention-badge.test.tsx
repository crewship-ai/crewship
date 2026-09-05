import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// #2398 — attention_class (B10's §12 contract) is a badge on the card.
//
// The server has written it on every threaded row since #2378; the pane
// ignored it. A reader told "Casey needs your input" and "Routine is risky"
// with the same chrome cannot tell an ask for input from an ask for a
// decision without opening both.

vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
vi.mock("../kind-actions", () => ({ KindActions: () => null }))

import { InboxDetail } from "../inbox-detail"
import { attentionBadge } from "../inbox-derive"

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  const ts = new Date("2026-09-05T10:00:00Z").toISOString()
  return {
    workspace_id: "ws",
    source_id: `s-${over.id}`,
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: ts,
    updated_at: ts,
    ...over,
  } as InboxItem
}

function renderDetail(it: InboxItem) {
  return render(
    <InboxDetail item={it} role="OWNER" onResolve={vi.fn()} onRefresh={vi.fn()} onArchive={vi.fn()} onMarkUnread={vi.fn()} />,
  )
}

afterEach(cleanup)

describe("attentionBadge", () => {
  it("names the four classes and nothing else", () => {
    expect(attentionBadge({ attention_class: "decision" } as InboxItem)?.label).toBe("Decision")
    expect(attentionBadge({ attention_class: "input" } as InboxItem)?.label).toBe("Input needed")
    expect(attentionBadge({ attention_class: "review" } as InboxItem)?.label).toBe("Review")
    expect(attentionBadge({ attention_class: "repair" } as InboxItem)?.label).toBe("Repair")
    expect(attentionBadge({} as InboxItem)).toBeNull()
    // An unknown value from a newer server is not guessed at.
    expect(attentionBadge({ attention_class: "escalate" } as unknown as InboxItem)).toBeNull()
  })
})

describe("InboxDetail — the attention badge", () => {
  it("shows the class on a run_needs_human card (plain card branch)", () => {
    renderDetail(item({ id: "i1", kind: "run_needs_human", title: "Casey needs your input on ENG-7", attention_class: "input", blocking: true }))
    expect(screen.getByTestId("attention-badge")).toHaveTextContent("Input needed")
  })

  it("shows the class on a decision card too (waitpoint with the contract)", () => {
    renderDetail(item({ id: "i2", kind: "waitpoint", title: "Approve the routine", attention_class: "decision" }))
    expect(screen.getByTestId("attention-badge")).toHaveTextContent("Decision")
  })

  it("renders no badge for a row that has not adopted the contract", () => {
    renderDetail(item({ id: "i3", kind: "message", title: "ENG-1 ready for review" }))
    expect(screen.queryByTestId("attention-badge")).not.toBeInTheDocument()
  })
})
