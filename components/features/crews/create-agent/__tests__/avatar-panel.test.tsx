// =============================================================================
// The avatar picker on New agent is a PANEL, not a second dialog.
//
// It used to mount AvatarPickerDialog — a Radix dialog — on top of the create
// surface, which is also a Radix dialog. Two overlays means two focus traps
// and two Escape handlers over one keystroke, the outer surface's discard
// guard cannot see the inner one, and on a phone the inner dialog renders
// inside the outer's bottom sheet at whatever width is left. New crew's icon
// picker was moved off that pattern already; this file pins that New agent
// stays off it too.
// =============================================================================

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateAgentDialog } from "../create-agent-dialog"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const CREWS = [{ id: "c1", slug: "engineering", name: "Engineering" }]

/**
 * The dialog reads the integrations + notification-channel catalogues on
 * mount for its Tools & notifications section, and vitest.setup.ts fails any
 * unmocked network call. Nothing in this file is about those lists, so they
 * are answered empty and everything else is left to the individual test.
 */
function stubFetch(rest: () => Response = () => new Response("{}", { status: 200 })) {
  return vi.spyOn(global, "fetch").mockImplementation(async (url) => {
    const u = String(url)
    if (u.includes("/api/v1/integrations")) {
      return new Response("[]", { status: 200, headers: { "Content-Type": "application/json" } })
    }
    if (u.includes("/api/v1/notification-channels")) {
      return new Response('{"channels":[]}', { status: 200, headers: { "Content-Type": "application/json" } })
    }
    return rest()
  })
}

beforeEach(() => {
  vi.restoreAllMocks()
  stubFetch()
})

function renderDialog() {
  return render(
    <CreateAgentDialog
      workspaceId="ws-1"
      open
      onOpenChange={vi.fn()}
      defaultCrewSlug="engineering"
      crews={CREWS}
      onCreated={vi.fn()}
    />,
  )
}

const openPicker = () =>
  fireEvent.click(screen.getByRole("button", { name: /Customize avatar/i }))

describe("<CreateAgentDialog> — leaving with unsaved input", () => {
  it("asks before the footer Cancel throws a draft away", async () => {
    // CreateSurfaceFooter routes Cancel through the discard guard unless the
    // caller opts out — `guardCancel` defaults to true (create-surface.tsx).
    // This dialog does not opt out, so all four exits (Esc, the overlay, the
    // header ×, and Cancel) ask the same question. Pinned because the default
    // used to be the other way round, and a silent Cancel is indistinguishable
    // from a working one until someone loses a draft.
    const onOpenChange = vi.fn()
    render(
      <CreateAgentDialog
        workspaceId="ws-1"
        open
        onOpenChange={onOpenChange}
        defaultCrewSlug="engineering"
        crews={CREWS}
        onCreated={vi.fn()}
      />,
    )
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
    fireEvent.click(screen.getByRole("button", { name: /^Cancel$/ }))

    await screen.findByText(/unsaved input/i)
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })
})

describe("<CreateAgentDialog> — the template row", () => {
  // It was six PersonaChips + "All 30 templates" + "Blank": eight pills that
  // wrap to two rows at the surface's 800px and take the top of the form
  // before any field is reached, for a control that is optional.
  it("is one control, not a row of pills", () => {
    renderDialog()
    const trigger = screen.getByRole("button", { name: /Choose a template/i })
    expect(trigger).toHaveTextContent(/Blank — browse \d+ templates/)

    // None of the featured personas are on the form any more; they live in
    // the catalogue behind this row, with the other two dozen.
    expect(screen.queryByRole("button", { name: /Filip —/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /Start blank — no template/ })).toBeNull()
  })

  it("states the pick, and only then offers a way to undo it", async () => {
    renderDialog()
    // Clear is not a permanent second control competing for the same decision.
    expect(screen.queryByRole("button", { name: /^Clear$/ })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: /Choose a template/i }))
    const first = await screen.findByRole("button", { name: /Filip/ })
    fireEvent.click(first)

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Choose a template/i })).toHaveTextContent(/Filip/),
    )
    fireEvent.click(screen.getByRole("button", { name: /^Clear$/ }))
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Choose a template/i })).toHaveTextContent(/Blank/),
    )
  })
})

