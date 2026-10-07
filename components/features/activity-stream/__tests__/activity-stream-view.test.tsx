import type { ComponentProps } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { JournalEntry } from "@/lib/types/journal"
import type { ChainSummary } from "@/hooks/use-chains"
import type { JournalLookupValue } from "@/hooks/use-journal-lookup"
import type { ActivitySidebar } from "../activity-sidebar"
import type { ActivityOverview } from "../activity-overview"
import type { ActivityDetail } from "../activity-detail"
import type { WorkflowPage } from "../workflow-page"
import type { RoutineRunsPage } from "../routine-runs-page"
import type { AgentsOverview, IssuesOverview, RoutinesLensOverview } from "../lens-overviews"
import type { AgentDrillDown, IssueDrillDown, RunDrillDown } from "../drill-downs"
import type { FeedRow } from "../feed-row"
const mock = vi.hoisted(() => ({
  list: vi.fn(), stream: vi.fn(), chains: vi.fn(), fetch: vi.fn(), lookup: vi.fn(), mobile: false,
  refresh: vi.fn(), loadMore: vi.fn(), prepend: vi.fn(), refreshChains: vi.fn(),
  sidebar: vi.fn<(p: ComponentProps<typeof ActivitySidebar>) => React.ReactNode>(),
  overview: vi.fn<(p: ComponentProps<typeof ActivityOverview>) => React.ReactNode>(),
  detail: vi.fn<(p: ComponentProps<typeof ActivityDetail>) => React.ReactNode>(),
  workflow: vi.fn<(p: ComponentProps<typeof WorkflowPage>) => React.ReactNode>(),
  routine: vi.fn<(p: ComponentProps<typeof RoutineRunsPage>) => React.ReactNode>(),
  agents: vi.fn<(p: ComponentProps<typeof AgentsOverview>) => React.ReactNode>(),
  issues: vi.fn<(p: ComponentProps<typeof IssuesOverview>) => React.ReactNode>(),
  routines: vi.fn<(p: ComponentProps<typeof RoutinesLensOverview>) => React.ReactNode>(),
  agent: vi.fn<(p: ComponentProps<typeof AgentDrillDown>) => React.ReactNode>(),
  issue: vi.fn<(p: ComponentProps<typeof IssueDrillDown>) => React.ReactNode>(),
  run: vi.fn<(p: ComponentProps<typeof RunDrillDown>) => React.ReactNode>(),
  row: vi.fn<(p: ComponentProps<typeof FeedRow>) => React.ReactNode>(),
}))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(window.location.search) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mock.mobile }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: mock.fetch }))
vi.mock("@/hooks/use-journal-list", () => ({ useJournalList: mock.list }))
vi.mock("@/hooks/use-journal-stream", () => ({ useJournalStream: mock.stream }))
vi.mock("@/hooks/use-journal-lookup", () => ({ useJournalLookup: mock.lookup }))
vi.mock("@/hooks/use-chains", () => ({ useChains: mock.chains }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: [{ id: "p1", slug: "triage", name: "Triage", invocation_count: 2 }] }) }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({ usePipelineSchedules: () => ({ schedules: [] }) }))
vi.mock("../activity-sidebar", async (original) => ({ ...await original<typeof import("../activity-sidebar")>(), ActivitySidebar: mock.sidebar }))
vi.mock("../activity-overview", () => ({ ActivityOverview: mock.overview, iconFor: () => () => null }))
vi.mock("../activity-detail", () => ({ ActivityDetail: mock.detail }))
vi.mock("../workflow-page", () => ({ WorkflowPage: mock.workflow }))
vi.mock("../routine-runs-page", () => ({ RoutineRunsPage: mock.routine }))
vi.mock("../lens-overviews", () => ({ AgentsOverview: mock.agents, IssuesOverview: mock.issues, RoutinesLensOverview: mock.routines }))
vi.mock("../drill-downs", () => ({ AgentDrillDown: mock.agent, IssueDrillDown: mock.issue, RunDrillDown: mock.run }))
vi.mock("../feed-row", () => ({ FeedRow: mock.row }))
import { ActivityStreamView } from "../activity-stream-view"
import { EMPTY_FACETS } from "../activity-sidebar"
const event = (id: string, extra: Partial<JournalEntry> = {}): JournalEntry => ({ id, workspace_id: "ws", ts: "2026-10-02T20:00:00Z", entry_type: "run.completed", severity: "info", actor_type: "system", summary: id, ...extra })
const entries = [event("e1", { crew_id: "c1", agent_id: "a1", mission_id: "m1", payload: { run_id: "r1", pipeline_slug: "triage", step_id: "step1" } }), event("e2", { payload: { run_id: "r2", routine_slug: "other", step: "step2" } })]
const chain: ChainSummary = { origin: "r1", started_by_kind: "schedule", started_by: "nightly", runs: 1, max_chain_depth: 0, failed_runs: 0, failed: false, first_activity: "2026-10-02T20:00:00Z", last_activity: "2026-10-02T20:00:01Z", duration_ms: 1000, issue_count: 0, agent_count: 0, routine_slug: "triage" }
const lookup: JournalLookupValue = { crews: new Map([["c1", { id: "c1", name: "Builders", slug: "builders", icon: null, color: "blue" }]]), agents: new Map([["a1", { id: "a1", name: "Alice", slug: "alice", crew_id: "c1", avatar_seed: null, avatar_style: null }]]), missions: new Map([["m1", { id: "m1", title: "Fix the failing integration in the workspace", status: "IN_PROGRESS" }]]), loading: false, refresh: vi.fn() }
const sidebar = () => mock.sidebar.mock.calls.at(-1)![0]
const overview = () => mock.overview.mock.calls.at(-1)![0]
const detail = () => mock.detail.mock.calls.at(-1)![0]
const listState = (over: Record<string, unknown> = {}) => ({ entries, loading: false, loadingMore: false, error: null, nextCursor: null, refresh: mock.refresh, loadMore: mock.loadMore, prependLive: mock.prepend, ...over })
const show = () => render(<ActivityStreamView workspaceId="ws & a" />)
beforeEach(() => {
  vi.clearAllMocks(); mock.mobile = false
  window.history.replaceState(null, "", "/activity")
  mock.list.mockReturnValue(listState())
  mock.stream.mockReturnValue({ status: "connected" })
  mock.chains.mockReturnValue({ chains: [chain], hasUnrecordedRuns: false, hasMore: false, error: null, refresh: mock.refreshChains })
  mock.lookup.mockReturnValue(lookup)
  mock.fetch.mockImplementation(async () => new Response(JSON.stringify({ missions: [{ id: "m1", title: "Fix", status: "IN_PROGRESS", priority: "high" }] })))
  mock.sidebar.mockImplementation((p) => <aside><input aria-label="Activity search" value={p.search} onChange={(e) => p.onSearchChange(e.target.value)} /><button onClick={p.onToggleCollapse}>Hide rail</button></aside>)
  mock.overview.mockImplementation((p) => <div>Overview: {p.entries.map((e) => e.id).join(",")}</div>)
  mock.detail.mockImplementation((p) => <div>Record {p.entry.id}<button onClick={p.onBack}>Close record</button></div>)
  mock.workflow.mockImplementation((p) => <div>Workflow {p.chain.origin}</div>)
  mock.routine.mockImplementation((p) => <div>Routine {p.slug}</div>)
  mock.agents.mockImplementation(() => <div>Agent overview</div>)
  mock.issues.mockImplementation(() => <div>Issue overview</div>)
  mock.routines.mockImplementation(() => <div>Routine overview</div>)
  mock.agent.mockImplementation((p) => <div>Agent {p.name}</div>)
  mock.issue.mockImplementation((p) => <div>Issue {p.issueId}</div>)
  mock.run.mockImplementation((p) => <div>Run {p.runID}</div>)
  mock.row.mockImplementation((p) => <button onClick={p.onSelect}>Row {p.entry.id}</button>)
})
afterEach(async () => { await act(async () => {}); cleanup() })

