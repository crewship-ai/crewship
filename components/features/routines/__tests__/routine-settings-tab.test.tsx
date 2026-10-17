// The routine page is the editor: Settings and Steps share one document and
// one save bar. Name and purpose alone apply at once; everything else becomes
// a draft (compare-and-set on the revision the edit started from).

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { toast } from "sonner"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import { useEditorSaveBar } from "../routine-page"
import { useRoutineEditor } from "../use-routine-editor"
import { RoutineSettingsTab } from "../routine-settings-tab"
import { RoutineStepsTab, agentSteps } from "../routine-steps-tab"
import type { RoutineDetail } from "../routines-detail-panel"
import type { RoutineFile } from "@/lib/routine-files"

const h = vi.hoisted(() => ({ fetcher: vi.fn(), push: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push, replace: vi.fn() }), useSearchParams: () => new URLSearchParams() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({
  useWorkspaceAgentDirectory: () => ({ agents: [{ id: "a1", slug: "nora", name: "Nora", crew_id: "crew_fin", agent_role: "LEAD" }], error: false }),
}))
vi.mock("@/components/features/settings/resource-share-dialog", () => ({ ResourceShareDialog: () => <button type="button">Share</button> }))
vi.mock("../routine-budget-card", () => ({ RoutineBudgetCard: () => <div data-testid="budget" /> }))
vi.mock("../routine-step-spine", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../routine-step-spine")>()),
  RoutineStepSpine: () => <div data-testid="spine" />,
}))
vi.mock("../routine-definition-canvas", () => ({ RoutineDefinitionCanvas: () => <div data-testid="map" /> }))
vi.mock("@/components/features/access/your-access", () => ({ YourAccess: () => null }))
vi.mock("../routine-reach-card", () => ({ RoutineReachCard: () => null }))

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

function Editor({ r, onChanged, tab, depth = "simple", files = [] }: { r: RoutineDetail; onChanged: () => void; tab: "settings" | "steps"; depth?: "simple" | "full"; files?: RoutineFile[] }) {
  const editor = useRoutineEditor("ws", r, onChanged)
  useEditorSaveBar(editor)
  return (
    <div className="relative">
      {tab === "settings" ? (
        <RoutineSettingsTab routine={r} workspaceId="ws" editor={editor} canEdit canManage depth={depth} lifecycle="active" onGovernance={vi.fn()} onDelete={vi.fn()} />
      ) : (
        <RoutineStepsTab routine={r} workspaceId="ws" editor={editor} canEdit depth={depth} files={files} routines={[]} />
      )}
      <PageSaveBar />
    </div>
  )
}
function renderEditor(props: Partial<React.ComponentProps<typeof Editor>> = {}) {
  const changed = props.onChanged ?? vi.fn()
  const view = render(
    <PageSaveProvider>
      <Editor r={props.r ?? routine} onChanged={changed} tab={props.tab ?? "settings"} depth={props.depth} files={props.files} />
    </PageSaveProvider>,
  )
  return { ...view, changed }
}
const loaded = () => waitFor(() => expect(screen.queryByText(/Loading the current draft/)).toBeNull())
const bar = () => within(screen.getByRole("region", { name: "Unsaved changes" }))
const draftPost = () => h.fetcher.mock.calls.find(([url, init]) => String(url).endsWith("/pipelines/drafts") && init?.method === "POST")

beforeEach(() => {
  h.fetcher.mockReset()
  vi.mocked(toast.success).mockReset()
})

describe("agentSteps", () => {
  it("finds agent steps at the top level and inside a foreach", () => {
    expect(agentSteps(definition).map((a) => a.path.join("."))).toEqual(["steps.0", "steps.1.foreach.steps.0"])
  })
})

