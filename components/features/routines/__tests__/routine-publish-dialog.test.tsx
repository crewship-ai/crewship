// Publish a draft: the review, the static check, the impact counts, and the
// two server calls that make it live — test_run for the token, publish with
// the draft's exact id and revision.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RoutinePublishDialog, publicationSummary, scheduleImpact } from "../routine-publish-dialog"

const h = vi.hoisted(() => ({ fetcher: vi.fn(), schedules: [] as unknown[] }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "usr_me" } }, status: "authenticated" }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({ schedules: h.schedules, loading: false, error: null, refresh: vi.fn() }),
}))

const published = {
  inputs: [{ name: "limit", label: "Auto-approve up to", type: "number", default: 1500 }],
  steps: [
    { id: "extract", type: "agent_run", agent_slug: "nora" },
    { id: "decide", type: "wait", name: "Ask Finance", wait: { kind: "approval" } },
    { id: "post", type: "http", http: { url: "https://erp.example/ledger", credential_ref: { type: "erp" } } },
  ],
}
const draftDefinition = {
  ...published,
  inputs: [{ name: "limit", label: "Auto-approve up to", type: "number", default: 2000 }],
}
const routine = { id: "pipe-1", slug: "invoice-intake", name: "Invoice intake", head_version: 3, definition: published, draft: { id: "drf_1", revision: 2, updated_at: new Date().toISOString(), updated_by: "usr_me" }, author_crew_id: "crew_fin" }
const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, text: async () => JSON.stringify(body) })

function mockServer(over: { test?: unknown; publish?: unknown; publishStatus?: number } = {}) {
  h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/invoice-intake/draft") && (!init || !init.method))
      return json({ id: "drf_1", slug: "invoice-intake", revision: 2, base_pipeline_id: "pipe-1", base_revision: 3, updated_by: "usr_me", updated_at: new Date().toISOString(), document: { slug: "invoice-intake", name: "Invoice intake", definition: draftDefinition, author_crew_id: "crew_fin" } })
    if (url.endsWith("/pipelines/test_run")) return json(over.test ?? { status: "DRY_RUN_OK", save_token: "tok_1" })
    if (url.endsWith("/invoice-intake/publish")) return json(over.publish ?? { slug: "invoice-intake" }, over.publishStatus ?? 200)
    if (url.endsWith("/invoice-intake/draft") && init?.method === "DELETE") return json({})
    throw new Error(`unexpected ${init?.method ?? "GET"} ${url}`)
  })
}

beforeEach(() => {
  h.fetcher.mockReset()
  h.schedules = []
  vi.stubGlobal("confirm", vi.fn(() => true))
  window.confirm = vi.fn(() => true)
})

describe("publicationSummary", () => {
  it("reads the five groups from the two definitions", () => {
    const { rows } = publicationSummary(published, draftDefinition)
    expect(rows).toEqual([
      ["Inputs", "Changed · Auto-approve up to"],
      ["Steps", "Unchanged"],
      ["People", "Unchanged · Ask Finance"],
      ["Effects", "Unchanged · agents nora, HTTP to erp.example"],
      ["Access", "Unchanged · no new credentials or hosts"],
    ])
  })
  it("flags a new host or credential under Access", () => {
    const { rows } = publicationSummary(published, { ...published, steps: [...published.steps, { id: "slack", type: "http", http: { url: "https://hooks.slack.com/x", credential_ref: { type: "slack" } } }] })
    expect(rows[1][1]).toBe("Added · slack")
    expect(rows[4][1]).toBe("Changed · needs slack · reaches hooks.slack.com")
  })
})

describe("scheduleImpact", () => {
  it("counts plans following the latest version apart from pinned ones", () => {
    const impact = scheduleImpact(
      [
        { id: "a", target_pipeline_id: "pipe-1", version_pinned: false },
        { id: "b", target_pipeline_slug: "invoice-intake", target_pipeline_version: 2 },
        { id: "c", target_pipeline_id: "other" },
      ] as never,
      routine,
    )
    expect(impact).toEqual({ latest: 1, pinned: 1 })
  })
})

