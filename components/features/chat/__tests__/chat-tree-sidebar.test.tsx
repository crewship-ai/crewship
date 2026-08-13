import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

// =============================================================================
// The agent tree — one left column for the whole chat surface.
//
// /chat and /chat/<agent> used to grow two different left columns (a centred
// narrow index on one, a flat 240px session list on the other) and neither was
// the shape /routines and /issues use. This is the shared one, built from
// components/layout/sidebar-kit so the chrome is the SAME chrome: a toolbar
// with search, collapsible section headers carrying counts, and rows whose
// selection is the tokenised accent bar rather than a hardcoded blue.
//
// What the tree is NOT is a second file browser, a second config page or a
// second memory view. It briefly gave every agent four folders — Sessions,
// Files, Asks, Memory — and three of them duplicated a surface that already
// existed (the right rail, the agent config tab, the agent canvas). The rule
// that settled it:
//
//   left column  = navigation between objects   ("where am I going")
//   right panel  = context of the open object   ("what is here")
//   config page  = the object's own settings
//
// So an agent expands to its threads and to nothing else, and the folder rows
// are asserted ABSENT below so nobody puts them back by accident.
// =============================================================================

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="agent-avatar" data-seed={seed} />,
}))

import { ChatTreeSidebar, type ChatTreeAgent, type ChatTreeThread } from "../chat-tree-sidebar"

// ---------------------------------------------------------------- fixtures

function agent(slug: string, name: string, extra: Partial<ChatTreeAgent> = {}): ChatTreeAgent {
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

function thread(
  id: string,
  title: string | null,
  minutesAgo = 5,
  extra: Partial<ChatTreeThread> = {},
): ChatTreeThread {
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
    ...extra,
  }
}

const ada = agent("ada", "Ada Lovelace")
const bob = agent("bob", "Bob Robot")

const BASE = {
  agents: [ada, bob],
  threadsByAgent: {
    "agent-ada": [thread("t-1", "Ship the export", 5), thread("t-2", "Weekly summary", 30)],
    "agent-bob": [thread("t-3", "Rebuild the index", 90)],
  } as Record<string, ChatTreeThread[]>,
  onOpenThread: vi.fn(),
  // The agent row is a destination, not just a disclosure. Both callers pass
  // these; the tree only ever reports the pick.
  onOpenAgent: vi.fn(),
  onShowAllAgents: vi.fn(),
}

function resetBase() {
  BASE.onOpenThread.mockReset()
  BASE.onOpenAgent.mockReset()
  BASE.onShowAllAgents.mockReset()
}

function agentRow(slug: string): HTMLElement {
  return screen.getByTestId(`chat-tree-agent-${slug}`)
}

function folderRows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="chat-tree-folder-"]'))
}

function threadRows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="chat-tree-thread-"]'))
}

function agentOrder(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="chat-tree-agent-"]')).map(
    (r) => r.dataset.testid!.replace("chat-tree-agent-", ""),
  )
}

