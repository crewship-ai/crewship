import * as React from "react"
import type { ComponentProps } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"
import type { JournalLookupValue } from "@/hooks/use-journal-lookup"
import type { JournalEntry } from "@/lib/types/journal"
import type { ActivityRunPage } from "../activity-run-page"
import type { ActivityDetail } from "../activity-detail"

const mock = vi.hoisted(() => ({
  list: vi.fn(),
  stream: vi.fn(),
  chains: vi.fn(),
  lookup: vi.fn(),
  records: vi.fn(),
  mobile: false,
  role: "ADMIN" as string | null,
  refreshChains: vi.fn(async () => {}),
  refreshList: vi.fn(async () => {}),
  prepend: vi.fn(),
  run: vi.fn<(p: ComponentProps<typeof ActivityRunPage>) => React.ReactNode>(),
  detail: vi.fn<(p: ComponentProps<typeof ActivityDetail>) => React.ReactNode>(),
}))

vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(window.location.search) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mock.mobile }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => new Response("{}")) }))
vi.mock("@/hooks/use-user-preference", async () => {
  const R = await import("react")
  return { useUserPreference: <T,>(_k: string, d: T) => R.useState<T>(d) }
})
vi.mock("@/hooks/use-journal-list", () => ({ useJournalList: mock.list }))
vi.mock("@/hooks/use-journal-stream", () => ({ useJournalStream: mock.stream }))
vi.mock("@/hooks/use-journal-lookup", () => ({ useJournalLookup: mock.lookup }))
vi.mock("@/hooks/use-chains", () => ({ useChains: mock.chains }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: mock.role }) }))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [{ id: "p1", slug: "triage", name: "Nightly triage" }, { id: "p2", slug: "digest", name: "Daily digest" }] }),
}))
vi.mock("@/hooks/use-pipeline-run-records", () => ({ usePipelineRunRecords: mock.records }))
vi.mock("../activity-run-page", () => ({ ActivityRunPage: mock.run }))
vi.mock("../activity-work-page", () => ({ ActivityWorkPage: () => <div>Work page</div> }))
vi.mock("../workflow-page", () => ({ WorkflowPage: () => <div>Workflow page</div> }))
vi.mock("../activity-detail", () => ({ ActivityDetail: mock.detail }))

import { ActivityStreamView } from "../activity-stream-view"

const NOW = Date.now()
const ago = (min: number) => new Date(NOW - min * 60_000).toISOString()

const chain = (origin: string, over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin,
  started_by_kind: "schedule",
  started_by: "nightly",
  runs: 1,
  max_chain_depth: 0,
  failed_runs: 0,
  failed: false,
  first_activity: ago(30),
  last_activity: ago(29),
  duration_ms: 42_000,
  issue_count: 0,
  agent_count: 0,
  routine_slug: "triage",
  ...over,
})

const yesterday = (h: number) => {
  const d = new Date(NOW)
  d.setDate(d.getDate() - 1)
  d.setHours(h, 0, 0, 0)
  return d.toISOString()
}

const CHAINS: ChainSummary[] = [
  chain("run_live", { running_runs: 1, routine_slug: "triage", agents: [{ id: "a1", name: "Scout", assignments: 1 }], agent_count: 1 }),
  chain("run_bad", { failed: true, failed_runs: 1, routine_slug: "triage", first_activity: ago(60) }),
  chain("run_d1", { routine_slug: "digest", first_activity: yesterday(9) }),
  chain("run_d2", { routine_slug: "digest", first_activity: yesterday(8) }),
  chain("run_d3", { routine_slug: "digest", first_activity: yesterday(7) }),
]

const change: JournalEntry = {
  id: "e1",
  workspace_id: "ws",
  ts: ago(45),
  entry_type: "mission.status_change",
  severity: "info",
  actor_type: "user",
  summary: "CRW-1 moved to In review",
  mission_id: "m1",
  payload: { from: "In progress", to: "In review" },
}

const lookup: JournalLookupValue = {
  crews: new Map([["c1", { id: "c1", name: "Research", slug: "research", icon: null, color: "teal" }]]),
  agents: new Map([["a1", { id: "a1", name: "Scout", slug: "scout", crew_id: "c1", avatar_seed: null, avatar_style: null }]]),
  missions: new Map([["m1", { id: "m1", title: "Fix flaky login", status: "IN_REVIEW", identifier: "CRW-1" }]]),
  loading: false,
  refresh: vi.fn(),
}

