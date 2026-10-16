import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

const state = vi.hoisted(() => ({ user: "h1", workspace: "w1", send: vi.fn(), fetch: vi.fn(), mode: "trusted", access: "trusted", mobile: false, pending: false, resolveProfile: () => {} }))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => ({ turns: [], sendMessage: state.send, stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(), loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(), isStreaming: false, connectionStatus: "connected" }) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: state.user } } }), useSessionSafe: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: state.workspace, loading: false, workspaces: state.access === "restricted" ? [{ currentUserAccessMode: "restricted" }] : [] }) }))
vi.mock("@/lib/api-fetch", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: state.fetch }))
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
vi.mock("../search/conversation-search", () => ({ ConversationSearch: () => null }))
vi.mock("../export/export-dialog", () => ({ ExportDialog: () => null }))
vi.mock("../files/restricted-files", () => ({ RestrictedFiles: () => null }))
vi.mock("../restricted-memory", () => ({ RestrictedMemory: () => null }))
vi.mock("../composer/chat-composer", () => ({ ChatComposer: ({ sendMessage, onSent }: { sendMessage: (text: string) => boolean; onSent: () => void }) => <button type="button" onClick={() => { if (sendMessage("Read the selected brief") !== false) onSent() }}>Send fixture message</button> }))

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => state.mobile }))

import { ChatPanel } from "../chat-panel"


const snapshot = { scope: "configured_chat_agent", model: "configured-model", payer: { label: "Safe payer", state: "ready" }, budgets: [{ id: "b", scope: "crew", remaining_usd: 60, limit_usd: 100, window: "month", mode: "hard", state: "ok" }], gate: { blocking: false, predicted_mode: "none", rule_matches: false }, constraints: [], estimate_usd: null }
function panel(chat = "chat1") {
 return <ChatPanel agentId="agent" sessionId={chat} agentName="Native" agentSlug="native" mobilePanel={state.mobile ? "chat" : undefined} />
}
function setup() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
 const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
 return render(panel(), { wrapper })
}
function entityCalls() { return state.fetch.mock.calls.filter(([url]) => String(url).includes("entity-refs")) }
function e2Calls() { return state.fetch.mock.calls.filter(([url]) => /\/(proposals|decisions)(\?|$)/.test(String(url))) }
function preflightCalls() { return state.fetch.mock.calls.filter(([url]) => String(url).includes("run-preflight")) }
beforeEach(() => {
 vi.clearAllMocks(); state.user = "h1"; state.workspace = "w1"; state.mode = "trusted"; state.access = "trusted"; state.mobile = false; state.pending = false
 state.fetch.mockImplementation(async (url: string) => {
  if (url.includes("execution-profile") && state.pending) return new Promise(resolve => { state.resolveProfile = () => resolve({ ok: true, status: 200, json: async () => ({ mode: "trusted" }) }) })
  if (/\/(proposals|decisions)(\?|$)/.test(url)) return { ok: true, status: 200, json: async () => ({ contract_version: 1, reset: true, next_cursor: "", snapshot_id: "preflight-scope", snapshot_complete: true, has_more: false, refs: [] }) }
  return { ok: true, status: 200, json: async () => url.includes("execution-profile") ? { mode: state.mode } : url.includes("run-preflight") ? snapshot : { messages: [], refs: [] } }
 })
})
describe("mounted configured-run preflight", () => {
 it.each([false, true])("reads verified scoped API above the composer (mobile=%s)", async mobile => {
  state.mobile = mobile; setup()
  const summary = await screen.findByText(/Configured model · price unknown/)
  expect(preflightCalls()).toHaveLength(1)
  expect(preflightCalls()[0][0]).toBe("/api/v1/chats/chat1/run-preflight?workspace_id=w1")
  const details = summary.closest("details")!
  expect(details.open).toBe(false)
  expect(details.compareDocumentPosition(screen.getByRole("button", { name: "Send fixture message" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(screen.getByLabelText("Configured model budget details")).toHaveAttribute("tabindex", "0")
  expect(screen.getByText(/Safe payer/)).toBeInTheDocument()
 })
 it.each(["pending", "restricted", "account-restricted"])("does not read preflight for %s", async mode => {
  state.pending = mode === "pending"; state.mode = mode === "restricted" ? "restricted" : "trusted"; state.access = mode === "account-restricted" ? "restricted" : "trusted"
  setup(); await waitFor(() => expect(state.fetch).toHaveBeenCalled())
  expect(preflightCalls()).toHaveLength(0)
  expect(entityCalls()).toHaveLength(0)
  expect(e2Calls()).toHaveLength(0)
 })
 it("clears old identity facts and waits for a fresh profile after scope changes", async () => {
  const view = setup(); await screen.findByText(/Safe payer/)
  const oldEntityCalls = entityCalls().length
  const oldE2Calls = e2Calls().length
  expect(oldE2Calls).toBe(2)
  state.user = "h2"; state.workspace = "w2"; state.pending = true
  view.rerender(panel("chat2"))
  await waitFor(() => expect(screen.queryByText(/Safe payer/)).not.toBeInTheDocument())
  expect(preflightCalls()).toHaveLength(1)
  expect(entityCalls()).toHaveLength(oldEntityCalls)
  expect(e2Calls()).toHaveLength(oldE2Calls)
  await act(async () => state.resolveProfile())
  await screen.findByText(/Safe payer/)
  expect(preflightCalls()).toHaveLength(2)
  await waitFor(() => expect(e2Calls()).toHaveLength(oldE2Calls + 2))
  expect(preflightCalls()[1][0]).toBe("/api/v1/chats/chat2/run-preflight?workspace_id=w2")
 })
})
