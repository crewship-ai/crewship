import { cleanup, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { VirtualConversation } from "../virtual-conversation"
import { DocumentResultCards } from "../document-result-cards"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
// There are deliberately no transcript turns. Accidentally rendering one must
// fail rather than manufacturing the history needed to display a saved result.
vi.mock("../turn-renderer", () => ({ TurnRenderer: () => { throw new Error("unexpected transcript turn") } }))
const clients: QueryClient[] = []
beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ results: [{
    page_id: "restored-document", type: "document", page_slug: "old-restored-slug", title: "Restored saved plan", revision: 0,
    created_at: "2026-10-17T00:00:00Z", agent_id: "former-agent", agent_name: "Ada", run_id: "ended-restored-run", on_behalf_of_user_id: "",
  }] })))
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()) })
function mount(enabled = true, currentUserId: string | null = "current-human") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(client)
  render(<QueryClientProvider client={client}>
    <VirtualConversation turns={[]} workspaceId="workspace" sessionId="recovered-chat" agentId="former-agent" historyLoading={false} isStreaming={false} animateAfter={0} onCopy={vi.fn()} onFileClick={vi.fn()} resolveAuthorName={() => null}
      footer={<DocumentResultCards workspaceId="workspace" chatId="recovered-chat" currentUserId={currentUserId} isStreaming={false} documentsEnabled={enabled} />} />
  </QueryClientProvider>)
}
it("shows a durable saved Document in the real empty virtual conversation after transcript recovery", async () => {
  mount()
  expect(screen.getByText("Start a conversation")).toBeTruthy()
  const link = await screen.findByRole("link", { name: "Open document Restored saved plan" })
  expect(link.getAttribute("href")).toBe("/pages?ref=restored-document&workspace_id=workspace")
  expect(apiFetch).toHaveBeenCalledWith("/api/v1/chats/recovered-chat/document-results?workspace_id=workspace", expect.objectContaining({ signal: expect.any(AbortSignal) }))
  expect(vi.mocked(apiFetch).mock.calls.every(call => !call[1]?.method)).toBe(true)
})
it.each(["disabled", "unknown human"])("does not request or show saved Documents when %s", reason => {
  mount(reason !== "disabled", reason === "unknown human" ? null : "current-human")
  expect(screen.getByText("Start a conversation")).toBeTruthy()
  expect(screen.queryByText("Restored saved plan")).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
})
