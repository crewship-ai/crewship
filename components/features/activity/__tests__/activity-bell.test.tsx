import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import {
  ActivityBell,
  activityFooterHref,
  defaultActivityChip,
  failedRuns,
  type ActivityChip,
} from "../activity-bell"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { ActiveRunItem } from "@/hooks/use-active-runs"
import { deriveActiveRoutineRuns } from "@/hooks/use-active-routine-runs"

// Retargeted from the removed header LiveRoutinesChip (feedback
// 2026-07-02): live routine runs now surface inside the existing
// Activity dropdown — badge on the icon, LIVE + RECENT sections in
// the ~400px panel. Same data layer (useActiveRoutineRuns), same
// cancel contract, so the chip's assertions carry over.

// Hoisted holder so the vi.mock factories can read per-test state.
const h = vi.hoisted(() => ({
  runs: [] as unknown[],
  agentItems: [] as unknown[],
  refresh: vi.fn(),
  role: "OWNER" as string | null,
}))

// The status summary has its own tests (layout/__tests__/par-system-status);
// here only its place in the panel is asserted.
vi.mock("@/components/layout/activity-system-status", () => ({
  ActivitySystemStatus: () => <div data-testid="activity-system-status-stub" />,
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

// importOriginal on use-active-routine-runs drags in use-realtime →
// use-websocket; stub the realtime layer so the mock chain stays flat.
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", role: h.role }),
}))

// The dropdown consumes the shared hook — feed it the same derivation
// the provider would produce, over per-test rows.
vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return {
    ...mod,
    useActiveRoutineRuns: () => ({
      ...mod.deriveActiveRoutineRuns(h.runs as PipelineRun[]),
      recentRuns: mod.deriveRecentTerminalRuns(h.runs as PipelineRun[]),
      recentDashboardRuns: mod.deriveRecentTerminalRuns(h.runs as PipelineRun[], 12),
      loading: false,
      error: null,
      refresh: h.refresh,
    }),
  }
})

// Agent runs still come from the legacy active-runs feed.
vi.mock("@/hooks/use-active-runs", () => ({
  useActiveRuns: () => ({
    runs: h.agentItems as ActiveRunItem[],
    count: (h.agentItems as ActiveRunItem[]).length,
    loading: false,
  }),
}))

// next/link → plain anchor so href assertions are trivial.
vi.mock("next/link", () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: ({ href, children, ...rest }: any) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}))

// motion renders spread animation props into the DOM in tests; strip them.
vi.mock("motion/react", () => ({
  AnimatePresence: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  motion: {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    span: ({ children, initial: _i, animate: _a, exit: _e, ...rest }: any) => (
      <span {...rest}>{children}</span>
    ),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    div: ({ children, initial: _i, animate: _a, exit: _e, ...rest }: any) => (
      <div {...rest}>{children}</div>
    ),
  },
}))

function run(overrides: Partial<PipelineRun>): PipelineRun {
  return {
    id: "run_cmr3gd000000000000000",
    pipeline_id: "pipe-1",
    pipeline_slug: "classify-ticket",
    pipeline_name: "Classify support ticket",
    status: "running",
    mode: "run",
    started_at: new Date(Date.now() - 12_000).toISOString(),
    ended_at: "",
    current_step_id: "ask-casey",
    step_outputs: null,
    cost_usd: 0.011,
    duration_ms: 0,
    triggered_via: "manual",
    triggered_by_id: "",
    invoking_crew_id: "",
    invoking_agent_id: "",
    invoking_user_id: "",
    error_message: "",
    failed_at_step: "",
    issue_identifier: "",
    ...overrides,
  } as PipelineRun
}

function openDropdown() {
  // The panel is the shared top-bar kit (BarMenu), not a Radix dropdown:
  // a plain click on the trigger, no pointer-capture polyfill needed.
  fireEvent.click(screen.getByTestId("activity-trigger"))
}

