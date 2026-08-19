import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within, cleanup, fireEvent, waitFor } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"

// useRouter/usePathname are globally mocked in vitest.setup.ts.
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: "OWNER" }),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { CommandPalette } from "../command-palette"

const SEARCH_PATH = "/api/v1/conversations/search"

interface ConvState {
  /** Hits the endpoint answers with. */
  hits: unknown[]
  /** When set, the endpoint fails with this status (503 = not configured). */
  failWith?: number
  /** Every query the endpoint was asked for, in order. */
  queries: string[]
}

const ISSUE_MATCHING_DEPLOY = {
  id: "i1",
  identifier: "ENG-1",
  title: "deploy the release branch",
  status: "todo",
  priority: "high",
  assignee_name: null,
  crew_name: null,
  crew_slug: null,
}

let conv: ConvState

beforeEach(() => {
  conv = { hits: [], queries: [] }
  vi.mocked(apiFetch).mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes(SEARCH_PATH)) {
      const body = JSON.parse(String(init?.body ?? "{}"))
      conv.queries.push(body.query)
      if (conv.failWith) {
        return { ok: false, status: conv.failWith, json: async () => ({}) } as unknown as Response
      }
      return {
        ok: true,
        status: 200,
        json: async () => ({ count: conv.hits.length, query: body.query, scope: "workspace", hits: conv.hits }),
      } as unknown as Response
    }
    // Entity lists. Only the issue list carries a fixture, and it matches
    // the same word the conversation search is given, so a test can tell
    // "the other groups still rendered" apart from "cmdk filtered them all
    // out because nothing matched".
    if (url.includes("/api/v1/issues")) {
      return { ok: true, status: 200, json: async () => [ISSUE_MATCHING_DEPLOY] } as unknown as Response
    }
    return { ok: true, status: 200, json: async () => [] } as unknown as Response
  })
})

afterEach(cleanup)

function hit(over: Record<string, unknown> = {}) {
  return {
    id: "m1",
    session_id: "chat-1",
    agent_id: "agent-1",
    agent_slug: "backend-bot",
    agent_name: "Backend Bot",
    role: "user",
    content: "please deploy the staging pipeline tonight",
    ts: new Date(Date.now() - 3 * 3600 * 1000).toISOString(),
    ...over,
  }
}

function openPalette() {
  return render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
}

function type(text: string) {
  const input = screen.getByPlaceholderText(/search issues/i)
  fireEvent.change(input, { target: { value: text } })
}

describe("CommandPalette — Conversations", () => {
  it("searches the server as you type and renders the matched messages", async () => {
    conv.hits = [hit()]
    openPalette()
    type("deploy")

    const group = await screen.findByRole("group", { name: /conversations/i })
    expect(within(group).getByText(/staging pipeline/)).toBeInTheDocument()
    expect(within(group).getByText(/Backend Bot/)).toBeInTheDocument()
    // Relative time, not a raw ISO string.
    expect(within(group).getByText(/3h ago/)).toBeInTheDocument()
  })

  it("links a hit to its thread, the URL notifications already deep-link to", async () => {
    conv.hits = [hit()]
    openPalette()
    type("deploy")

    const group = await screen.findByRole("group", { name: /conversations/i })
    const row = within(group).getByText(/staging pipeline/).closest("[data-href]")
    expect(row?.getAttribute("data-href")).toBe("/chat/backend-bot?session=chat-1")
  })

  it("debounces: a burst of keystrokes is one request, for the last of them", async () => {
    conv.hits = [hit()]
    openPalette()
    type("d")
    type("de")
    type("dep")
    type("deploy")

    await screen.findByRole("group", { name: /conversations/i })
    // Give any trailing timer a chance to fire before counting.
    await new Promise((r) => setTimeout(r, 300))
    expect(conv.queries).toEqual(["deploy"])
  })

  it("stays quiet for a query too short to mean anything", async () => {
    conv.hits = [hit()]
    openPalette()
    type("d")

    await new Promise((r) => setTimeout(r, 400))
    expect(conv.queries).toEqual([])
    expect(screen.queryByRole("group", { name: /conversations/i })).not.toBeInTheDocument()
  })

  it("simply does not appear when the endpoint is unconfigured (503)", async () => {
    conv.failWith = 503
    openPalette()
    type("deploy")

    await waitFor(() => expect(conv.queries).toEqual(["deploy"]))
    expect(screen.queryByRole("group", { name: /conversations/i })).not.toBeInTheDocument()
    // No toast, no error row: the user did not ask for this search.
    expect(screen.queryByText(/failed|error/i)).not.toBeInTheDocument()
    // The rest of the palette is unaffected — a search nobody asked for
    // must never cost the groups that did load.
    const issues = await screen.findByRole("group", { name: /issues/i })
    expect(within(issues).getByText(/release branch/)).toBeInTheDocument()
  })

  it("never blocks the entity groups on the conversation request", async () => {
    // A search that never resolves must not stop anything else rendering.
    vi.mocked(apiFetch).mockImplementation(async (url: string) => {
      if (url.includes(SEARCH_PATH)) return new Promise<Response>(() => {})
      if (url.includes("/api/v1/issues")) {
        return { ok: true, status: 200, json: async () => [ISSUE_MATCHING_DEPLOY] } as unknown as Response
      }
      return { ok: true, status: 200, json: async () => [] } as unknown as Response
    })
    openPalette()
    type("deploy")
    const issues = await screen.findByRole("group", { name: /issues/i })
    expect(within(issues).getByText(/release branch/)).toBeInTheDocument()
  })

  it("clears its rows when the query is emptied", async () => {
    conv.hits = [hit()]
    openPalette()
    type("deploy")
    await screen.findByRole("group", { name: /conversations/i })

    type("")
    await waitFor(() =>
      expect(screen.queryByRole("group", { name: /conversations/i })).not.toBeInTheDocument(),
    )
  })
})

describe("CommandPalette — Conversations vs the empty state", () => {
  it("does not say 'No results found' while it is showing conversation hits", async () => {
    // The entity groups match nothing here: the ONLY rows are the ones the
    // server returned, and cmdk does not count them (they are force-mounted
    // precisely because its own filter cannot see the whole message).
    conv.hits = [hit({ content: "the postmortem for the checkout outage" })]
    openPalette()
    type("postmortem")

    const group = await screen.findByRole("group", { name: /conversations/i })
    expect(within(group).getByText(/checkout outage/)).toBeInTheDocument()
    expect(screen.queryByText(/no results found/i)).not.toBeInTheDocument()
  })
})
