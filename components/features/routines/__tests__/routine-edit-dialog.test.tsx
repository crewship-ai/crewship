// Edit: identity applies at once, a definition change becomes a draft.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { RoutineEditDialog, agentSteps } from "../routine-edit-dialog"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({ useWorkspaceAgentDirectory: () => ({ agents: [{ id: "a1", slug: "nora", name: "Nora" }], error: false }) }))
vi.mock("@/components/crew-icon-popover", () => ({ CrewIconPopover: () => <div data-testid="icon-popover" /> }))

const definition = {
  display_name: "Invoice intake",
  description: "Reads invoices.",
  inputs: [
    { name: "auto_approve_limit", label: "Auto-approve up to", type: "number", min: 0, max: 5000, default: 1500, description: "0 to 5 000." },
    { name: "cost_center", label: "Cost center", type: "string", options: ["Operations", "Marketing"], default: "Operations" },
  ],
  steps: [
    { id: "extract", type: "agent_run", name: "Read the invoice", agent_slug: "nora", prompt: "Read {{ inputs.invoice_path }}" },
    { id: "loop", type: "foreach", foreach: { items: "x", steps: [{ id: "judge", type: "agent_run", name: "Judge", agent_slug: "nora", prompt: "Judge it", outcomes: { grader_agent_slug: "vale", criteria: [{ name: "a", rule: "a" }] } }] } },
  ],
  slash: { enabled: false },
}
const routine = {
  id: "pipe-1", slug: "invoice-intake", name: "Invoice intake", description: "Reads invoices.", definition, definition_hash: "h", dsl_version: "1.0",
  ephemeral: false, workspace_visible: true, invocation_count: 0, authored_via: "user_api", created_at: "", updated_at: "", head_version: 3, author_crew_id: "crew_fin",
} as unknown as RoutineDetail
const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, clone() { return this }, text: async () => JSON.stringify(body) })

function mockServer(existingDraft = false) {
  h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/invoice-intake/draft") && !init?.method)
      return json(existingDraft
        ? { id: "drf_1", slug: "invoice-intake", revision: 2, base_pipeline_id: "pipe-1", base_revision: 3, document: { slug: "invoice-intake", name: "Invoice intake", description: "Reads invoices.", definition: { ...definition, max_cost_usd: 9 }, author_crew_id: "crew_fin" } }
        : { id: "", slug: "invoice-intake", revision: 0, base_pipeline_id: "pipe-1", base_revision: 3, document: {} })
    if (url.endsWith("/pipelines/drafts") && init?.method === "POST") {
      const sent = JSON.parse(String(init.body))
      return json({ ...sent, id: sent.id || "drf_new", revision: sent.revision + 1 })
    }
    if (url.endsWith("/pipelines/save") && init?.method === "POST") return json({ slug: "invoice-intake" })
    if (url.endsWith("/appearance")) return json({})
    throw new Error(`unexpected ${init?.method ?? "GET"} ${url}`)
  })
}

beforeEach(() => {
  h.fetcher.mockReset()
  vi.mocked(toast.success).mockReset()
})

describe("agentSteps", () => {
  it("finds agent steps at the top level and inside a foreach", () => {
    expect(agentSteps(definition).map((a) => a.path.join("."))).toEqual(["steps.0", "steps.1.foreach.steps.0"])
  })
})

