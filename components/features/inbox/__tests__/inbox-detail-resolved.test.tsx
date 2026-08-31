import { describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

import { DecisionCard, InboxDetail } from "../inbox-detail"

// DecisionCard is written for a gate that is still open, but History renders
// the same card for decisions that are already made. A denied hire therefore
// announced itself as "Waiting on your decision", with a live expiry countdown
// and a greyed Approve button — which reads as a permissions problem, not as a
// decision that already happened. Inbox v2 promotes History to a primary
// destination, so this is now the first thing a customer sees there.
function resolved(overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "ibx-1",
    workspace_id: "ws-1",
    kind: "waitpoint",
    source_id: "src-1",
    title: "Hire ephemeral agent: Software Development (30m)",
    state: "resolved",
    priority: "high",
    blocking: true,
    resolved_action: "denied",
    resolved_at: "2026-08-30T21:05:00Z",
    created_at: "2026-08-30T21:00:00Z",
    updated_at: "2026-08-30T21:05:00Z",
    payload: { kind: "hire", timeout_at: "2026-08-31T21:00:00Z" },
    ...overrides,
  }
}

const noop = () => {}

describe("DecisionCard on an already-decided row", () => {
  it("does not claim the decision is still waiting", () => {
    render(<DecisionCard item={resolved()} role="OWNER" onResolve={noop} onRefresh={noop} />)
    expect(screen.getByText(/decision record/i)).toBeTruthy()
    expect(screen.queryByText(/waiting on your decision/i)).toBeNull()
  })

  it("does not run an expiry countdown against a closed decision", () => {
    render(<DecisionCard item={resolved()} role="OWNER" onResolve={noop} onRefresh={noop} />)
    expect(screen.queryByText(/expires/i)).toBeNull()
    expect(screen.getByText(/denied/i)).toBeTruthy()
  })

  it("offers no decision buttons at all, disabled or otherwise", () => {
    render(<DecisionCard item={resolved()} role="OWNER" onResolve={noop} onRefresh={noop} />)
    for (const label of [/approve/i, /deny/i, /reject/i]) {
      expect(screen.queryByRole("button", { name: label })).toBeNull()
    }
  })

  it("still offers the decision on a row that is genuinely pending", () => {
    render(
      <DecisionCard
        item={resolved({ state: "unread", resolved_action: undefined, resolved_at: undefined })}
        role="OWNER"
        onResolve={noop}
        onRefresh={noop}
      />,
    )
    expect(screen.queryByText(/decision record/i)).toBeNull()
    expect(screen.getByText(/expires/i)).toBeTruthy()
  })
})

vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER" }) }))

describe("a decision whose source is gone", () => {
  const orphan = (over: Partial<InboxItem> = {}): InboxItem => ({
    ...resolved({ state: "unread", resolved_action: undefined, resolved_at: undefined }),
    ...over,
  })

  it("offers no way out while the source is still live", () => {
    render(<InboxDetail item={orphan()} role="OWNER" onResolve={noop} onArchive={noop} onMarkUnread={noop} onRefresh={noop} />)
    expect(screen.queryByRole("button", { name: /^Archive$/ })).toBeNull()
  })

  it("offers Archive once the server says the source is gone", () => {
    // Before source_missing existed the client guessed from the payload, got
    // waitpoints wrong in every case, and left them stuck in Needs action with
    // no route to History.
    render(
      <InboxDetail
        item={orphan({ source_missing: true })}
        role="OWNER" onResolve={noop} onArchive={noop} onMarkUnread={noop} onRefresh={noop}
      />,
    )
    expect(screen.getByRole("button", { name: /^Archive$/ })).toBeTruthy()
  })
})
