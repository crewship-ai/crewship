import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// KindActions is the part of the inbox that talks to the server, and its
// branches were learned from failures: an empty approve body that silently
// denied, a self-patch that 409s because the source cascades the row, a retry
// that posts to a slug the payload never carried. These tests pin the branches
// the surface-level suite does not reach — the rarer sources, and every path
// where the network says no.

const apiFetch = vi.fn()
const waitpointDecide = vi.fn()
const escalationResolve = vi.fn()
const toastError = vi.fn()
const toastSuccess = vi.fn()

vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetch(...a),
}))
vi.mock("@/lib/api/waitpoints", () => ({ waitpointDecide: (...a: unknown[]) => waitpointDecide(...a) }))
vi.mock("@/lib/api/escalations", () => ({ escalationResolve: (...a: unknown[]) => escalationResolve(...a) }))
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: (...a: unknown[]) => toastSuccess(...a) },
}))

import { KindActions } from "../kind-actions"

function item(over: Partial<InboxItem> & Pick<InboxItem, "kind">): InboxItem {
  return {
    id: "i", workspace_id: "ws", source_id: "src", title: "t",
    state: "unread", priority: "medium", blocking: false,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    ...over,
  } as InboxItem
}

const onResolve = vi.fn()
const onRefresh = vi.fn()

function mount(i: InboxItem, disabled = false) {
  return render(<KindActions item={i} onResolve={onResolve} onRefresh={onRefresh} disabled={disabled} />)
}

beforeEach(() => {
  vi.clearAllMocks()
  apiFetch.mockResolvedValue({ ok: true, json: async () => ({}) })
  waitpointDecide.mockResolvedValue({ ok: true })
  escalationResolve.mockResolvedValue({ ok: true })
})
afterEach(cleanup)

describe("hire waitpoints ride a different endpoint", () => {
  const hire = item({ kind: "waitpoint", source_id: "agt-1", payload: { kind: "hire" } })

  it("approves through /agents/{id}/approve-hire, not the pipeline waitpoint route", async () => {
    mount(hire)
    fireEvent.click(screen.getByRole("button", { name: /Approve hire/ }))

    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith("/api/v1/agents/agt-1/approve-hire", expect.anything()))
    expect(waitpointDecide).not.toHaveBeenCalled()
  })

  it("says where denial happens rather than hiding a missing button", () => {
    mount(hire)
    expect(screen.getByText(/fire the agent from its crew page/i)).toBeInTheDocument()
  })

  it("surfaces a network failure instead of looking like success", async () => {
    apiFetch.mockRejectedValueOnce(new Error("offline"))
    mount(hire)
    fireEvent.click(screen.getByRole("button", { name: /Approve hire/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/offline/)))
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("surfaces a server refusal", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 403, json: async () => ({ error: "nope" }) })
    mount(hire)
    fireEvent.click(screen.getByRole("button", { name: /Approve hire/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("nope"))
  })
})

describe("credential escalations", () => {
  it("offers one-click approve when the agent already proposed a value", async () => {
    mount(item({
      kind: "escalation", source_id: "esc-1",
      payload: { escalation_type: "CREDENTIAL", has_pending_credential: true },
    }))
    fireEvent.click(screen.getByRole("button", { name: /Approve/ }))

    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("esc-1", "approve", expect.any(String), "ws"))
  })

  it("is reject-only when a human still has to supply the secret", () => {
    mount(item({ kind: "escalation", source_id: "esc-1", payload: { escalation_type: "CREDENTIAL" } }))

    expect(screen.queryByRole("button", { name: /Approve/ })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Reject/ })).toBeInTheDocument()
    expect(screen.getByText(/crew’s escalations panel/i)).toBeInTheDocument()
  })

  it("points at the source when the row has no escalations record behind it", async () => {
    escalationResolve.mockResolvedValueOnce({ ok: false, status: 404, error: "not found" })
    mount(item({ kind: "escalation", source_id: "esc-1", payload: { escalation_type: "GENERAL" } }))
    fireEvent.click(screen.getByRole("button", { name: /Approve/ }))

    // A raw "404" tells the reader nothing about what to do next.
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/Resolve this from its source/)))
  })
})

describe("escalations with no inline decision", () => {
  it("explains itself rather than offering a button that 409s", () => {
    // No escalation_type: keeper, persona and routine proposals all land here.
    mount(item({ kind: "escalation", payload: { request_type: "access" } }))

    expect(screen.queryByRole("button")).not.toBeInTheDocument()
    expect(screen.getByText(/No decision to make here/i)).toBeInTheDocument()
  })
})

describe("skill proposals", () => {
  it("rejects through the proposed-skills endpoint", async () => {
    mount(item({ kind: "escalation", payload: { kind: "skill_proposal", crew_id: "c1", file_name: "f.md" } }))
    fireEvent.click(screen.getByRole("button", { name: /Reject/ }))

    await waitFor(() => {
      expect(apiFetch.mock.calls[0][0]).toContain("/api/v1/skills/proposed/reject")
    })
  })

  it("reports a refusal from the server", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 500, json: async () => ({ error: "boom" }) })
    mount(item({ kind: "escalation", payload: { kind: "skill_proposal", crew_id: "c1", file_name: "f.md" } }))
    fireEvent.click(screen.getByRole("button", { name: /Approve/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("boom"))
  })
})

