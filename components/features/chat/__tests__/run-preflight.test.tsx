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
it("shows payer, remaining budget and actual prediction without inventing price", async () => {
 fetch.mockResolvedValue(new Response(JSON.stringify(base)))
 setup()
 await screen.findByText(/nonblocking review predicted/)
 expect(screen.getByText(/Workspace payer/)).toBeInTheDocument()
 expect(screen.getByText(/60.00.*remaining/)).toBeInTheDocument()
 expect(screen.getByText(/Price before the run is unknown/)).toBeInTheDocument()
 expect(fetch.mock.calls[0][0]).toContain("workspace_id=workspace")
})
it("drops protected facts on revoked access and hides disabled surfaces", async () => {
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(base))).mockResolvedValueOnce(new Response("{}", { status: 404 }))
 setup()
 await screen.findByText(/Workspace payer/)
 fireEvent.click(screen.getByRole("button", { name: "Refresh budget and gate" }))
 await waitFor(() => expect(screen.queryByText(/Workspace payer/)).not.toBeInTheDocument())
 cleanup(); fetch.mockClear(); setup(false)
 expect(fetch).not.toHaveBeenCalled()
})
it("reports unknown budget on an outage without a stale spending promise", async () => {
 fetch.mockRejectedValue(new Error("offline")); setup()
 expect(await screen.findByRole("status")).toHaveTextContent("budget and gate are unavailable")
 expect(screen.queryByText(/Workspace payer/)).not.toBeInTheDocument()
})
