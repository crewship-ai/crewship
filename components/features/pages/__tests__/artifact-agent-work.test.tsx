import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ArtifactAgentWorkProvider, ArtifactAgentRequests, ArtifactSendButton } from "../artifact-agent-work"
import { apiFetch } from "@/lib/api-fetch"
import type { ArtifactRequestsResult } from "@/hooks/use-artifact-requests"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
let data: ArtifactRequestsResult
let lost: boolean
let readStatus: number
const writes: Record<string, unknown>[] = []
const clients: QueryClient[] = []
beforeEach(() => {
  data = { can_dispatch: true, eligible_chats: [{ chat_id: "private", title: "Report work", agent_id: "a", agent_name: "Ada", agent_slug: "ada" }], requests: [] }
  lost = false; readStatus = 200; writes.length = 0
  vi.mocked(apiFetch).mockImplementation(async (_url, options) => {
    if (options?.method === "POST") {
      writes.push(JSON.parse(String(options.body)))
      if (lost) { lost = false; throw new Error("lost response") }
      return new Response(JSON.stringify({ id: "request1" }), { status: 202 })
    }
    return new Response(JSON.stringify(readStatus === 200 ? data : { error: "read failed" }), { status: readStatus })
  })
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()) })
function mount(head = 1, revision = 1) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client)
  const dirty = vi.fn()
  const content = (h: number) => <QueryClientProvider client={client}><ArtifactAgentWorkProvider workspaceId="ws" slug="report" revision={revision} head={h} onDirtyChange={dirty} onViewRevision={vi.fn()}><ArtifactSendButton /><ArtifactAgentRequests /></ArtifactAgentWorkProvider></QueryClientProvider>
  const view = render(content(head))
  return { client, dirty, head: (h: number) => view.rerender(content(h)) }
}
async function draft() {
  fireEvent.click(await screen.findByRole("button", { name: "Send to agent" }))
  fireEvent.change(screen.getByLabelText("Private agent chat"), { target: { value: "private" } })
  fireEvent.change(screen.getByLabelText("Instructions"), { target: { value: "Clarify the report" } })
}
it("does not dispatch until explicit send and retries an uncertain submission with the same identity", async () => {
  const view = mount(); await draft()
  expect(writes).toHaveLength(0)
  lost = true
  fireEvent.click(screen.getByRole("button", { name: "Send request" }))
  await screen.findByText(/send could not be confirmed/)
  readStatus = 500
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["artifact-requests"] }) })
  expect((screen.getByLabelText("Instructions") as HTMLTextAreaElement).value).toBe("Clarify the report")
  expect(view.dirty).toHaveBeenLastCalledWith(true)
  readStatus = 200
  fireEvent.click(screen.getByRole("button", { name: "Retry send" }))
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[0]).toEqual(writes[1])
  expect(writes[0]).toMatchObject({ chat_id: "private", base_rev: 1, instruction: "Clarify the report", submission_id: expect.any(String) })
  expect(writes[0]).not.toHaveProperty("intent_id")
})
it("retains the captured revision and draft when a newer head arrives", async () => {
  const view = mount(); await draft(); view.head(2)
  expect(screen.getByText(/Revision 1 is no longer latest/)).toBeTruthy()
  expect((screen.getByRole("button", { name: "Send request" }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByLabelText("Instructions") as HTMLTextAreaElement).value).toBe("Clarify the report")
  expect(writes).toHaveLength(0)
})
it("hides owner controls for readers and never treats success without a revision as published", async () => {
  data.can_dispatch = false; data.eligible_chats = []
  data.requests = [{ id: "r", chat_id: "opaque", agent_id: "a", agent_slug: "private-agent", instruction: "Work", base_rev: 1, status: "succeeded", created_at: "2026-10-13T12:00:00Z", updated_at: "2026-10-13T12:00:00Z" }]
  mount()
  await screen.findByText("Result unavailable")
  expect(screen.queryByRole("button", { name: "Send to agent" })).toBeNull()
  expect(screen.queryByRole("link", { name: "Open agent chat" })).toBeNull()
  expect(screen.queryByRole("button", { name: /View result revision/ })).toBeNull()
  expect(writes).toHaveLength(0)
})
it("purges a private draft after read access is revoked", async () => {
  const view = mount(); await draft(); readStatus = 403
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["artifact-requests"] }) })
  await waitFor(() => expect(screen.queryByLabelText("Instructions")).toBeNull())
  expect(view.dirty).toHaveBeenLastCalledWith(false)
})
it("requires a new explicit submission when retrying a failed job", async () => {
  data.requests = [{ id: "failed", chat_id: "private", agent_id: "a", instruction: "Try again", base_rev: 1, status: "failed", created_at: "2026-10-13T12:00:00Z", updated_at: "2026-10-13T12:00:00Z" }]
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Retry as new request" }))
  expect((screen.getByLabelText("Instructions") as HTMLTextAreaElement).value).toBe("Try again")
  expect(writes).toHaveLength(0)
  fireEvent.click(screen.getByRole("button", { name: "Send request" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0].submission_id).toEqual(expect.any(String))
  expect(writes[0]).not.toHaveProperty("request_id")
})
it("sends cancellation to the server without inventing a terminal result", async () => {
  data.requests = [{ id: "running", chat_id: "private", agent_id: "a", instruction: "Work", base_rev: 1, status: "running", created_at: "2026-10-13T12:00:00Z", updated_at: "2026-10-13T12:00:00Z" }]
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Cancel request" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({})
  await waitFor(() => expect(screen.getByText("Running")).toBeTruthy())
  expect(screen.queryByText("Cancelled")).toBeNull()
})
