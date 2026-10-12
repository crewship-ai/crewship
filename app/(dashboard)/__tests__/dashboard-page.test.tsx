import type { ReactNode } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { AgentSummary, CrewSummary, CrewSpendResponse, RunInsightsResponse, RuntimeCapacityResponse, TimeseriesResponse } from "../dashboard-types"
import DashboardPage from "../page"

type Query<T> = { data: T | undefined; isPending: boolean; isError: boolean; refetch: ReturnType<typeof vi.fn> }
const state = vi.hoisted(() => {
  const query = <T,>(): Query<T> => ({ data: undefined, isPending: false, isError: false, refetch: vi.fn() })
  return {
    workspace: { workspaceId: "a" as string | null, loading: false }, admin: false as boolean | null,
    fetch: vi.fn(), events: new Map<string, () => void>(), invalidateA: vi.fn(), invalidateB: vi.fn(), volumeCalls: vi.fn(), insightsCalls: vi.fn(), agentCalls: vi.fn(), routineRefresh: vi.fn(),
    agents: query<AgentSummary[]>(), crews: query<CrewSummary[]>(), review: query<unknown[]>(), progress: query<unknown[]>(), completed: query<unknown[]>(), runs: query<unknown[]>(),
    insights: query<RunInsightsResponse>(), capacity: query<RuntimeCapacityResponse>(), spend: query<CrewSpendResponse>(), volume: query<Partial<TimeseriesResponse>>(),
    gaps: new Map<string, { crewId: string }[]>(),
  }
})
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => state.workspace }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => state.admin }))
vi.mock("@/lib/server-base", () => ({ serverFetch: (...args: unknown[]) => state.fetch(...args) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (event: string, callback: () => void) => { state.events.set(event, callback) } }))
vi.mock("@/hooks/use-dashboard-data", () => ({
  useAgentSummaries: (...args: unknown[]) => { state.agentCalls(...args); return state.agents },
  useCrewSummaries: () => state.crews,
  useDashboardResults: (_ws: string, kind: string) => kind === "REVIEW" ? state.review : kind === "IN_PROGRESS" ? state.progress : state.completed,
  useDashboardActiveRuns: () => state.runs,
  useRunsInsights: (...args: unknown[]) => { state.insightsCalls(...args); return state.insights },
  useRuntimeCapacity: () => state.capacity,
  useMetricsTimeseries: (...args: unknown[]) => { state.volumeCalls(...args); return state.volume },
  useCrewSpend: () => state.spend,
  useCrewServiceSummaries: () => ({ byCrew: new Map() }),
  useInvalidateDashboard: (ws: string) => ws === "a" ? state.invalidateA : state.invalidateB,
}))
vi.mock("@/hooks/use-active-routine-runs", () => ({ useActiveRoutineRuns: () => ({ runs: [], recentDashboardRuns: [], loading: false, error: null, refresh: state.routineRefresh }) }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({ usePipelineSchedules: () => ({ schedules: [] }) }))
vi.mock("@/hooks/use-credential-readiness", () => ({ useCredentialReadiness: () => ({ gapsByCredential: state.gaps }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInbox: () => ({ items: [], loading: false, error: null, activeByKind: {}, decisionCount: 0 }) }))
vi.mock("@/components/ui/detail", async (importActual) => ({
  ...await importActual<typeof import("@/components/ui/detail")>(), Appear: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}))
vi.mock("@/components/features/dashboard/dashboard-overview", async (importActual) => ({
  ...await importActual<typeof import("@/components/features/dashboard/dashboard-overview")>(),
  AttentionStrip: ({ items }: { items: { id: string; label: string; detail: string }[] }) => <div data-testid="attention">{items.map((item) => <p key={item.id}>{item.label}: {item.detail}</p>)}</div>,
  OutcomeKpis: ({ spendUsd, spendPerRun }: { spendUsd: number | null | undefined; spendPerRun: number | null }) => <div data-testid="spend" data-per-run={String(spendPerRun)}>{spendUsd === undefined ? "pending" : spendUsd === null ? "unmetered" : String(spendUsd)}</div>,
  UpNext: () => <div>Upcoming routines</div>,
}))
vi.mock("@/components/features/dashboard/fleet-board", async (importActual) => ({
  ...await importActual<typeof import("@/components/features/dashboard/fleet-board")>(),
  FleetBoard: ({ cards }: { cards: { row: { crew: CrewSummary }; spendUsd: number | null }[] }) => <div data-testid="fleet">{cards.map((c) => <span key={c.row.crew.id}>{c.row.crew.name}:{String(c.spendUsd)}</span>)}</div>,
}))
vi.mock("@/components/features/dashboard/dashboard-results", () => ({
  DashboardResults: ({ onRetry, loading, error, workspaceId }: { onRetry: () => void; loading: boolean; error: boolean; workspaceId: string }) => <button onClick={onRetry} data-loading={String(loading)} data-error={String(error)} data-workspace={workspaceId}>Retry results</button>,
}))
vi.mock("@/components/features/dashboard/run-volume-chart", () => ({
  RunVolumeChart: (props: { buckets: unknown[]; series: unknown[]; window: string }) => <pre data-testid="chart">{JSON.stringify(props)}</pre>,
}))
vi.mock("@/components/features/dashboard/recipes-cards", () => ({ RecipesEmptyState: ({ workspaceId, onInstalled }: { workspaceId: string; onInstalled: () => void }) => <button onClick={onInstalled}>Install recipe in {workspaceId}</button> }))
vi.mock("@/components/features/dashboard/pages-strip", () => ({ PagesStrip: () => <div>Workspace pages</div> }))
vi.mock("@/components/features/dashboard/welcome-checklist", () => ({ WelcomeChecklist: ({ firstAgentId }: { firstAgentId: string | null }) => <div data-testid="welcome">{firstAgentId ?? "no first agent"}</div> }))

const crew = (id: string): CrewSummary => ({ id, name: `Crew ${id}`, slug: `${id}-slug`, color: "blue", icon: null })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
beforeEach(() => {
  state.workspace = { workspaceId: "a", loading: false }; state.admin = false; state.gaps = new Map(); state.events.clear()
  for (const key of ["agents", "crews", "review", "progress", "completed", "runs", "insights", "capacity", "spend", "volume"] as const) {
    state[key].data = undefined; state[key].isPending = false; state[key].isError = false; state[key].refetch.mockReset()
  }
  for (const mock of [state.fetch, state.invalidateA, state.invalidateB, state.volumeCalls, state.insightsCalls, state.agentCalls, state.routineRefresh]) mock.mockReset()
  state.fetch.mockResolvedValue(json({ completed: true }))
  window.localStorage.clear()
})
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks() })
async function loaded() { await screen.findByRole("button", { name: "Retry results" }) }

