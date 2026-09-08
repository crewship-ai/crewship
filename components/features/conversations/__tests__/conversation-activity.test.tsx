import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ConversationActivity } from "../conversation-activity"
const fetcher = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
function setup(canManage: boolean) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><ConversationActivity workspaceId="ws" userId="pavel" conversationId="room" canManage={canManage} /></QueryClientProvider>)
}
beforeEach(() => fetcher.mockReset())
describe("channel activity settings", () => {
  it("lets a creator update one subscription while preserving the other in the workspace scope", async () => {
    let settings = { issues: false, routines: true }
    fetcher.mockImplementation(async (_url, options) => {
      if (options?.method === "PUT") settings = JSON.parse(options.body)
      return Response.json(settings)
    })
    setup(true)
    const issues = await screen.findByRole("checkbox", { name: "Issue updates" })
    fireEvent.click(issues)
    await waitFor(() => expect(issues).toBeChecked())
    expect(fetcher).toHaveBeenCalledWith("/api/v1/conversations/room/activity?workspace_id=ws", expect.objectContaining({ method: "PUT", body: JSON.stringify({ issues: true, routines: true }) }))
    expect(screen.getByRole("checkbox", { name: "Routine results" })).toBeChecked()
  })
  it("shows subscriptions to members without enabling unauthorized edits", async () => {
    fetcher.mockResolvedValue(Response.json({ issues: true, routines: false }))
    setup(false)
    expect(await screen.findByRole("checkbox", { name: "Issue updates" })).toBeDisabled()
    expect(screen.getByRole("checkbox", { name: "Routine results" })).toBeDisabled()
    expect(fetcher.mock.calls.every(([, options]) => options?.method !== "PUT")).toBe(true)
  })
  it("preserves saved state and exposes a failed update", async () => {
    fetcher.mockImplementation(async (_url, options) => options?.method === "PUT" ? new Response("", { status: 403 }) : Response.json({ issues: false, routines: false }))
    setup(true)
    fireEvent.click(await screen.findByRole("checkbox", { name: "Issue updates" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Unable to save")
    expect(screen.getByRole("checkbox", { name: "Issue updates" })).not.toBeChecked()
  })
})
