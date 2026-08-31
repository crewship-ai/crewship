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
      scope="direct"
      onScopeChange={() => {}}
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
      threadsByAgent: { "a-morgan": [morganThread], "a-riley": [] },
      threadErrors: {},
    })
    expect(screen.getByText("Not started yet")).toBeInTheDocument()
  })
})

describe("<ConversationsSidebar> — 'Not started yet' means we asked and got nothing", () => {
  // Three ways an agent can have no rows without having no history. All three
  // end the same way if the column guesses: the row is offered as a fresh
  // start and clicking it writes a second conversation over the first.

  it("files nobody under it while the fan-out is still in flight", () => {
    renderSidebar({ threadsByAgent: {}, threadsLoaded: false })
    expect(screen.getByText("Loading…")).toBeInTheDocument()
    expect(screen.queryByText("Not started yet")).not.toBeInTheDocument()
  })

  it("does not file an agent the fan-out never asked about", () => {
    // Past AGENT_FANOUT_CAP: no list and no error, and unlike the two other
    // absences this one never resolves on its own.
    renderSidebar({ threadsByAgent: { "a-morgan": [morganThread] }, threadErrors: {} })
    expect(screen.queryByText("Not started yet")).not.toBeInTheDocument()
  })

  it("files an agent whose list came back empty", () => {
    renderSidebar({ threadsByAgent: { "a-morgan": [morganThread], "a-riley": [] } })
    expect(screen.getByText("Not started yet")).toBeInTheDocument()
  })
})

describe("<ConversationsSidebar> — the primary bucket asks which KIND", () => {
  // The strip used to be All · Unread · Live: a scope and two predicates in
  // one exclusive control, two of them permanently reading 0. /issues and
  // /routines had already settled the shape — one bucket section carrying the
  // question the page is actually sorted by, everything else in the Filter
  // popover — and this column was the one that had not adopted it.

  it("offers Direct, Routines and Issues as the bucket section", () => {
    renderSidebar({ threadsByAgent: { "a-morgan": [morganThread] } })

    expect(screen.getByText("Show")).toBeInTheDocument()
    for (const label of ["Direct", "Routines", "Issues"]) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
  })

  it("puts a count on every bucket when the server reported the totals", () => {
    // The reason `X-Chat-Kind-Counts` exists. The fetch is scoped, so the
    // response holds one kind and the other buckets cannot be counted from
    // it — and a bucket row without a number is the one thing that would make
    // this column read differently from /routines' STATUS section.
    renderSidebar({
      threadsByAgent: { "a-morgan": [morganThread] },
      kindCounts: { direct: 1, routine: 182, issue: 0, agent: 3 },
    })

    // Routines carries routine + agent — delegation rides in that bucket.
    expect(screen.getByText("185")).toBeInTheDocument()
    expect(screen.getByText("0")).toBeInTheDocument()
  })

  it("invents no number when the server did not report totals", () => {
    // An older server, or a proxy that dropped the header. The selected
    // bucket falls back to what it can actually see; the others say nothing,
    // because nothing is what is known.
    renderSidebar({ threadsByAgent: { "a-morgan": [morganThread] }, kindCounts: null })

    const routines = screen.getByText("Routines").closest("li")
    expect(routines?.textContent).toBe("Routines")
  })
})

describe("<ConversationsSidebar> — state narrowing lives in the Filter popover", () => {
  it("counts unread CONVERSATIONS, not unread messages", () => {
    const noisy = { ...morganThread, id: "morgan-1", unread_count: 7 }
    const quieter = { ...morganThread, id: "morgan-2", unread_count: 2 }
    renderSidebar({ threadsByAgent: { "a-morgan": [noisy, quieter] } })

    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    const unread = screen.getByRole("button", { name: /unread only/i })
    // Summing message counts put "Unread 9" over a two-row list, so the
    // number meant something different from every other number on the strip.
    expect(unread.textContent).toContain("2")
    expect(unread.textContent).not.toContain("9")
  })

  it("badges the trigger so a facet is never hidden by being one click away", () => {
    // The one real cost of moving these off the surface. The badge is what
    // pays it — /issues and /routines both rely on it for the same reason.
    renderSidebar({ threadsByAgent: { "a-morgan": [{ ...morganThread, unread_count: 1 }] } })

    const trigger = screen.getByRole("button", { name: /filter/i })
    expect(trigger.textContent).not.toContain("1")

    fireEvent.click(trigger)
    fireEvent.click(screen.getByRole("button", { name: /unread only/i }))
    expect(screen.getByRole("button", { name: /filter/i }).textContent).toContain("1")
  })

  it("composes with the bucket instead of replacing it", () => {
    // The fatal flaw of the exclusive strip: "unread routines" was a real
    // question it could not express, because choosing Unread threw the scope
    // away. Here the scope is a fetch parameter and the facet is a predicate
    // over what came back, so they cannot collide.
    const onScopeChange = vi.fn()
    renderSidebar({
      threadsByAgent: { "a-morgan": [{ ...morganThread, unread_count: 1 }] },
      scope: "routine",
      onScopeChange,
    })

    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    fireEvent.click(screen.getByRole("button", { name: /unread only/i }))

    expect(onScopeChange).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: /filter/i }).textContent).toContain("1")
  })
})