it("holds dashboard queries until onboarding answers and offers recipes for an empty workspace", async () => {
  let resolve!: (value: Response) => void
  state.fetch.mockReturnValue(new Promise<Response>((done) => { resolve = done }))
  render(<DashboardPage />)
  expect(state.agentCalls).toHaveBeenLastCalledWith("a", { enabled: false })
  expect(screen.queryByRole("button", { name: "Retry results" })).not.toBeInTheDocument()
  await act(async () => resolve(json({ completed: true })))
  await loaded()
  expect(state.agentCalls).toHaveBeenLastCalledWith("a", { enabled: true })
  fireEvent.click(screen.getByRole("button", { name: "Install recipe in a" }))
  expect(state.invalidateA).toHaveBeenCalledTimes(1)
})

it.each(["24h", "7d", "30d"])("uses the selected %s period for both insights and chart buckets", async (period) => {
  render(<DashboardPage />)
  await loaded()
  fireEvent.click(screen.getByRole("button", { name: period }))
  expect(screen.getByRole("button", { name: period })).toHaveAttribute("aria-pressed", "true")
  expect(state.volumeCalls).toHaveBeenLastCalledWith("a", { metric: "runs_count", window: period, bucket: period === "24h" ? "1h" : "1d", group_by: "crew" }, { enabled: true })
  expect(state.insightsCalls).toHaveBeenLastCalledWith("a", period, { enabled: true })
})

