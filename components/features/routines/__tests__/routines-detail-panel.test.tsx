import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { RoutinesDetailPanel } from "../routines-detail-panel"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

// Hoisted holder so vi.mock factories can read per-test state.
const h = vi.hoisted(() => ({
  records: [] as unknown[],
  refreshRecords: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: {}, role: "OWNER", loading: false }),
}))

vi.mock("@/hooks/use-pending-approval", () => ({
  usePendingApproval: () => ({
    waitpoint: null,
    loading: false,
    error: null,
    deciding: false,
    decide: vi.fn(),
    refresh: vi.fn(),
  }),
}))

vi.mock("@/hooks/use-pipeline-run-records", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-pipeline-run-records")>()),
  usePipelineRunRecords: () => ({
    records: h.records,
    legacy: false,
    loading: false,
    error: null,
    refresh: h.refreshRecords,
  }),
}))

// Stub the heavy sub-tabs so this test stays about the header toolbar.
vi.mock("@/components/features/routines/routine-overview-tab", () => ({
  RoutineOverviewTab: () => <div data-testid="overview-tab" />,
}))
vi.mock("@/components/features/routines/routine-editor-tab", () => ({
  RoutineEditorTab: () => <div data-testid="editor-tab" />,
}))
vi.mock("@/components/features/routines/routine-runs-tab", () => ({
  RoutineRunsTab: () => <div data-testid="runs-tab" />,
}))
vi.mock("@/components/features/routines/routine-versions-tab", () => ({
  RoutineVersionsTab: () => <div data-testid="versions-tab" />,
}))
vi.mock("@/components/features/routines/routine-schedules-tab", () => ({
  RoutineSchedulesTab: () => <div data-testid="schedules-tab" />,
}))
vi.mock("@/components/features/routines/routine-webhooks-tab", () => ({
  RoutineWebhooksTab: () => <div data-testid="webhooks-tab" />,
}))
vi.mock("@/components/features/routines/routine-waitpoints-tab", () => ({
  RoutineWaitpointsTab: () => <div data-testid="waitpoints-tab" />,
}))
vi.mock("@/components/features/routines/routine-flow-diagram", () => ({
  RoutineFlowDiagram: () => <div data-testid="flow-diagram" />,
}))
vi.mock("@/components/features/routines/routine-dry-run-report", () => ({
  RoutineDryRunReport: () => <div data-testid="dry-run-report" />,
}))
vi.mock("@/components/features/routines/routine-approval-banner", () => ({
  RoutineApprovalBanner: () => <div data-testid="approval-banner" />,
}))
vi.mock("@/components/features/activity/pipeline-run-activity", () => ({
  PipelineRunActivity: () => <div data-testid="run-activity" />,
}))

const NOW = new Date().toISOString()

const ROUTINE = {
  id: "pipe-1",
  slug: "daily-report",
  name: "Daily report",
  dsl_version: "1",
  definition: {},
  definition_hash: "h",
  ephemeral: false,
  workspace_visible: true,
  invocation_count: 3,
  authored_via: "ui",
  created_at: NOW,
  updated_at: NOW,
  status: "active",
}

function activeRecord(id: string): PipelineRunRecord {
  return {
    id,
    pipeline_id: "pipe-1",
    pipeline_slug: "daily-report",
    status: "running",
    mode: "run",
    started_at: NOW,
    cost_usd: 0,
    duration_ms: 0,
    triggered_via: "manual",
  }
}

const okJSON = (body: unknown) =>
  ({
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
  }) as unknown as Response

const defaultProps = {
  workspaceId: "ws-1",
  slug: "daily-report",
  onClose: vi.fn(),
  onChanged: vi.fn(),
}

function mockApi({
  cancel = okJSON({ run_id: "run-live-1", cancel_requested: true }),
  run = okJSON({ run_id: "run-new-1", status: "running" }),
}: { cancel?: Response; run?: Response } = {}) {
  vi.mocked(apiFetch).mockImplementation(async (url, init) => {
    const u = String(url)
    if (init?.method === "POST" && u.includes("/pipelines/runs/") && u.endsWith("/cancel")) {
      return cancel
    }
    if (init?.method === "POST" && u.endsWith("/run")) {
      return run
    }
    return okJSON(ROUTINE)
  })
}

async function renderPanel() {
  render(<RoutinesDetailPanel {...defaultProps} />)
  await waitFor(() => expect(screen.getByText("Daily report")).toBeInTheDocument())
}

describe("<RoutinesDetailPanel> — header Cancel button", () => {
  beforeEach(() => {
    h.records = []
    mockApi()
  })

  it("keeps Cancel disabled when no run is active", async () => {
    await renderPanel()
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled()
  })

  it("cancels the single active run and toasts success", async () => {
    h.records = [activeRecord("run-live-1")]
    await renderPanel()
    const btn = screen.getByRole("button", { name: "Cancel" })
    expect(btn).not.toBeDisabled()
    fireEvent.click(btn)
    await waitFor(() => {
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws-1/pipelines/runs/run-live-1/cancel",
        expect.objectContaining({ method: "POST" }),
      )
      expect(toast.success).toHaveBeenCalled()
      expect(h.refreshRecords).toHaveBeenCalled()
    })
  })

  it("surfaces 403 as a permission toast", async () => {
    h.records = [activeRecord("run-live-1")]
    mockApi({
      cancel: {
        ok: false,
        status: 403,
        statusText: "Forbidden",
        json: async () => ({}),
        text: async () => "Forbidden",
      } as unknown as Response,
    })
    await renderPanel()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(
        "Cancel failed",
        expect.objectContaining({ description: expect.stringMatching(/permission/i) }),
      )
    })
  })
})

describe("<RoutinesDetailPanel> — 422 missing-integration toast", () => {
  beforeEach(() => {
    h.records = []
    mockApi({
      run: {
        ok: false,
        status: 422,
        statusText: "Unprocessable Entity",
        json: async () => ({}),
        text: async () =>
          JSON.stringify({
            missing_integrations: ["slack"],
            detail: "Slack is not connected for crew Ops",
          }),
      } as unknown as Response,
    })
  })

  it("explains the missing integration in English with a Manage integrations action", async () => {
    await renderPanel()
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    const [message, opts] = vi.mocked(toast.error).mock.calls[0] as [
      string,
      { description?: string; action?: { label: string } },
    ]
    // English copy — the Czech string is the regression.
    expect(message).not.toMatch(/Tahle|potřebuje|připojená/)
    expect(message).toMatch(/Slack/)
    expect(message).toMatch(/integration/i)
    expect(opts.description).toBe("Slack is not connected for crew Ops")
    expect(opts.action?.label).toBe("Manage integrations")
  })
})
