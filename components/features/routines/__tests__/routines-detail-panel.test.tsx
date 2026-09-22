import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { RoutinesDetailPanel } from "../routines-detail-panel"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

// Hoisted holder so vi.mock factories can read per-test state.
const h = vi.hoisted(() => ({
  access: { role: "OWNER", capabilities: [] as string[] },
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
  useAbilities: () => ({ abilities: {}, ...h.access, loading: false }),
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

beforeEach(() => { h.access.role = "OWNER"; h.access.capabilities = [] })

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
    if (
      init?.method === "POST" &&
      u.includes("/pipelines/runs/") &&
      u.endsWith("/cancel")
    ) {
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

/** Stop lives in the live-run banner and asks first. */
async function stopFromBanner() {
  fireEvent.click(screen.getByRole("button", { name: "Stop" }))
  expect(await screen.findByText("Stop this run?")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Stop run" }))
}

describe("<RoutinesDetailPanel> — live-run banner and Stop", () => {
  beforeEach(() => {
    h.records = []
    mockApi()
  })

  it("shows no banner and no Stop when no run is active", async () => {
    await renderPanel()
    expect(screen.queryByTestId("routine-live-run-banner")).toBeNull()
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull()
  })

  it("names a waiting run and offers Decide, which opens the run", async () => {
    h.records = [{ ...activeRecord("run-wait-1"), status: "waiting" }]
    const onRunStarted = vi.fn()
    render(<RoutinesDetailPanel {...defaultProps} onRunStarted={onRunStarted} />)
    await waitFor(() => expect(screen.getByText("Daily report")).toBeInTheDocument())
    const banner = screen.getByTestId("routine-live-run-banner")
    expect(banner).toHaveTextContent("A run is waiting for your decision")
    fireEvent.click(screen.getByRole("button", { name: "Decide" }))
    expect(onRunStarted).toHaveBeenCalledWith("run-wait-1")
  })

  it("stops the active run after confirming and toasts Stop requested", async () => {
    h.records = [activeRecord("run-live-1")]
    await renderPanel()
    expect(screen.getByTestId("routine-live-run-banner")).toHaveTextContent("A run is in progress")
    expect(screen.getByRole("button", { name: "Watch" })).toBeInTheDocument()
    await stopFromBanner()
    await waitFor(() => {
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws-1/pipelines/runs/run-live-1/cancel",
        expect.objectContaining({ method: "POST" }),
      )
      expect(toast.success).toHaveBeenCalledWith("Stop requested", expect.anything())
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
    await stopFromBanner()
    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(
        "Stop failed",
        expect.objectContaining({
          description: expect.stringMatching(/permission/i),
        }),
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
    fireEvent.click(screen.getByRole("dialog").querySelector("button[type=submit]")!)
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

// ── The open input form belongs to the routine that opened it ────────────
//
// This panel is a detail view over a list. Picking another row changes the
// `slug` prop under a dialog that is already open, and the submit posts to
// whatever `slug` is at that moment — so a form built from routine A's
// declared inputs could start routine B with values B never declared. Not a
// theoretical ordering: clicking a second row while the form is up is the
// obvious thing to do when you realise you opened the wrong one.
describe("<RoutinesDetailPanel> — the input form and the selected routine", () => {
  const WITH_INPUTS = {
    ...ROUTINE,
    slug: "routine-a",
    name: "Routine A",
    definition: {
      inputs: [{ name: "obdobi", type: "string", default: "2026-01" }],
    },
  }
  const OTHER = {
    ...ROUTINE,
    slug: "routine-b",
    name: "Routine B",
    definition: { inputs: [{ name: "quarter", type: "string" }] },
  }

  function mockFor(routine: typeof ROUTINE) {
    vi.mocked(apiFetch).mockImplementation(async (url, init) => {
      const u = String(url)
      if (init?.method === "POST" && u.endsWith("/run")) {
        return okJSON({ run_id: "run-x", status: "running" })
      }
      return okJSON(u.includes("routine-b") ? OTHER : routine)
    })
  }

  it("closes the form when the selection moves to another routine", async () => {
    mockFor(WITH_INPUTS)
    const { rerender } = render(
      <RoutinesDetailPanel {...defaultProps} slug="routine-a" />,
    )
    await waitFor(() => expect(screen.getByText("Routine A")).toBeInTheDocument())

    fireEvent.click(screen.getByRole("button", { name: /^Run$/ }))
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument())
    expect(screen.getByLabelText(/obdobi/i)).toBeInTheDocument()

    rerender(<RoutinesDetailPanel {...defaultProps} slug="routine-b" />)

    // Gone, rather than left showing A's fields above B's name.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("never posts a run for a routine the form was not built from", async () => {
    mockFor(WITH_INPUTS)
    const { rerender } = render(
      <RoutinesDetailPanel {...defaultProps} slug="routine-a" />,
    )
    await waitFor(() => expect(screen.getByText("Routine A")).toBeInTheDocument())
    fireEvent.click(screen.getByRole("button", { name: /^Run$/ }))
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument())

    vi.mocked(apiFetch).mockClear()
    rerender(<RoutinesDetailPanel {...defaultProps} slug="routine-b" />)
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())

    // Whatever else happened, no run was started — and in particular not
    // one addressed to routine-b carrying routine-a's obdobi.
    const runPosts = vi
      .mocked(apiFetch)
      .mock.calls.filter(
        ([u, init]) => init?.method === "POST" && String(u).endsWith("/run"),
      )
    expect(runPosts).toEqual([])
  })

  it("refuses to open a form while the loaded routine is not the selected one", async () => {
    // fetchRoutine sets `loading` but leaves `routine` on the previous one
    // until the response lands, and the toolbar renders under
    // `{routine && …}` — so for the width of that fetch the Run button is
    // live while the panel still shows the last routine's definition.
    // Opening there builds the form from A's inputs and posts it to B.
    let release: () => void = () => {}
    vi.mocked(apiFetch).mockImplementation(async (url, init) => {
      const u = String(url)
      if (init?.method === "POST" && u.endsWith("/run")) {
        return okJSON({ run_id: "run-x", status: "running" })
      }
      if (u.includes("routine-b")) {
        // Hold routine B's fetch open: `slug` is already B, `routine` is
        // still A. That window is the bug.
        await new Promise<void>((r) => {
          release = r
        })
        return okJSON(OTHER)
      }
      return okJSON(WITH_INPUTS)
    })

    const { rerender } = render(
      <RoutinesDetailPanel {...defaultProps} slug="routine-a" />,
    )
    await waitFor(() => expect(screen.getByText("Routine A")).toBeInTheDocument())

    rerender(<RoutinesDetailPanel {...defaultProps} slug="routine-b" />)
    // A's toolbar is still on screen, under B's slug.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /^Run$/ })).toBeInTheDocument(),
    )
    fireEvent.click(screen.getByRole("button", { name: /^Run$/ }))

    // No form, and nothing started. Opening one here could only be wrong.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    const runPosts = vi
      .mocked(apiFetch)
      .mock.calls.filter(
        ([u, init]) => init?.method === "POST" && String(u).endsWith("/run"),
      )
    expect(runPosts).toEqual([])

    release()
  })

  it("still runs the selected routine with its own inputs", async () => {
    // The guard must not cost the ordinary path.
    mockFor(WITH_INPUTS)
    render(<RoutinesDetailPanel {...defaultProps} slug="routine-a" />)
    await waitFor(() => expect(screen.getByText("Routine A")).toBeInTheDocument())

    fireEvent.click(screen.getByRole("button", { name: /^Run$/ }))
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText(/obdobi/i), {
      target: { value: "2026-07" },
    })
    fireEvent.click(screen.getByRole("dialog").querySelector("button[type=submit]")!)

    await waitFor(() => {
      const post = vi
        .mocked(apiFetch)
        .mock.calls.find(
          ([u, init]) => init?.method === "POST" && String(u).endsWith("/run"),
        )
      expect(post).toBeTruthy()
      expect(String(post![0])).toContain("/pipelines/routine-a/run")
      expect(new Headers(post![1]?.headers).get("Prefer")).toBe("respond-async")
      expect(new Headers(post![1]?.headers).get("Idempotency-Key")).toBeTruthy()
      expect(JSON.parse((post![1] as RequestInit).body as string)).toEqual({
        inputs: { obdobi: "2026-07" },
      })
    })
  })
})

describe("routine start delivery", () => {
  it("reuses the start key after a lost response and renews it after confirmation", async () => {
    vi.clearAllMocks()
    h.records = []
    const post = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("Response lost"))
      .mockResolvedValue(okJSON({ run_id: "recovered-run", status: "running" }))
    vi.mocked(apiFetch).mockImplementation(async (url, init) => {
      if (init?.method === "POST" && String(url).endsWith("/run")) return post(url, init)
      return okJSON(ROUTINE)
    })
    await renderPanel()
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    fireEvent.click(screen.getByRole("dialog").querySelector("button[type=submit]")!)
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Run" })).not.toBeDisabled(),
    )
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    fireEvent.click(screen.getByRole("dialog").querySelector("button[type=submit]")!)
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    const key = (i: number) =>
      new Headers(post.mock.calls[i][1].headers).get("Idempotency-Key")
    expect(key(0)).toBeTruthy()
    expect(key(1)).toBe(key(0))
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Run" })).not.toBeDisabled(),
    )
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    fireEvent.click(screen.getByRole("dialog").querySelector("button[type=submit]")!)
    await waitFor(() => expect(post).toHaveBeenCalledTimes(3))
    expect(key(2)).not.toBe(key(0))
  })

  it("sends one request for two submissions before React rerenders", async () => {
    vi.clearAllMocks()
    h.records = []
    let finish!: (r: Response) => void
    const post = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve
        }),
    )
    vi.mocked(apiFetch).mockImplementation(async (url, init) => {
      if (init?.method === "POST" && String(url).endsWith("/run")) return post()
      return okJSON(ROUTINE)
    })
    await renderPanel()
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    const run = screen
      .getByRole("dialog")
      .querySelector<HTMLButtonElement>("button[type=submit]")!
    act(() => {
      run.click()
      run.click()
    })
    expect(post).toHaveBeenCalledTimes(1)
    await act(async () => {
      finish(okJSON({ run_id: "one-run" }))
    })
  })
})