describe("<RoutineEditDialog>", () => {
  it("saves name and purpose at once through the rename path, without a draft", async () => {
    mockServer()
    const changed = vi.fn()
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onChanged={changed} />)
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    expect(screen.getByRole("tab", { name: "Identity", selected: true })).toBeInTheDocument()
    expect(screen.getByText(/Identity applies at once; definition changes become a draft/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Invoice intake v2" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    const save = h.fetcher.mock.calls.find(([url]) => String(url).endsWith("/pipelines/save"))!
    const body = JSON.parse(String(save[1].body))
    expect(body).toMatchObject({ slug: "invoice-intake", name: "Invoice intake v2", description: "Reads invoices.", skip_test_gate: true, skip_governance_gate: true })
    expect(body.definition.display_name).toBe("Invoice intake v2")
    expect(h.fetcher.mock.calls.some(([url]) => String(url).endsWith("/pipelines/drafts"))).toBe(false)
    expect(toast.success).toHaveBeenCalledWith("Name and purpose saved")
  })

  it("saves identity and input edits together without first changing the published recipe", async () => {
    mockServer()
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onChanged={() => {}} />)
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "New invoice name" } })
    fireEvent.click(screen.getByRole("tab", { name: /Inputs/ }))
    fireEvent.change(screen.getByLabelText("Default", { selector: "#routine-edit-input-auto_approve_limit-default" }), { target: { value: "500" } })
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(h.fetcher.mock.calls.some(([url]) => String(url).endsWith("/pipelines/save"))).toBe(false)
    const call = h.fetcher.mock.calls.find(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")!
    expect(JSON.parse(String(call[1].body)).document).toMatchObject({ name: "New invoice name", definition: { display_name: "New invoice name" } })
  })

  it("preserves identity authored in the draft when only an input changes", async () => {
    mockServer(true)
    const fetch = h.fetcher.getMockImplementation()!
    h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      const response = await fetch(url, init)
      if (url.endsWith("/invoice-intake/draft") && !init?.method) {
        const draft = await response.json()
        draft.document.name = "Next release name"
        draft.document.description = "Next release purpose"
        return json(draft)
      }
      return response
    })
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onChanged={() => {}} />)
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    fireEvent.click(screen.getByRole("tab", { name: /Inputs/ }))
    fireEvent.change(screen.getByLabelText("Default", { selector: "#routine-edit-input-auto_approve_limit-default" }), { target: { value: "500" } })
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    const call = h.fetcher.mock.calls.find(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")!
    expect(JSON.parse(String(call[1].body)).document).toMatchObject({ name: "Next release name", description: "Next release purpose" })
  })

  it("shows an unavailable draft baseline and prevents saving", async () => {
    h.fetcher.mockResolvedValue(json({ error: "temporarily unavailable" }, 503))
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onChanged={() => {}} />)
    await screen.findByText(/Could not load the routine draft/)
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Changed" } })
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
    expect(h.fetcher.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  })

  it("does not call an unverified file missing in Edit", () => {
    mockServer()
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} files={[{ path: "scripts/a.py", language: "py", step_ids: ["extract"], present: false, status: "unverified" }]} onChanged={() => {}} />)
    fireEvent.click(screen.getByRole("tab", { name: "Steps & files" }))
    expect(screen.queryByText("missing on the share")).toBeNull()
    expect(screen.getByText("not verified")).toBeInTheDocument()
  })

  it("saves a changed input default as a new draft from the published definition", async () => {
    mockServer()
    const changed = vi.fn()
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onChanged={changed} />)
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    fireEvent.click(screen.getByRole("tab", { name: "Inputs · 2" }))
    const card = screen.getByTestId("routine-edit-input-auto_approve_limit")
    expect(card).toHaveTextContent("auto_approve_limit")
    expect(card).toHaveTextContent("0 – 5000")
    fireEvent.change(screen.getByLabelText("Default", { selector: "#routine-edit-input-auto_approve_limit-default" }), { target: { value: "2000" } })
    expect(screen.getByText(/saved as draft r1; published v3 keeps running/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    const save = h.fetcher.mock.calls.find(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")!
    const body = JSON.parse(String(save[1].body))
    expect(body).toMatchObject({ id: "", slug: "invoice-intake", revision: 0, base_pipeline_id: "pipe-1", base_revision: 3 })
    expect(body.document.definition.inputs[0].default).toBe(2000)
    expect(body.document).toMatchObject({ slug: "invoice-intake", name: "Invoice intake", author_crew_id: "crew_fin" })
    expect(h.fetcher.mock.calls.some(([url]) => String(url).endsWith("/pipelines/save"))).toBe(false)
    expect(toast.success).toHaveBeenCalledWith("Saved as draft r1 · publish to make it live")
  })

  it("edits an agent prompt inside a foreach and continues the existing draft revision", async () => {
    mockServer(true)
    const changed = vi.fn()
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={{ ...routine, draft: { id: "drf_1", revision: 2, updated_at: "" } }} onChanged={changed} />)
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    fireEvent.click(screen.getByRole("tab", { name: "Agent prompts · 2" }))
    expect(screen.getByText(/checked by vale/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("Judge"), { target: { value: "Judge it carefully" } })
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    const save = h.fetcher.mock.calls.find(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")!
    const body = JSON.parse(String(save[1].body))
    expect(body).toMatchObject({ id: "drf_1", revision: 2 })
    // Built on the draft's definition (max_cost_usd 9 came from it), with the new prompt.
    expect(body.document.definition.max_cost_usd).toBe(9)
    expect(body.document.definition.steps[1].foreach.steps[0].prompt).toBe("Judge it carefully")
    expect(toast.success).toHaveBeenCalledWith("Saved as draft r3 · publish to make it live")
  })

  it("sends the revision it opened on, not the revision the server has at save time", async () => {
    // Open on r2; a colleague saves r3 while the dialog is open. The save must
    // carry r2 so the server's compare-and-set refuses it, instead of reading
    // r3 back and overwriting the colleague's work under r3's number.
    let serverRevision = 2
    h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/invoice-intake/draft") && !init?.method)
        return json({ id: "drf_1", slug: "invoice-intake", revision: serverRevision, base_pipeline_id: "pipe-1", base_revision: 3, document: { slug: "invoice-intake", name: "Invoice intake", description: "Reads invoices.", definition: { ...definition, max_cost_usd: 9 }, author_crew_id: "crew_fin" } })
      if (url.endsWith("/pipelines/drafts") && init?.method === "POST") {
        const sent = JSON.parse(String(init.body))
        if (sent.revision !== serverRevision) return json({ error: "draft revision conflict" }, 409)
        return json({ ...sent, revision: sent.revision + 1 })
      }
      throw new Error(`unexpected ${init?.method ?? "GET"} ${url}`)
    })
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={{ ...routine, draft: { id: "drf_1", revision: 2, updated_at: "" } }} onChanged={() => {}} />)
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    serverRevision = 3
    fireEvent.click(screen.getByRole("tab", { name: "Agent prompts · 2" }))
    fireEvent.change(screen.getByLabelText("Judge"), { target: { value: "Judge it carefully" } })
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }))
    await waitFor(() => expect(screen.getByText(/Someone saved a newer draft meanwhile/)).toBeInTheDocument())
    const save = h.fetcher.mock.calls.find(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")!
    expect(JSON.parse(String(save[1].body))).toMatchObject({ id: "drf_1", revision: 2 })
    expect(toast.success).not.toHaveBeenCalled()
  })

  it("maps limits and the slash command onto DSL fields", async () => {
    mockServer()
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} onChanged={() => {}} />)
    await waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
    fireEvent.click(screen.getByRole("tab", { name: "Limits" }))
    expect(screen.getByText(/Sketch — which of these belong in the web/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/Cost cap per run/), { target: { value: "5" } })
    fireEvent.click(screen.getByRole("radio", { name: "One at a time" }))
    fireEvent.click(screen.getByRole("radio", { name: "Clean it and continue" }))
    fireEvent.click(screen.getByRole("tab", { name: "Slash command" }))
    fireEvent.click(screen.getByRole("switch", { name: "Offer as a slash command" }))
    fireEvent.change(screen.getByLabelText(/^Label/), { target: { value: "Invoice" } })
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }))
    await waitFor(() => expect(h.fetcher.mock.calls.some(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")).toBe(true))
    const save = h.fetcher.mock.calls.find(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")!
    const def = JSON.parse(String(save[1].body)).document.definition
    expect(def.max_cost_usd).toBe(5)
    expect(def).toMatchObject({ concurrency_key: "invoice-intake", max_concurrent: 1, guardrails: { input: { prompt_injection: { action: "sanitize" } } }, slash: { enabled: true, label: "Invoice" } })
  })

  it("shows the read-only steps and files summary with the CLI path", () => {
    mockServer()
    render(<RoutineEditDialog open onOpenChange={() => {}} workspaceId="ws" routine={routine} files={[{ path: "scripts/a.py", language: "py", step_ids: ["extract"], present: false, status: "missing" }]} onChanged={() => {}} />)
    fireEvent.click(screen.getByRole("tab", { name: "Steps & files" }))
    expect(screen.getByText(/Read-only in the web. 2 steps, 1 file/)).toBeInTheDocument()
    expect(screen.getByText(/crewship routine draft get invoice-intake/)).toBeInTheDocument()
    expect(screen.getByText("used by Read the invoice")).toBeInTheDocument()
    expect(screen.getByText("missing on the share")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
  })
})
