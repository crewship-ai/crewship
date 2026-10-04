import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
const mocks = vi.hoisted(() => ({ can: vi.fn(), success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: mocks.success, error: mocks.error } }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ abilities: { can: mocks.can } }) }))
vi.mock("@/lib/api-fetch", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: vi.fn() }))
import { apiFetch } from "@/lib/api-fetch"
import { CrewPolicyControls } from "../crew-policy-controls"

const ok = (body: unknown) => new Response(JSON.stringify(body))
const policy = { autonomy_level: "guided", behavior_mode: "warn", set_at: "2026-10-03T08:00:00Z", reason: "Initial review" }
const props = { crewId: "crew-one", workspaceId: "workspace-one" }
function feeds() { vi.mocked(apiFetch).mockImplementation(async (input, init) => {
  if (init?.method === "PUT") return ok({ ...policy, ...JSON.parse(String(init.body)) })
  if (init?.method === "PATCH") return ok(JSON.parse(String(init.body)))
  return ok(String(input).endsWith("/policy") ? policy : { max_ephemeral_agents: 10 })
}) }
const writes = () => vi.mocked(apiFetch).mock.calls.filter(([, init]) => ["PUT", "PATCH"].includes(init?.method ?? ""))
const quota = () => screen.getByRole("spinbutton", { name: "Ephemeral agent quota" })
function setQuota(value: string) { fireEvent.change(quota(), { target: { value } }) }
function setPolicy() { fireEvent.click(screen.getByTestId("autonomy-trusted")); fireEvent.change(screen.getByRole("textbox", { name: /Reason/ }), { target: { value: "  Approved change  " } }) }
const save = () => fireEvent.click(screen.getByRole("button", { name: "Save policy" }))
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (value: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
beforeEach(() => { mocks.can.mockReset().mockReturnValue(true); mocks.success.mockReset(); mocks.error.mockReset(); vi.mocked(apiFetch).mockReset(); feeds() })
afterEach(cleanup)

describe("Crew policy and ephemeral quota controls", () => {
  it("loads values and audit history with workspace headers and derives edit authority", async () => {
    render(<CrewPolicyControls {...props} />)
    expect(screen.getByText("Loading policy…")).toBeInTheDocument()
    expect(await screen.findByTestId("autonomy-guided")).toHaveAttribute("aria-pressed", "true")
    expect(quota()).toHaveValue(10)
    expect(screen.getByText(/Initial review/)).toBeInTheDocument()
    expect(mocks.can).toHaveBeenCalledWith("manage", "Crew")
    for (const [, init] of vi.mocked(apiFetch).mock.calls) expect(init?.headers).toEqual({ "X-Workspace-ID": "workspace-one" })
  })
  it.each([false, undefined])("respects read-only authority with override %s", async canEdit => {
    mocks.can.mockReturnValue(false)
    render(<CrewPolicyControls {...props} canEdit={canEdit} />)
    expect(await screen.findByTestId("autonomy-guided")).toBeDisabled(); expect(quota()).toBeDisabled()
  })
  it("honors an explicit edit override and cancels pending changes", async () => {
    mocks.can.mockReturnValue(false)
    render(<CrewPolicyControls {...props} canEdit />)
    expect(await screen.findByTestId("autonomy-strict")).toBeEnabled()
    fireEvent.click(screen.getByTestId("autonomy-strict")); setQuota("20")
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(quota()).toHaveValue(10); expect(screen.getByTestId("autonomy-guided")).toHaveAttribute("aria-pressed", "true")
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument(); expect(writes()).toHaveLength(0)
  })
  it("blocks full autonomy with block behavior until the operator chooses warn", async () => {
    vi.mocked(apiFetch).mockImplementation(async input => ok(String(input).endsWith("/policy") ? { ...policy, behavior_mode: "block" } : { max_ephemeral_agents: 10 }))
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-full")
    fireEvent.click(screen.getByTestId("autonomy-full"))
    expect(screen.getByText("Forbidden combination")).toBeInTheDocument()
    expect(screen.getByTestId("behavior-block")).toBeDisabled()
    fireEvent.change(screen.getByRole("textbox", { name: /Reason/ }), { target: { value: "Approved" } })
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled()
    fireEvent.click(screen.getByTestId("behavior-warn"))
    expect(screen.queryByText("Forbidden combination")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save policy" })).toBeEnabled()
  })
  it.each(["", "-1", "1.5", "101"])("rejects quota %s without sending a mutation", async value => {
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); setQuota(value)
    expect(quota()).toHaveAttribute("aria-invalid", "true")
    expect(screen.getByText("Must be a whole number between 0 and 100.")).toBeInTheDocument(); expect(writes()).toHaveLength(0)
  })
  it.each(["0", "100"])("saves boundary quota %s without requiring a policy reason", async value => {
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); setQuota(value); save()
    await waitFor(() => expect(mocks.success).toHaveBeenCalledWith("Quota updated"))
    expect(writes()).toHaveLength(1); expect(writes()[0][1]?.method).toBe("PATCH")
    expect(JSON.parse(String(writes()[0][1]?.body))).toEqual({ max_ephemeral_agents: Number(value) })
    expect(quota()).toHaveValue(Number(value))
  })
  it.each([false, true])("saves policy first with a trimmed reason and quota dirty=%s", async both => {
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); setPolicy(); if (both) setQuota("20"); save()
    await waitFor(() => expect(mocks.success).toHaveBeenCalledWith(both ? "Policy + quota updated" : "Policy updated"))
    expect(writes().map(([, init]) => init?.method)).toEqual(both ? ["PUT", "PATCH"] : ["PUT"])
    expect(JSON.parse(String(writes()[0][1]?.body))).toEqual({ autonomy_level: "trusted", behavior_mode: "warn", reason: "Approved change" })
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
  })
  it.each(["http", "network", "unknown", "json"])("shows policy load failure %s", async failure => {
    vi.mocked(apiFetch).mockImplementation(async input => {
      if (!String(input).endsWith("/policy")) return ok({})
      if (failure === "http") return new Response(null, { status: 403 })
      if (failure === "network") throw new Error("offline")
      if (failure === "unknown") throw "unreadable"
      return new Response("{")
    })
    render(<CrewPolicyControls {...props} />)
    await waitFor(() => expect(screen.queryByText("Loading policy…")).not.toBeInTheDocument())
    expect(screen.queryByTestId("autonomy-guided")).not.toBeInTheDocument()
    expect(document.body.textContent).toMatch(failure === "network" ? /offline/ : failure === "http" ? /HTTP 403/ : failure === "unknown" ? /Failed to load policy/ : /JSON/)
  })
  it.each(["http", "network", "missing", "json"])("retains policy and defaults quota when metadata is %s", async failure => {
    vi.mocked(apiFetch).mockImplementation(async input => {
      if (String(input).endsWith("/policy")) return ok({ ...policy, set_at: null, reason: null })
      if (failure === "http") return new Response(null, { status: 503 })
      if (failure === "network") throw new Error("offline")
      if (failure === "json") return new Response("{")
      return ok({})
    })
    render(<CrewPolicyControls {...props} />)
    expect(await screen.findByTestId("autonomy-guided")).toBeEnabled(); expect(quota()).toHaveValue(10)
  })
  it.each(["message", "empty", "invalid"])("aborts both saves when policy is refused with %s body", async body => {
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); setPolicy(); setQuota("20")
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(body === "message" ? JSON.stringify({ error: "Denied" }) : body === "empty" ? "{}" : "{", { status: 403 }))
    save(); await waitFor(() => expect(mocks.error).toHaveBeenCalledWith(`Failed to update policy: ${body === "message" ? "Denied" : "HTTP 403"}`))
    expect(writes()).toHaveLength(1); expect(quota()).toHaveValue(20)
    expect(screen.getByRole("textbox", { name: /Reason/ })).toHaveValue("  Approved change  ")
  })
  it.each([false, true])("reports quota refusal with policy saved=%s and keeps quota retryable", async both => {
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); if (both) setPolicy(); setQuota("20")
    if (both) vi.mocked(apiFetch).mockResolvedValueOnce(ok({ ...policy, autonomy_level: "trusted" }))
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ error: "Limit refused" }), { status: 409 }))
    save(); await waitFor(() => expect(mocks.error).toHaveBeenCalledWith(expect.stringContaining(both ? "Policy saved, but quota update failed: Limit refused" : "Failed to update quota: Limit refused")))
    const count = writes().length; vi.mocked(apiFetch).mockResolvedValueOnce(ok({ max_ephemeral_agents: 20 })); save()
    await waitFor(() => expect(mocks.success).toHaveBeenCalledWith("Quota updated")); expect(writes().slice(count).map(([, init]) => init?.method)).toEqual(["PATCH"])
  })
  it("does not continue an old policy save into a quota mutation after workspace changes", async () => {
    const view = render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); setPolicy(); setQuota("20")
    const old = deferred<Response>(); vi.mocked(apiFetch).mockReturnValueOnce(old.promise); save()
    view.rerender(<CrewPolicyControls {...props} workspaceId="workspace-two" />)
    await screen.findByTestId("autonomy-guided")
    await act(async () => old.resolve(ok({ ...policy, autonomy_level: "trusted" })))
    expect(writes().map(([, init]) => init?.method)).toEqual(["PUT"])
    expect(mocks.success).not.toHaveBeenCalled(); expect(quota()).toHaveValue(10)
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
  })
  it("ignores a delayed policy response after crew selection changes", async () => {
    const old = deferred<Response>(); vi.mocked(apiFetch).mockReturnValueOnce(old.promise)
    const view = render(<CrewPolicyControls {...props} />)
    view.rerender(<CrewPolicyControls {...props} crewId="crew-two" />)
    await screen.findByTestId("autonomy-guided")
    await act(async () => old.resolve(ok({ ...policy, autonomy_level: "full" })))
    expect(screen.getByTestId("autonomy-guided")).toHaveAttribute("aria-pressed", "true")
  })
  it("disables a pending save when edit authority is removed", async () => {
    const view = render(<CrewPolicyControls {...props} canEdit />); await screen.findByTestId("autonomy-guided"); setPolicy()
    view.rerender(<CrewPolicyControls {...props} canEdit={false} />)
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled()
    save(); expect(writes()).toHaveLength(0)
  })
  it.each([new Error("offline"), "disconnected"])("reports mutation transport failure %s and preserves the draft", async failure => {
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); setPolicy()
    vi.mocked(apiFetch).mockRejectedValueOnce(failure); save()
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith(failure instanceof Error ? "offline" : "Failed to update policy"))
    expect(screen.getByRole("button", { name: "Save policy" })).toBeEnabled()
  })
  it.each(["{}", "{"])("reports status-only quota refusals for body %s", async body => {
    render(<CrewPolicyControls {...props} />); await screen.findByTestId("autonomy-guided"); setQuota("20")
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(body, { status: 409 })); save()
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Failed to update quota: HTTP 409"))
    expect(quota()).toHaveValue(20)
  })
  it.each(["load policy", "load quota", "save policy", "save quota", "policy refusal", "quota refusal", "transport"])("ignores late %s bodies or errors after unmount", async stage => {
    const old = deferred<unknown>()
    if (stage === "load policy") vi.mocked(apiFetch).mockResolvedValueOnce({ ok: true, json: () => old.promise } as Response)
    if (stage === "load quota") vi.mocked(apiFetch).mockResolvedValueOnce(ok(policy)).mockResolvedValueOnce({ ok: true, json: () => old.promise } as Response)
    const view = render(<CrewPolicyControls {...props} />)
    if (!stage.startsWith("load")) {
      await screen.findByTestId("autonomy-guided")
      if (stage.includes("quota")) setQuota("20"); else setPolicy()
      if (stage === "transport") vi.mocked(apiFetch).mockImplementationOnce(() => old.promise as Promise<Response>)
      else vi.mocked(apiFetch).mockResolvedValueOnce({ ok: !stage.includes("refusal"), status: 409, json: () => old.promise } as Response)
      save()
    }
    await act(async () => {})
    const signals = vi.mocked(apiFetch).mock.calls.map(([, init]) => init?.signal)
    view.unmount()
    expect(signals.every(signal => signal?.aborted)).toBe(true)
    await act(async () => { if (stage === "transport") old.reject(new Error("late")); else old.resolve(stage.includes("quota") ? { max_ephemeral_agents: 20 } : policy) })
    expect(mocks.success).not.toHaveBeenCalled(); expect(mocks.error).not.toHaveBeenCalled()
  })

})
