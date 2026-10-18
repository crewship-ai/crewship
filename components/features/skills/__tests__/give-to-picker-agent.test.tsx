import { describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen, within } from "@testing-library/react"
import { GiveToPicker, heldSkillsDiff } from "@/components/features/skills/give-to-picker"

// Direction "agent-to-skills" (CRW-03, #3481): one agent, the workspace's skills.
const library = [
  { id: "s1", name: "web-research", display_name: "Web research" },
  { id: "s2", name: "pdf-reports", display_name: "PDF reports", category: "DOCS" },
  { id: "s3", name: "code-review", display_name: "Code review" },
]
const agent = { id: "a1", name: "Scout" }
const base = { direction: "agent-to-skills" as const, agent, library, held: new Map([["s1", "as1"]]), onClose: vi.fn() }

describe("heldSkillsDiff", () => {
  it("adds newly checked skills and removes unchecked held ones", () => {
    const held = new Map([["s1", "as1"], ["s2", "as2"]])
    expect(heldSkillsDiff(held, new Set(["s2", "s3"]))).toEqual({ add: ["s3"], remove: [{ skillId: "s1", assignmentId: "as1" }] })
  })
})

describe("GiveToPicker direction agent-to-skills", () => {
  it("searches, sends only the diff and reports once", async () => {
    const write = vi.fn(async () => undefined)
    const onClose = vi.fn()
    const onApplied = vi.fn()
    render(<GiveToPicker {...base} write={write} onApplied={onApplied} onClose={onClose} />)
    const dialog = screen.getByRole("dialog", { name: /Give skills to Scout/ })
    expect(within(dialog).getByRole("checkbox", { name: "Web research" })).toBeChecked()
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Search skills" }), { target: { value: "pdf" } })
    expect(within(dialog).getAllByRole("checkbox")).toHaveLength(1)
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "PDF reports" }))
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Search skills" }), { target: { value: "" } })
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Web research" }))
    expect(within(dialog).getByText("1 added · 1 removed")).toBeInTheDocument()
    await act(async () => { fireEvent.click(within(dialog).getByRole("button", { name: "Apply" })) })
    expect(write.mock.calls.map(([c]) => c)).toEqual(
      expect.arrayContaining([{ kind: "add", skillId: "s2" }, { kind: "remove", skillId: "s1", assignmentId: "as1" }]),
    )
    expect(write).toHaveBeenCalledTimes(2)
    expect(onApplied).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it("partial failure keeps the dialog open and Retry resends only the failed change", async () => {
    const write = vi.fn(async (c: { skillId: string }) => { if (c.skillId === "s3") throw new Error("no") })
    const onClose = vi.fn()
    render(<GiveToPicker {...base} write={write} onClose={onClose} />)
    fireEvent.click(screen.getByRole("checkbox", { name: "PDF reports" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Code review" }))
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Apply" })) })
    expect(screen.getByText(/1 change failed for Code review/)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    write.mockClear()
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Retry" })) })
    expect(write).toHaveBeenCalledTimes(1)
    expect(write.mock.calls[0][0]).toMatchObject({ kind: "add", skillId: "s3" })
  })

  it("loading and failed library states", () => {
    const { rerender } = render(<GiveToPicker {...base} library={null} write={vi.fn()} />)
    expect(screen.getByRole("status")).toHaveTextContent("Loading skills")
    rerender(<GiveToPicker {...base} library={null} loadFailed write={vi.fn()} />)
    expect(screen.getByRole("alert")).toHaveTextContent("could not be loaded")
  })

  it("phone 390 / tablet 1024: rows keep the 44 px target and the surface is a bottom sheet below sm", () => {
    render(<GiveToPicker {...base} write={vi.fn()} />)
    const dialog = screen.getByRole("dialog")
    expect(dialog.className).toContain("max-sm:bottom-0")
    const row = screen.getByRole("checkbox", { name: "PDF reports" }).closest("label")!
    expect(row.className).toContain("min-h-11")
    expect(dialog.className).toContain("sm:max-w-[480px]")
  })
})
