import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

// =============================================================================
// F21 / B9 (#2362) — editing a webhook used to mean delete + recreate, which
// rotated the token and broke every configured sender. This dialog edits
// name / rate limit / inputs template / enabled in place, with secret
// rotation as one explicit, opt-in, confirmed checkbox. Confirms:
//   - the dialog never sends a field that could change the token,
//   - rotate_secret only appears in the saved body when the box is checked
//     AND the confirmation is accepted,
//   - a declined confirmation does not save at all.
// =============================================================================

import { RoutineWebhookEditorDialog } from "../routine-webhook-editor-dialog"
import type { PipelineWebhook } from "@/hooks/use-pipeline-webhooks"

function baseWebhook(overrides: Partial<PipelineWebhook> = {}): PipelineWebhook {
  return {
    id: "wh-1",
    workspace_id: "ws-1",
    name: "github-pr-reviews",
    target_pipeline_id: "p1",
    target_pipeline_slug: "pr-review",
    token: "",
    signing_secret_set: true,
    inputs_template: { source: "github" },
    enabled: true,
    rate_limit_per_min: 60,
    fire_count: 12,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  }
}

const onSave = vi.fn()
const onCancel = vi.fn()

beforeEach(() => {
  vi.clearAllMocks()
})

/** Answer the rotation question with `label`. */
async function answer(label: "Rotate and save" | "Cancel") {
  const dialog = await screen.findByRole("alertdialog", { name: "Rotate the signing secret?" })
  fireEvent.click(within(dialog).getByRole("button", { name: label }))
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
  return dialog
}

describe("RoutineWebhookEditorDialog", () => {
  it("renders nothing with no webhook", () => {
    const { container } = render(
      <RoutineWebhookEditorDialog webhook={null} onCancel={onCancel} onSave={onSave} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it("prefills name, rate limit and inputs template from the webhook", () => {
    render(<RoutineWebhookEditorDialog webhook={baseWebhook()} onCancel={onCancel} onSave={onSave} />)
    expect(screen.getByLabelText(/^name$/i)).toHaveValue("github-pr-reviews")
    expect(screen.getByLabelText(/rate limit/i)).toHaveValue(60)
    expect(screen.getByLabelText(/inputs template/i)).toHaveValue(JSON.stringify({ source: "github" }, null, 2))
  })

  it("saves name/rate-limit/inputs-template edits without rotate_secret by default", () => {
    render(<RoutineWebhookEditorDialog webhook={baseWebhook()} onCancel={onCancel} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: "renamed" } })
    fireEvent.change(screen.getByLabelText(/rate limit/i), { target: { value: "120" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    expect(onSave).toHaveBeenCalledTimes(1)
    const body = onSave.mock.calls[0][0]
    expect(body.name).toBe("renamed")
    expect(body.rate_limit_per_min).toBe(120)
    expect(body.rotate_secret).toBeUndefined()
    // No field in the body can ever carry a token/URL change — the dialog
    // has no such field to begin with, which is the actual guarantee.
    expect(body).not.toHaveProperty("token")
  })

  it("rejects invalid JSON in the inputs template instead of saving", () => {
    render(<RoutineWebhookEditorDialog webhook={baseWebhook()} onCancel={onCancel} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText(/inputs template/i), { target: { value: "{not json" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByText(/must be valid json/i)).toBeInTheDocument()
  })

  it("only sends rotate_secret:true after an explicit confirmation", async () => {
    render(<RoutineWebhookEditorDialog webhook={baseWebhook()} onCancel={onCancel} onSave={onSave} />)
    fireEvent.click(screen.getByRole("switch", { name: /rotate signing secret/i }))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(onSave).not.toHaveBeenCalled()
    const dialog = await answer("Rotate and save")
    expect(dialog).toHaveTextContent("The URL stays the same")
    expect(dialog).toHaveTextContent("Every sender signing with the current secret fails verification")
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ rotate_secret: true })))
  })

  it("does not save at all when the rotation confirmation is declined", async () => {
    render(<RoutineWebhookEditorDialog webhook={baseWebhook()} onCancel={onCancel} onSave={onSave} />)
    fireEvent.click(screen.getByRole("switch", { name: /rotate signing secret/i }))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await answer("Cancel")
    expect(onSave).not.toHaveBeenCalled()
    // Cancel answers the question only; the editor stays open with its edits.
    expect(onCancel).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog", { name: "Edit webhook" })).toBeInTheDocument()
  })

  it("calls onCancel without saving", () => {
    render(<RoutineWebhookEditorDialog webhook={baseWebhook()} onCancel={onCancel} onSave={onSave} />)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onCancel).toHaveBeenCalled()
    expect(onSave).not.toHaveBeenCalled()
  })
})
