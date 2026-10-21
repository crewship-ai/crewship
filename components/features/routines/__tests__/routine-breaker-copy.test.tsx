import { describe, it, expect, vi, beforeEach } from "vitest"
import { render as renderUI, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { RoutinesDetailPanel } from "../routines-detail-panel"

// ODCH-RT-12: turning a breaker-disabled schedule back on zeroes
// consecutive_failures and disabled_reason at once (updateSchedule,
// internal/pipeline/schedules.go), not "after the next successful run".

const h = vi.hoisted(() => ({ update: vi.fn() }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-access-mode", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-access-mode")>()),
  useAccessMode: () => "trusted",
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: {}, role: "OWNER", capabilities: [], loading: false }),
}))
vi.mock("@/hooks/use-access-me", () => ({
  useAccessMe: () => ({
    access: { actions: { run: { state: "conditional", reason: "missing_role_or_capability" } } },
    loading: false,
    error: false,
    refresh: vi.fn(),
  }),
}))
vi.mock("@/hooks/use-pending-approval", () => ({
  usePendingApproval: () => ({ waitpoint: null, loading: false, error: null, deciding: false, decide: vi.fn(), refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-pipeline-run-records", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-pipeline-run-records")>()),
  usePipelineRunRecords: () => ({ records: [], legacy: false, loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({
    schedules: [
      {
        id: "sch-1",
        name: "Every morning",
        target_pipeline_id: "pipe-1",
        target_pipeline_slug: "daily-report",
        cron_expr: "0 8 * * *",
        enabled: false,
        disabled_reason: "circuit_breaker",
        consecutive_failures: 5,
      },
    ],
    update: h.update,
  }),
}))
vi.mock("@/components/features/routines/routine-runs-tab", () => ({ RoutineRunsTab: () => null }))
vi.mock("@/components/features/routines/routine-versions-tab", () => ({ RoutineVersionsTab: () => null }))
vi.mock("@/components/features/routines/routine-schedules-tab", () => ({ RoutineSchedulesTab: () => null }))
vi.mock("@/components/features/routines/routine-webhooks-tab", () => ({ RoutineWebhooksTab: () => null }))
vi.mock("@/components/features/routines/routine-flow-diagram", () => ({ RoutineFlowDiagram: () => null }))
vi.mock("@/components/features/routines/routine-dry-run-report", () => ({ RoutineDryRunReport: () => null }))
vi.mock("@/components/features/routines/routine-approval-banner", () => ({ RoutineApprovalBanner: () => null }))
vi.mock("@/components/features/activity/pipeline-run-activity", () => ({ PipelineRunActivity: () => null }))

const NOW = new Date().toISOString()
const ROUTINE = {
  id: "pipe-1", slug: "daily-report", name: "Daily report", dsl_version: "1", definition: {}, definition_hash: "h",
  ephemeral: false, workspace_visible: true, invocation_count: 3, authored_via: "ui", created_at: NOW, updated_at: NOW, status: "active",
}

function render(ui: Parameters<typeof renderUI>[0]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return renderUI(ui, { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
}

beforeEach(() => {
  cleanup()
  h.update.mockReset()
  h.update.mockResolvedValue({})
  vi.mocked(toast.success).mockClear()
  vi.mocked(apiFetch).mockImplementation(async () => ({ ok: true, status: 200, json: async () => ROUTINE, text: async () => JSON.stringify(ROUTINE) }) as unknown as Response)
})

describe("circuit-breaker banner and toast", () => {
  it("says the failure count restarts when the schedule is turned back on, not after a later success", async () => {
    render(<RoutinesDetailPanel workspaceId="ws-1" slug="daily-report" onClose={vi.fn()} onChanged={vi.fn()} />)
    const banner = await screen.findByTestId("routine-breaker")
    expect(banner).not.toHaveTextContent(/next successful run/i)
    expect(banner).toHaveTextContent(/failure count is back to zero/i)
  })

  it("the toast after turning it back on says the count is already back to zero", async () => {
    render(<RoutinesDetailPanel workspaceId="ws-1" slug="daily-report" onClose={vi.fn()} onChanged={vi.fn()} />)
    await screen.findByTestId("routine-breaker")
    fireEvent.click(screen.getByRole("button", { name: "Turn back on" }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    const [title, opts] = vi.mocked(toast.success).mock.calls[0] as [string, { description: string }]
    expect(title).toBe("Schedule is on again")
    expect(opts.description).not.toMatch(/next successful run/i)
    expect(opts.description).toMatch(/back to zero/i)
  })
})
