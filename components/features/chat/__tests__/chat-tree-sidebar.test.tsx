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
// The tree part is what the flat list could not express: an agent is a thing
// with Sessions, Files, Asks and Memory, not a list of threads.
// =============================================================================

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="agent-avatar" data-seed={seed} />,
}))

import {
  ChatTreeSidebar,
  CHAT_FOLDERS,
  type ChatTreeAgent,
  type ChatTreeThread,
} from "../chat-tree-sidebar"

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

function thread(id: string, title: string | null, extra: Partial<ChatTreeThread> = {}): ChatTreeThread {
  const iso = new Date().toISOString()
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
    "agent-ada": [thread("t-1", "Ship the export"), thread("t-2", "Weekly summary")],
    "agent-bob": [thread("t-3", "Rebuild the index")],
  } as Record<string, ChatTreeThread[]>,
  onOpenThread: vi.fn(),
  onOpenFolder: vi.fn(),
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

describe("<ChatTreeSidebar> — the tree", () => {
  beforeEach(() => {
    BASE.onOpenThread.mockReset()
    BASE.onOpenFolder.mockReset()
  })
  afterEach(() => cleanup())

  it("renders one row per agent, and nothing beneath them until one is expanded", () => {
    render(<ChatTreeSidebar {...BASE} />)

    expect(agentRow("ada")).toBeInTheDocument()
    expect(agentRow("bob")).toBeInTheDocument()
    // A tree that is open everywhere is a list again.
    expect(folderRows()).toHaveLength(0)
    expect(threadRows()).toHaveLength(0)
  })

  it("expanding an agent reveals exactly Sessions / Files / Asks / Memory", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))

    const labels = folderRows().map((r) => r.textContent?.trim() ?? "")
    expect(labels).toHaveLength(4)
    for (const folder of CHAT_FOLDERS) {
      expect(labels.some((l) => l.startsWith(folder.label))).toBe(true)
    }
    // …and only for the agent that was opened.
    expect(screen.getByTestId("chat-tree-folder-ada-sessions")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-folder-bob-sessions")).toBeNull()
  })

  it("expanding Sessions lists that agent's threads — and only that agent's", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))
    fireEvent.click(screen.getByTestId("chat-tree-folder-ada-sessions"))

    const titles = threadRows().map((r) => r.textContent ?? "")
    expect(titles).toHaveLength(2)
    expect(titles.join(" ")).toContain("Ship the export")
    expect(titles.join(" ")).toContain("Weekly summary")
    // Bob's thread belongs under Bob.
    expect(titles.join(" ")).not.toContain("Rebuild the index")
  })

  it("selecting a thread reports the thread and its agent", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))
    fireEvent.click(screen.getByTestId("chat-tree-folder-ada-sessions"))
    fireEvent.click(screen.getByTestId("chat-tree-thread-t-2"))

    expect(BASE.onOpenThread).toHaveBeenCalledWith(expect.objectContaining({ slug: "ada" }), "t-2")
  })

  it("selecting a folder reports the folder — that is what swaps the centre pane", () => {
    render(<ChatTreeSidebar {...BASE} />)

    fireEvent.click(agentRow("ada"))
    fireEvent.click(screen.getByTestId("chat-tree-folder-ada-files"))

    expect(BASE.onOpenFolder).toHaveBeenCalledWith(expect.objectContaining({ slug: "ada" }), "files")
  })

  it("opens the active agent (and its sessions) without being clicked", () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="bob" activeFolder="sessions" activeThreadId="t-3" />)

    // Arriving at /chat/bob?session=t-3 must show where you are, not an
    // unexpanded row you have to find and open.
    expect(screen.getByTestId("chat-tree-folder-bob-sessions")).toBeInTheDocument()
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
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" activeFolder="sessions" activeThreadId="t-1" />)

    const selected = screen.getByTestId("chat-tree-thread-t-1")
    expect(selected.className).toContain("row-selected")
    expect(selected.className).not.toMatch(/bg-blue-|border-blue-/)
    expect(screen.getByTestId("chat-tree-thread-t-2").className).not.toContain("row-selected")
  })

  it("indents a nested row 24px and a leaf 48px", () => {
    render(<ChatTreeSidebar {...BASE} activeAgentSlug="ada" activeFolder="sessions" />)

    // ml-6 = 24px (the kit's `indent`), ml-12 = 48px.
    expect(screen.getByTestId("chat-tree-folder-ada-sessions").className).toContain("ml-6")
    expect(screen.getByTestId("chat-tree-thread-t-1").className).toContain("ml-12")
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
        thread("t-1", "Ship the export", { unread_count: 2 }),
        thread("t-2", "Closed out", { ended_at: new Date().toISOString() }),
      ],
      "agent-cleo": [thread("t-9", "Indexing")],
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

  it("picking a facet narrows the tree to the agents it matches", () => {
    render(<ChatTreeSidebar {...props} />)

    fireEvent.click(statusRow("running"))

    expect(screen.getByTestId("chat-tree-agent-cleo")).toBeInTheDocument()
    expect(screen.queryByTestId("chat-tree-agent-ada")).toBeNull()
  })
})