describe("<CreateAgentDialog> — the avatar tile", () => {
  it("puts nothing on top of the portrait", () => {
    // The tile carried a pencil-in-a-circle badge at its bottom-right. That
    // works on New crew, where the tile is a flat glyph on a gradient and the
    // corner is empty. Here a DiceBear portrait fills the whole tile, so the
    // badge always landed on the face and read as a blob stuck to it —
    // reported twice as "a weird circle" before it was read as a control.
    renderDialog()
    const tile = screen.getByRole("button", { name: /Customize avatar/i })

    // The portrait is the only thing in the button.
    expect(tile.children).toHaveLength(1)
    expect(tile.querySelector("img")).not.toBeNull()
  })

  it("says what the avatar is, and opens the picker from there", () => {
    // The affordance the badge was carrying, moved to the caption New crew's
    // identity step already uses for the same job — and it now says what the
    // avatar currently IS, which the badge never did.
    renderDialog()
    const caption = screen.getByRole("button", { name: /tap to change/i })
    expect(caption).toHaveTextContent(/Bottts Neutral/i)

    fireEvent.click(caption)
    expect(screen.getByText(/Avatar — new agent/i)).toBeInTheDocument()
  })
})

describe("<CreateAgentDialog> — the avatar picker", () => {
  it("swaps the surface it is in, rather than stacking a dialog on it", () => {
    renderDialog()
    expect(document.querySelectorAll('[data-slot="dialog-content"]')).toHaveLength(1)

    openPicker()

    // Still exactly one overlay after opening the picker. This is the whole
    // point of the change; a second one here is the regression.
    expect(document.querySelectorAll('[data-slot="dialog-content"]')).toHaveLength(1)
    expect(screen.getByText(/Avatar — new agent/i)).toBeInTheDocument()
  })

  it("hides the form while it is up, and brings it back on the way out", () => {
    renderDialog()
    expect(screen.getByPlaceholderText("Filip")).toBeInTheDocument()

    openPicker()
    // The panel replaces the body; it does not sit beside it.
    expect(screen.queryByPlaceholderText("Filip")).toBeNull()
    expect(screen.getByLabelText("Avatar seed")).toBeInTheDocument()

    fireEvent.click(screen.getAllByRole("button", { name: /^Back$/ })[0])
    expect(screen.getByPlaceholderText("Filip")).toBeInTheDocument()
  })

  it("writes straight through to the draft — there is no agent to save to yet", async () => {
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
    openPicker()

    fireEvent.change(screen.getByLabelText("Avatar seed"), { target: { value: "chosen-seed" } })
    fireEvent.click(screen.getAllByRole("button", { name: /^Back$/ })[0])

    // Backing out keeps the pick: unlike the standalone dialog, this panel has
    // no Save/Cancel of its own, the same as New crew's icon picker.
    openPicker()
    await waitFor(() =>
      expect(screen.getByLabelText("Avatar seed")).toHaveValue("chosen-seed"),
    )
  })

  it("does not create the agent when ⌘↵ is pressed inside the panel", () => {
    const fetchSpy = stubFetch()
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
    openPicker()

    fireEvent.keyDown(document.querySelector('[data-slot="dialog-content"]')!, {
      key: "Enter",
      metaKey: true,
    })

    // The shortcut closes the panel. Firing it through to submit would create
    // an agent from a surface the user is not looking at. The catalogue reads
    // fire on mount regardless — the POST is what must be absent.
    const created = fetchSpy.mock.calls.find(
      ([url, init]) =>
        String(url).includes("/api/v1/agents") && (init as RequestInit | undefined)?.method === "POST",
    )
    expect(created).toBeUndefined()
    expect(screen.getByPlaceholderText("Filip")).toBeInTheDocument()
  })
})
