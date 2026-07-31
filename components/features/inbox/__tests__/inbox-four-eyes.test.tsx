import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"
import type { Escalation } from "@/lib/types/escalation"

// Issue #1574 — #1559 made the four-eyes requirement visible on the crew
// escalations panel and left the inbox alone, so the OTHER surface with a
// one-click Approve on the same escalation still rendered it as if one person
// could close it. These tests pin two things:
//
//   1. the inbox reading pane says it, off the server's read-time answer and
//      not off the payload frozen at raise time, and
//   2. it says it in the SAME words as the escalations panel — the two are one
//      component, and a test that renders both and compares the text is what
//      keeps them from drifting into two descriptions of one rule.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetch(...a),
}))
vi.mock("@/lib/api/waitpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/waitpoints")>()),
  waitpointDecide: vi.fn(),
}))
vi.mock("@/lib/api/escalations", () => ({ escalationResolve: vi.fn() }))
vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))

import { InboxDetail } from "../inbox-detail"
import { EscalationResponseCard } from "@/components/features/escalations/escalation-response-card"

const CRED_ITEM: InboxItem = {
  id: "in-1",
  workspace_id: "ws-test",
  kind: "escalation",
  source_id: "esc-1",
  title: "Credential approval: Deploy Key",
  sender_type: "agent",
  sender_name: "casey",
  state: "unread",
  priority: "high",
  blocking: true,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
  // The payload as CreateEscalation writes it at raise time: the type, and
  // whether a value is already waiting in the vault. Nothing about four-eyes,
  // because nobody can know at raise time what the answer will be at read time.
  payload: { escalation_type: "CREDENTIAL", has_pending_credential: true },
}

function renderInbox(over: Partial<InboxItem>) {
  render(
    <InboxDetail
      item={{ ...CRED_ITEM, ...over }}
      role="OWNER"
      onResolve={() => {}}
      onArchive={() => {}}
      onMarkUnread={() => {}}
      onRefresh={() => {}}
    />,
  )
}

const BASE_ESCALATION: Escalation = {
  id: "esc-1",
  type: "CREDENTIAL",
  from_name: "Casey",
  from_slug: "casey",
  reason: "need a deploy key",
  context: null,
  metadata: null,
  peer_conversation_id: null,
  status: "PENDING",
  resolution: null,
  action: null,
  redirect_to: null,
  resolved_by: null,
  resolved_at: null,
  created_at: "2026-07-30T10:00:00Z",
  credential_id: "cred1",
  second_approver_required: false,
  second_approver_by_workspace: false,
  second_approver_by_tier: false,
  security_level_label: null,
}

describe("inbox four-eyes notice (#1574)", () => {
  beforeEach(() => apiFetch.mockReset())
  afterEach(() => cleanup())

  it("says nothing when a single approver can resolve it", () => {
    renderInbox({})
    expect(screen.queryByTestId("escalation-four-eyes")).not.toBeInTheDocument()
  })

  it("names the tier when the tier alone forces the rule", () => {
    renderInbox({
      second_approver_required: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent("L4 · critical")
    // The surprising half: the workspace switch is off and the rule applies
    // anyway, and no amount of switching can turn it off.
    expect(note).toHaveTextContent(/second-approver setting is off/i)
    expect(note).toHaveTextContent(/tighten/i)
    // And who is refused — the person reading this row is the one refused.
    expect(note).toHaveTextContent("@casey")
  })

  it("names the workspace setting when that is what forces the rule", () => {
    renderInbox({
      second_approver_required: true,
      second_approver_by_workspace: true,
      security_level_label: "L2 · medium",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent(/workspace/i)
    // The tier is NOT the reason here; saying it were sends an operator to the
    // credential's level looking for a cause that isn't there.
    expect(note).not.toHaveTextContent(/tighten/i)
  })

  it("says both when both hold independently", () => {
    renderInbox({
      second_approver_required: true,
      second_approver_by_workspace: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent(/workspace/i)
    expect(note).toHaveTextContent("L4 · critical")
    expect(note).toHaveTextContent(/regardless/i)
  })

  it("reads the server's answer, not the payload frozen at raise time", () => {
    // A payload that flatly contradicts the read-time answer. It is the shape
    // an "enrich the stored payload" fix would produce after the credential was
    // re-tiered, and it is stale in the direction that matters — an unguarded
    // Approve for a credential somebody has since marked critical.
    renderInbox({
      payload: {
        escalation_type: "CREDENTIAL",
        has_pending_credential: true,
        second_approver_required: false,
      },
      second_approver_required: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    })

    expect(screen.getByTestId("escalation-four-eyes")).toHaveTextContent("L4 · critical")
  })

  it("stays quiet on a non-credential escalation the server did not flag", () => {
    renderInbox({ payload: { escalation_type: "TEXT" } })
    expect(screen.queryByTestId("escalation-four-eyes")).not.toBeInTheDocument()
  })

  it("uses the same words as the crew escalations panel", () => {
    // Same facts, two surfaces. If either grows its own copy of the rule this
    // fails, which is the whole reason the notice is one component.
    const facts = {
      second_approver_required: true,
      second_approver_by_workspace: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    }

    const inbox = render(
      <InboxDetail
        item={{ ...CRED_ITEM, ...facts }}
        role="OWNER"
        onResolve={() => {}}
        onArchive={() => {}}
        onMarkUnread={() => {}}
        onRefresh={() => {}}
      />,
    )
    const inboxText = inbox.getByTestId("escalation-four-eyes").textContent
    cleanup()

    const panel = render(
      <EscalationResponseCard
        escalation={{ ...BASE_ESCALATION, ...facts }}
        workspaceId="ws-test"
        crewId="crew-1"
        onResolved={() => {}}
      />,
    )
    expect(panel.getByTestId("escalation-four-eyes").textContent).toBe(inboxText)
  })
})
