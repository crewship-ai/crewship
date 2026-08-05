import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import type { JournalEntry } from "@/lib/types/journal"

// Control the two data hooks the timeline composes. We drive `entries`
// directly so the test exercises humanize + render, not the fetch layer.
let mockEntries: JournalEntry[] = []
let mockLoading = false

vi.mock("@/hooks/use-journal-list", () => ({
  useJournalList: () => ({
    entries: mockEntries,
    loading: mockLoading,
    prependLive: () => {},
    nextCursor: null,
    loadingMore: false,
    error: null,
    refresh: async () => {},
    loadMore: async () => {},
  }),
}))

vi.mock("@/hooks/use-journal-stream", () => ({
  useJournalStream: () => ({ status: "connected", lastError: null }),
}))

import { RunActivityTimeline } from "@/components/features/activity/run-activity-timeline"

function entry(over: Partial<JournalEntry> & Pick<JournalEntry, "entry_type" | "ts">): JournalEntry {
  return {
    id: over.id ?? "j_" + over.entry_type + over.ts,
    workspace_id: "ws_1",
    severity: "info",
    actor_type: "agent",
    summary: "",
    ...over,
  } as JournalEntry
}

describe("RunActivityTimeline", () => {
  beforeEach(() => {
    mockEntries = []
    mockLoading = false
  })

  it("renders nothing without any filter", () => {
    const { container } = render(<RunActivityTimeline workspaceId="ws_1" params={{}} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("renders humanized rows oldest-first and a step count", () => {
    mockEntries = [
      entry({ entry_type: "run.completed", ts: "2026-06-26T10:31:09Z", payload: { cost_usd: 0.0021, steps: 3 } }),
      entry({ entry_type: "file.written", ts: "2026-06-26T10:31:08Z", payload: { path: "/tmp/x.txt", size: 412 } }),
      entry({ entry_type: "run.started", ts: "2026-06-26T10:31:02Z" }),
    ]
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} />)
    expect(screen.getByText("Run started")).toBeInTheDocument()
    expect(screen.getByText("Wrote file")).toBeInTheDocument()
    expect(screen.getByText("Completed")).toBeInTheDocument()
    expect(screen.getByText("3 steps")).toBeInTheDocument()
    // Completed cost meta is surfaced.
    expect(screen.getByText(/\$0\.0021/)).toBeInTheDocument()
  })

  it("shows the Running indicator when opened with no terminal entry", () => {
    mockEntries = [entry({ entry_type: "run.started", ts: "2026-06-26T10:31:02Z" })]
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} />)
    expect(screen.getByText("Running")).toBeInTheDocument()
  })

  it("does not show Running once a terminal entry arrives", () => {
    mockEntries = [
      entry({ entry_type: "run.started", ts: "2026-06-26T10:31:02Z" }),
      entry({ entry_type: "run.failed", ts: "2026-06-26T10:31:05Z", severity: "error", payload: { error: "boom" } }),
    ]
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} />)
    expect(screen.queryByText("Running")).not.toBeInTheDocument()
    expect(screen.getByText("Failed")).toBeInTheDocument()
    expect(screen.getByText("boom")).toBeInTheDocument()
  })

  it("renders an empty-state message when the run has no surfaced steps", () => {
    mockEntries = []
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} hideWhenEmpty={false} />)
    expect(screen.getByText(/No activity recorded/i)).toBeInTheDocument()
  })

  it("hides entirely when empty and hideWhenEmpty is on (default)", () => {
    mockEntries = []
    const { container } = render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe("RunActivityTimeline — card variant", () => {
  beforeEach(() => {
    mockEntries = []
    mockLoading = false
  })

  const twoSteps = () => [
    entry({ entry_type: "run.started", ts: "2026-06-26T10:31:02Z" }),
    entry({ entry_type: "file.written", ts: "2026-06-26T10:31:08Z", payload: { path: "/tmp/x.txt", size: 412 } }),
  ]

  it("wraps itself in the detail card chrome and hands the step count to the subtitle", () => {
    // On the issue detail this was the one section rendered bare — a heading
    // and rows sitting on the page background, next to a stack of cards.
    mockEntries = twoSteps()
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} card />)
    const root = screen.getByTestId("run-activity")
    expect(root).toHaveClass("rounded-xl", "border", "bg-card")
    expect(screen.getByText("Run activity")).toBeInTheDocument()
    expect(screen.getByText("2 steps")).toBeInTheDocument()
    // The card draws the header; the rail must not draw a second one.
    expect(root.querySelector(".border-t")).toBeNull()
  })

  it("pluralises the subtitle for a single step", () => {
    mockEntries = [entry({ entry_type: "run.started", ts: "2026-06-26T10:31:02Z" })]
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} card />)
    expect(screen.getByText("1 step")).toBeInTheDocument()
  })

  it("keeps the running indicator visible in card mode", () => {
    mockEntries = [entry({ entry_type: "run.started", ts: "2026-06-26T10:31:02Z" })]
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} card />)
    expect(screen.getByText("Running")).toBeInTheDocument()
  })

  it("leaves the bare variant alone — other screens still get the rail", () => {
    // The timeline is shared with the routine run panel and the activity bar;
    // the card is opt-in precisely so those call sites do not move.
    mockEntries = twoSteps()
    render(<RunActivityTimeline workspaceId="ws_1" params={{ trace_id: "trace_1" }} />)
    const root = screen.getByTestId("run-activity")
    expect(root).not.toHaveClass("rounded-xl")
    expect(screen.getByText("2 steps")).toBeInTheDocument()
  })
})
