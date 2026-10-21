import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"

// The phone conversation drawer was 280px wide, which left its search field
// about 100px beside Search all chats, Filter and Close ("Search n").

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

describe("<ChatClient> phone conversation drawer", () => {
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

  it("gives the conversation list most of a phone's width", async () => {
    render(<ChatClient />)
    await screen.findByTestId("chat-panel")
    fireEvent.click(screen.getByRole("button", { name: "Show conversations" }))
    const drawer = screen.getByRole("button", { name: "Close conversation list" }).nextElementSibling!
    expect(drawer.firstElementChild).toHaveClass("w-[min(320px,85vw)]")
  })
})
