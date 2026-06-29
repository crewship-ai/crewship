import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", async (importActual) => ({
  ...(await importActual<Record<string, unknown>>()),
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

import { KindActions } from "../inbox-list"

// Minimal InboxItem for an agent-authored skill proposal (rides the escalation
// kind, disambiguated by payload.kind, per the inbox card).
function skillProposalItem(overrides: Record<string, unknown> = {}) {
  return {
    id: "ibx_escalation_skillprop:crew-1:skill-x.md",
    workspace_id: "ws-1",
    kind: "escalation",
    source_id: "skillprop:crew-1:skill-x.md",
    title: "Skill proposed for review: deploy-x",
    state: "unread",
    priority: "high",
    blocking: true,
    created_at: "2026-06-29T00:00:00Z",
    updated_at: "2026-06-29T00:00:00Z",
    payload: { kind: "skill_proposal", crew_id: "crew-1", file_name: "skill-x.md", slug: "deploy-x" },
    ...overrides,
  }
}

describe("Inbox skill_proposal card", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({}) })
  })

  function renderCard() {
    return render(
      <KindActions item={skillProposalItem() as never} onResolve={vi.fn()} onRefresh={vi.fn()} disabled={false} />,
    )
  }

  it("renders Approve and Reject for an agent-authored skill proposal", () => {
    renderCard()
    expect(screen.getByRole("button", { name: /approve/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /reject/i })).toBeInTheDocument()
  })

  it("approves via the proposed-skills endpoint with crew_id + file_name", async () => {
    renderCard()
    fireEvent.click(screen.getByRole("button", { name: /approve/i }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [url, opts] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe("/api/v1/skills/proposed/approve?workspace_id=ws-1")
    expect(opts.method).toBe("POST")
    expect(JSON.parse(opts.body as string)).toEqual({ crew_id: "crew-1", file_name: "skill-x.md" })
  })

  it("rejects via the proposed-skills reject endpoint", async () => {
    renderCard()
    fireEvent.click(screen.getByRole("button", { name: /reject/i }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    const [url] = apiFetch.mock.calls[0] as [string]
    expect(url).toBe("/api/v1/skills/proposed/reject?workspace_id=ws-1")
  })
})
