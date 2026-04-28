import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, act } from "@testing-library/react"

// Mock next/navigation hooks because Next.js 15 doesn't ship a test
// runtime — the real hooks throw outside an App Router context.
const mockReplace = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({
    replace: mockReplace,
    push: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
  usePathname: () => "/",
}))

// Mock useWorkspace so we don't need a real session/cookie.
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

// Mock ChatPanel so we don't pull in the entire WS pipeline. We only
// care that the chat layout renders the panel for a found agent.
vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({ agentId, sessionId }: { agentId: string; sessionId: string }) => (
    <div data-testid="chat-panel" data-agent-id={agentId} data-session-id={sessionId}>
      ChatPanel mock
    </div>
  ),
}))

import { ChatPageClient } from "../chat-page-client"

const mockAgents = [
  {
    id: "agent-1",
    name: "Filip",
    slug: "filip",
    status: "IDLE",
    role_title: "Data Analyst",
    avatar_seed: "filip",
    avatar_style: null,
    crew: { name: "Research", slug: "research", avatar_style: null },
  },
]

describe("<ChatPageClient> — slug resolution from URL", () => {
  beforeEach(() => {
    mockReplace.mockClear()
    // Set the URL the test "is on". Static export quirk: useParams() in
    // Next.js 15 returns the prerender placeholder ("_") even after
    // navigation. The fix is for the component to read pathname instead.
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip" },
    })

    // Mock fetch globally for agent list + chat session create.
    global.fetch = vi.fn((url: string, init?: RequestInit) => {
      const u = String(url)
      if (u.includes("/api/v1/agents") && !u.includes("/chats")) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve(mockAgents),
        }) as unknown as Promise<Response>
      }
      if (u.includes("/chats") && init?.method === "POST") {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            id: "session-1",
            title: null,
            status: "ACTIVE",
            message_count: 0,
            started_at: new Date().toISOString(),
            ended_at: null,
          }),
        }) as unknown as Promise<Response>
      }
      if (u.includes("/chats")) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([]),
        }) as unknown as Promise<Response>
      }
      return Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve("") }) as unknown as Promise<Response>
    }) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("reads agent slug from window.location.pathname (not useParams)", async () => {
    // Regression: useParams returns "_" in static export. The component
    // must pull from window.location.pathname instead. If this test
    // breaks, the dev VM is going to render "Could not read agent slug
    // from URL" again.
    render(<ChatPageClient />)

    // After mount, the agent fetch fires and the chat layout appears.
    await waitFor(
      () => expect(screen.getByText(/Filip/)).toBeInTheDocument(),
      { timeout: 3000 },
    )
  })

  it("does NOT show 'Could not read agent slug' when URL is valid", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(screen.queryByText(/Could not read agent slug/)).not.toBeInTheDocument())
  })

  it("falls back to error UI when URL is the static placeholder /chat/_", async () => {
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/_" },
    })

    render(<ChatPageClient />)
    await waitFor(
      () => expect(screen.getByText(/Could not read agent slug/)).toBeInTheDocument(),
      { timeout: 3000 },
    )
  })

  it("renders error UI when the slug is valid but the agent is missing", async () => {
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/ghost" },
    })

    render(<ChatPageClient />)
    await waitFor(
      () => expect(screen.getByText(/not found in workspace/)).toBeInTheDocument(),
      { timeout: 3000 },
    )
  })

  it("creates a session and navigates with ?session= query when none provided", async () => {
    render(<ChatPageClient />)
    await waitFor(() => expect(mockReplace).toHaveBeenCalled(), { timeout: 3000 })
    const callUrl = mockReplace.mock.calls[0][0] as string
    expect(callUrl).toMatch(/^\/chat\/filip\?session=session-1$/)
  })
})

describe("<ChatPageClient> — never blank", () => {
  it("always renders something visible during initial mount", () => {
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/chat/filip" },
    })
    const { container } = render(<ChatPageClient />)
    // Must render at least the skeleton wrapper (height + class).
    expect(container.firstChild).not.toBeNull()
    // Either shows a skeleton or has already settled into the chat shell.
    // The thing we're guarding against is a totally empty page.
    expect(container.querySelector("div")).not.toBeNull()
  })
})
