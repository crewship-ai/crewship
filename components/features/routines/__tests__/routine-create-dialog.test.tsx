import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

const h = vi.hoisted(() => ({ role: "MANAGER" as string }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => [] })) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role }) }))
vi.mock("./../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div data-testid="graph" />,
}))

const editorProps: { code?: string; language?: string }[] = []
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: (p: { code: string; language: string }) => {
    editorProps.push({ code: p.code, language: p.language })
    return <div data-testid="editor" />
  },
}))

import { RoutineCreateDialog } from "../routine-create-dialog"

// The create dialog was the one authoring surface that had not moved:
// a bare textarea of JSON, an "invalid JSON" badge with no line, and
// no graph — while the routine editor next door had YAML, schema
// completion, located errors and the step graph. You authored blind,
// then edited with help.

const PROPS = { workspaceId: "ws-1", open: true, onClose: vi.fn(), onCreated: vi.fn() }

describe("<RoutineCreateDialog>", () => {
  beforeEach(() => {
    editorProps.length = 0
    h.role = "MANAGER"
  })

  it("opens the writer on the real editor, in YAML", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    expect(screen.getByTestId("editor")).toBeInTheDocument()
    expect(editorProps[editorProps.length - 1].language).toBe("yaml")
    // YAML, not the JSON the textarea used to hold.
    expect(editorProps[editorProps.length - 1].code).not.toContain('"dsl_version"')
    expect(editorProps[editorProps.length - 1].code).toContain("dsl_version:")
  })

  it("opens on the code, with the graph one click away", () => {
    // They used to sit side by side. With the identity aside also on screen
    // that is three columns inside an 800px surface — the graph got ~240px
    // and the code ~52% of the rest, so the thing you come here to do (type
    // a DSL) was the narrower of the two. Code leads; the graph is a reading
    // of it.
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))

    expect(screen.queryByTestId("graph")).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("radio", { name: "Preview" }))
    expect(screen.getByTestId("graph")).toBeInTheDocument()

    // And back, without losing the buffer — it is a look, not a mode.
    fireEvent.click(screen.getByRole("radio", { name: "Code" }))
    expect(screen.queryByTestId("graph")).not.toBeInTheDocument()
  })

  it("hides the test-gate escape hatch from a role the server would refuse", () => {
    // skip_test_gate is roleManage on the server. Offering it to a
    // MANAGER buys them a 403 and reads as a broken product.
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    expect(screen.queryByText(/Skip test-run gate/i)).not.toBeInTheDocument()
  })

  it("offers it to an ADMIN", () => {
    h.role = "ADMIN"
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    expect(screen.getByText(/Skip test-run gate/i)).toBeInTheDocument()
  })

  it("no longer calls the hand-written path step-by-step", () => {
    // It never stepped. It was one text field.
    render(<RoutineCreateDialog {...PROPS} />)
    expect(screen.queryByText(/step by step/i)).not.toBeInTheDocument()
  })
})

// ── The entry tiles are three routes, not one route and two greys ────────
//
// Two of the three carried accent="slate" and no meta, so the picker read as
// "the blue one, and some other options". /design gives each route its own
// colour and says in a word what you trade: fastest, or full control.
describe("New routine — the three entry tiles", () => {
  function tile(name: RegExp) {
    return screen.getByRole("button", { name }) as HTMLButtonElement
  }

  it("gives each route a colour of its own", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    const glyphTint = (name: RegExp) =>
      tile(name).querySelector("span")?.className ?? ""

    const describe_ = glyphTint(/^Describe it/)
    const fork = glyphTint(/^Fork an existing routine/)
    const write = glyphTint(/^Write it yourself/)

    expect(describe_).not.toBe(fork)
    expect(fork).not.toBe(write)
    expect(write).not.toBe(describe_)
  })

  it("says what each route trades rather than marking one with a sparkle", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    expect(tile(/^Describe it/).textContent).toMatch(/fastest/i)
    expect(tile(/^Write it yourself/).textContent).toMatch(/full control/i)
  })
})