describe("<ActivityBell> badge", () => {
  beforeEach(() => {
    h.runs = []
    h.agentItems = []
    h.role = "OWNER"
  })

  it("hides the badge when nothing is live", () => {
    render(<ActivityBell />)
    expect(screen.queryByTestId("activity-badge")).not.toBeInTheDocument()
  })

  it("badges the live routine-run count in blue", () => {
    h.runs = [run({ id: "r1" }), run({ id: "r2", pipeline_slug: "digest" })]
    render(<ActivityBell />)
    const badge = screen.getByTestId("activity-badge")
    expect(badge).toHaveTextContent("2")
    expect(badge.className).toContain("bg-primary")
  })

  it("turns the badge amber when a run awaits approval", () => {
    h.runs = [run({ id: "r1" }), run({ id: "r2", status: "waiting" })]
    render(<ActivityBell />)
    const badge = screen.getByTestId("activity-badge")
    expect(badge).toHaveTextContent("2")
    expect(badge.className).toContain("bg-warn")
  })

  it("keeps the emerald badge semantics for agent-only activity", () => {
    h.agentItems = [
      // Agent runs deep-link to the agent's chat, not the pipeline trace (#846).
      { id: "a1", kind: "agent", label: "Casey", href: "/chat/casey" },
    ]
    render(<ActivityBell />)
    const badge = screen.getByTestId("activity-badge")
    expect(badge).toHaveTextContent("1")
    expect(badge.className).toContain("bg-success")
  })
})

describe("<ActivityBell> dropdown", () => {
  beforeEach(() => {
    h.runs = []
    h.agentItems = []
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ cancel_requested: true }),
      text: async () => "",
    } as unknown as Response)
  })

  it("shows LIVE rows with current step, cost, review + trace links", async () => {
    // #1223: started_at must be pinned explicitly. The run() default
    // re-reads Date.now() per call, so r1 and r2 only tied when both
    // calls landed in the same millisecond — when the clock ticked
    // between them r2 came out newer and, since the LIVE list is sorted
    // newest-first, rendered above r1. Fixed timestamps make the order
    // a property of the fixture rather than of scheduling luck.
    h.runs = [
      run({ id: "r1", started_at: "2026-07-15T10:00:01.000Z" }),
      run({
        id: "r2",
        pipeline_slug: "approval-gate",
        pipeline_name: "Approval gate demo",
        status: "waiting",
        current_step_id: "wait-for-human",
        started_at: "2026-07-15T10:00:00.000Z",
      }),
    ]
    render(<ActivityBell />)
    openDropdown()

    await waitFor(() => {
      expect(screen.getByText("Classify support ticket")).toBeInTheDocument()
    })
    // Current step id rendered for the running row.
    expect(screen.getByText(/ask-casey/)).toBeInTheDocument()
    // Cost is on the row meta (mono, right).
    expect(screen.getAllByText(/\$0\.0110/).length).toBeGreaterThan(0)
    // The waiting row speaks the rail's words (#2988), and Decide opens the
    // run, where the approval is decidable — not the routine's definition.
    // Exact string — the header pill carries a longer count text.
    expect(screen.getByText("waiting for you")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /decide/i })).toHaveAttribute("href", "/activity?run=r2")
    // Trace deep-link per row. Asserted as a set, not by index: the
    // claim under test is "every live row deep-links to its own run",
    // which says nothing about row order — indexing in coupled this to
    // the sort and made the test fail on an ordering it never meant to
    // pin (#1223).
    const traceHrefs = screen
      .getAllByRole("link", { name: /open run/i })
      .map((el) => el.getAttribute("href"))
    expect(new Set(traceHrefs)).toEqual(
      new Set(["/activity?run=r1", "/activity?run=r2"]),
    )
  })

  it("caps LIVE at 6 rows and deep-links the footer to the active bucket", async () => {
    h.runs = Array.from({ length: 8 }, (_, i) =>
      run({
        id: `r${i}`,
        pipeline_slug: `routine-${i}`,
        pipeline_name: `Routine ${i}`,
        started_at: new Date(Date.now() - i * 1000).toISOString(),
      }),
    )
    render(<ActivityBell />)
    openDropdown()

    await waitFor(() => {
      expect(screen.getAllByRole("link", { name: /open run/i })).toHaveLength(6)
    })
    const viewAll = screen.getByRole("link", { name: /view all activity/i })
    expect(viewAll).toHaveAttribute("href", "/activity?status=active")
  })

  it("renders the RECENT section with terminal runs", async () => {
    h.runs = [
      run({ id: "live" }),
      run({
        id: "done-1",
        pipeline_slug: "cost-spike-probe",
        pipeline_name: "Cost spike probe",
        status: "completed",
        ended_at: new Date(Date.now() - 12 * 60_000).toISOString(),
        cost_usd: 0.0001,
      }),
      run({
        id: "boom-1",
        pipeline_slug: "flaky-sync",
        pipeline_name: "Flaky sync",
        status: "failed",
        ended_at: new Date(Date.now() - 30 * 60_000).toISOString(),
        cost_usd: 0.02,
      }),
    ]
    render(<ActivityBell />)
    openDropdown()
    // A run is live, so the panel opens on the Live chip; Recent is one click.
    expect(screen.queryByText("Cost spike probe")).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId("activity-chip-recent"))

    await waitFor(() => {
      expect(screen.getByText("Cost spike probe")).toBeInTheDocument()
    })
    expect(screen.getByText("Flaky sync")).toBeInTheDocument()
    expect(screen.getByText(/Completed · 12m ago · \$0\.0001/)).toBeInTheDocument()
    expect(screen.getByText(/Could not finish · 30m ago/)).toBeInTheDocument()
  })

  it("links the footer to plain /activity when nothing is live", async () => {
    h.runs = [
      run({
        id: "done-1",
        status: "completed",
        ended_at: new Date(Date.now() - 5 * 60_000).toISOString(),
      }),
    ]
    render(<ActivityBell />)
    openDropdown()

    const viewAll = await screen.findByRole("link", { name: /view all activity/i })
    expect(viewAll).toHaveAttribute("href", "/activity")
  })

  it("POSTs the workspace-scoped cancel endpoint and refreshes", async () => {
    h.runs = [run({ id: "r-cancel-me" })]
    render(<ActivityBell />)
    openDropdown()

    const cancelBtn = await screen.findByRole("button", { name: /cancel run/i })
    fireEvent.click(cancelBtn)

    await waitFor(() => {
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws-1/pipelines/runs/r-cancel-me/cancel",
        expect.objectContaining({ method: "POST" }),
      )
      expect(toast.success).toHaveBeenCalled()
      expect(h.refresh).toHaveBeenCalled()
    })
  })

  it("surfaces a permission toast when cancel returns 403", async () => {
    vi.mocked(apiFetch).mockResolvedValue({
      ok: false,
      status: 403,
      statusText: "Forbidden",
      json: async () => ({}),
      text: async () => "Forbidden",
    } as unknown as Response)
    h.runs = [run({ id: "r-denied" })]
    render(<ActivityBell />)
    openDropdown()

    const cancelBtn = await screen.findByRole("button", { name: /cancel run/i })
    fireEvent.click(cancelBtn)

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(
        "Cancel failed",
        expect.objectContaining({ description: expect.stringMatching(/permission/i) }),
      )
    })
  })
})

