import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// The tree's search box and ⌘K are two doors onto the same question — "where
// was that conversation" — and they behave differently: the tree filters
// titles it already has, ⌘K asks the server about message bodies. Somebody who
// searches the tree, finds nothing, and then opens ⌘K is telling you the tree
// search is scoped too narrowly, and that is only visible if both doors emit
// the same event under a different `source`.
//
// Titles are not recorded. A session title is derived from the first message,
// so it is content wearing a label's clothes.
// =============================================================================

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="agent-avatar" data-seed={seed} />,
}))

import { resetChatTelemetry, setChatTelemetrySink, type ChatEvent } from "@/lib/telemetry"

import {
  ChatTreeSidebar,
  type ChatTreeAgent,
  type ChatTreeThread,
} from "../chat-tree-sidebar"

function agent(slug: string, name: string): ChatTreeAgent {
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
  }
}

function thread(id: string, title: string | null, minutesAgo = 5): ChatTreeThread {
  const iso = new Date(Date.now() - minutesAgo * 60_000).toISOString()
  return {
    id,
    title,
    status: "ACTIVE",
    message_count: 3,
    started_at: iso,
    ended_at: null,
    last_activity_at: iso,
    unread_count: 0,
    origin: "UI",
  }
}

const alice = agent("alice", "Alice")
const THREADS = {
  "agent-alice": [
    thread("t1", "rebuild the payroll export"),
    thread("t2", "rebuild the invoice importer", 10),
    thread("t3", "holiday roster", 20),
  ],
}

const onOpenThread = vi.fn()

let events: ChatEvent[]
const named = (name: string) => events.filter((e) => e.name === name)

function renderSidebar() {
  return render(
    <ChatTreeSidebar
      agents={[alice]}
      threadsByAgent={THREADS}
      activeAgentSlug="alice"
      onOpenThread={onOpenThread}
      onOpenAgent={vi.fn()}
    />,
  )
}

function search(text: string) {
  fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: text } })
}

beforeEach(() => {
  onOpenThread.mockClear()
  events = []
  resetChatTelemetry()
  setChatTelemetrySink((e) => events.push(e))
})

afterEach(cleanup)

describe("searching the tree", () => {
  it("records one search with the number of threads it left standing", async () => {
    renderSidebar()
    search("rebuild")

    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))
    expect(named("conversation_search_run")[0].payload).toMatchObject({
      result_count: 2,
      has_results: true,
      source: "sidebar",
    })
  })

  it("records a search that matched nothing", async () => {
    renderSidebar()
    search("zzzzz")

    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))
    expect(named("conversation_search_run")[0].payload).toMatchObject({
      result_count: 0,
      has_results: false,
    })
  })

  it("records one search per pause, not one per keystroke", async () => {
    renderSidebar()
    search("r")
    search("re")
    search("reb")
    search("rebuild")

    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 400))
    expect(named("conversation_search_run")).toHaveLength(1)
  })

  it("records nothing while the box is empty — that is not a search", async () => {
    renderSidebar()
    await new Promise((r) => setTimeout(r, 400))
    expect(named("conversation_search_run")).toHaveLength(0)
  })
})

describe("opening a thread from a search", () => {
  it("records the rank of the row that was opened, and still opens it", async () => {
    renderSidebar()
    search("rebuild")
    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))

    fireEvent.click(screen.getByTestId("chat-tree-thread-t2"))

    expect(named("conversation_search_result_opened")).toHaveLength(1)
    expect(named("conversation_search_result_opened")[0].payload).toMatchObject({
      position: 1,
      result_count: 2,
      session_id: "t2",
      source: "sidebar",
    })
    expect(onOpenThread).toHaveBeenCalledWith(alice, "t2")
  })

  it("records nothing about ranking when no search was running", () => {
    renderSidebar()
    fireEvent.click(screen.getByTestId("chat-tree-thread-t1"))

    expect(named("conversation_search_result_opened")).toHaveLength(0)
    expect(onOpenThread).toHaveBeenCalledWith(alice, "t1")
  })
})

describe("the tree carries no titles into telemetry", () => {
  it("emits neither the search text nor any session title", async () => {
    renderSidebar()
    search("payroll")
    await waitFor(() => expect(named("conversation_search_run")).toHaveLength(1))
    fireEvent.click(screen.getByTestId("chat-tree-thread-t1"))

    const serialized = JSON.stringify(events)
    expect(serialized).not.toContain("payroll")
    expect(serialized).not.toContain("rebuild")
    expect(serialized).not.toContain("Alice")
  })
})

describe("telemetry cannot break the tree", () => {
  it("a throwing sink still opens the thread", async () => {
    setChatTelemetrySink(() => {
      throw new Error("sink exploded")
    })
    renderSidebar()
    search("rebuild")
    expect(() => fireEvent.click(screen.getByTestId("chat-tree-thread-t2"))).not.toThrow()
    expect(onOpenThread).toHaveBeenCalledWith(alice, "t2")
  })
})