describe("Settings", () => {
  it("identity saves immediately through the rename path, without a draft", async () => {
    mockServer()
    const { changed } = renderEditor()
    await loaded()
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Invoice intake v2" } })
    expect(screen.getByTestId("routine-save-hint")).toHaveTextContent("Name and purpose apply at once")
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    const save = h.fetcher.mock.calls.find(([url]) => String(url).endsWith("/pipelines/save"))!
    const body = JSON.parse(String(save[1].body))
    expect(body).toMatchObject({ slug: "invoice-intake", name: "Invoice intake v2", description: "Reads invoices.", skip_test_gate: true, skip_governance_gate: true })
    expect(body.definition.display_name).toBe("Invoice intake v2")
    expect(draftPost()).toBeUndefined()
    expect(toast.success).toHaveBeenCalledWith("Name and purpose saved")
  })

  it("keeps a draft-only routine unpublished when only its name changes", async () => {
    mockServer(true)
    renderEditor({ r: { ...routine, head_version: 0 } })
    await loaded()
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Renamed unpublished copy" } })
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(h.fetcher.mock.calls.some(([url]) => String(url).endsWith("/pipelines/save"))).toBe(false)
    expect(JSON.parse(String(draftPost()![1].body)).document).toMatchObject({ name: "Renamed unpublished copy" })
  })

  it("inputs and limits go to the draft; identity edited with them goes there too", async () => {
    mockServer()
    renderEditor()
    await loaded()
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "New invoice name" } })
    fireEvent.change(screen.getByLabelText("Default", { selector: "#routine-edit-input-auto_approve_limit-default" }), { target: { value: "500" } })
    expect(screen.getByTestId("routine-save-hint")).toHaveTextContent("saved as draft r1; the live version keeps running")
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(h.fetcher.mock.calls.some(([url]) => String(url).endsWith("/pipelines/save"))).toBe(false)
    const body = JSON.parse(String(draftPost()![1].body))
    expect(body).toMatchObject({ id: "", slug: "invoice-intake", revision: 0, base_pipeline_id: "pipe-1", base_revision: 3 })
    expect(body.document).toMatchObject({ name: "New invoice name", author_crew_id: "crew_fin", definition: { display_name: "New invoice name" } })
    expect(body.document.definition.inputs[0].default).toBe(500)
    expect(toast.success).toHaveBeenCalledWith("Saved as draft r1 · publish to make it live")
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
    renderEditor()
    await loaded()
    fireEvent.change(screen.getByLabelText("Default", { selector: "#routine-edit-input-auto_approve_limit-default" }), { target: { value: "500" } })
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(JSON.parse(String(draftPost()![1].body)).document).toMatchObject({ name: "Next release name", description: "Next release purpose" })
  })

  it("shows an unavailable draft baseline and refuses to save", async () => {
    h.fetcher.mockResolvedValue(json({ error: "temporarily unavailable" }, 503))
    renderEditor()
    await screen.findByText(/Could not load the routine draft/)
    expect(screen.getByLabelText(/^Name/)).toBeDisabled()
    expect(h.fetcher.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  })

  it("maps limits and the chat command onto DSL fields (guardrails in Full detail)", async () => {
    mockServer()
    renderEditor({ depth: "full" })
    await loaded()
    fireEvent.change(screen.getByLabelText(/Cost cap per run/), { target: { value: "5" } })
    fireEvent.click(screen.getByRole("radio", { name: "One at a time" }))
    fireEvent.click(screen.getByRole("radio", { name: "Clean it and continue" }))
    fireEvent.click(screen.getByRole("switch", { name: "Offer as a slash command" }))
    fireEvent.change(screen.getByLabelText(/^Label/, { selector: "#routine-edit-slash" }), { target: { value: "Invoice" } })
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(draftPost()).toBeDefined())
    const def = JSON.parse(String(draftPost()![1].body)).document.definition
    expect(def.max_cost_usd).toBe(5)
    expect(def).toMatchObject({ concurrency_key: "invoice-intake", max_concurrent: 1, guardrails: { input: { prompt_injection: { action: "sanitize" } } }, slash: { enabled: true, label: "Invoice" } })
  })

  it("keeps model tiers and guardrails out of Simple", async () => {
    mockServer()
    renderEditor()
    await loaded()
    expect(screen.queryByRole("radiogroup", { name: "Suspicious text in inputs" })).toBeNull()
    expect(screen.getByText(/Model tiers, input guardrails and estimates are in Full detail/)).toBeInTheDocument()
  })

  it("sends the revision it opened on, not the revision the server has at save time", async () => {
    let serverRevision = 2
    h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/invoice-intake/draft") && !init?.method)
        return json({ id: "drf_1", slug: "invoice-intake", revision: serverRevision, base_pipeline_id: "pipe-1", base_revision: 3, document: { slug: "invoice-intake", name: "Invoice intake", description: "Reads invoices.", definition: { ...definition, max_cost_usd: 9 }, author_crew_id: "crew_fin" } })
      if (url.endsWith("/pipelines/drafts") && init?.method === "POST") {
        const sent = JSON.parse(String(init.body))
        if (sent.revision !== serverRevision) return json({ error: "routine draft changed or its published recipe changed; reload and review before publishing" }, 409)
        return json({ ...sent, revision: sent.revision + 1 })
      }
      throw new Error(`unexpected ${init?.method ?? "GET"} ${url}`)
    })
    renderEditor({ r: { ...routine, draft: { id: "drf_1", revision: 2, updated_at: "" } } })
    await loaded()
    serverRevision = 3
    fireEvent.change(screen.getByLabelText(/Cost cap per run/), { target: { value: "7" } })
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(screen.getByText(/Someone saved a newer draft or published recipe/)).toBeInTheDocument())
    expect(JSON.parse(String(draftPost()![1].body))).toMatchObject({ id: "drf_1", revision: 2 })
    expect(toast.success).not.toHaveBeenCalled()
  })

  it.each([false, true])("guards unsaved edits without treating an existing draft as dirty (%s)", async (existingDraft) => {
    mockServer(existingDraft)
    renderEditor()
    await loaded()
    const clean = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(clean)
    expect(clean.defaultPrevented).toBe(false)
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Unsaved invoice name" } })
    const dirty = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(dirty)
    expect(dirty.defaultPrevented).toBe(true)
  })

  it("leaving with unsaved changes asks in the app's dialog, not the browser's", async () => {
    mockServer()
    const previousConfirm = window.confirm
    const confirm = vi.fn(() => false)
    window.confirm = confirm
    const anchor = document.createElement("a")
    anchor.href = "/activity"
    document.body.append(anchor)
    try {
      renderEditor()
      await loaded()
      fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Retain this name" } })
      const click = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })
      anchor.dispatchEvent(click)
      expect(click.defaultPrevented).toBe(true)
      expect(confirm).not.toHaveBeenCalled()
      expect(await screen.findByText(/Leave with 1 unsaved change/)).toBeInTheDocument()
      fireEvent.click(screen.getByRole("button", { name: "Stay" }))
      expect(screen.getByLabelText(/^Name/)).toHaveValue("Retain this name")
    } finally {
      anchor.remove()
      window.confirm = previousConfirm
    }
  })

  it("Discard drops only local edits; nothing is sent", async () => {
    mockServer(true)
    renderEditor()
    await loaded()
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Discard local name" } })
    fireEvent.click(bar().getByRole("button", { name: "Discard" }))
    await waitFor(() => expect(screen.getByLabelText(/^Name/)).toHaveValue("Invoice intake"))
    expect(h.fetcher.mock.calls.some(([, init]) => init?.method && init.method !== "GET")).toBe(false)
  })
})

