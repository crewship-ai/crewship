import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
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

const ended = (mins: number) => new Date(Date.now() - mins * 60_000).toISOString()
const reset = () => {
  h.runs = []
  h.agentItems = []
  h.role = "OWNER"
}

describe("<ActivityBell> badge", () => {
  beforeEach(reset)

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
    h.agentItems = [{ id: "a1", kind: "agent", label: "Casey", href: "/chat/casey" }]
    render(<ActivityBell />)
    const badge = screen.getByTestId("activity-badge")
    expect(badge).toHaveTextContent("1")
    expect(badge.className).toContain("bg-success")
  })
})

// v67 `H.POPS.actbell` (PAR-145): a 480 px panel, a fleet line, chips
// All / Waiting / Running / Could not finish, the work grouped under them with
// the action each one needs inline, and a footer with today's tally.
describe("<ActivityBell> v67 panel", () => {
  beforeEach(reset)

  const WORK = () => [
    run({ id: "w1", pipeline_name: "Weekly competitor scan", status: "waiting", started_at: ended(12) }),
    run({ id: "r1", pipeline_name: "Review pull request", current_step_id: "run-tests", started_at: ended(2) }),
    run({ id: "f1", pipeline_slug: "backlog", pipeline_name: "Nightly backlog triage", status: "failed", ended_at: ended(30), error_message: "Missing credential LINEAR_TOKEN" }),
    run({ id: "f2", pipeline_slug: "rotation", pipeline_name: "Cert rotation", status: "failed", ended_at: ended(50), error_message: "Timed out" }),
    run({ id: "d1", pipeline_slug: "digest", pipeline_name: "Digest", status: "completed", ended_at: ended(5), cost_usd: 0.25 }),
  ]

  it("is 480 px wide on a desktop and a full-width sheet under md (tablet 1024 / phone 390)", () => {
    render(<ActivityBell />)
    openDropdown()
    const cls = screen.getByTestId("activity-popover").className
    expect(cls).toContain("w-[480px]")
    expect(cls).toContain("max-md:fixed")
    expect(cls).toContain("max-md:inset-x-2")
  })

  it("groups the work: Waiting for a person, Running, Could not finish — each with its count", () => {
    h.runs = WORK()
    render(<ActivityBell />)
    openDropdown()
    for (const [label, count] of [["Waiting for a person", "1"], ["Running", "1"], ["Could not finish", "2"]]) {
      const section = screen.getByTestId(`bar-menu-section-${label.toLowerCase().replace(/\s+/g, "-")}`)
      expect(within(section).getByText(label).className).not.toMatch(/uppercase|tracking-wider/)
      expect(within(section).getByText(count)).toBeInTheDocument()
    }
  })

  it("offers the chips All / Waiting / Running / Could not finish with counts, All selected", () => {
    h.runs = WORK()
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByTestId("activity-chip-all")).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("activity-chip-all")).toHaveTextContent(/All\s*4/)
    expect(screen.getByTestId("activity-chip-waiting")).toHaveTextContent(/Waiting\s*1/)
    expect(screen.getByTestId("activity-chip-running")).toHaveTextContent(/Running\s*1/)
    expect(screen.getByTestId("activity-chip-failed")).toHaveTextContent(/Could not finish\s*2/)
  })

  it("narrows to one group when a chip is chosen", () => {
    h.runs = WORK()
    render(<ActivityBell />)
    openDropdown()
    fireEvent.click(screen.getByTestId("activity-chip-failed"))
    expect(screen.getByText("Nightly backlog triage")).toBeInTheDocument()
    expect(screen.queryByText("Weekly competitor scan")).not.toBeInTheDocument()
    expect(screen.queryByText("Review pull request")).not.toBeInTheDocument()
  })

  it("puts the action inline: Decide on a waiting run, Open on a failed one, Fix credential when the error names one", () => {
    h.runs = WORK()
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByRole("link", { name: "Decide" })).toHaveAttribute("href", "/activity?run=w1")
    expect(screen.getByRole("link", { name: "Fix credential" })).toHaveAttribute("href", "/credentials")
    expect(screen.getByRole("link", { name: "Open" })).toHaveAttribute("href", "/activity?run=f2")
  })

  it("shows the running step and the elapsed time, with no Cancel in the panel", () => {
    h.runs = WORK()
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByText(/run-tests/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /cancel/i })).toBeNull()
  })

  it("carries the connection, fleet and build status under the header (PAR-001)", () => {
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByTestId("activity-system-status-stub")).toBeInTheDocument()
  })

  it("closes the day in the footer: completed today and what it cost, then Open Activity", () => {
    h.runs = WORK()
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByTestId("activity-footer-tally")).toHaveTextContent("1 completed today · $0.25")
    expect(screen.getByRole("link", { name: /Open Activity/ })).toHaveAttribute("href", "/activity?status=waiting")
  })

  it("sets figures in sans tabular numerals, not mono", () => {
    h.runs = WORK()
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByTestId("activity-popover").querySelector(".font-mono")).toBeNull()
  })

  it("caps each group at 6 rows and points the footer at the active bucket", () => {
    h.runs = Array.from({ length: 8 }, (_, i) =>
      run({ id: `r${i}`, pipeline_slug: `routine-${i}`, pipeline_name: `Routine ${i}`, started_at: new Date(Date.now() - i * 1000).toISOString() }),
    )
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getAllByTestId("activity-row")).toHaveLength(6)
    expect(screen.getByText("+2 more running")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /Open Activity/ })).toHaveAttribute("href", "/activity?status=active")
  })

  it("lists an agent run under Running and opens it from the row", () => {
    h.agentItems = [{ id: "a1", kind: "agent", label: "Casey", sublabel: "Triage", href: "/chat/casey" }]
    render(<ActivityBell />)
    openDropdown()
    expect(within(screen.getByTestId("bar-menu-section-running")).getByText("Casey")).toBeInTheDocument()
  })

  it("says so when nothing is running", () => {
    render(<ActivityBell />)
    openDropdown()
    expect(screen.getByText("Nothing running right now")).toBeInTheDocument()
    expect(screen.queryByTestId("activity-chip")).not.toBeInTheDocument()
  })

  it("never nests a link inside a button, and the row still opens the run", () => {
    h.runs = WORK()
    const { container } = render(<ActivityBell />)
    openDropdown()
    expect(container.querySelector("button a"), "an anchor inside a button").toBeNull()
    expect(container.querySelector("a button"), "a button inside an anchor").toBeNull()
    expect(screen.getAllByTestId("activity-row")[0].closest("[role=button], button")).not.toBeNull()
  })
})

