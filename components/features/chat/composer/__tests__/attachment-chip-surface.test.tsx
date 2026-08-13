import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, within, act } from "@testing-library/react"

// =============================================================================
// One attachment list, one visible chip renderer.
//
// `AttachmentZone` draws BOTH a drop target and the chip list for the session's
// attachments. The composer mounts one; a form's `file` / `photo` field mounts
// another. With a sheet open, both were drawing the same list, so an uploaded
// file showed up twice on one screen and the user had no way to tell whether
// they had attached it twice.
//
// The rule these pin: while a sheet is showing an upload control, the SHEET is
// where the chips live — that is where the field asking for the file is, and
// removing a chip there is editing that field's answer. In every other case
// (no sheet, or a form with nothing to upload) the composer keeps them, because
// the composer's paperclip is then the only way a file got there at all.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("../mention-autocomplete", () => ({
  MentionAutocomplete: () => null,
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"
import type { AskForm, AskValues } from "../../asks/types"

/** Stand-in for lib/ask-template.ts — see asks/__tests__/ask-form-sheet.test.tsx. */
const renderAskTemplate = (form: AskForm, values: AskValues) =>
  form.template.replace(/\{\{(\w+)\}\}/g, (_m, k) => {
    const v = values[k]
    return Array.isArray(v) ? v.join(", ") : v == null ? "" : `${v}`
  })

const withUpload: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj fakturu od {{supplier}}",
  attachment: "required",
  fields: [
    { name: "supplier", label: "Supplier", type: "text", required: true },
    { name: "document", label: "Document", type: "file" },
  ],
}

/** Two upload fields is the same duplication one level down: two slots, two
 *  AttachmentZones, one list. */
const twoUploads: AskForm = {
  ...withUpload,
  id: "receipt-2",
  fields: [
    { name: "document", label: "Document", type: "file" },
    { name: "shot", label: "Photo", type: "photo" },
  ],
}

/** A form that asks for an attachment but renders no upload control of its own
 *  — the composer's paperclip is the only way to satisfy it, so the composer
 *  must keep showing what was attached. */
const noUploadField: AskForm = {
  id: "expense",
  label: "Log an expense",
  template: "Expense from {{supplier}}",
  attachment: "required",
  fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
}

const baseProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Riley",
  isStreaming: false,
  connectionStatus: "connected",
  stopGeneration: vi.fn(),
  ensureSession: vi.fn(async () => {}),
  sendMessage: vi.fn(),
}

const FILE = "IMG_4821.heic"

function attachReady(name = FILE) {
  // act(): the store is written from outside React, exactly as a finished
  // upload writes it, and the chips are what re-render in response.
  act(() => {
    useComposerStore.setState({
      attachments: {
        "sess-1": [
          {
            id: "att-1",
            name,
            size: 2100,
            type: "image/heic",
            status: "ready",
            url: `/output/riley/attachments/sess-1/${name}`,
            path: `attachments/sess-1/${name}`,
          },
        ],
      },
    })
  })
}

/** Every chip carries the filename once, so this is "how many chips are on
 *  screen for this file". */
function chipsFor(name = FILE): HTMLElement[] {
  return screen.queryAllByText(name)
}

describe("attachment chips have exactly one home at a time", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
  })

  it("shows an uploaded file once, in the sheet, while a form with an upload field is open", () => {
    render(
      <ChatComposer
        {...baseProps}
        variant="desktop"
        askForm={withUpload}
        onCloseAskForm={vi.fn()}
        renderAskTemplate={renderAskTemplate}
      />,
    )
    attachReady()

    expect(chipsFor()).toHaveLength(1)
    // …and it is the sheet's copy: the field that asked for the file is the
    // thing the chip is the answer to.
    expect(within(screen.getByTestId("ask-sheet")).getAllByText(FILE)).toHaveLength(1)
  })

  it("shows it once even when the form has two upload fields", () => {
    render(
      <ChatComposer
        {...baseProps}
        variant="desktop"
        askForm={twoUploads}
        onCloseAskForm={vi.fn()}
        renderAskTemplate={renderAskTemplate}
      />,
    )
    attachReady()

    expect(chipsFor()).toHaveLength(1)
  })

  it("keeps the chips in the composer for a form that renders no upload control", () => {
    render(
      <ChatComposer
        {...baseProps}
        variant="desktop"
        askForm={noUploadField}
        onCloseAskForm={vi.fn()}
        renderAskTemplate={renderAskTemplate}
      />,
    )
    attachReady()

    expect(chipsFor()).toHaveLength(1)
    // Not in the sheet — there is no upload field there to own it.
    expect(within(screen.getByTestId("ask-sheet")).queryAllByText(FILE)).toHaveLength(0)
  })

  it("hands the chips back to the composer when the sheet closes without sending", () => {
    const { rerender } = render(
      <ChatComposer
        {...baseProps}
        variant="desktop"
        askForm={withUpload}
        onCloseAskForm={vi.fn()}
        renderAskTemplate={renderAskTemplate}
      />,
    )
    attachReady()
    expect(within(screen.getByTestId("ask-sheet")).getAllByText(FILE)).toHaveLength(1)

    // Cancel / Escape / tap-away all land here: the sheet unmounts and nothing
    // was sent. The upload already reached the server and the store is
    // untouched, so the file must still be attached — and visible, or the next
    // send would carry a file the user can no longer see.
    rerender(
      <ChatComposer
        {...baseProps}
        variant="desktop"
        askForm={null}
        onCloseAskForm={vi.fn()}
        renderAskTemplate={renderAskTemplate}
      />,
    )

    expect(screen.queryByTestId("ask-sheet")).toBeNull()
    expect(useComposerStore.getState().attachments["sess-1"]).toHaveLength(1)
    expect(chipsFor()).toHaveLength(1)
  })

  it("does the same on the mobile composer", () => {
    render(
      <ChatComposer
        {...baseProps}
        variant="mobile"
        askForm={withUpload}
        onCloseAskForm={vi.fn()}
        renderAskTemplate={renderAskTemplate}
      />,
    )
    attachReady()

    expect(chipsFor()).toHaveLength(1)
    expect(within(screen.getByTestId("ask-sheet")).getAllByText(FILE)).toHaveLength(1)
  })

  it("shows the chips in the composer when no sheet is open at all", () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)
    attachReady()

    expect(chipsFor()).toHaveLength(1)
  })
})