it.each(["pending", "failed", "empty", "metered"])("distinguishes %s spend from unmetered work", async (mode) => {
  state.spend.isPending = mode === "pending"; state.spend.isError = mode === "failed"
  state.spend.data = { rows: mode === "metered" ? [{ crew_id: "one", cost_usd: 3, call_count: 1, input_tokens: 1, output_tokens: 1 }, { crew_id: "two", cost_usd: 1, call_count: 1, input_tokens: 1, output_tokens: 1 }] : [], since: "", until: "" }
  state.insights.data = { window: "24h", totals: { total: 4, succeeded: 3, failed: 1, running: 0 }, duration: { p50_ms: 10, p95_ms: 20 }, by_trigger: [], by_model: [], by_crew: [], top_agents: [], truncated: false }
  render(<DashboardPage />)
  await loaded()
  expect(screen.getByTestId("spend")).toHaveTextContent(mode === "metered" ? "4" : mode === "empty" ? "unmetered" : "pending")
  expect(screen.getByTestId("spend")).toHaveAttribute("data-per-run", mode === "metered" ? "1" : "null")
})

it("scopes instance-wide holds to current workspace crews and counts each crew once", async () => {
  state.crews.data = [crew("one")]
  state.gaps = new Map([["credential-a", [{ crewId: "one" }, { crewId: "one" }]]])
  state.capacity.data = { enabled: true, held: [
    { crew_id: "foreign", reason: "memory", detail: "FOREIGN PRIVATE DETAIL", since: "", waited_ms: 0 },
    { crew_id: "one", reason: "memory", detail: "Current crew is waiting", since: "", waited_ms: 0 },
    { crew_id: "one", reason: "memory", detail: "Duplicate hold", since: "", waited_ms: 0 },
  ] }
  render(<DashboardPage />)
  await loaded()
  expect(screen.getByTestId("attention")).toHaveTextContent("1 crew waiting for capacity")
  expect(screen.getByTestId("attention")).not.toHaveTextContent("FOREIGN PRIVATE DETAIL")
  expect(screen.queryByRole("button", { name: /Install recipe/ })).not.toBeInTheDocument()
})

it("builds a run chart from wire buckets and resolves crew identity by id, slug, name or fallback", async () => {
  state.crews.data = [crew("one"), crew("two"), crew("three")]
  state.volume.data = { series_labels: { one: "One", "two-slug": "Two", renamed: "CREW THREE", unknown: "Other crew" }, buckets: [{ ts: "2026-10-03", series: { one: 2, "two-slug": 3, renamed: 4, unknown: 1 } }] }
  render(<DashboardPage />)
  await loaded()
  const chart = JSON.parse(screen.getByTestId("chart").textContent!)
  expect(chart.buckets).toEqual([{ ts: "2026-10-03", one: 2, "two-slug": 3, renamed: 4, unknown: 1 }])
  expect(chart.series.map((s: { key: string }) => s.key)).toEqual(["one", "two-slug", "renamed", "unknown"])
  expect(screen.getByText("10 runs")).toBeVisible()
})

it("keeps the rest of the dashboard available if metric labels are missing", async () => {
  state.volume.data = { buckets: [{ ts: "now", series: {} }] }
  render(<DashboardPage />)
  await loaded()
  expect(JSON.parse(screen.getByTestId("chart").textContent!).series).toEqual([])
  expect(screen.getByText("Workspace pages")).toBeVisible()
})

