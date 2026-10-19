import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { UnifiedChatProvider, UnifiedConversationSection } from "../unified-chat"

const api = vi.hoisted(() => vi.fn())
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams() }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "alice" } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))

const mk = (id: string, title: string) => ({ id, title, kind: "channel", is_direct: false, access_scope: "workspace", created_by: "alice", last_sequence: 0, unread_count: 0 })
let rooms = [mk("a", "Alpha"), mk("b", "Beta")]
let client: QueryClient
const tree = (query: string, favorites?: string[]) => (
  <QueryClientProvider client={client}><UnifiedChatProvider><UnifiedConversationSection query={query} section="rooms" favorites={favorites} onFavorite={() => {}} /></UnifiedChatProvider></QueryClientProvider>
)
const titles = () => screen.getAllByRole("button").map((b) => b.textContent ?? "").filter((t) => /Alpha|Beta|Gamma/.test(t)).map((t) => t.replace(/Workspace channel/, "").trim())
async function background(next: typeof rooms) {
  rooms = next
  await act(async () => { await client.invalidateQueries({ queryKey: ["workspace-conversations"] }); await new Promise((r) => setTimeout(r, 20)) })
}

beforeEach(() => {
  rooms = [mk("a", "Alpha"), mk("b", "Beta")]
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  api.mockReset()
  api.mockImplementation(async () => Response.json({ conversations: rooms, next_offset: null }))
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("with the flag on, a background reorder under the pointer waits for Refresh", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_SIDEBAR_VIEWS", "true")
  render(tree(""))
  await screen.findByText("Alpha")
  fireEvent.pointerEnter(screen.getByRole("region", { name: "Groups and channels" }))
  await background([mk("c", "Gamma"), mk("b", "Beta"), mk("a", "Alpha")])
  expect(titles()).toEqual(["Alpha", "Beta"])
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
  expect(titles()).toEqual(["Gamma", "Beta", "Alpha"])
})

it("releasing the pointer applies the pending order", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_SIDEBAR_VIEWS", "true")
  render(tree(""))
  await screen.findByText("Alpha")
  const section = screen.getByRole("region", { name: "Groups and channels" })
  fireEvent.pointerEnter(section)
  await background([mk("b", "Beta"), mk("a", "Alpha")])
  expect(titles()).toEqual(["Alpha", "Beta"])
  fireEvent.pointerLeave(section)
  expect(titles()).toEqual(["Beta", "Alpha"])
})

it("the user's own search applies immediately even while the pointer is on the list", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_SIDEBAR_VIEWS", "true")
  const { rerender } = render(tree(""))
  await screen.findByText("Alpha")
  fireEvent.pointerEnter(screen.getByRole("region", { name: "Groups and channels" }))
  rerender(tree("beta"))
  expect(titles()).toEqual(["Beta"])
  expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull()
})

it("a favourite toggle moves the row immediately while hovering", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_SIDEBAR_VIEWS", "true")
  const { rerender } = render(tree("", []))
  await screen.findByText("Alpha")
  fireEvent.pointerEnter(screen.getByRole("region", { name: "Groups and channels" }))
  rerender(tree("", ["room:a"]))
  expect(titles()).toEqual(["Beta"])
})

it("without the flag the list follows live data immediately", async () => {
  render(tree(""))
  await screen.findByText("Alpha")
  fireEvent.pointerEnter(screen.getByRole("region", { name: "Groups and channels" }))
  await background([mk("b", "Beta"), mk("a", "Alpha")])
  expect(titles()).toEqual(["Beta", "Alpha"])
})
