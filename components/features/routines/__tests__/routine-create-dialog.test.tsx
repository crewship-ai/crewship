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

  it("shows the step graph beside the code", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    expect(screen.getByTestId("graph")).toBeInTheDocument()
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