describe("<RoutinePublishDialog>", () => {
  it("shows the title, the changes, the check and the impact, then publishes with the draft's id and revision", async () => {
    mockServer()
    h.schedules = [
      { id: "a", target_pipeline_id: "pipe-1", version_pinned: false },
      { id: "b", target_pipeline_id: "pipe-1", version_pinned: true, target_pipeline_version: 2 },
    ]
    const published = vi.fn()
    render(<RoutinePublishDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} activeRuns={1} onPublished={published} onDiscarded={() => {}} />)
    expect(screen.getByText("Publish draft r2 as v4")).toBeInTheDocument()
    expect(await screen.findByTestId("publish-changes")).toHaveTextContent("What changes vs v3")
    expect(screen.getByTestId("publish-changes")).toHaveTextContent("Changed · Auto-approve up to")
    await waitFor(() => expect(screen.getByTestId("publish-checked")).toHaveTextContent("runs no agents, scripts or services"))
    const after = screen.getByTestId("publish-after")
    expect(after).toHaveTextContent("Run and 1 schedule following the latest version use v4 from the next start")
    expect(after).toHaveTextContent("1 pinned schedule stays on its version")
    expect(after).toHaveTextContent("1 running or waiting run keeps v3")
    expect(after).toHaveTextContent("v3 stays in Versions and can be restored")
    const publish = screen.getByRole("button", { name: "Publish v4" })
    await waitFor(() => expect(publish).toBeEnabled())
    fireEvent.click(publish)
    await waitFor(() => expect(published).toHaveBeenCalledWith("invoice-intake"))
    const call = h.fetcher.mock.calls.find(([url]) => String(url).endsWith("/publish"))!
    expect(call[0]).toBe("/api/v1/workspaces/ws/pipelines/invoice-intake/publish")
    expect(JSON.parse(String(call[1].body))).toEqual({ id: "drf_1", revision: 2, save_token: "tok_1", approve_risk: false })
    const test = h.fetcher.mock.calls.find(([url]) => String(url).endsWith("/test_run"))!
    expect(JSON.parse(String(test[1].body))).toEqual({ definition: draftDefinition, sample_inputs: {}, author_crew_id: "crew_fin" })
  })

  it("keeps Publish off when the static check fails", async () => {
    mockServer({ test: { status: "FAILED", error: "step post: unknown credential" } })
    render(<RoutinePublishDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onPublished={() => {}} onDiscarded={() => {}} />)
    await waitFor(() => expect(screen.getByTestId("publish-checked")).toHaveTextContent("unknown credential"))
    expect(screen.getByRole("button", { name: "Publish v4" })).toBeDisabled()
  })

  it("asks to approve capability changes only when the server refuses for them", async () => {
    mockServer({ publish: { error: "Review the capability changes", risk_reasons: ["reaches a new host"] }, publishStatus: 409 })
    render(<RoutinePublishDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onPublished={() => {}} onDiscarded={() => {}} />)
    expect(screen.queryByLabelText("Approve capability changes")).toBeNull()
    const publish = screen.getByRole("button", { name: "Publish v4" })
    await waitFor(() => expect(publish).toBeEnabled())
    fireEvent.click(publish)
    const approve = await screen.findByLabelText("Approve capability changes")
    expect(screen.getByText(/reaches a new host/)).toBeInTheDocument()
    expect(publish).toBeDisabled()
    fireEvent.click(approve)
    expect(publish).toBeEnabled()
    mockServer()
    fireEvent.click(publish)
    await waitFor(() => {
      const calls = h.fetcher.mock.calls.filter(([url]) => String(url).endsWith("/publish"))
      expect(JSON.parse(String(calls.at(-1)![1].body)).approve_risk).toBe(true)
    })
  })

  it("discards the draft through DELETE with its id and revision", async () => {
    mockServer()
    const discarded = vi.fn()
    render(<RoutinePublishDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onPublished={() => {}} onDiscarded={discarded} />)
    const discard = screen.getByRole("button", { name: "Discard draft" })
    await waitFor(() => expect(discard).toBeEnabled())
    fireEvent.click(discard)
    await waitFor(() => expect(discarded).toHaveBeenCalled())
    const call = h.fetcher.mock.calls.find(([, init]) => init?.method === "DELETE")!
    expect(call[0]).toBe("/api/v1/workspaces/ws/pipelines/invoice-intake/draft")
    expect(JSON.parse(String(call[1].body))).toEqual({ id: "drf_1", revision: 2 })
  })
})
