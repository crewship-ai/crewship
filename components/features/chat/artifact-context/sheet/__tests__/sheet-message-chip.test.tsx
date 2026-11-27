import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, act } from "@testing-library/react"
import { SheetMessageChip } from "../sheet-message-chip"

const usePage = vi.fn()
vi.mock("@/hooks/use-pages", () => ({ usePage: (...args: unknown[]) => usePage(...args) }))

const chip = (over: Partial<Parameters<typeof SheetMessageChip>[0]> = {}) => {
  const props = { workspaceId: "ws1", pageId: "pg_1", allowed: false, blocked: null, warning: null, sending: false, checking: false, refusal: null as string | null, onAllowedChange: vi.fn(), onRemove: vi.fn(), ...over }
  render(<SheetMessageChip {...props} />)
  return props
}

describe("SheetMessageChip", () => {
  beforeEach(() => { cleanup(); usePage.mockReset(); usePage.mockReturnValue({ page: { id: "pg_1", name: "Quarterly plan" }, notFound: false }) })

  it("says the choice holds for the chat when it stays on for every message", () => {
    chip({ allowed: true, standing: true })
    expect(screen.getByRole("group", { name: "Sheet for this message" })).toHaveTextContent("The agent may change this Sheet in this chat until you turn this off.")
    expect(screen.queryByText(/in this message only/)).toBeNull()
  })

  it("names the Sheet and says the agent gets nothing while the choice is off", () => {
    chip()
    expect(screen.getByRole("group", { name: "Sheet for this message" })).toHaveTextContent("Quarterly plan")
    const choice = screen.getByRole("switch", { name: /Allow agent edits/ })
    expect(choice).not.toBeChecked()
    expect(choice).toHaveAccessibleDescription(/does not get this Sheet/)
  })

  it("is chosen with the keyboard or a click, and says it is for one message", () => {
    const { onAllowedChange } = chip()
    fireEvent.click(screen.getByRole("switch"))
    expect(onAllowedChange).toHaveBeenCalledWith(true)
    cleanup()
    chip({ allowed: true })
    expect(screen.getByRole("switch")).toHaveAccessibleDescription(/this message only/)
  })

  it("says in advance why it cannot be chosen", () => {
    chip({ blocked: "A message carries either a Page or a Sheet." })
    expect(screen.getByRole("switch")).toBeDisabled()
    expect(screen.getByText("A message carries either a Page or a Sheet.")).toBeInTheDocument()
  })

  it("says in advance that unsaved work keeps the Sheet with the person", () => {
    chip({ allowed: true, warning: "Your last change is not saved yet." })
    expect(screen.getByText("Your last change is not saved yet.")).toBeInTheDocument()
  })

  it("reads what the choice means and what to expect as one line under the Sheet, not two separate notes", () => {
    chip({ allowed: true, warning: "Your last change is not saved yet." })
    const line = screen.getByText("Your last change is not saved yet.").closest("p")
    expect(line).not.toBeNull()
    expect(line).toBe(screen.getByText("The agent may change this Sheet in this message only.").closest("p"))
    expect(screen.getByRole("switch")).toHaveAccessibleDescription("The agent may change this Sheet in this message only. Your last change is not saved yet.")
    cleanup()
    chip({ notice: "The Sheet changed, so the choice was turned off." })
    expect(screen.getByText("The Sheet changed, so the choice was turned off.").closest("p")).toBe(screen.getByText("The agent does not get this Sheet with the message.").closest("p"))
  })

  it("shows no name of a Sheet the reader cannot open, and cannot be chosen", () => {
    usePage.mockReturnValue({ page: null, notFound: true })
    chip()
    expect(screen.queryByText("Quarterly plan")).toBeNull()
    expect(screen.getByRole("switch")).toBeDisabled()
  })

  it("can be removed from the message", () => {
    const { onRemove } = chip()
    fireEvent.click(screen.getByRole("button", { name: "Remove Quarterly plan from this message" }))
    expect(onRemove).toHaveBeenCalled()
  })

  it("offers no choice the server says would be refused, and says the server's reason", () => {
    chip({ refusal: "The agent can change only a Sheet in your own private space." })
    expect(screen.queryByRole("switch")).toBeNull()
    expect(screen.getByText("The agent can change only a Sheet in your own private space.")).toBeInTheDocument()
  })

  it("says it is checking until the server answered, with the choice not yet available", () => {
    chip({ checking: true })
    expect(screen.getByRole("switch")).toBeDisabled()
    expect(screen.getByText(/Checking whether the agent may change this Sheet/)).toBeInTheDocument()
  })

  it("turns a choice made earlier off once the server refuses", () => {
    const { onAllowedChange } = chip({ allowed: true, refusal: "No." })
    expect(onAllowedChange).toHaveBeenCalledWith(false)
  })
  // The choice says in one sentence what it allows, on hover and on keyboard focus.
  it("explains Allow agent edits in one sentence when the switch has focus, keeping its own description", () => {
    chip()
    const choice = screen.getByRole("switch", { name: /Allow agent edits/ })
    act(() => choice.focus())
    expect(screen.getByRole("tooltip")).toHaveTextContent("The agent may change this workbook in this message. Every change can be undone.")
    expect(choice).toHaveAccessibleDescription(/does not get this Sheet/)
  })
})