it("shares counts and metadata and wires live entries to the same journal window", async () => {
  show()
  await waitFor(() => expect(sidebar().issues).toHaveLength(1))
  expect(screen.getByText("Overview: e1,e2")).toBeVisible()
  expect(sidebar().crewCounts).toEqual({ c1: 1 })
  expect(sidebar().issueCounts).toEqual({ m1: 1 })
  expect(sidebar().routineCounts).toEqual({ triage: 1, other: 1 })
  expect(mock.fetch).toHaveBeenCalledWith("/api/v1/missions?workspace_id=ws%20%26%20a&limit=200")
  expect(mock.list.mock.calls.at(-1)![0]).toMatchObject({ workspaceId: "ws & a", limit: 300, maxEntries: 2000 })
  expect(mock.stream.mock.calls.at(-1)![0].onEntry).toBe(mock.prepend)
  expect(overview().agentName("a1")).toBe("Alice")
  expect(overview().agentName()).toBeUndefined()
  expect(overview().crewName("c1")).toBe("Builders")
  expect(overview().crewName()).toBeUndefined()
  expect(overview().crewMeta("c1")).toEqual({ icon: null, color: "blue" })
  expect(overview().crewMeta()).toBeUndefined()
  act(() => sidebar().onRetryChains?.())
  expect(mock.refreshChains).toHaveBeenCalledOnce()
})
it.each(["polling", "connecting"])("reports %s transport without hiding the window", (status) => {
  mock.stream.mockReturnValue({ status }); show()
  expect(screen.getByText(status)).toBeVisible()
})
it("requests answer events with waiting asks and uses the larger waiting window", async () => {
  show()
  act(() => overview().onScope("waiting"))
  const request = mock.list.mock.calls.at(-1)![0]
  expect(request.limit).toBe(500)
  expect(request.params.entry_type).toContain("approval.requested")
  expect(request.params.entry_type).toContain("approval.granted")
  expect(mock.row).not.toHaveBeenCalled()
  await waitFor(() => expect(screen.getByText("Nothing here")).toBeVisible())
  fireEvent.click(screen.getByRole("button", { name: "Reset" }))
  expect(screen.getByText("Overview: e1,e2")).toBeVisible()
})
it("combines server facets, debounces search and removes individual chips", async () => {
  show()
  act(() => sidebar().onChange({ ...EMPTY_FACETS, sources: ["run"], severities: ["warn"], crewIDs: ["c1"], agentIDs: ["a1"], range: "7d", showTelemetry: true }))
  fireEvent.change(screen.getByRole("textbox", { name: "Activity search" }), { target: { value: " needle " } })
  await waitFor(() => expect(mock.list.mock.calls.at(-1)![0].params.q).toBe("needle"))
  expect(mock.list.mock.calls.at(-1)![0].params).toMatchObject({ crew_ids: "c1", agent_ids: "a1", severity: "warn", exclude_entry_type: undefined })
  fireEvent.click(screen.getAllByRole("button", { name: "Remove filter" })[0])
  await waitFor(() => expect(mock.list.mock.calls.at(-1)![0].params.q).toBeUndefined())
  for (let i = 0; i < 5; i++) fireEvent.click(screen.getAllByRole("button", { name: "Remove filter" })[0])
  await waitFor(() => expect(mock.list.mock.calls.at(-1)![0].params.q).toBeUndefined())
  expect(sidebar().facets).toMatchObject({ crewIDs: [], agentIDs: [], severities: [], sources: [], range: "24h" })
})
it.each(["routine", "run", "step", "issue"] as const)("pins a %s crumb and clears it", (kind) => {
  show()
  const id = { routine: "triage", run: "r1", step: "step1", issue: "m1" }[kind]
  act(() => overview().onSpineClick({ kind, id, label: "Pinned" }))
  if (kind === "issue") expect(mock.list.mock.calls.at(-1)![0].params.mission_id).toBe("m1")
  else expect(overview().entries.map((e) => e.id)).toEqual(["e1"])
  fireEvent.click(screen.getByRole("button", { name: "Remove filter" }))
  expect(overview().entries).toEqual(entries)
})
it("clears intersecting filters atomically instead of showing reassuring empty totals", () => {
  show()
  act(() => {
    overview().onSpineClick({ kind: "run", id: "missing", label: "Unknown" })
    sidebar().onChange({ ...EMPTY_FACETS, crewIDs: ["c1"], agentIDs: ["a1"] })
  })
  expect(screen.getByText("No activity matches these filters")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Clear all filters" }))
  expect(sidebar().facets).toEqual(EMPTY_FACETS)
  expect(screen.getByText("Overview: e1,e2")).toBeVisible()
})
it("opens records from the keyboard, moves between them, and ignores typing in inputs", () => {
  show()
  fireEvent.keyDown(window, { key: "j" })
  expect(screen.getByText("Record e1")).toBeVisible()
  fireEvent.keyDown(window, { key: "j" })
  expect(screen.getByText("Record e2")).toBeVisible()
  fireEvent.keyDown(window, { key: "k" })
  expect(screen.getByText("Record e1")).toBeVisible()
  const input = screen.getByRole("textbox", { name: "Activity search" }); input.focus()
  fireEvent.keyDown(input, { key: "Escape" })
  expect(screen.getByText("Record e1")).toBeVisible()
  input.blur()
  fireEvent.keyDown(window, { key: "Escape" })
  expect(screen.queryByText("Record e1")).toBeNull()
  fireEvent.keyDown(window, { key: "x" })
  expect(screen.getByText("Overview: e1,e2")).toBeVisible()
})
it("restores a record URL and popstate, and forgets records outside the loaded window", () => {
  window.history.replaceState(null, "", "/activity?entry=e1")
  show()
  expect(screen.getByText("Record e1")).toBeVisible()
  act(() => detail().onSpineClick({ kind: "run", id: "r1", label: "Run" }))
  expect(screen.queryByText("Record e1")).toBeNull()
  act(() => { window.history.replaceState(null, "", "/activity?entry=missing&lens=agents"); window.dispatchEvent(new PopStateEvent("popstate")) })
  expect(window.location.search).not.toContain("entry=")
  expect(screen.getByText("Agent overview")).toBeVisible()
})
it("renders failed and done buckets, pagination, and selection from a row", () => {
  mock.list.mockReturnValue(listState({ nextCursor: "older" }))
  const view = show()
  act(() => sidebar().onChange({ ...EMPTY_FACETS, scope: "failed" }))
  expect(mock.list.mock.calls.at(-1)![0].params.severity).toBe("error")
  fireEvent.click(screen.getByRole("button", { name: "Load older" }))
  expect(mock.loadMore).toHaveBeenCalledOnce()
  act(() => sidebar().onChange({ ...EMPTY_FACETS, scope: "done" }))
  fireEvent.click(screen.getByRole("button", { name: "Row e1" }))
  expect(screen.getByText("Record e1")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Close record" }))
  mock.list.mockReturnValue(listState({ nextCursor: "older", loadingMore: true }))
  view.rerender(<ActivityStreamView workspaceId="ws & a" />)
  expect(screen.getByRole("button", { name: "Load older" })).toBeDisabled()
})
it("renders loading and errors as such and retries a failed journal read", () => {
  mock.list.mockReturnValue(listState({ entries: [], loading: true }))
  const view = show()
  expect(screen.getByText("Loading activity…")).toBeVisible()
  mock.list.mockReturnValue(listState({ entries: [], error: "offline" }))
  view.rerender(<ActivityStreamView workspaceId="ws & a" />)
  expect(screen.getByText("Could not load activity")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Try again" }))
  expect(mock.refresh).toHaveBeenCalledOnce()
})
it("starts mobile filters closed and closes their overlay", () => {
  mock.mobile = true; show()
  fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }))
  expect(screen.getByRole("textbox", { name: "Activity search" })).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Close filters" }))
  expect(screen.queryByRole("textbox", { name: "Activity search" })).toBeNull()
})

