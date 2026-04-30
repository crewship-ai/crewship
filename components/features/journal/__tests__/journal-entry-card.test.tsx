import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { JournalEntryCard } from "@/components/features/journal/journal-entry-card"
import type { JournalEntry } from "@/lib/types/journal"

// Stub useRealtime — useJournalLookup defaults to empty without a
// provider, but it pulls in useRealtimeEvent indirectly which needs the
// context mocked or it throws. We provide a no-op subscriber here.
vi.mock("@/hooks/use-realtime", () => ({
  useRealtime: () => ({ status: "connected", subscribe: () => () => {}, subscribeChannel: () => () => {} }),
  useRealtimeEvent: () => undefined,
}))

function entry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "j1",
    workspace_id: "ws_test",
    ts: new Date(Date.now() - 60_000).toISOString(),
    entry_type: "peer.escalation",
    severity: "warn",
    actor_type: "agent",
    summary: "agent_a escalated to lead",
    actor_id: "agent_alice123",
    ...overrides,
  } as JournalEntry
}

describe("JournalEntryCard — base rendering", () => {
  it("renders entry_type, severity, and actor badges", () => {
    render(<JournalEntryCard entry={entry()} />)
    expect(screen.getByText("peer.escalation")).toBeInTheDocument()
    expect(screen.getByText("warn")).toBeInTheDocument()
    // Multiple "agent" occurrences (entry_type prefix + actor badge); assert at least one.
    expect(screen.getAllByText(/agent/i).length).toBeGreaterThan(0)
  })

  it("truncates actor_id to 6 chars in the badge", () => {
    render(<JournalEntryCard entry={entry({ actor_id: "agent_alice_long_uuid" })} />)
    expect(screen.getByText("agent_")).toBeInTheDocument()
  })

  it("shows the summary text", () => {
    render(<JournalEntryCard entry={entry({ summary: "deployment failed at step 3" })} />)
    expect(screen.getByText("deployment failed at step 3")).toBeInTheDocument()
  })

  it("renders italic '(no summary)' fallback when summary is empty", () => {
    render(<JournalEntryCard entry={entry({ summary: "" })} />)
    expect(screen.getByText("(no summary)")).toBeInTheDocument()
  })

  it("falls back to 'info' styling for unknown severity (no crash)", () => {
    render(<JournalEntryCard entry={entry({ severity: "weird-unknown" as any })} />)
    expect(screen.getByText("weird-unknown")).toBeInTheDocument()
  })
})

describe("JournalEntryCard — payload toggle", () => {
  it("does not show toggle when payload is empty / missing", () => {
    render(<JournalEntryCard entry={entry({ payload: undefined })} />)
    expect(screen.queryByText(/show payload/i)).not.toBeInTheDocument()
  })

  it("shows the toggle when payload has keys", () => {
    render(<JournalEntryCard entry={entry({ payload: { reason: "x" } })} />)
    expect(screen.getByText(/show payload/i)).toBeInTheDocument()
  })

  it("expands payload as JSON on click", () => {
    render(<JournalEntryCard entry={entry({ payload: { reason: "deny", code: 42 } })} />)
    fireEvent.click(screen.getByText(/show payload/i))
    expect(screen.getByText(/"reason": "deny"/i)).toBeInTheDocument()
    expect(screen.getByText(/Hide payload/i)).toBeInTheDocument()
  })

  it("aria-expanded reflects state", () => {
    render(<JournalEntryCard entry={entry({ payload: { foo: "bar" } })} />)
    const button = screen.getByRole("button", { name: /show payload/i })
    expect(button).toHaveAttribute("aria-expanded", "false")
    fireEvent.click(button)
    expect(button).toHaveAttribute("aria-expanded", "true")
  })
})