it("retries every results source together after a partial failure", async () => {
  state.review.isError = true
  state.progress.isPending = true
  render(<DashboardPage />)
  await loaded()
  const retry = screen.getByRole("button", { name: "Retry results" })
  expect(retry).toHaveAttribute("data-error", "true")
  expect(retry).toHaveAttribute("data-loading", "true")
  fireEvent.click(retry)
  for (const query of [state.review, state.progress, state.completed, state.runs]) expect(query.refetch).toHaveBeenCalledTimes(1)
  expect(state.routineRefresh).toHaveBeenCalledTimes(1)
})

it("coalesces realtime bursts and cancels a pending refresh on unmount", async () => {
  const view = render(<DashboardPage />)
  await loaded()
  vi.useFakeTimers()
  act(() => { for (const cb of state.events.values()) cb(); vi.advanceTimersByTime(219) })
  expect(state.invalidateA).not.toHaveBeenCalled()
  act(() => vi.advanceTimersByTime(1))
  expect(state.invalidateA).toHaveBeenCalledTimes(1)
  act(() => state.events.get("issue.created")!())
  view.unmount()
  act(() => vi.advanceTimersByTime(300))
  expect(state.invalidateA).toHaveBeenCalledTimes(1)
})

it("does not invalidate the former workspace when its queued realtime refresh fires", async () => {
  const view = render(<DashboardPage />)
  await loaded()
  vi.useFakeTimers()
  act(() => state.events.get("run.completed")!())
  state.workspace = { workspaceId: "b", loading: false }
  view.rerender(<DashboardPage />)
  act(() => vi.advanceTimersByTime(300))
  expect(state.invalidateA).not.toHaveBeenCalled()
  act(() => { state.events.get("run.completed")!(); vi.advanceTimersByTime(220) })
  expect(state.invalidateB).toHaveBeenCalledTimes(1)
})

it.each(["refused", "network"])("allows a loaded workspace when onboarding status is %s", async (mode) => {
  state.fetch.mockImplementation(() => mode === "refused" ? Promise.resolve(json({}, 503)) : Promise.reject(new Error("offline")))
  render(<DashboardPage />)
  await loaded()
})

it("uses the saved first agent and tolerates unavailable storage", async () => {
  vi.spyOn(window.localStorage, "getItem").mockReturnValue("agent-one")
  const view = render(<DashboardPage />)
  await loaded()
  expect(screen.getByTestId("welcome")).toHaveTextContent("agent-one")
  view.unmount()
  vi.spyOn(window.localStorage, "getItem").mockImplementation(() => { throw new Error("unavailable") })
  render(<DashboardPage />)
  await loaded()
  expect(screen.getByTestId("welcome")).toHaveTextContent("no first agent")
})

it.each([0, 1, 2, 3])("keeps the %s-crew skeleton while workspace identity is unresolved", async (count) => {
  state.workspace.loading = true
  state.crews.data = Array.from({ length: count }, (_, i) => crew(String(i)))
  const view = render(<DashboardPage />)
  await act(async () => {})
  expect(screen.queryByRole("button", { name: "Retry results" })).not.toBeInTheDocument()
  expect(view.container.querySelectorAll('[data-slot="skeleton"]').length).toBe(count ? 11 : 10)
})

it.each(["admin", "onboarding"])("redirects the %s landing without enabling dashboard reads", async (destination) => {
  const assign = vi.spyOn(window.location, "assign").mockImplementation(() => {})
  if (destination === "admin") { state.workspace.workspaceId = null; state.admin = true }
  else state.fetch.mockResolvedValue(json({ completed: false }))
  render(<DashboardPage />)
  await waitFor(() => expect(assign).toHaveBeenCalledWith(`/${destination}`))
  expect(state.agentCalls).toHaveBeenLastCalledWith(state.workspace.workspaceId, { enabled: false })
})
