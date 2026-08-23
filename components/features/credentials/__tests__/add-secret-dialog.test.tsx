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
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
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

// A centred card is right on a laptop and wrong on a phone: the shared
// DialogContent insets itself by 1rem, which leaves a 358px column that the
// step bar, six shape tiles and a two-button footer all have to share, and it
// pins the card vertically so the footer lands under the browser chrome.
//
// These two assertions changed when the surface moved onto CreateSurface, and
// only because the SHELL answers both questions differently — the behaviour
// being pinned (not a 358px centred card on a phone; one of a small set of
// fixed widths above it) is the same one.
//
//  · The phone layout was a hand-rolled full-screen takeover (inset-0,
//    100dvh, square corners). The shell's answer is a bottom sheet capped at
//    92dvh: the primary action lands at the thumb instead of mid-screen, and
//    the page you came from stays visible above it.
//  · 680px was this surface's own number. `md` is 640, one of the shell's
//    four widths — which is the entire point of the migration.
describe("on a phone", () => {
  it("becomes a bottom sheet rather than a centred 358px card", () => {
    renderDialog()
    const dialog = screen.getByRole("dialog")
    for (const cls of [
      "max-sm:inset-x-0",
      "max-sm:bottom-0",
      "max-sm:max-h-[92dvh]",
      "max-sm:max-w-none",
      "max-sm:rounded-t-2xl",
      "max-sm:translate-x-0",
      "max-sm:translate-y-0",
    ]) {
      expect(dialog.className).toContain(cls)
    }
  })

  it("stays a centred modal from sm up, at one of the shell's four widths", () => {
    renderDialog()
    const dialog = screen.getByRole("dialog")
    expect(dialog.className).toContain("sm:max-w-[640px]")
    expect(dialog.className).toContain("sm:max-h-[min(85vh,720px)]")
  })

  it("scrolls only the body, so the header and the actions never leave", () => {
    renderDialog()
    const body = screen.getByTestId("wizard-body")
    const dialog = screen.getByRole("dialog")
    expect(dialog.className).toContain("overflow-hidden")
    expect(body.className).toContain("overflow-y-auto")
    // The title and the step bar sit above the scrollport, the actions below.
    expect(body.contains(screen.getByRole("heading", { name: /add a credential/i }))).toBe(false)
    expect(body.contains(screen.getByTestId("wizard-footer"))).toBe(false)
  })
})

// The door is one of twelve, and the twelve are being moved onto one shell
// (components/layout/create-surface.tsx). These are the properties that come
// from the shell rather than from this file: one of four widths, the bottom
// sheet on a phone, the single scrollport — and the discard guard, which no
// hand-rolled dialog in the product had except the page editor.
describe("the shared create surface", () => {
  it("mounts CreateSurface rather than a dialog of its own", () => {
    renderDialog()
    const dialog = screen.getByRole("dialog")
    // md: one of the shell's four widths, not the twelfth bespoke one.
    expect(dialog.className).toContain("sm:max-w-[640px]")
    // The shell's geometry, verbatim — if these drift the surface has
    // stopped sharing the shell.
    expect(dialog.className).toContain("max-sm:max-h-[92dvh]")
    expect(dialog.className).toContain("max-sm:rounded-t-2xl")
    expect(dialog.className).toContain("overflow-hidden")
  })

  it("still posts the credential the wizard collected", async () => {
    h.apiFetch.mockResolvedValue({ ok: true, status: 201, json: async () => ({ id: "cred_new" }) })
    const { onSuccess } = renderDialog()

    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })
    fireEvent.change(screen.getByLabelText(/name \(which account\)/i), { target: { value: "internal-thing" } })
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.click(screen.getByRole("button", { name: /save secret/i }))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    const call = h.apiFetch.mock.calls.find(([url]) => String(url).startsWith("/api/v1/credentials?"))!
    expect(String(call[0])).toContain("workspace_id=ws1")
    expect(JSON.parse(String((call[1] as { body?: string }).body))).toMatchObject({
      name: "internal-thing",
      value: "abc123",
      type: "CLI_TOKEN",
      scope: "WORKSPACE",
    })
  })

  it("asks before throwing away a half-typed secret", async () => {
    renderDialog()
    fireEvent.click(screen.getByRole("button", { name: /^continue$/i }))
    fireEvent.change(screen.getByLabelText(/^token$/i), { target: { value: "abc123" } })

    fireEvent.keyDown(document.body, { key: "Escape", code: "Escape" })
    expect(await screen.findByRole("alertdialog")).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: /discard this credential/i })).toBeInTheDocument()
  })
})

describe("dismissal", () => {
  it("closes when the wizard cancels", () => {
    const { onOpenChange } = renderDialog()
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
