import React from "react"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import { RunPreflight, type ChatRunPreflight } from "../run-preflight"
const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
const base: ChatRunPreflight = { scope: "configured_chat_agent", agent_id: "a", model: "configured-model", payer: { state: "ready", category: "connected_provider_account", label: "Workspace payer" }, budgets: [{ id: "b", scope: "crew", window: "month", mode: "hard", limit_usd: 100, spent_usd: 40, remaining_usd: 60, state: "ok" }], gate: { configured_mode: "sync", predicted_mode: "async", rule_matches: true, blocking: false, reason: "review" }, estimate_usd: null, estimate_state: "unknown", constraints: ["Snapshot only: funds are not reserved and approval is not granted."] }
function setup(enabled = true) {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
 return render(<QueryClientProvider client={client}><RunPreflight chatId="chat" workspaceId="workspace" userId="viewer" enabled={enabled} /></QueryClientProvider>)
}
afterEach(() => { cleanup(); fetch.mockReset() })
// The composer stays quiet unless a run would actually be held: a predicted
// approval or a hard budget that is used up. Advisory facts (payer, model,
// unknown price, nonblocking review) are not the customer's concern in Chat.
it("shows nothing for a run that will not be held", async () => {
 fetch.mockResolvedValue(new Response(JSON.stringify(base)))
 const view = setup()
 await waitFor(() => expect(fetch).toHaveBeenCalled())
 await new Promise((r) => setTimeout(r, 0))
 expect(view.container).toBeEmptyDOMElement()
 expect(fetch.mock.calls[0][0]).toContain("workspace_id=workspace")
})
it("says plainly when the run will wait for approval", async () => {
 fetch.mockResolvedValue(new Response(JSON.stringify({ ...base, gate: { ...base.gate, blocking: true, predicted_mode: "sync" } })))
 setup()
 expect(await screen.findByRole("status")).toHaveTextContent("Runs in this chat wait for approval before they start.")
 expect(screen.queryByText(/price|payer|configured model/i)).toBeNull()
})
it("says plainly when a hard budget is used up", async () => {
 fetch.mockResolvedValue(new Response(JSON.stringify({ ...base, budgets: [{ ...base.budgets[0], remaining_usd: 0, state: "exceeded" }] })))
 setup()
 expect(await screen.findByRole("status")).toHaveTextContent("The budget for this agent is used up. New runs are blocked until it resets.")
})
it("does not warn about a soft budget that is over", async () => {
 fetch.mockResolvedValue(new Response(JSON.stringify({ ...base, budgets: [{ ...base.budgets[0], mode: "soft", state: "exceeded" }] })))
 const view = setup()
 await waitFor(() => expect(fetch).toHaveBeenCalled())
 await new Promise((r) => setTimeout(r, 0))
 expect(view.container).toBeEmptyDOMElement()
})
it("hides disabled surfaces and stays silent on denial or outage", async () => {
 setup(false)
 expect(fetch).not.toHaveBeenCalled()
 cleanup()
 fetch.mockResolvedValueOnce(new Response("{}", { status: 404 }))
 const denied = setup()
 await waitFor(() => expect(fetch).toHaveBeenCalled())
 expect(denied.container).toBeEmptyDOMElement()
 cleanup(); fetch.mockReset()
 fetch.mockRejectedValue(new Error("offline"))
 const outage = setup()
 await waitFor(() => expect(fetch).toHaveBeenCalled())
 await new Promise((r) => setTimeout(r, 0))
 expect(outage.container).toBeEmptyDOMElement()
})