it.each(["missing run ID", "unreadable JSON"])(
  "does not confirm a start with %s",
  async (failure) => {
    vi.clearAllMocks()
    h.records = []
    const invalid =
      failure === "missing run ID"
        ? okJSON({})
        : ({
            ok: true,
            json: async () => {
              throw new SyntaxError("Unreadable response")
            },
          } as unknown as Response)
    const post = vi
      .fn()
      .mockResolvedValueOnce(invalid)
      .mockResolvedValue(okJSON({ run_id: "recovered" }))
    vi.mocked(apiFetch).mockImplementation(async (url, init) => {
      if (init?.method === "POST" && String(url).endsWith("/run")) return post(url, init)
      return okJSON(ROUTINE)
    })
    await renderPanel()
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    fireEvent.click(screen.getByRole("dialog").querySelector("button[type=submit]")!)
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(toast.success).not.toHaveBeenCalled()
    expect(defaultProps.onChanged).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Run" }))
    fireEvent.click(screen.getByRole("dialog").querySelector("button[type=submit]")!)
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    const key = (i: number) =>
      new Headers(post.mock.calls[i][1].headers).get("Idempotency-Key")
    expect(key(1)).toBe(key(0))
  },
)

describe("<RoutinesDetailPanel> — a slug with a draft and no published routine", () => {
  // A copy, a draft the lead saved, or one saved from the CLI: the routine
  // does not exist yet (404), but its draft does. The page is built from the
  // draft so it can be read and published; Run waits for the first version.
  const notFound = () =>
    ({ ok: false, status: 404, json: async () => ({ error: "not found" }), text: async () => "not found" }) as unknown as Response
  const DRAFT = {
    id: "drf_copy",
    slug: "daily-report-copy",
    revision: 1,
    base_pipeline_id: "",
    base_revision: 0,
    updated_at: "2026-09-15T10:00:00Z",
    document: {
      slug: "daily-report-copy",
      name: "Daily report (copy)",
      description: "Copied from Daily report.",
      definition: { name: "daily-report-copy", display_name: "Daily report (copy)", steps: [{ id: "a", type: "transform", transform: { input: "x", expression: "." } }] },
      author_crew_id: "crew-1",
    },
  }

  beforeEach(() => {
    h.records = []
    vi.mocked(apiFetch).mockImplementation(async (url) => {
      const u = String(url)
      if (u.endsWith("/pipelines/daily-report-copy/draft")) return okJSON(DRAFT)
      if (u.endsWith("/pipelines/daily-report-copy")) return notFound()
      return okJSON([])
    })
  })

  it("renders the draft as the routine, disables Run and offers Publish", async () => {
    render(<RoutinesDetailPanel {...defaultProps} slug="daily-report-copy" />)
    expect(await screen.findByText("Daily report (copy)")).toBeInTheDocument()
    expect(screen.queryByText(/fetch routine: 404/)).toBeNull()
    const run = screen.getByRole("button", { name: /^Run$/ })
    expect(run).toBeDisabled()
    expect(run.closest("span")).toHaveAttribute("title", expect.stringMatching(/Publish the draft first/))
    expect(screen.getByRole("button", { name: /Publish draft r1/ })).toBeInTheDocument()
  })

  it("still reports a real 404 when there is no draft either", async () => {
    vi.mocked(apiFetch).mockImplementation(async (url) => {
      const u = String(url)
      if (u.endsWith("/draft")) return okJSON({ id: "", slug: "gone", revision: 0, base_pipeline_id: "", base_revision: 0, document: {} })
      if (u.endsWith("/pipelines/gone")) return notFound()
      return okJSON([])
    })
    render(<RoutinesDetailPanel {...defaultProps} slug="gone" />)
    expect(await screen.findByText(/fetch routine: 404/)).toBeInTheDocument()
  })

  it.each([403, 500, "network"])("preserves a draft lookup failure (%s) instead of reporting not found", async (status) => {
    vi.mocked(apiFetch).mockImplementation(async (url) => {
      if (String(url).endsWith("/draft")) {
        if (status === "network") throw new Error("Draft network unavailable")
        return { ok: false, status, json: async () => ({ error: `Draft request failed: ${status}` }) } as Response
      }
      if (String(url).endsWith("/pipelines/gone")) return notFound()
      return okJSON([])
    })
    render(<RoutinesDetailPanel {...defaultProps} slug="gone" />)
    expect(await screen.findByText(status === "network" ? "Draft network unavailable" : `Draft request failed: ${status}`)).toBeInTheDocument()
    expect(screen.queryByText(/fetch routine: 404/)).toBeNull()
  })
})


describe("routine Run permission", () => {
  it.each([
    ["MEMBER", [], false],
    ["VIEWER", [], false],
    ["VIEWER", ["routine.run"], true],
    ["MEMBER", ["routine.create"], false],
    ["MANAGER", [], true],
  ] as const)("%s with %j", async (role, caps, allowed) => {
    h.access.role = role; h.access.capabilities = [...caps]; h.records = []
    mockApi(); await renderPanel()
    const run = screen.getByRole("button", { name: "Run" })
    expect(run).toHaveProperty("disabled", !allowed)
    if (!allowed) {
      expect(run.parentElement).toHaveAttribute("title", expect.stringContaining("permission"))
      fireEvent.click(run)
      expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
    }
  })
})
