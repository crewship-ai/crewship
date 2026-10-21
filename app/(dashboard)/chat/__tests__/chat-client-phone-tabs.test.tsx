import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// On a phone the conversation's sections are one row of tabs. Memory used to
// be reachable only from a second, nested row inside Artifacts and Work.

let searchParams = new URLSearchParams()
vi.mock("next/navigation", () => ({ useSearchParams: () => searchParams }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-1", loading: false }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))
vi.mock("@/lib/telemetry", () => ({ emitChatEvent: vi.fn() }))
vi.mock("@/components/features/chat/conversations-sidebar", async () => {
  const actual = await vi.importActual<typeof import("@/components/features/chat/conversations-sidebar")>("@/components/features/chat/conversations-sidebar")
  return { ...actual, ConversationsSidebar: () => <div data-testid="sidebar" /> }
})
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ mobilePanel }: { mobilePanel?: string }) => <div data-testid="chat-panel" data-panel={mobilePanel ?? ""} />,
}))

import { ChatClient } from "../chat-client"

const riley = { id: "a-riley", name: "Riley", slug: "riley", status: "IDLE", crew_id: "c1" }
const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetchMock(...args) }))
const ok = (body: unknown) => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: () => Promise.resolve(body) } as Response)

describe("<ChatClient> phone section tabs", () => {
  beforeEach(() => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })))
    window.history.replaceState = vi.fn()
    Object.defineProperty(window, "innerWidth", { value: 390, writable: true, configurable: true })
    Object.defineProperty(window, "location", { configurable: true, writable: true, value: { ...window.location, pathname: "/chat/riley", search: "" } })
    searchParams = new URLSearchParams()
    apiFetchMock.mockReset()
    apiFetchMock.mockImplementation((url: string) => (/\/api\/v1\/agents\?/.test(String(url)) ? ok([riley]) : ok([])))
  })
  afterEach(() => vi.restoreAllMocks())

  it("offers Memory beside Chat, Artifacts and Work and opens it", async () => {
    render(<ChatClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-panel", "chat"))
    const row = screen.getByRole("tablist", { name: "Chat panel" })
    expect(within(row).getAllByRole("tab").map(tab => tab.textContent)).toEqual(["Chat", "Artifacts", "Work", "Memory"])
    fireEvent.click(within(row).getByRole("tab", { name: "Memory" }))
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toHaveAttribute("data-panel", "memory"))
  })
})