// Sanity: the derivation the mock uses matches what the real provider
// exports (guards against the mock drifting from the hook contract).
describe("mock parity", () => {
  it("deriveActiveRoutineRuns is importable and pure", () => {
    const d = deriveActiveRoutineRuns([run({ id: "x" })])
    expect(d.activeCount).toBe(1)
  })
})

// A RECENT row is clickable, and its meta line carries the SourcePill — which
// renders a next/link <Link> when the run was triggered by an issue. The kit
// makes a row a <button> when it has an onClick and no actions, so that row
// put an <a> inside a <button>: invalid markup, a React validateDOMNesting
// warning, and activation the browsers disagree about. The kit documents this
// rule for `actions` and this row broke it two files away.
describe("<ActivityBell> recent rows — markup", () => {
  beforeEach(() => {
    h.runs = []
    h.agentItems = []
    h.role = "OWNER"
  })

  it("never nests a link inside the clickable row", async () => {
    h.runs = [
      run({
        id: "done-issue",
        pipeline_slug: "triage",
        pipeline_name: "Triage",
        status: "completed",
        ended_at: new Date(Date.now() - 60_000).toISOString(),
        triggered_via: "issue",
        issue_identifier: "ENG-1",
      }),
    ]
    const { container } = render(<ActivityBell />)
    openDropdown()

    await waitFor(() => expect(screen.getByText("Triage")).toBeInTheDocument())
    expect(container.querySelector("button a"), "an anchor inside a button").toBeNull()
    expect(container.querySelector("a button"), "a button inside an anchor").toBeNull()
  })

  it("still reaches the run's trace from that row", async () => {
    h.runs = [
      run({
        id: "done-issue",
        pipeline_name: "Triage",
        status: "completed",
        ended_at: new Date(Date.now() - 60_000).toISOString(),
        triggered_via: "issue",
        issue_identifier: "ENG-1",
      }),
    ]
    render(<ActivityBell />)
    openDropdown()
    await waitFor(() => expect(screen.getByText("Triage")).toBeInTheDocument())

    // The row is still one control that opens the run, and it still says
    // which issue caused it — the chip just stopped being a second
    // destination competing with the row that carries it.
    const row = screen.getByRole("button", { name: /Triage/ })
    expect(row).toBeInTheDocument()
    expect(row).toHaveTextContent("ENG-1")
  })

  it("lists a stopped run under Recent in its own words and colour (#2988)", async () => {
    h.runs = [
      run({
        id: "stop-1",
        pipeline_slug: "nightly",
        pipeline_name: "Nightly",
        status: "cancelled",
        ended_at: new Date(Date.now() - 5 * 60_000).toISOString(),
      }),
      run({
        id: "stop-2",
        pipeline_slug: "sync",
        pipeline_name: "Sync",
        status: "interrupted",
        ended_at: new Date(Date.now() - 6 * 60_000).toISOString(),
      }),
    ]
    render(<ActivityBell />)
    openDropdown()
    expect(await screen.findByText(/Cancelled · 5m ago/)).toBeInTheDocument()
    expect(screen.getByText(/Interrupted · 6m ago/)).toBeInTheDocument()
    expect(document.querySelector(".bg-success")).toBeNull()
  })

  it("points the footer at the waiting bucket when something waits on a person", async () => {
    h.runs = [run({ id: "w1", status: "waiting" })]
    render(<ActivityBell />)
    openDropdown()
    const viewAll = await screen.findByRole("link", { name: /view all activity/i })
    expect(viewAll).toHaveAttribute("href", "/activity?status=waiting")
  })

  it("offers Cancel only to the roles the cancel API accepts", async () => {
    h.role = "MANAGER"
    h.runs = [run({ id: "r-live" })]
    render(<ActivityBell />)
    openDropdown()
    await screen.findByRole("link", { name: /open run/i })
    expect(screen.queryByRole("button", { name: /cancel run/i })).toBeNull()
  })
})