describe("<ConversationsSidebar> — the row does not stretch its children", () => {
  // `.row-interactive` is `display:flex` with no `align-items`, so children
  // stretch to the row's height. Every other sidebar in the app has one-line
  // rows, where stretch and centre are indistinguishable; this is the first
  // two-line row and it stretched the unread count into a tall capsule and
  // dropped the live dot to the bottom of the row instead of the portrait.
  //
  // jsdom has no layout engine, so this cannot assert the pixels — it pins the
  // two declarations that produce them, which is what a later edit would
  // remove. The pixels were checked in a browser.

  it("centres the row's children rather than letting them stretch", () => {
    renderSidebar({ threadsByAgent: { "a-morgan": [{ ...morganThread, unread_count: 3 }] } })

    const row = screen.getByText("Morgan's thread").closest("li")
    expect(row?.className).toContain("items-center")
  })

  it("gives the unread count a square floor, so one digit is a circle", () => {
    // Same geometry as the bell badge in bar-menu: a 16px box with a 16px
    // minimum width. Only a two- or three-digit count grows into a capsule.
    // 7, not 3: the Show section's own count is 3 (one per scope), and a
    // fixture that collides with the chrome tests the query, not the code.
    renderSidebar({ threadsByAgent: { "a-morgan": [{ ...morganThread, unread_count: 7 }] } })

    const badge = screen.getByText("7")
    expect(badge.className).toContain("h-4")
    expect(badge.className).toContain("min-w-[16px]")
    expect(badge.className).toContain("rounded-full")
  })
})

describe("<ConversationsSidebar> — the collapse control keeps its name", () => {
  // `SidebarCollapseButton` sets its own aria-label and then spreads
  // `...props` over it. So forwarding an OPTIONAL override straight through
  // does not fall back when it is undefined — React removes the attribute and
  // the control goes nameless. It renders identically, so nothing catches it
  // but a query by name.

  it("names itself when the caller passes no override", () => {
    renderSidebar({ onToggleCollapse: () => {} })
    expect(screen.getByRole("button", { name: /collapse sidebar/i })).toBeInTheDocument()
  })

  it("takes the caller's name when there is one", () => {
    // On a phone the column IS the drawer, so "Collapse sidebar" is the wrong
    // description of what the button does.
    renderSidebar({ onToggleCollapse: () => {}, collapseLabel: "Close conversations" })
    expect(screen.getByRole("button", { name: "Close conversations" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /collapse sidebar/i })).toBeNull()
  })

  it("offers no collapse control where the layout cannot fold", () => {
    renderSidebar({})
    expect(screen.queryByRole("button", { name: /collapse|close conversations/i })).toBeNull()
  })
})

describe("<ConversationsSidebar> — starting a conversation asks who with", () => {
  it("opens a picker instead of choosing the first agent in the roster", () => {
    // `onStartConversation(roster[0])` made the one button on the column
    // silently pick the agent — and choosing the agent IS the decision.
    const onStartConversation = vi.fn()
    renderSidebar({ threadsByAgent: { "a-morgan": [morganThread] }, onStartConversation })

    fireEvent.click(screen.getByRole("button", { name: /new conversation/i }))
    expect(onStartConversation).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText("Riley"))
    expect(onStartConversation).toHaveBeenCalledWith(riley)
  })

  it("offers every agent, not only the ones with no history", () => {
    // A second conversation with somebody you already talk to is the common
    // case, and the roster section this replaced listed only idle agents.
    renderSidebar({ threadsByAgent: { "a-morgan": [morganThread], "a-riley": [] } })

    fireEvent.click(screen.getByRole("button", { name: /new conversation/i }))
    expect(screen.getByText("Morgan")).toBeInTheDocument()
    expect(screen.getByText("Riley")).toBeInTheDocument()
  })
})
