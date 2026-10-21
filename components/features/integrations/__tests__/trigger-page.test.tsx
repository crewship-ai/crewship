import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("../views/incoming-webhooks-view", () => ({ RecentReceipts: () => <p>Receipts</p> }))

import { TriggerPage } from "../triggers-space"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import { triggerRows } from "../triggers-model"
import type { IncomingData } from "../use-incoming-endpoints"
import type { IncomingEndpoint, IncomingTarget } from "../incoming-model"
import type { PipelineWebhook } from "@/hooks/use-pipeline-webhooks"

const r1: IncomingTarget = { id: "r1", slug: "review", name: "Review PRs", kind: "routine" }
const r2: IncomingTarget = { id: "r2", slug: "triage", name: "Triage", kind: "routine" }
const hook: PipelineWebhook = { id: "w1", workspace_id: "ws", name: "github-prs", target_pipeline_id: "r1", token: "", signing_secret_set: true, inputs_template: { repo: "{{ inputs.event.repository.full_name }}" }, enabled: true, rate_limit_per_min: 60, fire_count: 4, created_at: "", updated_at: "", ingress_profile: "github" }
const endpoint: IncomingEndpoint = { id: "w1", name: "github-prs", target: r1, sender: "GitHub", signed: true, enabled: true, path: "", routine: hook }

function setup(pane: "overview" | "mapping" | "settings" = "settings") {
  const update = vi.fn(async () => hook)
  const remove = vi.fn(async () => {})
  const data = { targets: [r1, r2], rows: [endpoint], hooks: { update, remove }, refresh: vi.fn(), loading: false, error: null } as unknown as IncomingData
  const onDeleted = vi.fn()
  render(<TriggerPage workspaceId="ws" row={triggerRows([endpoint])[0]} data={data} pane={pane} onPane={vi.fn()} canEdit onBack={vi.fn()} onDeleted={onDeleted} />)
  return { update, remove, onDeleted }
}

afterEach(cleanup)