describe("<ActivityBell> icon active state", () => {
  beforeEach(reset)

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

// Sanity: the derivation the mock uses matches what the real provider
// exports (guards against the mock drifting from the hook contract).
describe("mock parity", () => {
  it("deriveActiveRoutineRuns is importable and pure", () => {
    expect(deriveActiveRoutineRuns([run({ id: "x" })]).activeCount).toBe(1)
  })
})

describe("activity chip helpers", () => {
  it("opens on All, whatever is live", () => {
    expect(defaultActivityChip(0)).toBe("all")
    expect(defaultActivityChip(3)).toBe("all")
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
    { chip: "all", live: 2, waiting: 1, want: "/activity?status=waiting" },
    { chip: "failed", live: 2, waiting: 1, want: "/activity?status=waiting" },
    { chip: "running", live: 2, waiting: 0, want: "/activity?status=active" },
    { chip: "all", live: 2, waiting: 0, want: "/activity?status=active" },
    { chip: "all", live: 0, waiting: 0, want: "/activity" },
    { chip: "failed", live: 0, waiting: 0, want: "/activity?status=failed" },
  ])("activityFooterHref($chip, $live, $waiting) → $want", ({ chip, live, waiting, want }) => {
    expect(activityFooterHref(chip, live, waiting)).toBe(want)
  })
})
