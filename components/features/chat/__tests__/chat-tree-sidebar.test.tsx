import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"

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
  // "Start a conversation", NOT the agent row — that is the filter, and the
  // filter reports nothing to anybody.
  onOpenAgent: vi.fn(),
}

function resetBase() {
  BASE.onOpenThread.mockReset()
  BASE.onOpenAgent.mockReset()
}

function agentRow(slug: string): HTMLElement {
  return screen.getByTestId(`chat-tree-agent-${slug}`)
}

/**
 * Rows leave under a spring, so "gone" is a thing that happens rather than a
 * thing that is true on the next line. Every assertion that an agent has been
 * filtered out waits for the exit to finish.
 */
async function expectGone(slug: string): Promise<void> {
  await waitFor(() => expect(screen.queryByTestId(`chat-tree-agent-${slug}`)).toBeNull())
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

  it("has no Files, Asks or Memory row — those are the right rail's, the config tab's and the canvas's", async () => {
    render(<ChatTreeSidebar {...BASE} />)

    // Opened from the keyboard, so the filter stays out of it and both
    // branches are on screen at once.
    fireEvent.keyDown(agentRow("ada"), { key: "ArrowRight" })
    fireEvent.keyDown(agentRow("bob"), { key: "ArrowRight" })

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

  // The defect, and the reason the row cannot simply go back to being a
  // disclosure: `onSelect={onToggle}` meant an agent with no threads had
  // `canExpand === false` and the row did nothing at all. Someone clicked
  // "Alex" and the product did not move — on the one surface that is supposed
  // to make starting a conversation easy.
  //
  // It was answered by making the row navigate, which is what the owner then
  // rejected. It is answered here by a row of its own, under the agent the
  // reader has narrowed to.
  it("offers a row to start one with an agent that has none, and reports only when it is used", async () => {
    const zed = agent("zed", "Zed Silent")
    render(
      <ChatTreeSidebar
        {...BASE}
        agents={[ada, zed]}
        threadsByAgent={{ ...BASE.threadsByAgent, "agent-zed": [] }}
      />,
    )

    // Not offered in the wide view: six threadless agents would grow six of
    // these, under a header that already has a New session button.
    expect(screen.queryByTestId("chat-tree-start-zed")).toBeNull()

    fireEvent.click(agentRow("zed"))
    await expectGone("ada")

    // Narrowing to an agent is not asking to talk to it.
    expect(BASE.onOpenAgent).not.toHaveBeenCalled()

    const start = screen.getByTestId("chat-tree-start-zed")
    expect(start).toHaveAccessibleName(/start a conversation with zed silent/i)
    // Keyboard-reachable, and it answers the same keys every row here does.
    expect(start).toHaveAttribute("tabindex", "0")
    fireEvent.keyDown(start, { key: "Enter" })
    expect(BASE.onOpenAgent).toHaveBeenCalledWith(expect.objectContaining({ slug: "zed" }))

    // …and the tree still does not pretend it has a list to show.
    expect(threadRows()).toHaveLength(0)
  })

  it("clicking an agent hides the others and unfolds it; clicking it again brings them back", async () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))
    await expectGone("bob")
    // Narrowing to an agent in order to show it closed would be a filter that
    // hides what it was asked to reveal.
    expect(threadRows()).toHaveLength(2)
    // It never asks anyone to open anything, and it never leaves the page.
    expect(BASE.onOpenAgent).not.toHaveBeenCalled()

    fireEvent.click(agentRow("ada"))
    await waitFor(() => expect(agentRow("bob")).toBeInTheDocument())
    expect(BASE.onOpenAgent).not.toHaveBeenCalled()

    // Closing the branch is still the chevron's job, and closing is not
    // filtering — the column must not widen back out because a branch shut.
    fireEvent.click(agentRow("ada"))
    await expectGone("bob")
    fireEvent.click(screen.getByTestId("chat-tree-expander-ada"))
    expect(threadRows()).toHaveLength(0)
    expect(screen.queryByTestId("chat-tree-agent-bob")).toBeNull()
  })

  it("clears the filter from the section header too — one click, and it is a click, not a link", async () => {
    render(<ChatTreeSidebar {...BASE} />)

    expect(screen.queryByTestId("chat-tree-clear-filter")).toBeNull()

    fireEvent.click(agentRow("ada"))
    await expectGone("bob")

    // This is NOT the "All agents" row it replaces: that one was a
    // router.push("/chat"), and it is gone with the navigation.
    const clear = screen.getByTestId("chat-tree-clear-filter")
    expect(clear.tagName).toBe("BUTTON")
    expect(clear).toHaveTextContent("2")
    fireEvent.click(clear)
    await waitFor(() => expect(agentRow("bob")).toBeInTheDocument())
  })

  it("expands and collapses from the keyboard, without opening anything", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.keyDown(agentRow("ada"), { key: "ArrowRight" })
    expect(threadRows()).toHaveLength(2)

    fireEvent.keyDown(agentRow("ada"), { key: "ArrowLeft" })
    expect(threadRows()).toHaveLength(0)
    expect(BASE.onOpenAgent).not.toHaveBeenCalled()
  })

  // The route USED to narrow this column: /chat listed everyone, /chat/<slug>
  // listed that one agent. It cost no state and it cost a page transition per
  // pick — every agent you looked at tore down and rebuilt the dashboard
  // chrome. Naming an agent in the URL now selects it and opens it, and
  // narrows nothing.
  it("lists every agent on /chat/<slug> — the route selects, it does not narrow", () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" />)

    expect(agentRow("ada")).toBeInTheDocument()
    expect(agentRow("bob")).toBeInTheDocument()
    // Selected means open: arriving somewhere and being shown a closed row is
    // the tree hiding where you are.
    expect(screen.getByTestId("chat-tree-thread-t-1")).toBeInTheDocument()
    // Nothing narrows it, so there is nothing to undo.
    expect(screen.queryByTestId("chat-tree-all-agents")).toBeNull()
    expect(screen.queryByTestId("chat-tree-clear-filter")).toBeNull()
  })

  it("lets a search reach past the filter — a search that cannot see the other six is a lie", async () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" />)

    fireEvent.click(agentRow("ada"))
    await expectGone("bob")

    fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: "bob" } })
    await waitFor(() => expect(agentRow("bob")).toBeInTheDocument())

    // Suspended, not cleared — clearing the box returns to the one agent the
    // reader had narrowed to, rather than silently widening the column.
    fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: "" } })
    await expectGone("bob")
    expect(agentRow("ada")).toBeInTheDocument()
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

  it("searches across agents and threads", async () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.change(screen.getByRole("textbox", { name: /search/i }), { target: { value: "rebuild" } })

    // Only the agent that owns a matching thread survives — once it has
    // finished animating out, which every row here now does.
    await waitFor(() => expect(screen.queryByTestId("chat-tree-agent-ada")).toBeNull())
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

  // While the filter is on, the list below these numbers can only ever hold
  // ONE agent's threads, so a workspace-wide count above it would be a number
  // the view cannot act on. It counts what it can show — and says whose it is,
  // because a number that changes meaning between two views without saying so
  // is worse than either meaning.
  //
  // The scope used to be the ROUTE's agent. It is the filter's now, which is
  // the only thing that narrows this column: `activeAgentSlug` alone must
  // leave the counts workspace-wide, because the list beneath them is too.
  it("counts the facets over the agent the FILTER is on, and names the scope", async () => {
    render(<ChatTreeSidebar {...props} activeAgentSlug="ada" />)

    // Named in the URL, nothing narrowed: three threads, and no name on the
    // header claiming otherwise.
    expect(within(statusRow("all")).getByText("3")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^status/i }).textContent).not.toContain("Ada Lovelace")

    fireEvent.click(screen.getByTestId("chat-tree-agent-ada"))
    await waitFor(() => expect(screen.queryByTestId("chat-tree-agent-cleo")).toBeNull())

    // Ada has two of the workspace's three threads; one of them is unread and
    // one is ended, and she is not the RUNNING agent.
    expect(within(statusRow("all")).getByText("2")).toBeInTheDocument()
    expect(within(statusRow("unread")).getByText("1")).toBeInTheDocument()
    expect(within(statusRow("running")).getByText("0")).toBeInTheDocument()
    expect(within(statusRow("done")).getByText("1")).toBeInTheDocument()

    expect(screen.getByRole("button", { name: /^status/i }).textContent).toContain("Ada Lovelace")
  })

  it("counts the whole workspace while nothing is filtered, and says nothing about an agent", () => {
    render(<ChatTreeSidebar {...props} />)

    expect(within(statusRow("all")).getByText("3")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^status/i }).textContent).not.toContain("Ada")
  })

  it("picking a facet narrows the tree to the agents it matches", async () => {
    render(<ChatTreeSidebar {...props} />)

    fireEvent.click(statusRow("running"))

    expect(screen.getByTestId("chat-tree-agent-cleo")).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByTestId("chat-tree-agent-ada")).toBeNull())
  })
})
