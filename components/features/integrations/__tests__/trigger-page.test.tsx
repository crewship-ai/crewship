import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("../views/incoming-webhooks-view", () => ({ RecentReceipts: () => <p>Receipts</p> }))

import { TriggerPage } from "../triggers-space"
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

  it("delete has its own text and waits for the server", async () => {
    const { remove, onDeleted } = setup()
    fireEvent.click(screen.getByRole("button", { name: "Delete…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent("The URL stops working; the sender gets 404")
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete trigger" }))
    await waitFor(() => expect(remove).toHaveBeenCalledWith("w1"))
    expect(onDeleted).toHaveBeenCalled()
  })

  it("shows the mapping read-only and never offers Replay or Test mapping", () => {
    setup("mapping")
    expect(screen.getByText("repo")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Replay|Test mapping|Send test/ })).not.toBeInTheDocument()
  })
})