it("walks from workflow through named nodes, back, and breadcrumb jumps", () => {
  show()
  act(() => sidebar().onSelectChain("r1"))
  expect(screen.getByText("Workflow r1")).toBeVisible()
  expect(screen.queryByText("Overview: e1,e2")).toBeNull()
  const workflow = () => mock.workflow.mock.calls.at(-1)![0]
  for (const [kind, id, label] of [["agent", "a1", "Alice"], ["crew", "c1", "Builders"], ["issue", "m1", "Fix the failing integration i…"], ["routine", "p1", "Triage"], ["step", "step1", "step1"]]) {
    act(() => workflow().onOpenNode(kind, id))
    expect(screen.getByRole("navigation", { name: "Activity trail" })).toHaveTextContent(label)
    // One stop up is Escape or the previous crumb; the back-bar's button is
    // the way OUT, as "Back to issues" is on /issues.
    fireEvent.keyDown(window, { key: "Escape" })
    expect(screen.getByText("Workflow r1")).toBeVisible()
  }
  act(() => workflow().onOpenNode("run", "r1"))
  expect(mock.run.mock.calls.at(-1)![0].routineSlug).toBe("triage")
  fireEvent.keyDown(window, { key: "Escape" })
  expect(screen.getByText("Workflow r1")).toBeVisible()
  act(() => workflow().onOpenNode("agent", "a1"))
  fireEvent.click(screen.getByRole("button", { name: "Back to activity" }))
  expect(screen.getByText("Overview: e1,e2")).toBeVisible()
})

