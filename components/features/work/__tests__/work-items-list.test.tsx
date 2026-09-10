/**
 * A row is a work item. Not an agent, not a conversation.
 *
 * §9's rule about run streams has a list-level twin: one agent can hold
 * several concurrent work items and one session can hold a queue of turns, so
 * a list that groups by either shows a reader one row where there are three
 * pieces of work — and the one they cancel is then a coin toss.
 */
import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

import { WorkItemsList, formatCost } from "../work-items-list"
import { WebhookDeliveriesList } from "../webhook-deliveries-list"
import type { WorkItem } from "@/hooks/use-work-items"
import type { WebhookDelivery } from "@/hooks/use-webhook-deliveries"

afterEach(cleanup)

function workItem(over: Partial<WorkItem> = {}): WorkItem {
  return {
    id: "cwork0000000000000001",
    workspace_id: "ws-1",
    source: "chat",
    source_ref: "",
    domain_kind: "",
    domain_id: "",
    agent_id: "agent-jamie",
    crew_id: "crew-1",
    session_id: "chat-42",
    class: "chat",
    authorized_by_user_id: "user-1",
    input_sha256: "a".repeat(64),
    target_revision: "",
    state: "queued",
    state_reason: "",
    generation: 0,
    attempts: 0,
    priority: 0,
    eligible_at: "2026-09-10T09:00:00.000000000Z",
    deadline_at: null,
    replay_of: null,
    replay_reason: "",
    created_at: "2026-09-10T09:00:00.000000000Z",
    updated_at: "2026-09-10T09:00:00.000000000Z",
    terminal_at: null,
    ...over,
  }
}

describe("WorkItemsList", () => {
  it("renders one row per work item even when they share an agent and a session", () => {
    render(
      <WorkItemsList
        items={[
          workItem({ id: "w-1" }),
          workItem({ id: "w-2", state: "running" }),
        ]}
        onSelect={() => {}}
      />,
    )
    expect(screen.getAllByRole("listitem")).toHaveLength(2)
    expect(document.querySelectorAll('[data-work-item-id="w-1"]')).toHaveLength(1)
    expect(document.querySelectorAll('[data-work-item-id="w-2"]')).toHaveLength(1)
  })

  it("shows the queued reason and the eligible time §9 asks for", () => {
    render(<WorkItemsList items={[workItem({ state: "queued" })]} onSelect={() => {}} />)
    expect(screen.getByText("Waiting for capacity.")).toBeTruthy()
  })

  it("renders an unresolved item as neither succeeded nor failed", () => {
    render(<WorkItemsList items={[workItem({ state: "needs_reconciliation" })]} onSelect={() => {}} />)
    const pill = document.querySelector("[data-work-outcome]")
    expect(pill?.getAttribute("data-work-outcome")).toBe("unresolved")
  })

  it("hands the selected item back whole", () => {
    const picked: string[] = []
    render(<WorkItemsList items={[workItem({ id: "w-7" })]} onSelect={(i) => picked.push(i.id)} />)
    fireEvent.click(screen.getByRole("listitem").querySelector("button") as HTMLElement)
    expect(picked).toEqual(["w-7"])
  })

  it("says nothing has been accepted rather than showing an empty table", () => {
    render(<WorkItemsList items={[]} onSelect={() => {}} />)
    expect(screen.getByText("No work in the ledger")).toBeTruthy()
  })
})

describe("formatCost", () => {
  it("does not round a real cost away to zero", () => {
    expect(formatCost(0)).toBe("—")
    expect(formatCost(0.004)).toBe("<$0.01")
    expect(formatCost(1.25)).toBe("$1.25")
  })
})

function deliveryRow(over: Partial<WebhookDelivery> = {}): WebhookDelivery {
  return {
    id: "cdlv1", workspace_id: "ws-1", endpoint_id: "e-1", endpoint_kind: "routine",
    profile: "github", source_delivery_id: "gh-1", event_type: "push", event_action: "",
    signing_key_id: "", body_sha256: "b".repeat(64), body_bytes: 2048,
    filter_decision: "accepted", filter_reason: "", target_revision: "",
    work_id: "cwork0000000000000001",
    received_at: "2026-09-10T09:00:00.000000000Z", dedup_expires_at: "2026-10-10T09:00:00.000000000Z",
    raw_body_available: true, raw_body_expires_at: null,
    ...over,
  }
}

describe("WebhookDeliveriesList", () => {
  it("shows an ignored delivery as a row with its reason, not as an absence", () => {
    render(
      <WebhookDeliveriesList
        deliveries={[deliveryRow({ id: "d-ig", filter_decision: "ignored", filter_reason: "ping event", work_id: null })]}
      />,
    )
    expect(screen.getByTestId("delivery-decision-d-ig").textContent).toContain("Ignored")
    expect(screen.getByText("ping event")).toBeTruthy()
  })

  it("says when the payload is gone, which is what makes the replay button honest", () => {
    render(<WebhookDeliveriesList deliveries={[deliveryRow({ raw_body_available: false })]} />)
    expect(screen.getByText("payload dropped — cannot be replayed")).toBeTruthy()
  })

  it("never renders the raw body or a signing secret — neither is on the wire", () => {
    const { baseElement } = render(<WebhookDeliveriesList deliveries={[deliveryRow()]} />)
    expect(baseElement.textContent?.toLowerCase()).not.toContain("secret")
  })

  it("opens the work an accepted delivery produced", () => {
    const opened: string[] = []
    render(<WebhookDeliveriesList deliveries={[deliveryRow()]} onOpenWork={(id) => opened.push(id)} />)
    fireEvent.click(screen.getByText(/^work /))
    expect(opened).toEqual(["cwork0000000000000001"])
  })
})