describe("<ChatTreeSidebar> — the tree", () => {
  beforeEach(resetBase)
  afterEach(() => cleanup())

  it("renders one row per agent, and nothing beneath them until one is expanded", () => {
    render(<ChatTreeSidebar {...BASE} />)

    expect(agentRow("ada")).toBeInTheDocument()
    expect(agentRow("bob")).toBeInTheDocument()
    // A tree that is open everywhere is a list again.
    expect(threadRows()).toHaveLength(0)
  })

  it("expands an agent straight to its threads — one step, not two", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))

    const titles = threadRows().map((r) => r.textContent ?? "")
    expect(titles).toHaveLength(2)
    expect(titles.join(" ")).toContain("Ship the export")
    expect(titles.join(" ")).toContain("Weekly summary")
    // Bob's thread belongs under Bob.
    expect(titles.join(" ")).not.toContain("Rebuild the index")
  })

  it("has no Files, Asks or Memory row — those are the right rail's, the config tab's and the canvas's", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))
    fireEvent.click(agentRow("bob"))

    expect(folderRows()).toHaveLength(0)
    for (const gone of [/^files$/i, /^asks$/i, /^memory$/i, /^sessions$/i]) {
      expect(screen.queryByText(gone)).toBeNull()
    }
    // …and the tree still shows what it is for.
    expect(threadRows().length).toBeGreaterThan(0)
  })

  it("gives an agent with no sessions no expander — a chevron that opens onto nothing is a lie", () => {
    const zed = agent("zed", "Zed Silent")
    render(
      <ChatTreeSidebar
        {...BASE}
        agents={[ada, zed]}
        threadsByAgent={{ ...BASE.threadsByAgent, "agent-zed": [] }}
      />,
    )

    expect(screen.getByTestId("chat-tree-expander-ada")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-expander-zed")).toBeNull()

    // Nothing unfolds under it, because there is nothing to unfold.
    fireEvent.click(agentRow("zed"))
    expect(threadRows()).toHaveLength(0)
  })

  // The defect: the row was `onSelect={onToggle}`, so an agent with no threads
  // had `canExpand === false` and the row did nothing at all. Someone clicked
  // "Alex" and the product did not move — on the one surface that is supposed
  // to make starting a conversation easy.
  it("clicking an agent with NO conversations asks to open one with it", () => {
    const zed = agent("zed", "Zed Silent")
    render(
      <ChatTreeSidebar
        {...BASE}
        agents={[ada, zed]}
        threadsByAgent={{ ...BASE.threadsByAgent, "agent-zed": [] }}
      />,
    )

    fireEvent.click(agentRow("zed"))

    expect(BASE.onOpenAgent).toHaveBeenCalledWith(expect.objectContaining({ slug: "zed" }))
    // …and the tree does not pretend it has a list to show.
    expect(threadRows()).toHaveLength(0)
  })

  it("clicking an agent that HAS conversations opens one too, and reveals the rest", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))

    // One rule for both cases: the row is a conversation, not a disclosure.
    expect(BASE.onOpenAgent).toHaveBeenCalledWith(expect.objectContaining({ slug: "ada" }))
    expect(threadRows()).toHaveLength(2)

    // Closing the branch again is the chevron's job, and closing is not
    // opening — it must not ask for a conversation a second time.
    fireEvent.click(screen.getByTestId("chat-tree-expander-ada"))
    expect(threadRows()).toHaveLength(0)
    expect(BASE.onOpenAgent).toHaveBeenCalledTimes(1)
  })

  it("expands and collapses from the keyboard, without opening anything", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.keyDown(agentRow("ada"), { key: "ArrowRight" })
    expect(threadRows()).toHaveLength(2)

    fireEvent.keyDown(agentRow("ada"), { key: "ArrowLeft" })
    expect(threadRows()).toHaveLength(0)
    expect(BASE.onOpenAgent).not.toHaveBeenCalled()
  })

  // The clutter: seven agents, six of them with no threads and a role line
  // each, around one conversation. The route already says which agent is in
  // hand — /chat is everyone, /chat/<slug> is that one — so the tree follows
  // the route rather than growing a mode of its own.
  it("focuses on the agent the route names, and lists everyone again on /chat", () => {
    const { rerender } = render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" />)

    expect(agentRow("ada")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull()
    // Focused means open: the threads are the point of narrowing to one agent.
    expect(screen.getByTestId("chat-tree-thread-t-1")).toBeInTheDocument()

    // A tree that narrows without a way out is a trap, so the way out is a row
    // of its own — reachable by pointer and by keyboard.
    const back = screen.getByTestId("chat-tree-all-agents")
    expect(back).toHaveAttribute("tabindex", "0")
    fireEvent.keyDown(back, { key: "Enter" })
    fireEvent.click(back)
    expect(BASE.onShowAllAgents).toHaveBeenCalledTimes(2)

    // …and /chat is everybody, with nothing to go back from.
    rerender(<ChatTreeSidebar {...BASE} activeAgentSlug={null} />)
    expect(agentRow("ada")).toBeInTheDocument()
    expect(agentRow("bob")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-all-agents")).toBeNull()
  })

  it("lets a search reach past the focus — a search that cannot see the other six is a lie", () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" />)

    expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull()

    fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: "bob" } })
    expect(screen.getByTestId("chat-tree-agent-bob")).toBeInTheDocument()

    // Clearing it returns to the one agent in hand.
    fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: "" } })
    expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull()
  })

  // "More sorted" was the whole of the request. There is no sort control here
  // and there is not going to be one: the list IS most-recent-first, and what
  // was missing is the timestamp that lets a reader see that for themselves.
  it("lists an agent's threads most-recent-first, and says how long ago each was active", () => {
    render(
      <ChatTreeSidebar
        {...BASE}
        agents={[ada]}
        // Deliberately handed over out of order — the tree sorts, it does not
        // trust its input.
        threadsByAgent={{
          "agent-ada": [thread("t-old", "Older", 120), thread("t-new", "Newer", 2)],
        }}
        activeAgentSlug="ada"
      />,
    )

    expect(threadRows().map((r) => r.dataset.testid)).toEqual([
      "chat-tree-thread-t-new",
      "chat-tree-thread-t-old",
    ])
    expect(screen.getByTestId("chat-tree-thread-t-new")).toHaveTextContent(/2m ago/)
    expect(screen.getByTestId("chat-tree-thread-t-old")).toHaveTextContent(/2h ago/)
  })

  // The label and the ordering must come from the same parse. `new Date()` reads
  // the legacy SQLite format ("2026-07-01 10:00:00", implicitly UTC) in the
  // local zone, so a naive timeAgo() would put a row hours away from where the
  // sort put it — and a label that contradicts the order is worse than none.
  it("reads a legacy timestamp the way it sorts it", () => {
    const legacy = new Date(Date.now() - 90 * 60_000)
      .toISOString()
      .replace("T", " ")
      .replace(/\.\d+Z$/, "")
    render(
      <ChatTreeSidebar
        {...BASE}
        agents={[ada]}
        threadsByAgent={{
          "agent-ada": [
            thread("t-legacy", "From the old rows", 0, {
              started_at: legacy,
              last_activity_at: null,
            }),
          ],
        }}
        activeAgentSlug="ada"
      />,
    )

    expect(screen.getByTestId("chat-tree-thread-t-legacy")).toHaveTextContent(/1h ago/)
  })

  it("orders agents by their most recent thread, so the top of the tree is where the work is", () => {
    const cleo = agent("cleo", "Cleo") // alphabetically first, quietest
    const zed = agent("zed", "Zed Silent") // no threads at all
    render(
      <ChatTreeSidebar
        {...BASE}
        agents={[cleo, zed, ada, bob]}
        threadsByAgent={{
          "agent-cleo": [thread("t-c", "Ancient history", 60 * 24 * 30)],
          "agent-zed": [],
          "agent-ada": [thread("t-1", "Ship the export", 5)],
          "agent-bob": [thread("t-3", "Rebuild the index", 90)],
        }}
      />,
    )

    // ada (5m) · bob (90m) · cleo (30d) · zed (never)
    expect(agentOrder()).toEqual(["ada", "bob", "cleo", "zed"])
  })

  it("says a thread list that FAILED failed — an empty tree is not a 500", () => {
    const onRetryThreads = vi.fn()
    render(
      <ChatTreeSidebar
        {...BASE}
        threadsByAgent={{ ...BASE.threadsByAgent, "agent-bob": [] }}
        threadErrors={{ "agent-bob": "HTTP 500" }}
        onRetryThreads={onRetryThreads}
      />,
    )

    const failed = screen.getByTestId("chat-tree-threads-error-bob")
    // The status is the difference between "your history is gone" and "the
    // server was briefly unhappy".
    expect(failed).toHaveTextContent(/500/)

    // …and the count next to the agent must not read as a confident zero.
    expect(agentRow("bob").textContent).not.toMatch(/\b0\b/)

    fireEvent.click(within(failed).getByRole("button", { name: /retry/i }))
    expect(onRetryThreads).toHaveBeenCalled()
  })

  it("selecting a thread reports the thread and its agent", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))
    fireEvent.click(screen.getByTestId("chat-tree-thread-t-2"))

    expect(BASE.onOpenThread).toHaveBeenCalledWith(expect.objectContaining({ slug: "ada" }), "t-2")
  })

  it("opens the active agent without being clicked", () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="bob" activeThreadId="t-3" />)

    // Arriving at /chat/bob?session=t-3 must show where you are, not an
    // unexpanded row you have to find and open.
    expect(screen.getByTestId("chat-tree-thread-t-3")).toBeInTheDocument()
  })
})