const chainsState = (over: Record<string, unknown> = {}) => ({
  chains: CHAINS,
  loading: false,
  error: null,
  hasUnrecordedRuns: false,
  hasMore: false,
  refresh: mock.refreshChains,
  ...over,
})
const listState = (over: Record<string, unknown> = {}) => ({
  entries: [change],
  loading: false,
  loadingMore: false,
  error: null,
  nextCursor: null,
  refresh: mock.refreshList,
  loadMore: vi.fn(),
  prependLive: mock.prepend,
  ...over,
})

const show = () => render(<ActivityStreamView workspaceId="ws" />)
// Queried by selector: an open Peek hides the page from the accessibility tree.
const table = () => document.querySelector<HTMLElement>("[role=table][aria-label=Activity]")!

beforeEach(() => {
  vi.clearAllMocks()
  mock.mobile = false
  mock.role = "ADMIN"
  window.history.replaceState(null, "", "/activity")
  mock.chains.mockReturnValue(chainsState())
  mock.list.mockReturnValue(listState())
  mock.stream.mockReturnValue({ status: "connected", reconnect: vi.fn() })
  mock.lookup.mockReturnValue(lookup)
  mock.records.mockReturnValue({ records: [] })
  mock.run.mockImplementation((p) => <div>Run page {p.runId}</div>)
  mock.detail.mockImplementation((p) => <div>Record {p.entry.id}</div>)
})
afterEach(async () => {
  await act(async () => {})
  cleanup()
  vi.useRealTimers()
})

