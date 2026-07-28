// "+ Add" on /credentials opens a centred modal, not a right-hand sheet.
//
// The shape is the point, not the taste: New crew is a Dialog, and a
// multi-step form that the user is meant to work THROUGH belongs in the same
// container everywhere in the app. A sheet reads as an inspector — something
// you slide out beside the thing you were already looking at — which is right
// for the detail view and wrong for a creation flow that owns the screen
// until it is finished or abandoned.
//
// These tests pin the container and the two behaviours the container is
// responsible for: the draft never survives a close, and cancel closes.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { AddSecretSheet } from "../add-secret-sheet"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => [] })
})

function renderDialog(open = true) {
  const onOpenChange = vi.fn()
  const onSuccess = vi.fn()
  const view = render(
    <AddSecretSheet
      workspaceId="ws1"
      open={open}
      onOpenChange={onOpenChange}
      onSuccess={onSuccess}
    />,
  )
  return { onOpenChange, onSuccess, ...view }
}

describe("container", () => {
  it("is a modal dialog, the same container New crew uses", () => {
    renderDialog()
    const dialog = screen.getByRole("dialog")
    expect(dialog).toBeInTheDocument()
    // role="dialog" alone proves nothing here: Radix builds Sheet on the
    // Dialog primitive, so a right-hand sheet answers that query too. The
    // ui-kit's data-slot is what actually separates them, which is why the
    // first version of this test passed against the sheet it was written to
    // replace.
    expect(dialog).toHaveAttribute("data-slot", "dialog-content")
    expect(dialog).not.toHaveAttribute("data-slot", "sheet-content")
  })

  it("names itself, so the modal is not an unlabelled box", () => {
    renderDialog()
    expect(screen.getByRole("dialog", { name: /add a credential/i })).toBeInTheDocument()
  })

  it("renders nothing while closed", () => {
    renderDialog(false)
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })
})

describe("draft hygiene", () => {
  it("does not keep the wizard mounted while closed", () => {
    const { rerender, onOpenChange, onSuccess } = renderDialog(true)
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    rerender(
      <AddSecretSheet
        workspaceId="ws1"
        open={false}
        onOpenChange={onOpenChange}
        onSuccess={onSuccess}
      />,
    )
    // A wizard that survives a close reopens on step 3 with somebody else's
    // pasted token still in state — a leak waiting for a screenshot.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })
})

describe("dismissal", () => {
  it("closes when the wizard cancels", () => {
    const { onOpenChange } = renderDialog()
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