describe("<ChatTreeSidebar> — kit chrome", () => {
  afterEach(() => cleanup())

  it("carries counts on its section headers", () => {
    render(<ChatTreeSidebar {...BASE} />)

    // STATUS counts the threads it filters; AGENTS counts the agents it lists.
    expect(screen.getByRole("button", { name: /^status/i }).textContent).toContain("3")
    expect(screen.getByRole("button", { name: /^agents/i }).textContent).toContain("2")
  })

  it("selects with the tokenised accent bar, never a hardcoded blue", () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" activeThreadId="t-1" />)

    const selected = screen.getByTestId("chat-tree-thread-t-1")
    expect(selected.className).toContain("row-selected")
    expect(selected.className).not.toMatch(/bg-blue-|border-blue-/)
    expect(screen.getByTestId("chat-tree-thread-t-2").className).not.toContain("row-selected")
  })

  it("indents a thread one step under its agent", () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" />)

    // ml-6 = 24px, the kit's one indent step. It used to be ml-12 because a
    // thread hung off a Sessions folder; with the folder gone, so is the level.
    expect(screen.getByTestId("chat-tree-thread-t-1").className).toContain("ml-6")
  })

  it("collapses to the kit's 44px rail and back", () => {
    const onToggleCollapsed = vi.fn()
    const { rerender } = render(<ChatTreeSidebar {...BASE} onToggleCollapsed={onToggleCollapsed} />)

    fireEvent.click(screen.getByRole("button", { name: /collapse sidebar/i }))
    expect(onToggleCollapsed).toHaveBeenCalled()

    rerender(<ChatTreeSidebar {...BASE} collapsed onToggleCollapsed={onToggleCollapsed} />)
    const rail = screen.getByTestId("chat-tree-sidebar")
    expect(rail.className).toContain("w-11")
    expect(screen.queryByTestId("chat-tree-agent-ada")).toBeNull()
    expect(screen.getByRole("button", { name: /expand sidebar/i })).toBeInTheDocument()
  })

  it("searches across agents and threads", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: "rebuild" } })

    // Only the agent that owns a matching thread survives.
    expect(screen.queryByTestId("chat-tree-agent-ada")).toBeNull()
    expect(screen.getByTestId("chat-tree-agent-bob")).toBeInTheDocument()
  })
})