// The chips (approved wireframe, 2026-10): a failed run used to be one red
// word among three Recent rows, gone as soon as two newer runs finished.
describe("<ActivityBell> chips", () => {
  beforeEach(() => {
    h.runs = []
    h.agentItems = []
    h.role = "OWNER"
  })

  const ended = (mins: number) => new Date(Date.now() - mins * 60_000).toISOString()

  it("opens on Live while anything is live, and shows only that section", () => {
    h.runs = [
      run({ id: "live" }),
      run({ id: "done", pipeline_name: "Done one", status: "completed", ended_at: ended(2) }),
    ]
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByTestId("activity-chip-live")).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("activity-chip-live")).toHaveTextContent("Live (1)")
    expect(screen.getByTestId("bar-menu-section-live")).toBeInTheDocument()
    expect(screen.queryByTestId("bar-menu-section-recent")).not.toBeInTheDocument()
  })

  it("opens on Recent when nothing is live", () => {
    h.runs = [run({ id: "done", status: "completed", ended_at: ended(2) })]
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByTestId("activity-chip-recent")).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("bar-menu-section-recent")).toBeInTheDocument()
  })

  it("keeps a failure findable under Failed after newer runs succeeded", () => {
    // Three newer successes push the failure out of the old 3-row Recent; the
    // Failed chip counts over the wider window and still lists it.
    h.runs = [
      run({ id: "ok1", pipeline_slug: "a", status: "completed", ended_at: ended(1) }),
      run({ id: "ok2", pipeline_slug: "b", status: "completed", ended_at: ended(2) }),
      run({ id: "ok3", pipeline_slug: "c", status: "completed", ended_at: ended(3) }),
      run({ id: "boom", pipeline_slug: "d", pipeline_name: "Flaky sync", status: "failed", ended_at: ended(4) }),
    ]
    render(<ActivityBell />)
    openDropdown()
    const failed = screen.getByTestId("activity-chip-failed")
    expect(failed).toHaveTextContent("Failed (1)")
    fireEvent.click(failed)
    expect(screen.getByTestId("bar-menu-section-failed")).toBeInTheDocument()
    expect(screen.getByText("Flaky sync")).toBeInTheDocument()
    expect(screen.getAllByRole("button").filter((b) => /Could not finish/.test(b.textContent ?? ""))).toHaveLength(1)
    expect(screen.getByRole("link", { name: /view all activity/i })).toHaveAttribute("href", "/activity?status=failed")
  })

  it("says so when there are no failures", () => {
    h.runs = [run({ id: "ok", status: "completed", ended_at: ended(1) })]
    render(<ActivityBell />)
    openDropdown()
    fireEvent.click(screen.getByTestId("activity-chip-failed"))
    expect(screen.getByText("No recent failures")).toBeInTheDocument()
  })

  it("shows no chips on an empty panel", () => {
    render(<ActivityBell />)
    openDropdown()
    expect(screen.queryByTestId("activity-chip")).not.toBeInTheDocument()
    expect(screen.getByText("Nothing running right now")).toBeInTheDocument()
  })
})

