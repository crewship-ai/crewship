import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

const mock = vi.hoisted(() => ({
  trace: vi.fn(),
  executions: vi.fn(),
  approval: vi.fn(),
  records: vi.fn(),
  role: "OWNER" as string | null,
  fetch: vi.fn(),
}))

vi.mock("@/hooks/use-trace", () => ({ useTrace: mock.trace }))
vi.mock("@/hooks/use-run-executions", () => ({ useRunExecutions: mock.executions }))
vi.mock("@/hooks/use-pending-approval", () => ({ usePendingApproval: mock.approval }))
vi.mock("@/hooks/use-pipeline-run-records", () => ({ usePipelineRunRecords: mock.records }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: mock.role }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: mock.fetch }))
vi.mock("@/components/features/routines/routine-step-spine", () => ({
  RoutineStepSpine: ({ title }: { title: string }) => <section aria-label="Steps">{title}</section>,
}))
vi.mock("@/components/features/routines/routine-approval-banner", () => ({
  RoutineApprovalBanner: () => <section aria-label="Approval">Approve or reject</section>,
}))
vi.mock("@/components/features/routines/routine-result-content", () => ({
  RoutineResultContent: ({ output }: { output: string }) => <p>{output}</p>,
}))
vi.mock("@/components/features/activity/typed-run-detail", () => ({
  TypedRunDetail: ({ runId }: { runId: string }) => <p>Typed detail {runId}</p>,
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { ActivityRunPage } from "../activity-run-page"
import type { ChainSummary } from "@/hooks/use-chains"

const run = (over: Record<string, unknown> = {}) => ({
  id: "run_1",
  pipeline_id: "p1",
  pipeline_slug: "repair-demo",
  pipeline_name: "repair-demo",
  status: "failed",
  mode: "run",
  started_at: new Date(Date.now() - 60_000).toISOString(),
  ended_at: new Date().toISOString(),
  current_step_id: "",
  cost_usd: 0.02,
  duration_ms: 12400,
  triggered_via: "webhook",
  triggered_by_id: "",
  invoking_crew_id: "",
  invoking_agent_id: "",
  invoking_user_id: "",
  error_message: "HTTP 502 Bad Gateway",
  failed_at_step: "post",
  issue_identifier: "",
  output: "",
  failure: { kind: "step", step_id: "post", step_name: "Post to delivery webhook", summary: "HTTP 502 Bad Gateway" },
  ...over,
})

const dsl = { steps: [{ id: "receive", name: "Receive submission" }, { id: "post", name: "Post to delivery webhook" }] }

const chain: ChainSummary = {
  origin: "run_1",
  started_by_kind: "webhook",
  started_by: "contact-form",
  routine_slug: "repair-demo",
  runs: 2,
  max_chain_depth: 1,
  failed_runs: 1,
  failed: true,
  first_activity: "2026-10-08T07:00:00Z",
  last_activity: "2026-10-08T07:00:12Z",
  duration_ms: 12000,
  issue_count: 1,
  agent_count: 1,
  issues: [{ id: "m2", identifier: "QUA-2", title: "Contact form broken", created: true }],
  agents: [{ id: "ag1", slug: "alex", name: "Alex", assignments: 1 }],
}

const graph = {
  nodes: [
    { id: "run:run_1", kind: "run", ref: "run_1", label: "repair-demo", depth: 0, chain_origin: "run_1" },
    { id: "routine:p2", kind: "routine", ref: "p2", label: "Normalize contact", depth: 1 },
    { id: "run:run_2", kind: "run", ref: "run_2", label: "normalize", depth: 2, chain_origin: "run_1", status: "completed", duration_ms: 2800 },
  ],
  edges: [
    { from: "run:run_1", to: "routine:p2", kind: "triggers" },
    { from: "routine:p2", to: "run:run_2", kind: "runs" },
  ],
}

function respond(url: string) {
  if (url.startsWith("/api/v1/chains/")) return new Response(JSON.stringify(graph))
  if (url.startsWith("/api/v1/journal/count")) return new Response(JSON.stringify({ total: 18 }))
  if (url.includes("/replay")) return new Response(JSON.stringify({ run_id: "run_3" }))
  return new Response("{}")
}

beforeEach(() => {
  vi.clearAllMocks()
  mock.role = "OWNER"
  mock.trace.mockReturnValue({ run: run(), dsl, loading: false, error: null, refresh: vi.fn() })
  mock.executions.mockReturnValue({
    rows: [],
    byStep: new Map([["receive", { latest: { status: "completed" }, rows: [], attempts: 1 }], ["post", { latest: { status: "failed" }, rows: [], attempts: 3 }]]),
    truncated: false,
    error: false,
    loading: false,
    loadMore: vi.fn(),
    refresh: vi.fn(),
  })
  mock.approval.mockReturnValue({ waitpoint: null, deciding: false, decide: vi.fn(), refresh: vi.fn(), loading: false, error: null })
  mock.records.mockReturnValue({
    records: [
      { id: "run_0", status: "completed", started_at: "2026-10-08T06:00:00Z" },
      { id: "run_1", status: "failed", started_at: "2026-10-08T07:00:00Z" },
    ],
  })
  mock.fetch.mockImplementation(async (url: string) => respond(url))
})
afterEach(cleanup)

describe("ActivityRunPage", () => {
  it("leads with the run: its name, its status in the rail's words, and what started it", () => {
    render(<ActivityRunPage workspaceId="ws" runId="run_1" chain={chain} routineName="Repair demo delivery" onOpenNode={vi.fn()} />)
    expect(screen.getByRole("heading", { name: "Repair demo delivery" })).toBeInTheDocument()
    expect(screen.getAllByText("Could not finish")[0]).toBeInTheDocument()
    expect(screen.getAllByText("Webhook").length).toBeGreaterThan(0)
    expect(screen.getByText("1 of 2")).toBeInTheDocument()
    expect(screen.getByText("$0.02")).toBeInTheDocument()
  })

  it("says why it failed above the steps", () => {
    render(<ActivityRunPage workspaceId="ws" runId="run_1" chain={chain} onOpenNode={vi.fn()} />)
    const why = screen.getByRole("region", { name: "Why it failed" })
    expect(within(why).getByText(/Post to delivery webhook/)).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Steps" })).toBeInTheDocument()
  })

  it("shows the approval instead of a banner while the run waits on a person", () => {
    mock.trace.mockReturnValue({ run: run({ status: "waiting", error_message: "", failure: null }), dsl, loading: false, error: null, refresh: vi.fn() })
    mock.approval.mockReturnValue({ waitpoint: { token: "t", timeout_at: "", prompt: "Send?" }, deciding: false, decide: vi.fn(), refresh: vi.fn() })
    render(<ActivityRunPage workspaceId="ws" runId="run_1" onOpenNode={vi.fn()} />)
    expect(screen.getByRole("region", { name: "Approval" })).toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "Why it failed" })).toBeNull()
  })

  it("nests the sub-run it caused and opens it on click", async () => {
    const onOpenNode = vi.fn()
    render(<ActivityRunPage workspaceId="ws" runId="run_1" chain={chain} onOpenNode={onOpenNode} />)
    const caused = await screen.findByRole("region", { name: "Caused by this run" })
    fireEvent.click(within(caused).getByRole("button", { name: /Normalize contact/ }))
    expect(onOpenNode).toHaveBeenCalledWith("run", "run_2")
  })

  it("links the routine, the issue and the agent, and lists the issue it created", () => {
    const onOpenNode = vi.fn()
    render(<ActivityRunPage workspaceId="ws" runId="run_1" chain={chain} routineName="Repair demo delivery" onOpenNode={onOpenNode} />)
    const linked = screen.getByRole("region", { name: "Linked to" })
    expect(within(linked).getByRole("link", { name: /Repair demo delivery/ })).toHaveAttribute("href", "/routines?slug=repair-demo&view=history")
    fireEvent.click(within(linked).getByRole("button", { name: /QUA-2/ }))
    expect(onOpenNode).toHaveBeenCalledWith("issue", "m2")
    expect(within(screen.getByRole("region", { name: "What it changed" })).getByText("Created")).toBeInTheDocument()
  })

  it("offers Retry to a manager and Stop only on a live run to an admin", async () => {
    const onOpenNode = vi.fn()
    mock.role = "MANAGER"
    render(<ActivityRunPage workspaceId="ws" runId="run_1" onOpenNode={onOpenNode} />)
    expect(screen.queryByRole("button", { name: /Stop/ })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }))
    await waitFor(() => expect(onOpenNode).toHaveBeenCalledWith("run", "run_3"))
    expect(mock.fetch.mock.calls.some(([u, o]) => String(u).endsWith("/pipelines/runs/run_1/replay") && o?.method === "POST")).toBe(true)
    cleanup()

    mock.role = "MEMBER"
    render(<ActivityRunPage workspaceId="ws" runId="run_1" onOpenNode={vi.fn()} />)
    expect(screen.queryByRole("button", { name: /Retry/ })).toBeNull()
    cleanup()

    mock.role = "ADMIN"
    mock.trace.mockReturnValue({ run: run({ status: "running", error_message: "", failure: null }), dsl, loading: false, error: null, refresh: vi.fn() })
    render(<ActivityRunPage workspaceId="ws" runId="run_1" onOpenNode={vi.fn()} />)
    expect(screen.getByRole("button", { name: /Stop/ })).toBeInTheDocument()
  })

  it("points admins at the raw events, and nobody else — the Journal is theirs", async () => {
    render(<ActivityRunPage workspaceId="ws" runId="run_1" onOpenNode={vi.fn()} />)
    expect(await screen.findByRole("link", { name: /18 raw events/ })).toHaveAttribute("href", "/journal?trace_id=run_1")
    cleanup()
    mock.role = "MANAGER"
    render(<ActivityRunPage workspaceId="ws" runId="run_1" onOpenNode={vi.fn()} />)
    expect(screen.queryByRole("link", { name: /raw events/i })).toBeNull()
  })

  it("hands a run that is not a routine run to the typed detail", () => {
    mock.trace.mockReturnValue({ run: null, dsl: null, loading: false, error: "HTTP 404", refresh: vi.fn() })
    render(<ActivityRunPage workspaceId="ws" runId="agent_run" onOpenNode={vi.fn()} />)
    expect(screen.getByText("Typed detail agent_run")).toBeInTheDocument()
  })

  it("lists an issue the run created even when no chain row carries it (#2986)", async () => {
    mock.fetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/chains/")) {
        return new Response(
          JSON.stringify({
            nodes: [
              { id: "run:run_1", kind: "run", ref: "run_1", label: "repair-demo", depth: 0 },
              { id: "issue:m9", kind: "issue", ref: "m9", label: "QUA-9", depth: 1 },
            ],
            edges: [{ from: "run:run_1", to: "issue:m9", kind: "produces" }],
          }),
        )
      }
      return respond(url)
    })
    render(<ActivityRunPage workspaceId="ws" runId="run_1" onOpenNode={vi.fn()} />)
    const changed = screen.getByRole("region", { name: "What it changed" })
    expect(await within(changed).findByText("QUA-9")).toBeInTheDocument()
    expect(within(changed).getByText("Created")).toBeInTheDocument()
  })
})