describe("<ChatTreeSidebar> — status facets are counted from real data", () => {
  afterEach(() => cleanup())

  const running = agent("cleo", "Cleo", { status: "RUNNING" })
  const props = {
    ...BASE,
    agents: [ada, running],
    threadsByAgent: {
      "agent-ada": [
        thread("t-1", "Ship the export", 5, { unread_count: 2 }),
        thread("t-2", "Closed out", 30, { ended_at: new Date().toISOString() }),
      ],
      "agent-cleo": [thread("t-9", "Indexing", 10)],
    } as Record<string, ChatTreeThread[]>,
  }

  function statusRow(id: string): HTMLElement {
    return screen.getByTestId(`chat-tree-status-${id}`)
  }

  it("counts All / Unread / Running / Done from the loaded threads", () => {
    render(<ChatTreeSidebar {...props} />)

    expect(within(statusRow("all")).getByText("3")).toBeInTheDocument()
    expect(within(statusRow("unread")).getByText("1")).toBeInTheDocument()
    // Running is the agent's own status (agents.status = RUNNING) — the only
    // live signal either endpoint carries.
    expect(within(statusRow("running")).getByText("1")).toBeInTheDocument()
    // ended_at is a real column; nothing writes it today, so a real zero is
    // exactly what this must be able to show.
    expect(within(statusRow("done")).getByText("1")).toBeInTheDocument()
  })

  it("has no facet it cannot count — 'Needs you' has no source and is not invented", () => {
    render(<ChatTreeSidebar {...props} />)

    expect(screen.queryByTestId("chat-tree-status-needs-you")).toBeNull()
    expect(screen.queryByText(/needs you/i)).toBeNull()
  })

  // In the focused view the list below these numbers can only ever hold ONE
  // agent's threads, so a workspace-wide count above it would be a number the
  // view cannot act on. It counts what it can show — and says whose it is,
  // because a number that changes meaning between two views without saying so
  // is worse than either meaning.
  it("counts the facets over the agent in focus, and names the scope", () => {
    render(<ChatTreeSidebar {...props} activeAgentSlug="ada" onShowAllAgents={vi.fn()} />)

    // Ada has two of the workspace's three threads; one of them is unread and
    // one is ended, and she is not the RUNNING agent.
    expect(within(statusRow("all")).getByText("2")).toBeInTheDocument()
    expect(within(statusRow("unread")).getByText("1")).toBeInTheDocument()
    expect(within(statusRow("running")).getByText("0")).toBeInTheDocument()
    expect(within(statusRow("done")).getByText("1")).toBeInTheDocument()

    expect(screen.getByRole("button", { name: /^status/i }).textContent).toContain("Ada Lovelace")
  })

  it("counts the whole workspace on /chat, and says nothing about an agent", () => {
    render(<ChatTreeSidebar {...props} />)

    expect(within(statusRow("all")).getByText("3")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^status/i }).textContent).not.toContain("Ada")
  })

  it("picking a facet narrows the tree to the agents it matches", () => {
    render(<ChatTreeSidebar {...props} />)

    fireEvent.click(statusRow("running"))

    expect(screen.getByTestId("chat-tree-agent-cleo")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-agent-ada")).toBeNull()
  })
})
