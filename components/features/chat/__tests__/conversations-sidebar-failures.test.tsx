import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

// =============================================================================
// A failed request must not render as an empty history.
//
// `useChatTreeData` goes to real trouble to keep the two apart — `error` for
// the roster, `threadErrors` for one agent's list — precisely because the
// version before it wrote `.then((r) => (r.ok ? r.json() : []))` and turned a
// 500 into "you have no conversations". The rewrite reintroduced the defect a
// layer up by not passing either value into the column.
//
// Three claims, and each one is a sentence the UI has no standing to say when
// the request failed:
//   * "No conversations yet."          — the roster never answered.
//   * "Not started yet"                — that agent's list never answered.
//   * a new draft opened on /chat/<slug> — written on top of a real history.
// =============================================================================

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ alt }: { alt?: string }) => <span data-testid="avatar" aria-label={alt} />,
}))

import { ConversationsSidebar } from "../conversations-sidebar"
import type { ChatTreeAgent, ChatTreeThread } from "../chat-tree-data"

const riley: ChatTreeAgent = {
  id: "a-riley",
  name: "Riley",
  slug: "riley",
  status: "IDLE",
} as ChatTreeAgent
const morgan: ChatTreeAgent = {
  id: "a-morgan",
  name: "Morgan",
  slug: "morgan",
  status: "IDLE",
} as ChatTreeAgent

const morganThread: ChatTreeThread = {
  id: "morgan-1",
  title: "Morgan's thread",
  started_at: "2026-08-21T10:00:00Z",
  last_activity_at: "2026-08-21T10:00:00Z",
} as ChatTreeThread

function renderSidebar(props: Partial<Parameters<typeof ConversationsSidebar>[0]> = {}) {
  return render(
    <ConversationsSidebar
      agents={[riley, morgan]}
      threadsByAgent={{}}
      threadsLoaded
      onSelectThread={() => {}}
      onStartConversation={() => {}}
      {...props}
    />,
  )
}

describe("<ConversationsSidebar> — a failed roster is not an empty one", () => {
  it("says the roster failed instead of 'No conversations yet.'", () => {
    renderSidebar({ agents: [], loadError: "HTTP 500" })

    expect(screen.getByTestId("conversations-roster-failure")).toBeInTheDocument()
    expect(screen.getByText(/could not be loaded/i)).toBeInTheDocument()
    // The status is carried verbatim: without it every retry is a guess.
    expect(screen.getByText("HTTP 500")).toBeInTheDocument()
    expect(screen.queryByText("No conversations yet.")).not.toBeInTheDocument()
  })

  it("offers a retry that re-reads the roster", () => {
    const onRetryRoster = vi.fn()
    renderSidebar({ agents: [], loadError: "HTTP 503", onRetryRoster })

    fireEvent.click(screen.getByRole("button", { name: /retry/i }))
    expect(onRetryRoster).toHaveBeenCalledTimes(1)
  })

  // A non-empty roster alongside `loadError` is defensive rather than
  // currently reachable — the hook empties the roster when the request fails.
  // Asserted with a roster present so the assertion is about the error clause
  // and not about `roster.length === 0`, which would pass either way.
  it("does not offer 'New conversation' while the roster is in doubt", () => {
    renderSidebar({ loadError: "HTTP 500" })
    expect(screen.getByRole("button", { name: /new conversation/i })).toBeDisabled()
  })

  it("still offers it when the roster loaded and is merely empty", () => {
    renderSidebar()
    expect(screen.getByRole("button", { name: /new conversation/i })).toBeEnabled()
    expect(screen.getByText("No conversations yet.")).toBeInTheDocument()
  })
})

describe("<ConversationsSidebar> — an agent whose list failed is not idle", () => {
  it("names the agent whose fan-out failed and keeps it out of 'Not started yet'", () => {
    renderSidebar({
      threadsByAgent: { "a-morgan": [morganThread] },
      threadErrors: { "a-riley": "HTTP 502" },
    })

    expect(screen.getByTestId("conversations-fanout-failure")).toBeInTheDocument()
    expect(screen.getByText(/Riley's conversations could not be loaded/)).toBeInTheDocument()
    expect(screen.getByText("HTTP 502")).toBeInTheDocument()
    // Riley has no rows, but the reason is unknown — not "nobody has talked to
    // Riley". Filing it under idle is what makes the next click write a second
    // conversation on top of a history the page could not read.
    expect(screen.queryByText("Not started yet")).not.toBeInTheDocument()
  })

  it("counts them when more than one agent's list is missing", () => {
    renderSidebar({ threadErrors: { "a-riley": "HTTP 502", "a-morgan": "HTTP 502" } })
    expect(screen.getByText("2 agents' conversations could not be loaded")).toBeInTheDocument()
  })

  it("retries the fan-out, not the roster", () => {
    const onRetryThreads = vi.fn()
    const onRetryRoster = vi.fn()
    renderSidebar({
      threadErrors: { "a-riley": "HTTP 502" },
      onRetryThreads,
      onRetryRoster,
    })

    fireEvent.click(screen.getByRole("button", { name: /retry/i }))
    expect(onRetryThreads).toHaveBeenCalledTimes(1)
    expect(onRetryRoster).not.toHaveBeenCalled()
  })

  it("keeps agents that genuinely have no threads in 'Not started yet'", () => {
    renderSidebar({
      threadsByAgent: { "a-morgan": [morganThread] },
      threadErrors: {},
    })
    expect(screen.getByText("Not started yet")).toBeInTheDocument()
  })
})