describe("Steps editor", () => {
  const openEditor = async (r: RoutineDetail = routine, files: RoutineFile[] = []) => {
    renderEditor({ tab: "steps", r, files })
    fireEvent.click(screen.getByRole("button", { name: "Edit steps" }))
    await loaded()
  }

  it("adds an agent step and saves a draft with a new revision", async () => {
    mockServer(true)
    await openEditor({ ...routine, draft: { id: "drf_1", revision: 2, updated_at: "" } })
    fireEvent.click(screen.getByRole("button", { name: "Add a step" }))
    fireEvent.click(screen.getByRole("button", { name: /Agent does a task/ }))
    fireEvent.click(screen.getByRole("radio", { name: "Nora" }))
    fireEvent.change(screen.getByLabelText("Instructions"), { target: { value: "Summarise {{ steps.extract.output }}" } })
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(draftPost()).toBeDefined())
    const body = JSON.parse(String(draftPost()![1].body))
    expect(body).toMatchObject({ id: "drf_1", revision: 2 })
    const steps = body.document.definition.steps
    expect(steps).toHaveLength(3)
    // Untouched steps stay exactly as they were; the new one is a valid agent step.
    expect(steps[0]).toEqual(definition.steps[0])
    expect(steps[1]).toEqual(definition.steps[1])
    expect(steps[2]).toMatchObject({ id: "task1", type: "agent_run", agent_slug: "nora", prompt: "Summarise {{ steps.extract.output }}" })
    expect(toast.success).toHaveBeenCalledWith("Saved as draft r3 · publish to make it live")
  })

  it("code and loop steps stay read-only with the CLI path; their agent prompts are editable", async () => {
    mockServer(true)
    await openEditor({ ...routine, draft: { id: "drf_1", revision: 2, updated_at: "" } }, [{ path: "scripts/a.py", language: "py", step_ids: ["extract"], present: false, status: "missing" }])
    expect(screen.getByText(/Code, transform, query and loop steps, checks, hooks and scripts are read-only here. 2 steps, 1 file/)).toBeInTheDocument()
    expect(screen.getByText(/crewship routine draft get invoice-intake/)).toBeInTheDocument()
    expect(screen.getByText("used by Read the invoice")).toBeInTheDocument()
    expect(screen.getByText("missing on the share")).toBeInTheDocument()
    expect(screen.getByText(/checked by vale/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("Judge"), { target: { value: "Judge it carefully" } })
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(draftPost()).toBeDefined())
    const body = JSON.parse(String(draftPost()![1].body))
    // Built on the draft's definition (max_cost_usd 9 came from it), with the new prompt.
    expect(body.document.definition.max_cost_usd).toBe(9)
    expect(body.document.definition.steps[1].foreach.steps[0].prompt).toBe("Judge it carefully")
  })

  it("does not call an unverified file missing", async () => {
    mockServer()
    await openEditor(routine, [{ path: "scripts/a.py", language: "py", step_ids: ["extract"], present: false, status: "unverified" }])
    expect(screen.queryByText("missing on the share")).toBeNull()
    expect(screen.getByText("not verified")).toBeInTheDocument()
  })

  it("refuses to remove a step whose result a later step reads", async () => {
    mockServer()
    const r = { ...routine, definition: { ...definition, steps: [definition.steps[0], { id: "tell", type: "notify", name: "Tell", notify: { to: "workspace", title: "Tell", body: "{{ steps.extract.output }}" } }] } } as unknown as RoutineDetail
    await openEditor(r)
    fireEvent.click(screen.getByRole("button", { name: "Step 1: Read the invoice" }))
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    expect(screen.getByRole("alert")).toHaveTextContent("“Read the invoice” cannot be removed: “Tell” reads its result.")
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
  })

  it("reorders steps", async () => {
    mockServer()
    const r = { ...routine, definition: { ...definition, steps: [definition.steps[0], { id: "tell", type: "notify", name: "Tell", notify: { to: "workspace", title: "Tell", body: "done" } }] } } as unknown as RoutineDetail
    await openEditor(r)
    fireEvent.click(screen.getByRole("button", { name: "Move Tell up" }))
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(draftPost()).toBeDefined())
    expect(JSON.parse(String(draftPost()![1].body)).document.definition.steps.map((s: { id: string }) => s.id)).toEqual(["tell", "extract"])
  })

  it("authors typed questions and custom decisions through the draft path", async () => {
    mockServer()
    const approval = { id: "review", type: "wait", name: "Review invoice", wait: { kind: "approval", timeout_seconds: 86400, approval_title: "Review" } }
    await openEditor({ ...routine, definition: { ...definition, steps: [approval] } } as unknown as RoutineDetail)
    fireEvent.click(screen.getByRole("button", { name: "Customize decision" }))
    fireEvent.click(screen.getByRole("button", { name: "+ Add question" }))
    fireEvent.change(screen.getByLabelText("Question"), { target: { value: "Approved amount" } })
    fireEvent.change(screen.getByLabelText("Answer type"), { target: { value: "number" } })
    fireEvent.change(screen.getByLabelText("Variable name"), { target: { value: "amount" } })
    fireEvent.change(screen.getByLabelText("Decision 1"), { target: { value: "Pay invoice" } })
    fireEvent.click(bar().getByRole("button", { name: "Save" }))
    await waitFor(() => expect(draftPost()).toBeDefined())
    const body = JSON.parse(String(draftPost()![1].body))
    expect(body).toMatchObject({ revision: 0, base_revision: 3, document: { definition: { steps: [{ wait: {
      kind: "approval", timeout_seconds: 86400, decision_form: {
        fields: [{ name: "amount", label: "Approved amount", type: "number" }],
        actions: [{ id: "continue", label: "Pay invoice", approved: true }, { id: "reject", approved: false }],
      },
    } }] } } })
    expect(approval.wait).not.toHaveProperty("decision_form")
  })

  it("an unchanged editor has nothing to save", async () => {
    mockServer()
    await openEditor()
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
  })
})
