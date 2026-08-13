import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, waitFor, within } from "@testing-library/react"

// ---------------------------------------------------------------- stubs

let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))

// The avatar component resolves stored renders and version-checks the style
// registry; neither is what this file is about.
vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed, alt }: { seed: string; alt?: string }) => (
    <span data-testid="agent-avatar" data-seed={seed}>
      {alt}
    </span>
  ),
}))

// O7: the index must never mount a live panel. Mocking it means "if the page
// imports and renders one, this testid appears" — and the assertion below says
// it must not.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: () => <div data-testid="chat-panel" />,
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { ChatHome, AGENT_FANOUT_CAP } from "../chat-home"

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
    expired_at: null,
    ...extra,
  }
}

function chat(id: string, title: string | null, minutesAgo: number, extra: Record<string, unknown> = {}) {
  const iso = new Date(Date.now() - minutesAgo * 60_000).toISOString()
  return {
    id,
    title,
    status: "ACTIVE",
    message_count: 4,
    started_at: iso,
    ended_at: null,
    last_activity_at: iso,
    unread_count: 0,
    ...extra,
  }
}

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

/** Route the fan-out by URL: one agents list + one chats list per agent. */
function routes(agents: unknown[], chatsByAgentId: Record<string, unknown[]>) {
  return (url: string) => {
    if (url.startsWith("/api/v1/agents?")) return ok(agents)
    const m = url.match(/^\/api\/v1\/agents\/([^/?]+)\/chats/)
    if (m) return ok(chatsByAgentId[m[1]] ?? [])
    throw new Error(`unexpected request: ${url}`)
  }
}

// ---------------------------------------------------------------- tests

describe("ChatHome — the /chat index", () => {
  beforeEach(() => {
    workspaceId = "ws-1"
    apiFetch.mockReset()
  })
  afterEach(() => cleanup())

  it("merges threads across agents and orders them newest first", async () => {
    const ada = agent("ada", "Ada")
    const bob = agent("bob", "Bob")
    apiFetch.mockImplementation(
      routes([ada, bob], {
        "agent-ada": [chat("c-old", "Oldest ada thread", 500), chat("c-mid", "Middle ada thread", 60)],
        "agent-bob": [chat("c-new", "Newest bob thread", 5)],
      }),
    )

    render(<ChatHome />)

    const list = await screen.findByRole("list", { name: /recent conversations/i })
    await waitFor(() => expect(within(list).getAllByRole("listitem")).toHaveLength(3))

    // Recency is the whole point of merging client-side: a per-agent list is
    // already sorted, a concatenation of them is not.
    const titles = within(list)
      .getAllByRole("listitem")
      .map((li) => li.textContent ?? "")
    expect(titles[0]).toContain("Newest bob thread")
    expect(titles[1]).toContain("Middle ada thread")
    expect(titles[2]).toContain("Oldest ada thread")
  })

  it("links a thread at /chat/<slug>?session=<id> — the shape notifications already emit", async () => {
    apiFetch.mockImplementation(
      routes([agent("ada", "Ada")], { "agent-ada": [chat("chat-42", "Ship the export", 10)] }),
    )

    render(<ChatHome />)

    const link = await screen.findByRole("link", { name: /ship the export/i })
    expect(link).toHaveAttribute("href", "/chat/ada?session=chat-42")
  })

  it("shows the agent that owns each thread", async () => {
    apiFetch.mockImplementation(
      routes([agent("ada", "Ada Lovelace")], { "agent-ada": [chat("chat-42", "Ship the export", 10)] }),
    )

    render(<ChatHome />)

    const link = await screen.findByRole("link", { name: /ship the export/i })
    expect(link.textContent).toContain("Ada Lovelace")
    expect(within(link).getByTestId("agent-avatar")).toHaveAttribute("data-seed", "ada")
  })

  it("lists the agents beneath the threads, each opening its own chat", async () => {
    apiFetch.mockImplementation(
      routes([agent("ada", "Ada"), agent("bob", "Bob")], {
        "agent-ada": [chat("chat-42", "Ship the export", 10)],
        "agent-bob": [],
      }),
    )

    render(<ChatHome />)

    const agentsList = await screen.findByRole("list", { name: /agents/i })
    expect(within(agentsList).getByRole("link", { name: /ada/i })).toHaveAttribute("href", "/chat/ada")
    expect(within(agentsList).getByRole("link", { name: /bob/i })).toHaveAttribute("href", "/chat/bob")
  })

  it("empty state — no agents at all points at where an agent is created", async () => {
    apiFetch.mockImplementation(routes([], {}))

    render(<ChatHome />)

    const cta = await screen.findByRole("link", { name: /create.*agent/i })
    expect(cta).toHaveAttribute("href", "/crews?new=agent")
    // No agents means no fan-out to run.
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })

  it("empty state — agents but no threads invites picking one", async () => {
    apiFetch.mockImplementation(routes([agent("ada", "Ada")], { "agent-ada": [] }))

    render(<ChatHome />)

    expect(await screen.findByText(/no conversations yet/i)).toBeInTheDocument()
    // The agent list IS the picker, so it has to be on screen for the copy to
    // mean anything.
    const agentsList = await screen.findByRole("list", { name: /agents/i })
    expect(within(agentsList).getByRole("link", { name: /ada/i })).toBeInTheDocument()
  })

  it("hides sessions with no messages — arriving at a chat used to create them", async () => {
    apiFetch.mockImplementation(
      routes([agent("ada", "Ada")], {
        "agent-ada": [chat("c-empty", null, 1, { message_count: 0 }), chat("c-real", "Real thread", 30)],
      }),
    )

    render(<ChatHome />)

    const list = await screen.findByRole("list", { name: /recent conversations/i })
    await waitFor(() => expect(within(list).getAllByRole("listitem")).toHaveLength(1))
    expect(within(list).getByText("Real thread")).toBeInTheDocument()
  })

  it("caps the fan-out so a large roster is not one request per agent", async () => {
    const many = Array.from({ length: AGENT_FANOUT_CAP + 8 }, (_, i) => agent(`a${i}`, `Agent ${i}`))
    apiFetch.mockImplementation(routes(many, {}))

    render(<ChatHome />)

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1 + AGENT_FANOUT_CAP))
    // Every agent still gets a row — only the thread fan-out is capped.
    const agentsList = await screen.findByRole("list", { name: /agents/i })
    expect(within(agentsList).getAllByRole("listitem")).toHaveLength(many.length)
  })

  it("never mounts a ChatPanel, and never opens a socket (O7)", async () => {
    const WebSocketSpy = vi.fn()
    vi.stubGlobal("WebSocket", WebSocketSpy)
    apiFetch.mockImplementation(
      routes([agent("ada", "Ada")], { "agent-ada": [chat("chat-42", "Ship the export", 10)] }),
    )

    render(<ChatHome />)
    await screen.findByRole("link", { name: /ship the export/i })

    expect(screen.queryByTestId("chat-panel")).toBeNull()
    expect(WebSocketSpy).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })
})