describe("TriggerPage", () => {
  it("retarget sends target_pipeline_id", async () => {
    const { update } = setup()
    fireEvent.change(screen.getByRole("combobox", { name: "Starts" }), { target: { value: "r2" } })
    await waitFor(() => expect(update).toHaveBeenCalledWith("w1", { target_pipeline_id: "r2" }))
  })

  it("rotate confirm says old one stops now", async () => {
    const { update } = setup()
    fireEvent.click(screen.getByRole("button", { name: "Rotate…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent("Rotate secret? The old one stops now.")
    fireEvent.click(within(dialog).getByRole("button", { name: "Rotate secret" }))
    await waitFor(() => expect(update).toHaveBeenCalledWith("w1", { rotate_secret: true }))
  })

  it("rotate can keep the old secret for a while and says so (ODCH-INT-11)", async () => {
    const { update } = setup()
    fireEvent.click(screen.getByRole("button", { name: "Rotate…" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.change(within(dialog).getByLabelText("Old value keeps working for"), { target: { value: "86400" } })
    expect(dialog).toHaveTextContent("Rotate secret? The old one keeps working for 24 hours.")
    fireEvent.click(within(dialog).getByRole("button", { name: "Rotate secret" }))
    await waitFor(() => expect(update).toHaveBeenCalledWith("w1", { rotate_secret: true, rotate_grace_seconds: 86400 }))
  })

  it("delete has its own text and waits for the server", async () => {
    const { remove, onDeleted } = setup()
    fireEvent.click(screen.getByRole("button", { name: "Delete…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent("The URL stops working; the sender gets 404")
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete trigger" }))
    await waitFor(() => expect(remove).toHaveBeenCalledWith("w1"))
    expect(onDeleted).toHaveBeenCalled()
  })

  it("never offers Replay or Send test", () => {
    setup("mapping")
    expect(screen.getByDisplayValue("repo")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Replay|Use last call|Send test/ })).not.toBeInTheDocument()
  })
})

// INT-02: the mapping is edited in the app, as a draft on the page's Save bar
// (PATCH inputs_template; event, raw and headers stay reserved).
describe("TriggerPage › Mapping", () => {
  function mapping(canEdit = true) {
    const update = vi.fn(async () => hook)
    const data = { targets: [r1, r2], rows: [endpoint], hooks: { update, remove: vi.fn() }, refresh: vi.fn(), loading: false, error: null } as unknown as IncomingData
    render(
      <PageSaveProvider>
        <TriggerPage workspaceId="ws" row={triggerRows([endpoint])[0]} data={data} pane="mapping" onPane={vi.fn()} canEdit={canEdit} onBack={vi.fn()} onDeleted={vi.fn()} />
        <PageSaveBar />
      </PageSaveProvider>,
    )
    return update
  }
  const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })

  it("edits, adds and removes inputs and writes nothing before Save", async () => {
    const update = mapping()
    fireEvent.change(screen.getByLabelText("Value of input 1"), { target: { value: "{{ inputs.event.repo }}" } })
    fireEvent.click(screen.getByRole("button", { name: "Add input" }))
    fireEvent.change(screen.getByLabelText("Input name 2"), { target: { value: "pr_number" } })
    fireEvent.change(screen.getByLabelText("Value of input 2"), { target: { value: "{{ inputs.event.pull_request.number }}" } })
    expect(bar()).toHaveTextContent("unsaved change")
    expect(update).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() =>
      expect(update).toHaveBeenCalledWith("w1", { inputs_template: { repo: "{{ inputs.event.repo }}", pr_number: "{{ inputs.event.pull_request.number }}" } }),
    )
  })

  it("Test mapping posts the sample to the dry run and shows what the routine would get (ODCH-INT-09)", async () => {
    const test = vi.fn(async () => ({ inputs: { repo: "acme/web", event: {}, raw: "", headers: {} }, ignored_template_keys: ["raw"], valid: true }))
    const data = { targets: [r1, r2], rows: [endpoint], hooks: { update: vi.fn(), test, remove: vi.fn() }, refresh: vi.fn(), loading: false, error: null } as unknown as IncomingData
    render(
      <PageSaveProvider>
        <TriggerPage workspaceId="ws" row={triggerRows([endpoint])[0]} data={data} pane="mapping" onPane={vi.fn()} canEdit onBack={vi.fn()} onDeleted={vi.fn()} />
        <PageSaveBar />
      </PageSaveProvider>,
    )
    const button = screen.getByRole("button", { name: "Test mapping" })
    expect(button).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Sample call JSON"), { target: { value: "nope" } })
    fireEvent.click(button)
    expect(await screen.findByRole("alert")).toHaveTextContent("not valid JSON")
    expect(test).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText("Sample call JSON"), { target: { value: '{"action":"opened"}' } })
    fireEvent.click(button)
    await waitFor(() => expect(test).toHaveBeenCalledWith("w1", { action: "opened" }))
    expect(await screen.findByText("The routine would accept this call.")).toBeInTheDocument()
    expect(screen.getByText("acme/web")).toBeInTheDocument()
    // The test reads the saved mapping, so an unsaved draft pauses it.
    fireEvent.change(screen.getByLabelText("Input name 1"), { target: { value: "repository" } })
    expect(button).toBeDisabled()
  })

  it("removing every input saves an empty mapping", async () => {
    const update = mapping()
    fireEvent.click(screen.getByRole("button", { name: "Remove input 1" }))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(update).toHaveBeenCalledWith("w1", { inputs_template: {} }))
  })

  it("Discard puts the saved mapping back", () => {
    const update = mapping()
    fireEvent.change(screen.getByLabelText("Input name 1"), { target: { value: "repository" } })
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(screen.getByLabelText("Input name 1")).toHaveValue("repo")
    expect(bar()).toBeNull()
    expect(update).not.toHaveBeenCalled()
  })

  it("a reserved or duplicate input name blocks Save", () => {
    mapping()
    fireEvent.change(screen.getByLabelText("Input name 1"), { target: { value: "headers" } })
    expect(screen.getByText("event, raw and headers come from the call and cannot be mapped")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Input name 1"), { target: { value: "repo" } })
    fireEvent.click(screen.getByRole("button", { name: "Add input" }))
    fireEvent.change(screen.getByLabelText("Input name 2"), { target: { value: "repo" } })
    expect(screen.getByText("Each input name can be used once")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
  })

  it("without the right to edit, the mapping is read-only", () => {
    mapping(false)
    expect(screen.getByLabelText("Input name 1")).toBeDisabled()
    expect(screen.queryByRole("button", { name: "Add input" })).not.toBeInTheDocument()
  })
})
