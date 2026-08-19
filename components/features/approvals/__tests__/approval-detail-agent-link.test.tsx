import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { ApprovalDetail } from "@/components/features/approvals/approval-detail"
import type { ApprovalRow } from "@/lib/types/approvals"

// =============================================================================
// The approval sheet's agent badge pointed at /crews/agents/<agent_id>, which
// the /crews redesign deleted.
//
// Unlike the other repairs there is no slug to be had here: ApprovalRow
// (lib/types/approvals.ts) carries agent_id and nothing else, the approvals
// page mounts no lookup provider, and the agent chip is decoration on a sheet
// whose actual job is approve/deny. Putting the id into ?agent= would be
// actively worse than a generic link — hooks/use-crews-selection.tsx matches
// that parameter against agent.slug, finds nothing, and clears it, so the URL
// rewrites itself and the canvas comes up empty.
//
// So the badge goes to plain /crews: a real route, the agent roster, one step
// from the agent. A working generic page beats a broken specific one.
// =============================================================================

const AGENT_ID = "ag_0f1e2d3c-4b5a-6978"
const CREW_ID = "crew_11223344-5566"

function row(overrides: Partial<ApprovalRow> = {}): ApprovalRow {
  return {
    id: "apr_aabbccdd-eeff",
    kind: "tool_call",
    reason: "wants to run terraform apply",
    status: "pending",
    created_at: "2026-08-01T10:00:00Z",
    agent_id: AGENT_ID,
    crew_id: CREW_ID,
    mission_id: null,
    ...overrides,
  } as ApprovalRow
}

function renderSheet(r: ApprovalRow = row()) {
  return render(
    <ApprovalDetail row={r} open onOpenChange={vi.fn()} onDecided={vi.fn()} />,
  )
}

describe("ApprovalDetail entity badges", () => {
  it("links the agent badge to a route that exists", () => {
    renderSheet()
    expect(screen.getByRole("link", { name: /^agent · / })).toHaveAttribute(
      "href",
      "/crews",
    )
  })

  it("does not put the agent id into ?agent=, which the canvas would clear", () => {
    renderSheet()
    const href = screen.getByRole("link", { name: /^agent · / }).getAttribute("href") ?? ""
    expect(href).not.toContain(AGENT_ID)
  })

  it("still shows the truncated agent id as the badge label", () => {
    renderSheet()
    // The id is the useful thing on screen — it is what a support ticket
    // quotes. Losing the deep link must not lose the identifier.
    expect(screen.getByText(`agent · ${AGENT_ID.slice(0, 8)}`)).toBeInTheDocument()
  })

  it("omits the agent badge entirely when the approval has no agent", () => {
    renderSheet(row({ agent_id: null }))
    expect(screen.queryByRole("link", { name: /^agent · / })).not.toBeInTheDocument()
  })
})
