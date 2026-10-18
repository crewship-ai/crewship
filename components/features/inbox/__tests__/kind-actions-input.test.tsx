import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"
import { decisionMetaFor } from "../inbox-derive"

// ODCH-I03 / I04: the Inbox takes a typed answer to an agent's question, the
// value of a credential an agent asked for, and activates a routine trigger,
// all through endpoints that already exist.

const apiFetch = vi.fn()
const escalationResolve = vi.fn()
const escalationSupplyCredential = vi.fn()
const toastError = vi.fn()
const toastSuccess = vi.fn()

vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetch(...a),
}))
vi.mock("@/lib/api/escalations", () => ({
  escalationResolve: (...a: unknown[]) => escalationResolve(...a),
  escalationSupplyCredential: (...a: unknown[]) => escalationSupplyCredential(...a),
}))
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: (...a: unknown[]) => toastSuccess(...a), info: vi.fn() },
}))

import { KindActions } from "../kind-actions"

function item(over: Partial<InboxItem> & Pick<InboxItem, "kind">): InboxItem {
  return {
    id: "i", workspace_id: "ws", source_id: "esc-1", title: "t", state: "unread", priority: "medium", blocking: false,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(), ...over,
  } as InboxItem
}
const onResolve = vi.fn()
const onRefresh = vi.fn()
const mount = (i: InboxItem, disabled = false) =>
  render(<KindActions item={i} onResolve={onResolve} onRefresh={onRefresh} disabled={disabled} />)

beforeEach(() => {
  vi.clearAllMocks()
  apiFetch.mockResolvedValue({ ok: true, json: async () => ({}) })
  escalationResolve.mockResolvedValue({ ok: true })
  escalationSupplyCredential.mockResolvedValue({ ok: true, credential: null })
})
afterEach(cleanup)

const question = item({ kind: "escalation", payload: { escalation_type: "TEXT" } })

describe("Question from an agent (I03)", () => {
  it("takes a written answer and sends it as the resolution", async () => {
    mount(question)
    const send = screen.getByRole("button", { name: "Send answer" })
    expect(send).toBeDisabled()
    fireEvent.change(screen.getByRole("textbox", { name: "Your answer" }), { target: { value: "  Fix lint first  " } })
    expect(send).toBeEnabled()
    fireEvent.click(send)
    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("esc-1", "approve", "Fix lint first", "ws"))
    await waitFor(() => expect(onRefresh).toHaveBeenCalledWith("approved"))
  })

  it("still rejects without an answer", async () => {
    mount(question)
    fireEvent.click(screen.getByRole("button", { name: /Reject/ }))
    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("esc-1", "reject", expect.any(String), "ws"))
  })

  it("keeps the typed answer when the server refuses", async () => {
    escalationResolve.mockResolvedValueOnce({ ok: false, status: 409, error: "already resolved" })
    mount(question)
    fireEvent.change(screen.getByRole("textbox", { name: "Your answer" }), { target: { value: "Merge" } })
    fireEvent.click(screen.getByRole("button", { name: "Send answer" }))
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(screen.getByRole("textbox", { name: "Your answer" })).toHaveValue("Merge")
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("other escalations keep Approve and Reject, not an answer box", () => {
    mount(item({ kind: "escalation", payload: { escalation_type: "GENERAL" } }))
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.getByRole("button", { name: /Approve/ })).toBeInTheDocument()
  })
})

describe("Credential request (I03)", () => {
  const ask = item({ kind: "escalation", payload: { escalation_type: "CREDENTIAL", needs_credential_value: true } })

  it("asks for the value in a masked field and supplies it", async () => {
    mount(ask)
    const field = screen.getByLabelText("Value") as HTMLInputElement
    expect(field.type).toBe("password")
    expect(field.autocomplete).toBe("off")
    const send = screen.getByRole("button", { name: "Supply credential" })
    expect(send).toBeDisabled()
    fireEvent.change(field, { target: { value: "secret_abc" } })
    fireEvent.click(send)
    await waitFor(() => expect(escalationSupplyCredential).toHaveBeenCalledWith("esc-1", "secret_abc", "ws"))
    await waitFor(() => expect(onRefresh).toHaveBeenCalledWith("approved"))
    expect((screen.getByLabelText("Value") as HTMLInputElement).value).toBe("")
  })

  it("clears the field and never repeats the value, even when the server refuses", async () => {
    escalationSupplyCredential.mockResolvedValueOnce({ ok: false, status: 403, error: "Forbidden" })
    const log = vi.spyOn(console, "error").mockImplementation(() => {})
    mount(ask)
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "secret_abc" } })
    fireEvent.click(screen.getByRole("button", { name: "Supply credential" }))
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect((screen.getByLabelText("Value") as HTMLInputElement).value).toBe("")
    expect(JSON.stringify([toastError.mock.calls, toastSuccess.mock.calls, log.mock.calls])).not.toContain("secret_abc")
    expect(document.body.textContent).not.toContain("secret_abc")
    log.mockRestore()
  })

  it("rejecting is still possible", async () => {
    mount(ask)
    fireEvent.click(screen.getByRole("button", { name: /Reject/ }))
    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("esc-1", "reject", "", "ws"))
  })

  it("an ask the agent already staged a value for stays one-click Approve / Reject", () => {
    mount(item({ kind: "escalation", payload: { escalation_type: "CREDENTIAL", has_pending_credential: true } }))
    expect(screen.queryByLabelText("Value")).toBeNull()
    expect(screen.getByRole("button", { name: /Approve/ })).toBeInTheDocument()
  })
})

describe("Routine trigger ready (I04)", () => {
  const ready = item({
    kind: "escalation", source_id: "sched-src",
    payload: { kind: "routine_trigger_activation", schedule_id: "sch-1", slug: "weekly-scan" },
  })

  it("is a decision for a manager, not an admin", () => {
    expect(decisionMetaFor(ready)).toMatchObject({ heading: "Routine trigger ready", requires: "create" })
  })

  it("Activate trigger posts the activation of that schedule and refreshes", async () => {
    mount(ready)
    fireEvent.click(screen.getByRole("button", { name: "Activate trigger" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith("/api/v1/workspaces/ws/pipeline-schedules/sch-1/activate", expect.objectContaining({ method: "POST" })))
    await waitFor(() => expect(onRefresh).toHaveBeenCalledWith("approved"))
  })

  it("has no Dismiss: the only way to leave it is deleting the trigger", () => {
    mount(ready)
    expect(screen.queryByRole("button", { name: /Dismiss|Keep off/ })).toBeNull()
  })

  it("shows nothing to a reader who may not activate (disabled by role)", () => {
    mount(ready, true)
    expect(screen.queryByRole("button", { name: "Activate trigger" })).toBeNull()
  })

  it("says why when the server refuses", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 409, json: async () => ({ error: "schedule is not awaiting activation" }) })
    mount(ready)
    fireEvent.click(screen.getByRole("button", { name: "Activate trigger" }))
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("schedule is not awaiting activation", expect.anything()))
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("without a schedule id there is nothing to activate", () => {
    mount(item({ kind: "escalation", payload: { kind: "routine_trigger_activation" } }))
    expect(screen.queryByRole("button", { name: "Activate trigger" })).toBeNull()
  })
})

describe("Artifact creation held (I04)", () => {
  it("stays without buttons: nothing is staged and no endpoint approves it", () => {
    mount(item({ kind: "escalation", payload: { kind: "page_create_held", requested_name: "Q4 board" } }))
    expect(screen.queryByRole("button")).toBeNull()
  })
})
