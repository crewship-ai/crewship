import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, waitFor, fireEvent, within } from "@testing-library/react"

// =============================================================================
// /chat with nothing selected.
//
// It used to be a centred narrow column in a very large empty page. The content
// was right — recent threads, then agents — it just had no left column beside
// it, which is what made a 2560px monitor render a strip.
//
// So: the same tree that /chat/<agent> has, on the left; the index as the RIGHT
// pane, with a max width so it does not become a 2000px line of text.
//
// What must NOT change: an index that has chosen no thread mounts no ChatPanel,
// and therefore opens no WebSocket (PRD O7). chat-home.test.tsx already pins
// that for the old layout; this pins it for the layout with the tree in it,
// because the tree is where a careless "preview the selected agent" would land.
// =============================================================================

const mockPush = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mockPush, replace: vi.fn(), back: vi.fn(), forward: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
  usePathname: () => "/chat",
}))

let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed, alt }: { seed: string; alt?: string }) => (
    <span data-testid="agent-avatar" data-seed={seed}>{alt}</span>
  ),
}))

vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: () => <div data-testid="chat-panel" />,
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { ChatHome } from "../chat-home"

// ---------------------------------------------------------------- fixtures

function agent(slug: string, name: string, extra: Record<string, unknown> = {}) {
  return {
    id: `agent-${slug}`,
    name,
    slug,
    status: "IDLE",
    role_title: "Engineer",
    avatar_seed: slug,
    avatar_style: null,
    avatar_url: null,
    crew_id: null,
    expired_at: null,
    ...extra,
  }
}

function chat(id: string, title: string | null, minutesAgo: number, extra: Record<string, unknown> = {}) {
  const iso = new Date(Date.now() - minutesAgo * 60_000).toISOString()
  return {
    id, title, status: "ACTIVE", message_count: 4,
    started_at: iso, ended_at: null, last_activity_at: iso, unread_count: 0, origin: "UI",
    ...extra,
  }
}

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

function routes(agents: unknown[], chatsByAgentId: Record<string, unknown[]>) {
  return (url: string) => {
    if (url.startsWith("/api/v1/agents?")) return ok(agents)
    const m = url.match(/^\/api\/v1\/agents\/([^/?]+)\/chats/)
    if (m) return ok(chatsByAgentId[m[1]] ?? [])
    throw new Error(`unexpected request: ${url}`)
  }
}

describe("ChatHome — the tree beside the index", () => {
  beforeEach(() => {
    workspaceId = "ws-1"
    apiFetch.mockReset()
    mockPush.mockReset()
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: false,
      addEventListener: () => {},
      removeEventListener: () => {},
    })))
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("renders the shared tree as the left column", async () => {
    apiFetch.mockImplementation(
      routes([agent("ada", "Ada")], { "agent-ada": [chat("chat-42", "Ship the export", 10)] }),
    )

    render(<ChatHome />)

    expect(await screen.findByTestId("chat-tree-sidebar")).toBeInTheDocument()
    expect(await screen.findByTestId("chat-tree-agent-ada")).toBeInTheDocument()
  })

  it("bounds the index pane so it does not stretch across a wide screen", async () => {
    apiFetch.mockImplementation(routes([agent("ada", "Ada")], { "agent-ada": [] }))

    render(<ChatHome />)
    await screen.findByTestId("chat-tree-sidebar")

    const pane = screen.getByTestId("chat-home-pane")
    expect(pane.className).toMatch(/max-w-/)
  })

  it("still mounts no ChatPanel and opens no socket, tree or no tree (O7)", async () => {
    const WebSocketSpy = vi.fn()
    vi.stubGlobal("WebSocket", WebSocketSpy)
    apiFetch.mockImplementation(
      routes([agent("ada", "Ada")], { "agent-ada": [chat("chat-42", "Ship the export", 10)] }),
    )

    render(<ChatHome />)
    await screen.findByRole("link", { name: /ship the export/i })
    await screen.findByTestId("chat-tree-agent-ada")

    expect(screen.queryByTestId("chat-panel")).toBeNull()
    expect(WebSocketSpy).not.toHaveBeenCalled()
  })

  // A per-agent /chats request that fails used to be caught into `[]`, which
  // draws as "this agent has no conversations" — in the product's primary
  // navigation, to someone whose server was unhappy for ten seconds.
  it("says a failed thread list failed, and re-asks when told to", async () => {
    let failing = true
    apiFetch.mockImplementation((url: string) => {
      if (url.startsWith("/api/v1/agents?")) return ok([agent("ada", "Ada")])
      if (/^\/api\/v1\/agents\/agent-ada\/chats/.test(url)) {
        return failing
          ? Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) })
          : ok([chat("chat-42", "Ship the export", 10)])
      }
      throw new Error(`unexpected request: ${url}`)
    })

    render(<ChatHome />)

    const failed = await screen.findByTestId("chat-tree-threads-error-ada")
    expect(failed).toHaveTextContent(/500/)

    failing = false
    fireEvent.click(within(failed).getByRole("button", { name: /retry/i }))

    await waitFor(() => expect(screen.queryByTestId("chat-tree-threads-error-ada")).toBeNull())
    // The list is really there this time — the agent has an expander again,
    // and it opens onto the thread the first request never delivered.
    fireEvent.click(screen.getByTestId("chat-tree-agent-ada"))
    expect(await screen.findByTestId("chat-tree-thread-chat-42")).toBeInTheDocument()
  })

  it("shares ONE fan-out with the index — the tree does not fetch the same lists again", async () => {
    const many = Array.from({ length: 3 }, (_, i) => agent(`a${i}`, `Agent ${i}`))
    apiFetch.mockImplementation(routes(many, {}))

    render(<ChatHome />)
    await screen.findByTestId("chat-tree-agent-a0")

    // 1 roster + 1 chats list per agent. A tree with its own copy of the
    // fetching would double this.
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1 + many.length))
  })
})