// ── Every mode has a way out, and it is the same way out ─────────────────
//
// This dialog is a router over four screens, and only two of them rendered a
// CreateSurfaceFooter: describe and advanced. Entry and fork rendered a body
// and nothing else, so the shell's own rule — "Cancel is always present,
// always leftmost of the action group, always in the same place"
// (create-surface.tsx) — was false on half of this surface. It was not a dead
// end (the header's × still closed it, as did Esc), which is exactly why it
// survived: the flow works, so nobody hits a wall, and the person who is
// looking for the button everywhere else in the product simply does not find
// it here. That is a discoverability cost paid quietly and forever.
//
// The shell already supports the shape these two screens need — a footer with
// no primary at all, because on both of them the ACTION IS A ROW (a route
// tile, a routine to fork). CreateSurfaceFooter made `primaryLabel`/`onPrimary`
// optional for precisely this case, and its doc comment names the defect these
// tests pin: "before this was optional it rendered no footer at all, and
// therefore no Cancel".
describe("New routine — every mode offers a Cancel", () => {
  function openDialog() {
    const onClose = vi.fn()
    render(
      <RoutineCreateDialog workspaceId="ws-1" open onClose={onClose} onCreated={vi.fn()} />,
    )
    return { onClose }
  }

  const cancel = () => screen.getByRole("button", { name: "Cancel" })

  it("gives the entry screen one, where there was only the header's ×", () => {
    const { onClose } = openDialog()
    fireEvent.click(cancel())
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("gives the fork list one too", () => {
    const { onClose } = openDialog()
    fireEvent.click(screen.getByText("Fork an existing routine"))
    // The header's back arrow returns you to the entry screen; Cancel leaves
    // the dialog. Two different exits, and the fork list offered only the one
    // that keeps you inside.
    fireEvent.click(cancel())
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("does not stop to ask on a screen with nothing on it to lose", () => {
    // `guardCancel` defaults to true, and it is LEFT at true here on purpose:
    // this Cancel closes the whole dialog, so it is the header ×'s twin and
    // must behave like it. The pickers that opt out (crew-icon,
    // avatar) do so because their Cancel means "back out of this panel" while
    // the surface behind it stays dirty — a different button wearing the same
    // word. What makes the default safe here is that the surface reports
    // `dirty` per mode, and neither the entry tiles nor the fork list holds
    // any input, so the guard is inert rather than a false alarm.
    const { onClose } = openDialog()
    fireEvent.click(cancel())
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("still asks on a screen that does hold input", () => {
    // The other half of the decision above, and the reason Cancel is not
    // opted out of the guard: on the editor there IS work, and Cancel must
    // not be the one exit that drops it silently while Esc, the overlay and
    // the × all ask. (Advanced mode already had a footer — this pins the
    // behaviour the two new footers were measured against.)
    const { onClose } = openDialog()
    fireEvent.click(screen.getByText("Write it yourself"))
    fireEvent.change(screen.getByPlaceholderText("Friendly name"), {
      target: { value: "nightly sweep" },
    })

    fireEvent.click(cancel())
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

// ── The footer hint has to be true on the screen it is printed on ────────
//
// CreateSurfaceFooter's hint defaults to "⌘↵ to confirm · Esc to cancel",
// which is the shell's contract and true wherever there is a primary to
// confirm. On the entry screen and the fork list there is none — the dialog's
// own handleKeyboardSubmit no-ops for both modes — so printing the default
// would have been the migration's other failure mode: not a missing control,
// but a documented keystroke that does nothing when you press it. Esc is the
// half of the contract these screens actually honour, so it is the half they
// claim. add-integration-dialog.tsx made the same call for the same reason.
describe("New routine — the keyboard hint", () => {
  it("names only Esc where ⌘↵ has nothing to confirm", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    expect(screen.queryByText("⌘↵")).not.toBeInTheDocument()
    expect(screen.getByText("Esc")).toBeInTheDocument()

    fireEvent.click(screen.getByText("Fork an existing routine"))
    expect(screen.queryByText("⌘↵")).not.toBeInTheDocument()
    expect(screen.getByText("Esc")).toBeInTheDocument()
  })

  it("keeps the full contract where there is a primary", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Describe it"))
    expect(screen.getByText("⌘↵")).toBeInTheDocument()
  })
})
