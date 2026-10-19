import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { AgentSummary, CrewSummary, CrewSpendResponse, RunInsightsResponse, RuntimeCapacityResponse, TimeseriesResponse } from "../dashboard-types"
import type { ProvisioningSummaryResponse } from "@/hooks/use-dashboard-data"
import { useAppStore } from "@/lib/store"
import DashboardPage from "../page"

type Query<T> = { data: T | undefined | null; isPending: boolean; isError: boolean; refetch: ReturnType<typeof vi.fn> }
const state = vi.hoisted(() => {
  const query = <T,>(): Query<T> => ({ data: undefined, isPending: false, isError: false, refetch: vi.fn() })
  return {
    workspace: { workspaceId: "a" as string | null, loading: false }, admin: false as boolean | null,
    session: { data: { user: { name: "Pavel Srba" } } as { user: { name: string } } | null, status: "authenticated" },
    fetch: vi.fn(), events: new Map<string, () => void>(), invalidateA: vi.fn(), invalidateB: vi.fn(), volumeCalls: vi.fn(), insightsCalls: vi.fn(), agentCalls: vi.fn(), routineRefresh: vi.fn(),
    agents: query<AgentSummary[]>(), crews: query<CrewSummary[]>(), review: query<unknown[]>(), progress: query<unknown[]>(), completed: query<unknown[]>(), runs: query<unknown[]>(),
    insights: query<RunInsightsResponse>(), capacity: query<RuntimeCapacityResponse>(), provisioning: query<ProvisioningSummaryResponse>(), credentials: query<unknown[]>(), spend: query<CrewSpendResponse>(), volume: query<Partial<TimeseriesResponse>>(),
    gaps: new Map<string, { crewId: string }[]>(),
    inbox: { items: [] as unknown[], loading: false, error: null as string | null, activeByKind: {} as Record<string, number>, decisionCount: 0, refresh: vi.fn() },
    schedules: [] as unknown[],
    realtime: "connected" as "connecting" | "connected" | "disconnected" | "error" | "unavailable",
  }
})
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => state.workspace }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => state.admin, useSessionSafe: () => state.session }))
vi.mock("@/lib/server-base", () => ({ serverFetch: (...args: unknown[]) => state.fetch(...args) }))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (event: string, callback: () => void) => { state.events.set(event, callback) },
  useRealtime: () => ({ status: state.realtime, subscribe: () => () => {}, subscribeChannel: () => () => {} }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-dashboard-data", () => ({
  useAgentSummaries: (...args: unknown[]) => { state.agentCalls(...args); return state.agents },
  useCrewSummaries: () => state.crews,
  useDashboardResults: (_ws: string, kind: string) => kind === "REVIEW" ? state.review : kind === "IN_PROGRESS" ? state.progress : state.completed,
  useDashboardActiveRuns: () => state.runs,
  useRunsInsights: (...args: unknown[]) => { state.insightsCalls(...args); return state.insights },
  useRuntimeCapacity: () => state.capacity,
  useProvisioningSummary: () => state.provisioning,
  useDashboardCredentials: () => state.credentials,
  useMetricsTimeseries: (...args: unknown[]) => { state.volumeCalls(...args); return state.volume },
  useCrewSpend: () => state.spend,
  useCrewServiceSummaries: () => ({ byCrew: new Map() }),
  useInvalidateDashboard: (ws: string) => ws === "a" ? state.invalidateA : state.invalidateB,
}))
vi.mock("@/hooks/use-active-routine-runs", () => ({ useActiveRoutineRuns: () => ({ runs: [], recentDashboardRuns: [], loading: false, error: null, refresh: state.routineRefresh }) }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({ usePipelineSchedules: () => ({ schedules: state.schedules }) }))
vi.mock("@/hooks/use-credential-readiness", () => ({ useCredentialReadiness: () => ({ gapsByCredential: state.gaps }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInbox: () => state.inbox }))
vi.mock("@/components/features/dashboard/dashboard-overview", async (importActual) => ({
  ...await importActual<typeof import("@/components/features/dashboard/dashboard-overview")>(),
  AttentionTiles: ({ items }: { items: { id: string; label: string; detail: string }[] }) => <div data-testid="attention">{items.map((item) => <p key={item.id}>{item.label}: {item.detail}</p>)}</div>,
  DashboardKpis: ({ spendUsd, spendPerRun }: { spendUsd: number | null | undefined; spendPerRun: number | null }) => <div data-testid="spend" data-per-run={String(spendPerRun)}>{spendUsd === undefined ? "pending" : spendUsd === null ? "unmetered" : String(spendUsd)}</div>,
  DashboardUpNext: () => <div>Upcoming routines</div>,
}))
vi.mock("@/components/features/dashboard/fleet-board", async (importActual) => ({
  ...await importActual<typeof import("@/components/features/dashboard/fleet-board")>(),
  FleetBoard: ({ cards }: { cards: { row: { crew: CrewSummary } }[] }) => <div data-testid="fleet">{cards.map((c) => <span key={c.row.crew.id}>{c.row.crew.name}</span>)}</div>,
}))
vi.mock("@/components/features/dashboard/dashboard-results", () => ({
  DashboardResults: ({ onRetry, loading, error, workspaceId }: { onRetry: () => void; loading: boolean; error: boolean; workspaceId: string }) => <button onClick={onRetry} data-loading={String(loading)} data-error={String(error)} data-workspace={workspaceId}>Retry results</button>,
}))
vi.mock("@/components/features/dashboard/run-volume-chart", () => ({
  RunVolumeChart: (props: { buckets: unknown[]; series: unknown[]; window: string }) => <pre data-testid="chart">{JSON.stringify(props)}</pre>,
}))
vi.mock("@/components/features/dashboard/dashboard-empty", () => ({ DashboardEmpty: ({ workspaceId, onInstalled }: { workspaceId: string; onInstalled: () => void }) => <div data-state="empty"><h2>Your workspace is ready</h2><button onClick={onInstalled}>Install recipe in {workspaceId}</button></div> }))
vi.mock("@/components/features/dashboard/pages-strip", () => ({ PagesStrip: () => <div>Workspace pages</div> }))
vi.mock("@/components/features/dashboard/welcome-checklist", () => ({ WelcomeChecklist: ({ firstAgentId }: { firstAgentId: string | null }) => <div data-testid="welcome">{firstAgentId ?? "no first agent"}</div> }))

const crew = (id: string): CrewSummary => ({ id, name: `Crew ${id}`, slug: `${id}-slug`, color: "blue", icon: null })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
beforeEach(() => {
  state.workspace = { workspaceId: "a", loading: false }; state.admin = false; state.gaps = new Map(); state.events.clear()
  state.session = { data: { user: { name: "Pavel Srba" } }, status: "authenticated" }
  state.inbox = { items: [], loading: false, error: null, activeByKind: {}, decisionCount: 0, refresh: vi.fn() }
  state.schedules = []
  state.realtime = "connected"
  for (const key of ["agents", "crews", "review", "progress", "completed", "runs", "insights", "capacity", "provisioning", "credentials", "spend", "volume"] as const) {
    state[key].data = undefined; state[key].isPending = false; state[key].isError = false; state[key].refetch.mockReset()
  }
  // A workspace with a crew renders the dashboard; tests of the empty
  // workspace clear it.
  state.crews.data = [crew("one")]
  for (const mock of [state.fetch, state.invalidateA, state.invalidateB, state.volumeCalls, state.insightsCalls, state.agentCalls, state.routineRefresh]) mock.mockReset()
  state.fetch.mockResolvedValue(json({ completed: true }))
  window.localStorage.clear()
})
afterEach(() => { cleanup(); act(() => useAppStore.getState().setPageBarSlot(null)); vi.useRealTimers(); vi.restoreAllMocks() })
async function loaded() { await screen.findByRole("button", { name: "Retry results" }) }

it("holds dashboard queries until onboarding answers", async () => {
  let resolve!: (value: Response) => void
  state.fetch.mockReturnValue(new Promise<Response>((done) => { resolve = done }))
  render(<DashboardPage />)
  expect(state.agentCalls).toHaveBeenLastCalledWith("a", { enabled: false })
  expect(screen.queryByRole("button", { name: "Retry results" })).not.toBeInTheDocument()
  await act(async () => resolve(json({ completed: true })))
  await loaded()
  expect(state.agentCalls).toHaveBeenLastCalledWith("a", { enabled: true })
})

it("zero crews renders one EmptyState and no section cards", async () => {
  state.crews.data = []
  const view = render(<DashboardPage />)
  await screen.findByText("Your workspace is ready")
  expect(view.container.querySelectorAll('[data-state="empty"]')).toHaveLength(1)
  for (const gone of ["Retry results", "Upcoming routines", "Workspace pages"]) expect(screen.queryByText(gone)).not.toBeInTheDocument()
  expect(screen.queryByTestId("attention")).not.toBeInTheDocument()
  expect(screen.queryByTestId("dashboard-greeting")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Install recipe in a" }))
  expect(state.invalidateA).toHaveBeenCalledTimes(1)
})

it.each(["24h", "7d", "30d"])("uses the selected %s period for both insights and chart buckets", async (period) => {
  render(<DashboardPage />)
  await loaded()
  fireEvent.click(screen.getByRole("radio", { name: period }))
  expect(screen.getByRole("radio", { name: period })).toHaveAttribute("aria-checked", "true")
  expect(state.volumeCalls).toHaveBeenCalledWith("a", { metric: "runs_count", window: period, bucket: period === "24h" ? "1h" : "1d", group_by: "crew" }, { enabled: true })
  expect(state.insightsCalls).toHaveBeenLastCalledWith("a", period, { enabled: true })
  expect(screen.getByRole("region", { name: `Run volume · ${period} · by crew` })).toBeInTheDocument()
})

it("no DashboardPeriodBar; window segment rendered into top-bar actions", async () => {
  const slot = document.createElement("div")
  slot.dataset.areaTitle = "Dashboard"
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  const view = render(<DashboardPage />)
  await loaded()
  const segment = within(slot).getByRole("radiogroup", { name: "Dashboard time window" })
  // The kit Segmented (COM-02), not a local copy: one radio group whose
  // arrow keys move and select.
  expect(segment).toHaveAttribute("data-slot", "segmented")
  expect(within(segment).getAllByRole("radio").map((b) => b.textContent)).toEqual(["24h", "7d", "30d"])
  fireEvent.keyDown(within(segment).getByRole("radio", { name: "24h" }), { key: "ArrowRight" })
  expect(within(segment).getByRole("radio", { name: "7d" })).toHaveAttribute("aria-checked", "true")
  // Nothing sticky of the page's own above the content, and the page body
  // carries no window control of its own.
  expect(view.container.querySelector(".sticky")).toBeNull()
  expect(within(view.container).queryByRole("radiogroup", { name: "Dashboard time window" })).toBeNull()
  slot.remove()
})

it("keeps the window in the top bar while the page is still loading", async () => {
  state.workspace.loading = true
  const view = render(<DashboardPage />)
  await act(async () => {})
  expect(screen.getByRole("radiogroup", { name: "Dashboard time window" })).toBeInTheDocument()
  expect(view.container.querySelector('[data-state="loading"]')).not.toBeNull()
  expect(screen.queryByRole("button", { name: "Retry results" })).not.toBeInTheDocument()
})

it("slow load says Still loading after 10 s and Retry reads agents and crews again", async () => {
  vi.useFakeTimers()
  state.crews.isPending = true
  render(<DashboardPage />)
  await act(async () => {})
  expect(screen.queryByText("Still loading…")).not.toBeInTheDocument()
  await act(async () => { vi.advanceTimersByTime(10_000) })
  expect(screen.getByText("Still loading…")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(state.crews.refetch).toHaveBeenCalledTimes(1)
  expect(state.agents.refetch).toHaveBeenCalledTimes(1)
})

it("the greeting is the page's one h1; the top bar title is not a heading", async () => {
  const slot = document.createElement("div")
  slot.dataset.areaTitle = "Dashboard"
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  render(<DashboardPage />)
  await loaded()
  const h1s = screen.getAllByRole("heading", { level: 1 })
  expect(h1s).toHaveLength(1)
  expect(h1s[0]).toBe(screen.getByTestId("dashboard-greeting"))
  slot.remove()
})

it("greeting by local time and meta omits zero parts", async () => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(new Date(2026, 9, 15, 9, 12))
  state.agents.data = [
    { id: "x", name: "X", slug: "x", role_title: null, agent_role: "worker", status: "RUNNING", crew: null, crew_id: "one", _count: { skills: 0, credentials: 0, chats: 0 } },
    { id: "y", name: "Y", slug: "y", role_title: null, agent_role: "worker", status: "IDLE", crew: null, crew_id: "one", _count: { skills: 0, credentials: 0, chats: 0 } },
  ]
  state.schedules = [{ id: "s", name: "Digest", enabled: true, next_run_at: new Date(2026, 9, 15, 9, 30).toISOString() }]
  state.inbox.decisionCount = 2
  render(<DashboardPage />)
  await loaded()
  expect(screen.getByTestId("dashboard-greeting")).toHaveTextContent("Good morning, Pavel")
  expect(screen.getByText("Thursday 15 October · 1 thing needs you · 1 agent running · next routine in 18 min")).toBeInTheDocument()
})

it("says nothing needs you only when the inbox answered, and drops empty parts", async () => {
  state.session = { data: { user: { name: "" } }, status: "authenticated" }
  const view = render(<DashboardPage />)
  await loaded()
  const meta = screen.getByTestId("dashboard-greeting").nextElementSibling!
  expect(meta.textContent).toMatch(/ · Nothing needs you$/)
  expect(screen.getByTestId("dashboard-greeting").textContent).toMatch(/^Good (morning|afternoon|evening)$/)
  view.unmount()
  state.inbox = { ...state.inbox, error: "403" }
  render(<DashboardPage />)
  await loaded()
  expect(screen.getByTestId("dashboard-greeting").nextElementSibling!.textContent).not.toContain("Nothing needs you")
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

it("part failure replaces only that card", async () => {
  state.insights.data = null
  state.volume.data = null
  render(<DashboardPage />)
  await loaded()
  expect(screen.getByText("Could not load the run summary")).toBeInTheDocument()
  expect(screen.getByText("Could not load run volume")).toBeInTheDocument()
  expect(screen.queryByTestId("spend")).not.toBeInTheDocument()
  expect(screen.queryByTestId("chart")).not.toBeInTheDocument()
  // The rest of the page is still there.
  expect(screen.getByText("Workspace pages")).toBeVisible()
  expect(screen.getByText("Upcoming routines")).toBeVisible()
  const retries = screen.getAllByRole("button", { name: "Retry" })
  for (const b of retries) fireEvent.click(b)
  expect(state.insights.refetch).toHaveBeenCalledTimes(1)
  expect(state.volume.refetch).toHaveBeenCalledTimes(1)
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
  expect(screen.queryByText("Your workspace is ready")).not.toBeInTheDocument()
})

it("says which crews need a rebuild, after capacity, with the first crew's name", async () => {
  state.crews.data = [crew("one")]
  state.capacity.data = { enabled: true, held: [{ crew_id: "one", reason: "memory", detail: "Current crew is waiting", since: "", waited_ms: 0 }] }
  state.provisioning.data = {
    failed: [{ id: "c9", slug: "web", name: "Web", error: "boom" }],
    needs_provision: [{ id: "c8", slug: "data", name: "Data" }],
    total: 2,
  }
  render(<DashboardPage />)
  await loaded()
  const text = screen.getByTestId("attention").textContent ?? ""
  expect(text).toContain("2 crews need a rebuild: Web")
  expect(text.indexOf("waiting for capacity")).toBeLessThan(text.indexOf("need a rebuild"))
})

it("asks for Success, P95 and Spend series for the same window as the chart", async () => {
  render(<DashboardPage />)
  await loaded()
  for (const metric of ["success_rate", "p95_duration_ms", "cost_usd"]) {
    expect(state.volumeCalls).toHaveBeenCalledWith("a", { metric, window: "24h", bucket: "1h", group_by: "none" }, { enabled: true })
  }
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

it.each(["admin", "onboarding"])("redirects the %s landing without enabling dashboard reads", async (destination) => {
  const assign = vi.spyOn(window.location, "assign").mockImplementation(() => {})
  if (destination === "admin") { state.workspace.workspaceId = null; state.admin = true }
  else state.fetch.mockResolvedValue(json({ completed: false }))
  const view = render(<DashboardPage />)
  await waitFor(() => expect(assign).toHaveBeenCalledWith(`/${destination}`))
  expect(state.agentCalls).toHaveBeenLastCalledWith(state.workspace.workspaceId, { enabled: false })
  // Nothing of the dashboard flashes before the redirect.
  expect(view.container.querySelector('[data-testid="attention"]')).toBeNull()
})

// DSH-01 (#3483): offline = one "Reconnecting…" banner (kit OfflineBanner)
// and the data under it dimmed and not clickable (kit StaleData), as v67
// states.js draws the Offline state. A blip under 3 s is not offline — the
// shell's realtime banner waits the same 3 s.
it.each(["disconnected", "error"] as const)("offline (%s) for 3 s shows one Reconnecting banner and dims the data", async (status) => {
  const view = render(<DashboardPage />)
  await loaded()
  vi.useFakeTimers({ now: new Date(2026, 9, 15, 14, 2) })
  state.realtime = status
  view.rerender(<DashboardPage />)
  act(() => vi.advanceTimersByTime(2_999))
  expect(view.container.querySelector('[data-state="offline"]')).toBeNull()
  expect(view.container.querySelector(".page-stale")).toBeNull()
  act(() => vi.advanceTimersByTime(1))
  const banners = view.container.querySelectorAll('[data-state="offline"]')
  expect(banners).toHaveLength(1)
  expect(banners[0]).toHaveTextContent("Reconnecting… Live updates are paused. You see data from 14:02.")
  const stale = view.container.querySelector(".page-stale")!
  expect(stale).not.toBeNull()
  expect(stale).toHaveAttribute("inert")
  // The whole data state is under it: head, attention, results, cards.
  for (const id of ["dashboard-greeting", "attention", "fleet"]) expect(stale.contains(screen.getByTestId(id))).toBe(true)
  expect(stale.contains(screen.getByRole("button", { name: "Retry results", hidden: true }))).toBe(true)
  expect(stale.contains(screen.getByText("Workspace pages"))).toBe(true)
  // The banner itself is not dimmed.
  expect(stale.contains(banners[0])).toBe(false)
})

it("Retry now reads the dashboard again; reconnecting lifts the dimming", async () => {
  const view = render(<DashboardPage />)
  await loaded()
  vi.useFakeTimers()
  state.realtime = "disconnected"
  view.rerender(<DashboardPage />)
  act(() => vi.advanceTimersByTime(3_000))
  fireEvent.click(screen.getByRole("button", { name: "Retry now" }))
  expect(state.invalidateA).toHaveBeenCalledTimes(1)
  state.realtime = "connected"
  view.rerender(<DashboardPage />)
  expect(view.container.querySelector('[data-state="offline"]')).toBeNull()
  expect(view.container.querySelector(".page-stale")).toBeNull()
})

it.each(["connecting", "unavailable"] as const)("%s realtime never dims the dashboard", async (status) => {
  state.realtime = status
  const view = render(<DashboardPage />)
  await loaded()
  vi.useFakeTimers()
  act(() => vi.advanceTimersByTime(10_000))
  expect(view.container.querySelector('[data-state="offline"]')).toBeNull()
  expect(view.container.querySelector(".page-stale")).toBeNull()
})