describe("waitpoints are source-managed", () => {
  it("refreshes rather than patching the inbox after a decision", async () => {
    mount(item({ kind: "waitpoint", source_id: "tok" }))
    fireEvent.click(screen.getByRole("button", { name: /Approve/ }))

    // CompleteApproval cascades the row server-side; a self-patch to resolved
    // is rejected with a 409 for exactly this kind.
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
    expect(onResolve).not.toHaveBeenCalled()
  })

  it("reports a decision the server would not take", async () => {
    waitpointDecide.mockResolvedValueOnce({ ok: false, error: "already decided or expired" })
    mount(item({ kind: "waitpoint", source_id: "tok" }))
    fireEvent.click(screen.getByRole("button", { name: /Deny/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("already decided or expired"))
    expect(onRefresh).not.toHaveBeenCalled()
  })
})

describe("failed runs", () => {
  it("refuses to guess a slug it was not given", async () => {
    mount(item({ kind: "failed_run", payload: { pipeline_id: "pl-1" } }))
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }))

    // The scheduler writes pipeline_id, not pipeline_slug. Posting to whatever
    // is left over would fire the wrong routine, or none.
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/slug missing/i)))
    expect(onResolve).toHaveBeenCalledWith("cancelled")
  })

  it("cancels without touching the routine", async () => {
    mount(item({ kind: "failed_run", payload: { pipeline_slug: "nightly" } }))
    fireEvent.click(screen.getByRole("button", { name: /^Cancel$/ }))

    await waitFor(() => expect(onResolve).toHaveBeenCalledWith("cancelled"))
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("reports a retry the server rejected", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 400, json: async () => ({ error: "bad inputs" }) })
    mount(item({ kind: "failed_run", payload: { pipeline_slug: "nightly" } }))
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("bad inputs"))
  })
})

describe("messages", () => {
  it("links to the chat and to the issue when the payload carries them", () => {
    mount(item({ kind: "message", payload: { chat_url: "/chat/atlas", issue_identifier: "ENG-6" } }))

    expect(screen.getByRole("link", { name: /Open chat/ })).toHaveAttribute("href", "/chat/atlas")
    expect(screen.getByRole("link", { name: /Open ENG-6/ })).toHaveAttribute("href", "/issues/ENG-6")
  })

  it("ignores a chat_url that is not an in-app path", () => {
    mount(item({ kind: "message", payload: { chat_url: "https://evil.example/x" } }))
    expect(screen.queryByRole("link", { name: /Open chat/ })).not.toBeInTheDocument()
  })
})

describe("schedule kinds", () => {
  it("dismisses a missed-occurrence notice", async () => {
    mount(item({ kind: "schedule_missed", payload: { schedule_id: "sch-1" } }))
    fireEvent.click(screen.getByRole("button", { name: /Dismiss/ }))

    await waitFor(() => expect(onResolve).toHaveBeenCalledWith("dismissed"))
  })

  it("falls back to Dismiss when a consolidation carries no proposal id", async () => {
    mount(item({ kind: "memory_consolidation", payload: {} }))
    fireEvent.click(screen.getByRole("button", { name: /Dismiss/ }))

    await waitFor(() => expect(onResolve).toHaveBeenCalledWith("dismissed"))
  })

  it("reports a consolidation the server refused", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 403, json: async () => ({ error: "owner only" }) })
    mount(item({ kind: "memory_consolidation", payload: { proposal_id: "p1" } }))
    fireEvent.click(screen.getByRole("button", { name: /Reject/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("owner only"))
  })
})

describe("schedules reach the endpoints the CLI already uses", () => {
  it("re-enables a tripped schedule with the same PATCH as `routine schedules enable`", async () => {
    mount(item({ kind: "schedule_circuit_breaker_tripped", payload: { schedule_id: "sch-1" } }))
    fireEvent.click(screen.getByRole("button", { name: /Re-enable schedule/ }))

    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws/pipeline-schedules/sch-1",
        expect.objectContaining({ method: "PATCH" }),
      ))
    expect(JSON.parse((apiFetch.mock.calls[0][1] as { body: string }).body)).toEqual({ enabled: true })
    await waitFor(() => expect(onResolve).toHaveBeenCalledWith("reenabled"))
  })

  it("reports a refused re-enable — it is OWNER/ADMIN only", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 403, json: async () => ({ error: "Forbidden" }) })
    mount(item({ kind: "schedule_circuit_breaker_tripped", payload: { schedule_id: "sch-1" } }))
    fireEvent.click(screen.getByRole("button", { name: /Re-enable schedule/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("Forbidden"))
    expect(onResolve).not.toHaveBeenCalled()
  })

  it("fires a missed schedule out of cycle", async () => {
    mount(item({ kind: "schedule_missed", payload: { schedule_id: "sch-2" } }))
    fireEvent.click(screen.getByRole("button", { name: /Run now/ }))

    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws/pipeline-schedules/sch-2/run",
        expect.objectContaining({ method: "POST" }),
      ))
    await waitFor(() => expect(onResolve).toHaveBeenCalledWith("ran"))
  })

  it("offers only Dismiss when the payload carries no schedule id", () => {
    mount(item({ kind: "schedule_circuit_breaker_tripped", payload: {} }))
    expect(screen.queryByRole("button", { name: /Re-enable/ })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Dismiss/ })).toBeInTheDocument()
  })
})

describe("resolved items", () => {
  it("disables every action once the row is closed", () => {
    mount(item({ kind: "message" }), true)
    expect(screen.getByRole("button", { name: /Dismiss/ })).toBeDisabled()
  })
})
