import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { KindActions } from "../inbox-list"
import type { InboxItem } from "@/hooks/use-inbox"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

function hireItem(): InboxItem {
  return {
    id: "inbox_1",
    workspace_id: "ws_test",
    kind: "waitpoint",
    source_id: "agent_123",
    title: "Hire ephemeral agent: Accounting & Finance (15m)",
    state: "unread",
    priority: "medium",
    blocking: true,
    payload: { kind: "hire", agent_id: "agent_123", crew_id: "crew_9" },
    created_at: "2026-06-25T11:00:00Z",
    updated_at: "2026-06-25T11:00:00Z",
  }
}

describe("KindActions — approve hire", () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({}),
    })
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    cleanup()
  })

  it("sends workspace_id with the approve-hire request so RequireWorkspace passes", async () => {
    render(
      <KindActions
        item={hireItem()}
        onResolve={vi.fn()}
        onRefresh={vi.fn()}
        disabled={false}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: /approve hire/i }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    const url = String(fetchMock.mock.calls[0][0])
    expect(url).toContain("/api/v1/agents/agent_123/approve-hire")
    // Without workspace_id the wsCtx (RequireWorkspace) middleware rejects
    // with 400 "workspace_id is required" before the handler runs.
    expect(url).toContain("workspace_id=ws_test")
  })
})
