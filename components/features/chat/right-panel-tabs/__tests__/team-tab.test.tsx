import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render as rtlRender, screen, waitFor } from "@testing-library/react"
import type { ReactElement, ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { TeamTab } from "../team-tab"

const { fetchMock, handlers } = vi.hoisted(() => ({ fetchMock: vi.fn(), handlers: new Map<string, () => void>() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, handler: () => void) => { handlers.set(event, handler) } }))
function render(element: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return rtlRender(element, { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
}
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
afterEach(() => { cleanup(); vi.resetAllMocks() })
const ok = (value: unknown) => ({ ok: true, json: async () => value })
function fixtures(url: string) {
  if (url.includes("/agents/agent-1?")) return ok({ slug: "marena", crew_id: "crew-1" })
  if (url.includes("/members?")) return ok([{ user_id: "human-1", user: { full_name: "Petra", email: "petra@example.test" } }])
  if (url.includes("/agents?")) return ok([{ id: "agent-1", slug: "marena", name: "Mařena", status: "IDLE" }])
  return ok([])
}
describe("TeamTab", () => {
  it("shows the real roster separately from agent requests and filters requests on the server", async () => {
    fetchMock.mockImplementation(fixtures)
    render(<TeamTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByText("Petra")).toBeTruthy()
    expect(screen.getByRole("link", { name: /Mařena/ }).getAttribute("href")).toBe("/chat/marena")
    expect(screen.getByText("No agent collaboration yet.")).toBeTruthy()
    expect(fetchMock.mock.calls.some(([url]) => url.includes("agent_id=agent-1&limit=20&offset=0"))).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => url.includes("peer-conversations")).length).toBe(2))
  })
  it("recovers from a failed load without falsely displaying an empty roster", async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 503 })
    render(<TeamTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByRole("alert")).toBeTruthy()
    expect(screen.queryByText("No people assigned to this crew.")).toBeNull()
    fetchMock.mockImplementation(fixtures)
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(await screen.findByText("Petra")).toBeTruthy()
  })
  it("pages filtered requests and resets the page when switching agents", async () => {
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("peer-conversations")) return ok(Array.from({ length: 20 }, (_, id) => ({
        id: String(id), from_name: "Mařena", from_slug: "marena", to_name: "Kodi", to_slug: "kodi",
        question: "Review task", response: null, status: "COMPLETED", created_at: "2026-09-06T12:00:00Z",
      })))
      if (url.includes("/agents/agent-2?")) return ok({ slug: "kodi", crew_id: "crew-1" })
      return fixtures(url)
    })
    const view = render(<TeamTab agentId="agent-1" workspaceId="ws-1" />)
    fireEvent.click(await screen.findByRole("button", { name: "Older" }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("agent_id=agent-1&limit=20&offset=20"))).toBe(true))
    view.rerender(<TeamTab agentId="agent-2" workspaceId="ws-1" />)
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("agent_id=agent-2&limit=20&offset=0"))).toBe(true))
  })
  it("keeps collaboration visible when the human roster is forbidden and refreshes on events", async () => {
    fetchMock.mockImplementation((url: string) => url.includes("/members?") ? { ok: false, status: 403 } : fixtures(url))
    render(<TeamTab agentId="agent-1" workspaceId="ws-1" />)
    expect((await screen.findByRole("alert")).textContent).toContain("Unable to load crew people")
    expect(screen.getByText("No agent collaboration yet.")).toBeTruthy()
    expect(screen.queryByText("No people assigned to this crew.")).toBeNull()
    handlers.get("peer_conversation.updated")!()
    await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => url.includes("peer-conversations")).length).toBe(2))
  })
  it("does not request another workspace when no workspace is selected", () => {
    render(<TeamTab agentId="agent-1" workspaceId={null} />)
    expect(screen.getByText("Select a workspace to view team conversations.")).toBeTruthy()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