describe("<ActivityBell> icon active state", () => {
  beforeEach(() => {
    h.runs = []
    h.agentItems = []
  })

  it.each([
    { name: "nothing live", runs: [], agents: [], active: false },
    { name: "a routine running", runs: [run({ id: "r" })], agents: [], active: true },
    { name: "a routine waiting", runs: [run({ id: "w", status: "waiting" })], agents: [], active: true },
    { name: "an agent run only", runs: [], agents: [{ id: "a", kind: "agent", label: "Casey", href: "/chat/casey" }], active: true },
    { name: "terminal runs only", runs: [run({ id: "d", status: "failed", ended_at: new Date().toISOString() })], agents: [], active: false },
  ])("$name → active=$active", ({ runs, agents, active }) => {
    h.runs = runs
    h.agentItems = agents
    render(<ActivityBell />)
    const trigger = screen.getByTestId("activity-trigger")
    if (active) {
      expect(trigger).toHaveAttribute("data-active", "true")
      expect(trigger.className).toContain("bg-primary/10")
    } else {
      expect(trigger).not.toHaveAttribute("data-active")
    }
  })
})

describe("activity chip helpers", () => {
  it.each([
    { live: 0, want: "recent" },
    { live: 1, want: "live" },
    { live: 9, want: "live" },
  ])("defaultActivityChip($live) → $want", ({ live, want }) => {
    expect(defaultActivityChip(live)).toBe(want)
  })

  it.each([
    { statuses: [], want: [] },
    { statuses: ["completed", "cancelled", "interrupted"], want: [] },
    { statuses: ["failed", "completed", "error", "FAILED"], want: ["s0", "s2", "s3"] },
  ])("failedRuns($statuses)", ({ statuses, want }) => {
    const runs = statuses.map((status, i) => run({ id: `s${i}`, status }))
    expect(failedRuns(runs).map((r) => r.id)).toEqual(want)
  })

  it.each<{ chip: ActivityChip; live: number; waiting: number; want: string }>([
    { chip: "live", live: 2, waiting: 1, want: "/activity?status=waiting" },
    { chip: "failed", live: 2, waiting: 1, want: "/activity?status=waiting" },
    { chip: "live", live: 2, waiting: 0, want: "/activity?status=active" },
    { chip: "live", live: 0, waiting: 0, want: "/activity" },
    { chip: "recent", live: 2, waiting: 0, want: "/activity" },
    { chip: "failed", live: 0, waiting: 0, want: "/activity?status=failed" },
  ])("activityFooterHref($chip, $live, $waiting) → $want", ({ chip, live, waiting, want }) => {
    expect(activityFooterHref(chip, live, waiting)).toBe(want)
  })
})

// PAR-001: the connection, fleet and container-build status that used to sit
// in the top bar as an amber chip and a Wi-Fi button live in this panel.
describe("ActivityBell status summary (PAR-001)", () => {
  it("renders the system status inside the open panel", async () => {
    render(<ActivityBell />)
    fireEvent.click(screen.getByTestId("activity-trigger"))
    expect(await screen.findByTestId("activity-system-status-stub")).toBeInTheDocument()
  })
})
