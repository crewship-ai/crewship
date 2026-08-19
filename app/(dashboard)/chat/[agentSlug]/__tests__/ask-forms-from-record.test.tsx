import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// `ask_forms` rides down the same path `suggested_prompts` does.
//
// The page resolves its agent out of the roster it already fetched for the
// tree, and hands ChatPanel the fields it needs from that record. `ask_forms`
// is one more column on the same row — it arrives on `GET /agents` exactly as
// `suggested_prompts` does (internal/api/agents_query.go) — so a second
// request for the detail endpoint, made further down inside the panel, was
// buying a value the page was already holding.
// =============================================================================

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), back: vi.fn(), forward: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
  usePathname: () => "/",
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

// Mocked down to the prop under test — the real panel opens a WebSocket.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ askForms }: { askForms?: string | null }) => (
    <div data-testid="chat-panel" data-ask-forms={askForms === undefined ? "(undefined)" : askForms ?? "(null)"} />
  ),
}))

import { ChatPageClient } from "../chat-page-client"

const receiptForm = JSON.stringify([
  {
    id: "receipt",
    label: "Add a receipt",
    template: "Zaúčtuj fakturu od {{supplier}}",
    attachment: "required",
    fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
  },
])

const agent = {
  id: "agent-1",
  name: "Filip",
  slug: "filip",
  status: "IDLE",
  role_title: "Data Analyst",
  avatar_seed: "filip",
  avatar_style: null,
  suggested_prompts: null as string | null,
  ask_forms: receiptForm as string | null,
  crew: { name: "Research", slug: "research", avatar_style: null },
}

const sessions = [
  { id: "sess-1", title: "Yesterday", status: "ACTIVE", message_count: 3, started_at: new Date().toISOString(), ended_at: null, origin: "UI" },
]

describe("<ChatPageClient> — the agent's ask forms come off the roster record", () => {
  beforeEach(() => {
    agent.ask_forms = receiptForm
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: false,
      addEventListener: () => {},
      removeEventListener: () => {},
    })))
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip", search: "" },
    })
    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/chats")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) }) as unknown as Promise<Response>
      }
      if (u.includes("/api/v1/agents")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([agent]) }) as unknown as Promise<Response>
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
    }) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("passes the record's ask_forms column straight through", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    expect(screen.getByTestId("chat-panel").getAttribute("data-ask-forms")).toBe(receiptForm)
  })

  it("passes null — not nothing — for an agent with no forms, so the panel does not go looking", async () => {
    agent.ask_forms = null
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    // "(undefined)" would mean the page said nothing, which is the panel's
    // signal to fetch the agent for itself.
    expect(screen.getByTestId("chat-panel").getAttribute("data-ask-forms")).toBe("(null)")
  })

  it("makes no request for the agent detail endpoint at all", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    const f = global.fetch as unknown as { mock: { calls: unknown[][] } }
    const detail = f.mock.calls
      .map((c) => String(c[0]))
      .filter((u) => /\/api\/v1\/agents\/[^/?]+(\?|$)/.test(u))
    expect(detail).toEqual([])
  })
})
