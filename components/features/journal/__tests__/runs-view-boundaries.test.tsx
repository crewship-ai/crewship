import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({ push: vi.fn(), events: new Map<string, () => void>() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: mocks.push }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (event: string, cb: () => void) => { mocks.events.set(event, cb) } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
import { apiFetch } from "@/lib/api-fetch"
import { RunsView } from "../runs-view"

const ok = (body: unknown) => new Response(JSON.stringify(body), { status: 200 })
const row = (id: string, overrides: Record<string, unknown> = {}) => ({
  id, agent_id: "agent", status: "COMPLETED", trigger_type: "USER", kind: "agent",
  started_at: "2026-10-03T08:00:00Z", finished_at: "2026-10-03T08:00:05Z",
  created_at: "2026-10-03T08:00:00Z", error_message: null, exit_code: 0,
  agent_name: id, agent_slug: id, triggerer: null, ...overrides,
})
const table = (rows: ReturnType<typeof row>[], page = 1) => ({ data: rows, stats: { running: 0, today: rows.length, failed: 0 }, pagination: { page, limit: 25, total: 30, total_pages: 2 } })
const insights = (overrides: Record<string, unknown> = {}) => ({
  window: "24h", totals: { total: 10, succeeded: 7, failed: 2, running: 1 },
  duration: { p50_ms: 1000, p95_ms: 2000 },
  by_trigger: [{ key: "CRON", total: 4, failed: 1 }, { key: "CUSTOM", total: 2, failed: 0 }],
  by_model: [{ key: "provider/model", total: 10, failed: 2 }],
  by_crew: [{ id: "c1", name: "Quality", total: 10, failed: 2 }, { id: "", name: "Support", total: 10, failed: 0 }],
  truncated: true, ...overrides,
})
function kind(input: RequestInfo | URL) {
  const url = new URL(String(input), "http://localhost")
  return { url, feed: url.pathname.endsWith("insights") ? "insights" : url.searchParams.has("page") ? "table" : "live" }
}
function feeds(rows = [row("Current run")]) {
  vi.mocked(apiFetch).mockImplementation(async input => {
    const { feed, url } = kind(input)
    return ok(feed === "insights" ? insights() : feed === "live" ? { data: [] } : table(rows, Number(url.searchParams.get("page"))))
  })
}
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const props = { workspaceId: "ws-one", workspaceLoading: false }
beforeEach(() => { vi.mocked(apiFetch).mockReset(); mocks.push.mockReset(); mocks.events.clear(); feeds() })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe("RunsView request and interaction boundaries", () => {
  it("does not fetch without workspace context", () => {
    const view = render(<RunsView workspaceId={null} workspaceLoading />)
    expect(apiFetch).not.toHaveBeenCalled()
    view.rerender(<RunsView workspaceId={null} workspaceLoading={false} />)
    expect(apiFetch).not.toHaveBeenCalled()
    expect(screen.getByText("No runs yet")).toBeInTheDocument()
  })

  it.each(["http", "network", "json"])("recovers from %s table failure using Try again", async failure => {
    let failed = false
    vi.mocked(apiFetch).mockImplementation(async input => {
      const { feed } = kind(input)
      if (feed === "insights") return ok(insights())
      if (feed === "live") return ok({ data: [] })
      if (!failed) { failed = true; if (failure === "http") return new Response(null, { status: 503 }); if (failure === "network") throw new Error("offline"); return new Response("{") }
      return ok(table([row("Recovered run")]))
    })
    render(<RunsView {...props} />)
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to load runs")
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(await screen.findByText("Recovered run")).toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("keeps the table usable if optional insight and live feeds fail", async () => {
    vi.mocked(apiFetch).mockImplementation(async input => {
      if (kind(input).feed !== "table") throw new Error("offline")
      return ok(table([row("Available run")]))
    })
    render(<RunsView {...props} />)
    expect(await screen.findByText("Available run")).toBeInTheDocument()
    expect(screen.getByText("fleet idle")).toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("refreshes all three feeds from a routine completion and retains visible rows", async () => {
    render(<RunsView {...props} />)
    await screen.findByText("Current run")
    vi.mocked(apiFetch).mockClear()
    await act(async () => mocks.events.get("pipeline.run.completed")!())
    expect(apiFetch).toHaveBeenCalledTimes(3)
    expect(new Set(vi.mocked(apiFetch).mock.calls.map(([input]) => kind(input).feed))).toEqual(new Set(["table", "live", "insights"]))
    expect(screen.getByText("Current run")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(6))
  })

  it("renders breakdowns and makes row navigation independent of nested links", async () => {
    feeds([row("Detailed run", { crew_name: "Crew", crew_slug: "crew", mission_identifier: "ABC-1", model: "provider/model" })])
    render(<RunsView {...props} />)
    const name = await screen.findByText("Detailed run")
    expect(screen.getByText("aggregates cover the most recent runs in this window (cap reached)")).toBeInTheDocument()
    expect(screen.getByText("Quality")).toBeInTheDocument()
    expect(screen.getByText("Support")).toBeInTheDocument()
    const root = name.closest("[data-row-root]")!
    fireEvent.keyDown(root, { key: "Escape" }); expect(mocks.push).not.toHaveBeenCalled()
    fireEvent.keyDown(name, { key: "Enter" }); expect(mocks.push).not.toHaveBeenCalled()
    fireEvent.click(within(root as HTMLElement).getByText("Crew")); expect(mocks.push).not.toHaveBeenCalled()
    fireEvent.click(within(root as HTMLElement).getByText("ABC-1")); expect(mocks.push).not.toHaveBeenCalled()
    fireEvent.click(within(root as HTMLElement).getByLabelText("Open agent")); expect(mocks.push).not.toHaveBeenCalled()
    fireEvent.click(root)
    fireEvent.keyDown(root, { key: "Enter" })
    fireEvent.keyDown(root, { key: " " })
    expect(mocks.push).toHaveBeenCalledTimes(3)
    expect(mocks.push).toHaveBeenLastCalledWith("/journal?tab=timeline&trace_id=Detailed%20run")
  })

  it.each(["table", "insights", "live"])("does not show an abandoned workspace's %s response", async feed => {
    const old = deferred<Response>()
    vi.mocked(apiFetch).mockImplementation(async input => {
      const parsed = kind(input)
      if (parsed.url.searchParams.get("workspace_id") === "ws-one" && parsed.feed === feed) return old.promise
      return ok(parsed.feed === "table" ? table([row("New workspace run")]) : parsed.feed === "insights" ? insights({ by_crew: [] }) : { data: [] })
    })
    const view = render(<RunsView {...props} />)
    view.rerender(<RunsView {...props} workspaceId="ws-two" />)
    await screen.findByText("New workspace run")
    await act(async () => old.resolve(ok(feed === "table" ? table([row("Abandoned run")]) : feed === "insights" ? insights({ by_crew: [{ id: "old", name: "Abandoned crew", total: 1, failed: 0 }] }) : { data: [row("Abandoned live run")] })))
    expect(screen.queryByText(/Abandoned/)).not.toBeInTheDocument()
    expect(screen.getByText("New workspace run")).toBeInTheDocument()
  })

  it("clears existing workspace rows when the next workspace fails to load", async () => {
    const view = render(<RunsView {...props} />)
    await screen.findByText("Current run")
    vi.mocked(apiFetch).mockResolvedValue(new Response(null, { status: 503 }))
    view.rerender(<RunsView {...props} workspaceId="ws-two" />)
    await screen.findByRole("alert")
    expect(screen.queryByText("Current run")).not.toBeInTheDocument()
    expect(screen.queryByText("Quality")).not.toBeInTheDocument()
  })

  it("supports internal windows, status filters, trigger selection and both pagination directions", async () => {
    render(<RunsView {...props} />)
    await screen.findByText("Current run")
    fireEvent.click(screen.getByRole("button", { name: "Next" }))
    await waitFor(() => expect(screen.getByText("Showing 26–30 of 30")).toBeInTheDocument())
    fireEvent.click(screen.getByRole("button", { name: "Previous" }))
    await waitFor(() => expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled())
    fireEvent.click(screen.getByRole("button", { name: "7d" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([input]) => String(input).includes("window=7d"))).toBe(true))
    for (const status of ["Running", "Completed", "Cancelled", "Timeout", "Failed", "All"]) {
      fireEvent.click(screen.getByRole("button", { name: status }))
      await waitFor(() => expect(screen.getByRole("button", { name: status })).toHaveAttribute("aria-pressed", "true"))
      await screen.findByText("Current run")
    }
    fireEvent.pointerDown(screen.getByRole("combobox"), { button: 0, ctrlKey: false, pointerType: "mouse" })
    fireEvent.click(await screen.findByRole("option", { name: "Webhook" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([input]) => String(input).includes("trigger=WEBHOOK"))).toBe(true))
  })

  it("reports controlled trigger and page changes without mutating their values", async () => {
    const onTriggerFilterChange = vi.fn(), onPageChange = vi.fn()
    render(<RunsView {...props} page={1} onPageChange={onPageChange} triggerFilter="all" onTriggerFilterChange={onTriggerFilterChange} />)
    await screen.findByText("Current run")
    fireEvent.click(screen.getByRole("button", { name: "Next" }))
    expect(onPageChange).toHaveBeenCalledWith(2)
    fireEvent.pointerDown(screen.getByRole("combobox"), { button: 0, ctrlKey: false, pointerType: "mouse" })
    fireEvent.click(await screen.findByRole("option", { name: "System" }))
    expect(onTriggerFilterChange).toHaveBeenCalledWith("SYSTEM")
  })

  it("renders all terminal states and fallback actors, timestamps, and triggers", async () => {
    feeds([
      row("timeout", { status: "TIMEOUT", agent_name: undefined, trigger_type: "", started_at: null }),
      row("cancelled", { status: "CANCELLED", agent_name: undefined, kind: "pipeline", trigger_type: "" }),
      row("waiting", { status: "QUEUED", trigger_type: "UNRECOGNIZED" }),
      row("active", { status: "RUNNING", started_at: null }),
    ])
    render(<RunsView {...props} />)
    expect(await screen.findByText("Unknown")).toBeInTheDocument()
    expect(screen.getByText("Routine run")).toBeInTheDocument()
    expect(screen.getByText("Routine", { exact: true })).toBeInTheDocument()
    expect(screen.getByText("Unrecognized")).toBeInTheDocument()
    expect(screen.getAllByText("Timeout").length).toBeGreaterThan(1)
  })

  it("navigates live executions with mouse and keyboard and advances elapsed durations", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] })
    vi.setSystemTime(new Date("2026-10-03T08:00:10Z"))
    vi.mocked(apiFetch).mockImplementation(async input => {
      const { feed } = kind(input)
      return ok(feed === "insights" ? insights() : feed === "live" ? { data: [row("Live run", { started_at: null, crew_name: null, status: "RUNNING", trigger_type: "" })] } : table([row("Timed run", { status: "RUNNING", finished_at: null })]))
    })
    let view!: ReturnType<typeof render>
    // Settle all three feeds and their passive effects before moving time.
    // Finding the live-feed row alone does not settle the separate table feed.
    await act(async () => { view = render(<RunsView {...props} />) })
    const live = screen.getByRole("button", { name: /Live run/ })
    expect(screen.getByText("1 live execution")).toBeInTheDocument()
    expect(screen.getByText("10s")).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getByText("12s")).toBeInTheDocument()
    fireEvent.keyDown(live, { key: "Escape" }); expect(mocks.push).not.toHaveBeenCalled()
    fireEvent.keyDown(live, { key: "Enter" }); fireEvent.keyDown(live, { key: " " }); fireEvent.click(live)
    expect(mocks.push).toHaveBeenCalledTimes(3)
    expect(mocks.push).toHaveBeenLastCalledWith("/journal?tab=timeline&trace_id=Live%20run")
    view.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it("shows an empty filtered table when no rows match and tolerates missing live data", async () => {
    vi.mocked(apiFetch).mockImplementation(async input => ok(kind(input).feed === "insights" ? insights({ totals: { total: 10, succeeded: 8, failed: 2, running: 0 } }) : kind(input).feed === "live" ? {} : table([])))
    render(<RunsView {...props} statusFilter="FAILED" />)
    expect(await screen.findByText("No runs match the current filters")).toBeInTheDocument()
    expect(screen.getByText("fleet idle")).toBeInTheDocument()
  })

  it.each(["table", "insights", "live"])("ignores delayed %s bodies and aborts reads when workspace is cleared", async feed => {
    const body = deferred<unknown>()
    const signals: AbortSignal[] = []
    vi.mocked(apiFetch).mockImplementation(async (input, init) => {
      if (init?.signal) signals.push(init.signal)
      const current = kind(input).feed
      if (current === feed) return { ok: true, json: () => body.promise } as Response
      return ok(current === "table" ? table([row("Clear me")]) : current === "insights" ? insights() : { data: [] })
    })
    const view = render(<RunsView {...props} />)
    await act(async () => {})
    view.rerender(<RunsView workspaceId={null} workspaceLoading={false} />)
    expect(signals.every(signal => signal.aborted)).toBe(true)
    await act(async () => body.resolve(feed === "table" ? table([row("Late body")]) : feed === "insights" ? insights({ by_crew: [{ id: "late", name: "Late body", total: 1, failed: 0 }] }) : { data: [row("Late body")] }))
    expect(screen.queryByText("Late body")).not.toBeInTheDocument()
    expect(screen.queryByText("Clear me")).not.toBeInTheDocument()
  })

})