describe("ActivityStreamView", () => {
  it("renders Everything list with summary line and Display", async () => {
    show()
    expect(screen.getByRole("heading", { level: 1, name: "Everything" })).toBeVisible()
    const summary = await screen.findByTestId("activity-summary")
    // 2 work rows today, 1 bundle of 3 runs yesterday, 1 change.
    expect(summary).toHaveTextContent("4 items")
    expect(summary).toHaveTextContent("1 could not finish")
    expect(within(summary).getByText("Live")).toBeVisible()
    expect(within(summary).getByRole("button", { name: /Display/ })).toBeVisible()

    const t = table()
    expect(within(t).getByText("Active now")).toBeVisible()
    expect(within(t).getByText("Yesterday")).toBeVisible()
    expect(within(t).getByText("×3 runs")).toBeVisible()
    expect(within(t).getByText("CRW-1 Fix flaky login")).toBeVisible()
    expect(within(t).getByText("In progress → In review")).toBeVisible()
    for (const h of ["Work", "Agent", "Took", "Started"]) expect(within(t).getByRole("columnheader", { name: h })).toBeInTheDocument()

    // The views count over the same window.
    // Everything folds repeated runs the way the list does.
    expect(screen.getByRole("button", { name: /Everything\s*4/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Running\s*1/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Could not finish\s*1/ })).toBeInTheDocument()
  })

  it("the list keeps one h1: the view title, not the top-bar path", async () => {
    show()
    await screen.findByTestId("activity-summary")
    const h1s = screen.getAllByRole("heading", { level: 1 })
    expect(h1s).toHaveLength(1)
    expect(h1s[0]).toHaveTextContent(/^Everything$/)
  })

  it("j/k moves selection and opens Peek", async () => {
    show()
    await screen.findByTestId("activity-summary")
    fireEvent.keyDown(window, { key: "j" })
    const peek = await screen.findByTestId("activity-peek-work")
    expect(peek).toHaveTextContent("How it connects")
    expect(table().querySelector("[data-selected=true]")).toHaveAttribute("data-row-id", "run_live")
    expect(window.location.search).toBe("?run=run_live")
    // Inside the Peek, J steps to the next row in the order the table shows:
    // the change 45 min ago, written as ?entry=.
    fireEvent.keyDown(screen.getByTestId("activity-peek-work"), { key: "j" })
    expect(await screen.findByTestId("activity-peek-change")).toHaveTextContent("In progress")
    expect(window.location.search).toBe("?entry=e1")
  })

  // RT-07 (#3492): a click on an Activity row of a routine run opens the run
  // peek in place (no navigation); the run's own page is one click further.
  it("RT-07 a click on a routine-run row opens the run peek in place, with its status, duration and Open run", async () => {
    show()
    await screen.findByTestId("activity-summary")
    const row = table().querySelector<HTMLElement>("[data-row-id=run_bad]")!
    fireEvent.click(row)
    const peek = await screen.findByTestId("activity-peek-work")
    expect(peek).toHaveTextContent("Could not finish")
    expect(peek).toHaveTextContent("Took")
    expect(screen.getByText("Routine run")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Open run" })).toBeInTheDocument()
    expect(window.location.pathname).toBe("/activity")
    expect(window.location.search).toBe("?run=run_bad")
    expect(table().querySelector("[data-selected=true]")).toHaveAttribute("data-row-id", "run_bad")
  })

  it("?run=<id> opens the Peek and closing it drops run from the URL", async () => {
    window.history.replaceState(null, "", "/activity?run=run_bad")
    show()
    expect(await screen.findByTestId("activity-peek-work")).toHaveTextContent("Could not finish")
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
    await waitFor(() => expect(screen.queryByTestId("activity-peek-work")).toBeNull())
    expect(window.location.search).toBe("")
  })

  it("?run=<id> outside the range widens the list range so the run is in the list", async () => {
    const old = chain("run_old", { routine_slug: "digest", first_activity: ago(10 * 24 * 60), last_activity: ago(10 * 24 * 60 - 1) })
    mock.chains.mockReturnValue(chainsState({ chains: [...CHAINS, old] }))
    window.history.replaceState(null, "", "/activity?run=run_old")
    show()
    expect(await screen.findByTestId("activity-peek-work")).toBeInTheDocument()
    await waitFor(() => expect(table().querySelector("[data-row-id=run_old]")).not.toBeNull())
    expect(window.location.search).toContain("range=30d")
    expect(window.location.search).toContain("run=run_old")
  })

  it("?run=<id> inside the range keeps the default range", async () => {
    window.history.replaceState(null, "", "/activity?run=run_bad")
    show()
    await screen.findByTestId("activity-peek-work")
    expect(window.location.search).not.toContain("range=")
  })

  it("Open run walks to the run's page", async () => {
    window.history.replaceState(null, "", "/activity?run=run_bad")
    show()
    await screen.findByTestId("activity-peek-work")
    fireEvent.click(screen.getByRole("button", { name: "Open run" }))
    expect(await screen.findByText("Run page run_bad")).toBeVisible()
    expect(window.location.search).toContain("walk=workflow%3Arun_bad")
    fireEvent.click(screen.getByRole("button", { name: /Back to activity/ }))
    expect(await screen.findByRole("heading", { level: 1, name: "Everything" })).toBeVisible()
  })

  it("a run page keeps one h1: its own title, not the top-bar path", async () => {
    mock.run.mockImplementation(() => <h1>Nightly triage</h1>)
    window.history.replaceState(null, "", "/activity?walk=workflow:run_bad")
    show()
    await screen.findByText("Nightly triage")
    const h1s = screen.getAllByRole("heading", { level: 1 })
    expect(h1s).toHaveLength(1)
    expect(h1s[0]).toHaveTextContent(/^Nightly triage$/)
  })

  it("while a run page loads, the top bar is the one h1 until the page names itself", async () => {
    let reveal = () => {}
    function LoadingRun() {
      const [ready, setReady] = React.useState(false)
      reveal = () => setReady(true)
      return ready ? <h1>Nightly triage</h1> : <div role="status">Loading run…</div>
    }
    mock.run.mockImplementation(() => <LoadingRun />)
    window.history.replaceState(null, "", "/activity?walk=workflow:run_bad")
    show()
    await screen.findByText("Loading run…")
    await waitFor(() => expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1))
    act(() => reveal())
    await screen.findByText("Nightly triage")
    await waitFor(() => {
      const h1s = screen.getAllByRole("heading", { level: 1 })
      expect(h1s).toHaveLength(1)
      expect(h1s[0]).toHaveTextContent(/^Nightly triage$/)
    })
  })

  it("an unknown run that is still loading keeps exactly one h1", async () => {
    mock.chains.mockReturnValue(chainsState({ chains: [], loading: true }))
    window.history.replaceState(null, "", "/activity?walk=workflow:run_gone")
    show()
    await waitFor(() => expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1))
  })

  it("a record page keeps one h1: the record's own", async () => {
    mock.detail.mockImplementation((p) => <h1>{p.entry.summary}</h1>)
    window.history.replaceState(null, "", "/activity?entry=e1")
    show()
    await screen.findByTestId("activity-peek-change")
    fireEvent.click(screen.getByRole("button", { name: "Open full record" }))
    await screen.findByText("CRW-1 moved to In review", { selector: "h1" })
    // Counted in the DOM: the closing Peek still hides the page from the
    // accessibility tree for a moment, and every h1 counts, hidden or not.
    await waitFor(() => {
      const h1s = document.querySelectorAll("h1")
      expect(h1s).toHaveLength(1)
      expect(h1s[0]).toHaveTextContent(/^CRW-1 moved to In review$/)
    })
  })

  it("keys 1–4 switch views and the view is written to the URL", async () => {
    show()
    await screen.findByTestId("activity-summary")
    fireEvent.keyDown(window, { key: "4" })
    expect(await screen.findByRole("heading", { level: 1, name: "Could not finish" })).toBeVisible()
    expect(window.location.search).toBe("?view=failed")
    // Changes belong to Everything only.
    expect(within(table()).queryByText("CRW-1 Fix flaky login")).toBeNull()
    fireEvent.keyDown(window, { key: "1" })
    expect(await screen.findByRole("heading", { level: 1, name: "Everything" })).toBeVisible()
  })

  it("Display › Group repeated runs off lists the runs one by one", async () => {
    show()
    await screen.findByTestId("activity-summary")
    fireEvent.click(screen.getByRole("button", { name: /Display/ }))
    fireEvent.click(await screen.findByRole("switch", { name: "Group repeated runs" }))
    await waitFor(() => expect(within(table()).queryByText("×3 runs")).toBeNull())
    expect(screen.getByTestId("activity-summary")).toHaveTextContent("6 items")
    expect(window.location.search).toBe("?bundle=off")
  })

  it("three updates to one issue on one day show as one story row with a story Peek", async () => {
    const upd = (id: string, h: number, from: string, to: string): JournalEntry => ({ ...change, id, ts: yesterday(h), payload: { from, to } })
    mock.list.mockReturnValue(listState({ entries: [upd("s3", 11, "In progress", "In review"), upd("s2", 10, "Todo", "In progress"), upd("s1", 9, "Backlog", "Todo")] }))
    show()
    const row = await within(table()).findByText("3 updates")
    expect(within(table()).getByText("Todo → In progress → In review")).toBeVisible()
    // One row for the three, not three rows.
    expect(table().querySelectorAll("[data-row-id=s1],[data-row-id=s2],[data-row-id=s3]")).toHaveLength(0)
    fireEvent.click(row)
    const peek = await screen.findByTestId("activity-peek-story")
    expect(peek).toHaveTextContent("What happened, in order")
    expect(peek).toHaveTextContent("3 updates about one issue on one day")
    const steps = within(peek).getAllByRole("button", { name: /Status changed/ })
    expect(steps).toHaveLength(3)
    // Oldest first, the way it happened.
    expect(steps[0]).toHaveTextContent("Backlog → Todo")
    expect(within(peek).getByRole("button", { name: /Everything about CRW-1 Fix flaky login/ })).toBeVisible()
    // A step opens that change in the Peek.
    fireEvent.click(steps[0])
    expect(await screen.findByTestId("activity-peek-change")).toHaveTextContent("Backlog")
    expect(window.location.search).toBe("?entry=s1")
  })

  it("Display › Group repeated runs off lists a story's updates one by one", async () => {
    const upd = (id: string, h: number): JournalEntry => ({ ...change, id, ts: yesterday(h) })
    mock.list.mockReturnValue(listState({ entries: [upd("s3", 11), upd("s2", 10), upd("s1", 9)] }))
    show()
    await within(table()).findByText("3 updates")
    fireEvent.click(screen.getByRole("button", { name: /Display/ }))
    fireEvent.click(await screen.findByRole("switch", { name: "Group repeated runs" }))
    await waitFor(() => expect(within(table()).queryByText("3 updates")).toBeNull())
    expect(table().querySelectorAll("[data-row-id=s1],[data-row-id=s2],[data-row-id=s3]")).toHaveLength(3)
  })

  it("old lens links group the list", async () => {
    window.history.replaceState(null, "", "/activity?lens=agents")
    show()
    expect(await within(table()).findByText("Scout")).toBeVisible()
    expect(within(table()).getByText("No agent")).toBeVisible()
    expect(window.location.search).toBe("?group=who")
  })

  it("a walk ending on an issue narrows the list to that issue", async () => {
    window.history.replaceState(null, "", "/activity?walk=issue:m1")
    show()
    expect(await screen.findByRole("heading", { level: 1, name: "CRW-1 Fix flaky login" })).toBeVisible()
    expect(screen.getByText(/Everything about this issue/)).toBeVisible()
    expect(screen.getByRole("link", { name: /Open issue/ })).toHaveAttribute("href", "/issues/CRW-1")
    expect(mock.list.mock.calls.at(-1)![0].params.mission_id).toBe("m1")
  })

  it("a link that names an agent or crew by slug, or an issue by identifier, narrows the list to it", async () => {
    window.history.replaceState(null, "", "/activity?agent=scout")
    const view = show()
    await waitFor(() => expect(table().querySelectorAll("[data-row-id]")).toHaveLength(1))
    expect(table().querySelector("[data-row-id=run_live]")).not.toBeNull()
    expect(window.location.search).toBe("?agent=a1")
    view.unmount()

    window.history.replaceState(null, "", "/activity?crew=research")
    const crew = show()
    await waitFor(() => expect(window.location.search).toBe("?crew=c1"))
    expect(table().querySelector("[data-row-id=run_live]")).not.toBeNull()
    crew.unmount()

    window.history.replaceState(null, "", "/activity?mission=CRW-1")
    show()
    expect(await screen.findByRole("heading", { level: 1, name: "CRW-1 Fix flaky login" })).toBeVisible()
    expect(window.location.search).toBe("?mission=m1")
  })

  it("keeps work rows and changes live from one stream", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    show()
    const onEntry = mock.stream.mock.calls.at(-1)![0].onEntry as (e: JournalEntry) => void
    act(() => onEntry({ ...change, id: "e2", entry_type: "mission.comment" }))
    expect(mock.prepend).toHaveBeenCalledWith(expect.objectContaining({ id: "e2" }))
    act(() => onEntry({ ...change, id: "e3", entry_type: "pipeline.run.completed" }))
    expect(mock.prepend).toHaveBeenCalledTimes(1)
    await act(async () => {
      vi.advanceTimersByTime(1_600)
    })
    expect(mock.refreshChains).toHaveBeenCalled()
  })

  it("shows the empty workspace", async () => {
    mock.chains.mockReturnValue(chainsState({ chains: [] }))
    mock.list.mockReturnValue(listState({ entries: [] }))
    const { rerender } = show()
    // The index answers once.
    mock.chains.mockReturnValue(chainsState({ chains: [], loading: true }))
    rerender(<ActivityStreamView workspaceId="ws" />)
    mock.chains.mockReturnValue(chainsState({ chains: [] }))
    rerender(<ActivityStreamView workspaceId="ws" />)
    expect(await screen.findByText("No activity yet")).toBeVisible()
    expect(screen.getByText("crewship activity")).toBeVisible()
  })

  it("says when filters hide everything and clears them", async () => {
    window.history.replaceState(null, "", "/activity?source=webhook")
    show()
    expect(await screen.findByText("Nothing matches these filters")).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
    expect(await screen.findByRole("heading", { level: 1, name: "Everything" })).toBeVisible()
  })

  it("keeps the changes when only the runs fail to load", async () => {
    mock.chains.mockReturnValue(chainsState({ chains: [], error: "HTTP 503" }))
    show()
    expect(await screen.findByText("Could not load runs and agent work")).toBeVisible()
    expect(within(table()).getByText("CRW-1 Fix flaky login")).toBeVisible()
    fireEvent.click(screen.getAllByRole("button", { name: "Retry" })[0])
    expect(mock.refreshChains).toHaveBeenCalled()
  })

  it("dims the list and offers a reconnect while live updates are down", async () => {
    const reconnect = vi.fn()
    mock.stream.mockReturnValue({ status: "error", reconnect })
    show()
    expect(await screen.findByText("Reconnecting…")).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }))
    expect(reconnect).toHaveBeenCalled()
  })
})