it.each(["agents", "issues", "routines"] as const)("gives the %s lens its own overview and entity drill-down", (lens) => {
  show()
  act(() => sidebar().onLens(lens))
  if (lens === "agents") {
    expect(screen.getByText("Agent overview")).toBeVisible()
    act(() => mock.agents.mock.calls.at(-1)![0].onOpenEntity("agent", "a1", "Alice"))
    expect(screen.getByText("Agent Alice")).toBeVisible()
    act(() => mock.agent.mock.calls.at(-1)![0].onOpenWorkflow("r1"))
    expect(screen.getByText("Workflow r1")).toBeVisible()
  } else if (lens === "issues") {
    expect(screen.getByText("Issue overview")).toBeVisible()
    act(() => mock.issues.mock.calls.at(-1)![0].onOpenEntity("issue", "m1", "Fix"))
    expect(screen.getByText("Issue m1")).toBeVisible()
    expect(mock.list.mock.calls.at(-1)![0].params.mission_id).toBe("m1")
  } else {
    expect(screen.getByText("Routine overview")).toBeVisible()
    act(() => mock.routines.mock.calls.at(-1)![0].onOpenRoutine("triage", "Triage"))
    expect(screen.getByText("Routine triage")).toBeVisible()
    act(() => mock.routine.mock.calls.at(-1)![0].onOpenRun("child-run"))
    expect(mock.run.mock.calls.at(-1)![0].routineSlug).toBe("triage")
    expect(window.location.search).toContain("run=child-run")
  }
})
it("resolves a deep-linked run's routine from the workflow index", () => {
  window.history.replaceState(null, "", "/activity?run=r1")
  show()
  expect(screen.getByText("Run r1")).toBeVisible()
  expect(mock.run.mock.calls.at(-1)![0].routineSlug).toBe("triage")
})
it("relabels inbound issue and routine IDs and preserves unknown node identities", () => {
  window.history.replaceState(null, "", "/activity?pipeline=triage&lens=routines")
  show()
  expect(screen.getByRole("navigation", { name: "Activity trail" })).toHaveTextContent("Triage")
  act(() => sidebar().onOpenEntity("issue", "m1", "m1"))
  expect(screen.getByRole("navigation", { name: "Activity trail" })).toHaveTextContent("Fix the failing integration i…")
  act(() => sidebar().onSelectChain("r1"))
  act(() => mock.workflow.mock.calls.at(-1)![0].onOpenNode("agent", "unknown"))
  expect(screen.getByText("Agent #known")).toBeVisible()
})
it("handles a workflow swept from the index and returns to overview", () => {
  show()
  act(() => sidebar().onSelectChain("gone"))
  expect(screen.getByText("This workflow is no longer in the index")).toBeVisible()
  fireEvent.click(screen.getAllByRole("button", { name: "Back" }).at(-1)!)
  expect(screen.getByText("Overview: e1,e2")).toBeVisible()
  act(() => sidebar().onSelectChain(null))
  expect(screen.queryByRole("navigation", { name: "Activity trail" })).toBeNull()
})
it("uses the same narrowed chain index for sidebar and lens overview", () => {
  show()
  act(() => sidebar().onLens("routines"))
  fireEvent.change(screen.getByRole("textbox", { name: "Activity search" }), { target: { value: "unmatched" } })
  expect(sidebar().chains).toEqual([])
  expect(mock.routines.mock.calls.at(-1)![0].chains).toEqual([])
  fireEvent.change(screen.getByRole("textbox", { name: "Activity search" }), { target: { value: "Triage" } })
  expect(sidebar().chains).toEqual([chain])
  expect(mock.routines.mock.calls.at(-1)![0].chains).toEqual([chain])
})
it.each(["array", "http", "network"])("handles %s issue metadata responses", async (kind) => {
  if (kind === "network") mock.fetch.mockRejectedValue(new Error("offline"))
  else mock.fetch.mockResolvedValue(new Response(JSON.stringify(kind === "array" ? [{ id: "new" }] : {}), { status: kind === "http" ? 503 : 200 }))
  show()
  await act(async () => {})
  expect(sidebar().issues).toEqual(kind === "array" ? [{ id: "new" }] : [])
})
it("opens a run under the Issues back-bar: the way out, then only the stops walked", () => {
  // #2979: /issues reads "‹ Back to issues › OPS-1". The home crumb would
  // repeat the button beside it ("Back to activity › Overview › …").
  show()
  act(() => sidebar().onSelectChain("r1"))
  const trail = screen.getByRole("navigation", { name: "Activity trail" })
  expect(trail).toHaveTextContent("Back to activity")
  expect(within(trail).queryByRole("button", { name: "Overview" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Back to activity" }))
  expect(screen.getByText("Overview: e1,e2")).toBeVisible()
})
it("hands the ledger opener to the rail", () => {
  const onOpenSection = vi.fn()
  render(<ActivityStreamView workspaceId="ws" onOpenSection={onOpenSection} />)
  sidebar().onOpenSection?.("deliveries")
  expect(onOpenSection).toHaveBeenCalledWith("deliveries")
})
