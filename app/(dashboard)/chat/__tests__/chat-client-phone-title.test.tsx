import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen } from "@testing-library/react"

// On a phone the Crews path gives way to the back control, so the bar itself
// names the agent; "Chat" alone would say nothing about who you talk to.

let searchParams = new URLSearchParams()
vi.mock("next/navigation", () => ({ useSearchParams: () => searchParams }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-1", loading: false }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))
vi.mock("@/lib/telemetry", () => ({ emitChatEvent: vi.fn() }))
vi.mock("@/components/features/chat/conversations-sidebar", async () => {
  const actual = await vi.importActual<typeof import("@/components/features/chat/conversations-sidebar")>("@/components/features/chat/conversations-sidebar")
  return { ...actual, ConversationsSidebar: () => <div data-testid="sidebar" /> }
})
vi.mock("@/components/features/chat/chat-panel", () => ({ ChatPanel: () => <div data-testid="chat-panel" /> }))

import { ChatClient } from "../chat-client"

const riley = { id: "a-riley", name: "Riley", slug: "riley", status: "IDLE", crew_id: "c1" }
const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetchMock(...args) }))
const ok = (body: unknown) => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: () => Promise.resolve(body) } as Response)

function mount(width: number) {
  Object.defineProperty(window, "innerWidth", { value: width, writable: true, configurable: true })
  Object.defineProperty(window, "location", { configurable: true, writable: true, value: { ...window.location, pathname: "/chat/riley", search: "" } })
  searchParams = new URLSearchParams()
  apiFetchMock.mockImplementation((url: string) => (/\/api\/v1\/agents\?/.test(String(url)) ? ok([riley]) : ok([])))
  render(<ChatClient />)
}

describe("<ChatClient> phone header", () => {
  beforeEach(() => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })))
    window.history.replaceState = vi.fn()
    apiFetchMock.mockReset()
  })
  afterEach(() => vi.restoreAllMocks())

  it("names the agent on a phone", async () => {
    mount(390)
    expect(await screen.findByRole("heading", { name: /Riley/ })).toBeInTheDocument()
  })

  it("keeps the plain title on desktop, where the Crews path names the agent", async () => {
    mount(1280)
    await screen.findByTestId("chat-panel")
    expect(screen.queryByRole("heading", { name: /Riley/ })).toBeNull()
  })
})
