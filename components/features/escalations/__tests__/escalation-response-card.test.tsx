import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import { EscalationResponseCard } from "../escalation-response-card"
import type { Escalation } from "@/lib/types/escalation"

// Issue #1559 — four-eyes is decided at resolve time from the workspace toggle
// AND the credential's tier, and the row showed neither. An Approve button that
// was going to 403 looked exactly like one that was not, so the refusal was the
// first thing that taught the operator the rule existed. The row now says which
// of the two controls applies, before the click.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

const BASE: Escalation = {
  id: "esc1",
  type: "CREDENTIAL",
  from_name: "Nela",
  from_slug: "nela",
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

function renderCard(overrides: Partial<Escalation>) {
  render(
    <EscalationResponseCard
      escalation={{ ...BASE, ...overrides }}
      workspaceId="ws1"
      crewId="crew1"
      onResolved={() => {}}
    />,
  )
}

describe("EscalationResponseCard four-eyes notice (#1559)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
  })

  it("says nothing when a single approver can resolve it", () => {
    renderCard({})
    expect(screen.queryByTestId("escalation-four-eyes")).not.toBeInTheDocument()
  })

  it("names the tier when the tier alone forces the rule", () => {
    renderCard({
      second_approver_required: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent("L4 · critical")
    // The bit that is genuinely surprising: the workspace switch is off and the
    // rule applies anyway, and no amount of switching can turn it off.
    expect(note).toHaveTextContent(/second-approver setting is off/i)
    expect(note).toHaveTextContent(/tighten/i)
    // And who is refused — this row is read by the person about to be refused.
    expect(note).toHaveTextContent("@nela")
  })

  it("names the workspace setting when that is what forces the rule", () => {
    renderCard({
      second_approver_required: true,
      second_approver_by_workspace: true,
      security_level_label: "L2 · medium",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent(/workspace/i)
    // The tier is NOT the reason here; claiming it were would send an operator
    // to the credential's level looking for a setting that isn't the cause.
    expect(note).not.toHaveTextContent(/tighten/i)
  })

  it("says both when both hold independently", () => {
    renderCard({
      second_approver_required: true,
      second_approver_by_workspace: true,
      second_approver_by_tier: true,
      security_level_label: "L4 · critical",
    })

    const note = screen.getByTestId("escalation-four-eyes")
    expect(note).toHaveTextContent(/workspace/i)
    expect(note).toHaveTextContent("L4 · critical")
    // Turning the workspace toggle off would not lift it — that is the whole
    // reason both are worth naming rather than picking the "more specific" one.
    expect(note).toHaveTextContent(/regardless/i)
  })
})
