import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { DecisionSubject } from "../inbox-detail"
import type { InboxItem } from "@/hooks/use-inbox"

// "He is asking for THIS credential, which is L1 or L2, and I have to
// approve it."
//
// A routine proposal said `credentials_required` and stopped. That is
// the risk CATEGORY — the reviewer's actual question is which ones, and
// the routine declares them. Without the list on the item the only way
// to answer it was to leave the inbox and read the DSL, which is why
// Approve became a reflex.

function item(payload: Record<string, unknown>): InboxItem {
  return {
    id: "ibx-1",
    workspace_id: "ws-1",
    kind: "escalation",
    source_id: "src-1",
    title: "Routine proposed for review: nightly",
    state: "unread",
    priority: "high",
    blocking: true,
    created_at: "2026-08-04T09:00:00Z",
    updated_at: "2026-08-04T09:00:00Z",
    payload,
  } as InboxItem
}

describe("<DecisionSubject> names what is being asked for", () => {
  it("lists the credentials a proposed routine declares, scope and all", () => {
    render(
      <DecisionSubject
        item={item({ kind: "routine_proposal", credentials_required: ["github:repo", "openai"] })}
      />,
    )
    // "github" and "github:repo" are different asks; collapsing them
    // would understate one.
    expect(screen.getByText("github:repo")).toBeInTheDocument()
    expect(screen.getByText("openai")).toBeInTheDocument()
  })

  it("lists integrations and egress hosts too", () => {
    render(
      <DecisionSubject
        item={item({
          kind: "routine_proposal",
          integrations_required: ["slack"],
          egress_targets: ["api.example.com"],
        })}
      />,
    )
    expect(screen.getByText("slack")).toBeInTheDocument()
    expect(screen.getByText("api.example.com")).toBeInTheDocument()
  })

  it("renders nothing at all when the routine declares nothing", () => {
    const { container } = render(<DecisionSubject item={item({ kind: "routine_proposal" })} />)
    expect(container.textContent).toBe("")
  })

  it("still shows a keeper credential request with its level", () => {
    // The other producer on this card. Its fields were already wired;
    // this pins that the routine additions did not displace them.
    render(
      <DecisionSubject
        item={item({
          request_type: "access",
          credential_name: "PROD_DB_PASSWORD",
          security_level: "L2",
          intent: "read the orders table",
        })}
      />,
    )
    expect(screen.getByText("PROD_DB_PASSWORD")).toBeInTheDocument()
    expect(screen.getByText("L2")).toBeInTheDocument()
    expect(screen.getByText("read the orders table")).toBeInTheDocument()
  })
})
