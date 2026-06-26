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