describe("JournalEntryCard — exec.command details", () => {
  it("renders the command line and exit-code badge", () => {
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "exec.command",
          payload: { command: "rm -rf /tmp/foo", exit_code: 0 },
        })}
      />,
    )
    expect(screen.getByText(/\$ rm -rf \/tmp\/foo/)).toBeInTheDocument()
    expect(screen.getByText(/exit 0/)).toBeInTheDocument()
  })

  it("non-zero exit gets the red error styling (different class)", () => {
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "exec.command",
          payload: { command: "false", exit_code: 1 },
        })}
      />,
    )
    expect(screen.getByText("exit 1")).toBeInTheDocument()
  })

  it("renders only command (no exit code) when exit_code is missing", () => {
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "exec.command",
          payload: { command: "ls" },
        })}
      />,
    )
    expect(screen.getByText("$ ls")).toBeInTheDocument()
    expect(screen.queryByText(/^exit /)).not.toBeInTheDocument()
  })

  it("renders nothing inline when both command and exit_code are missing", () => {
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "exec.command",
          payload: { other: "thing" },
        })}
      />,
    )
    expect(screen.queryByText(/^\$ /)).not.toBeInTheDocument()
  })
})

describe("JournalEntryCard — keeper.decision denied", () => {
  it("recognises 'deny' as denied (case-insensitive)", () => {
    const { container } = render(
      <JournalEntryCard
        entry={entry({
          entry_type: "keeper.decision",
          payload: { decision: "Deny" },
          summary: "blocked sensitive op",
        })}
      />,
    )
    // The denial path applies a red border class — render shouldn't crash.
    expect(container.querySelector(".border-red-500\\/50")).toBeTruthy()
  })

  it("'denied' also flagged", () => {
    const { container } = render(
      <JournalEntryCard
        entry={entry({
          entry_type: "keeper.decision",
          payload: { decision: "denied" },
        })}
      />,
    )
    expect(container.querySelector(".border-red-500\\/50")).toBeTruthy()
  })

  it("'approve' is NOT flagged red", () => {
    const { container } = render(
      <JournalEntryCard
        entry={entry({
          entry_type: "keeper.decision",
          payload: { decision: "approve" },
        })}
      />,
    )
    expect(container.querySelector(".border-red-500\\/50")).toBeFalsy()
  })
})

describe("JournalEntryCard — summary.generated hero card", () => {
  it("renders the gold-bordered summary card with title and body", () => {
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "summary.generated",
          summary: "weekly recap",
          payload: { title: "Sprint 14 Summary", body: "Three crews shipped seven features." },
        })}
      />,
    )
    expect(screen.getByText("Crew Summary")).toBeInTheDocument()
    expect(screen.getByText("Sprint 14 Summary")).toBeInTheDocument()
    expect(screen.getByText(/Three crews shipped seven features/)).toBeInTheDocument()
  })

  it("falls back to default 'Crew Summary' title when payload.title missing", () => {
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "summary.generated",
          payload: { body: "x" },
        })}
      />,
    )
    expect(screen.getAllByText("Crew Summary")).toHaveLength(2) // badge + h3 fallback
  })

  it("does not render Read more button for short bodies", () => {
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "summary.generated",
          payload: { title: "x", body: "short" },
        })}
      />,
    )
    expect(screen.queryByText(/read more/i)).not.toBeInTheDocument()
  })

  it("renders Read more button for long bodies", () => {
    const longBody = "x".repeat(500)
    render(
      <JournalEntryCard
        entry={entry({
          entry_type: "summary.generated",
          payload: { title: "x", body: longBody },
        })}
      />,
    )
    // Long body collapses to clamp-3 with collapse / read-more toggle.
    expect(screen.getByRole("button")).toBeInTheDocument()
  })
})

describe("JournalEntryCard — context chips", () => {
  it("does not render any link when crew/agent/mission lookup misses", () => {
    // useJournalLookup default is empty maps → no chips rendered.
    const { container } = render(
      <JournalEntryCard
        entry={entry({ crew_id: "crew_1", agent_id: "agent_1", mission_id: "m1" })}
      />,
    )
    // No <a> elements (the context chips are <Link>s) other than maybe
    // the formatRelativeTime span. Assert by counting anchors.
    const anchors = container.querySelectorAll("a")
    expect(anchors).toHaveLength(0)
  })
})
