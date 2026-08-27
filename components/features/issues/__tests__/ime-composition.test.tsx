// Enter is not "submit" for everyone.
//
// A reader typing Japanese, Chinese or Korean composes a character in the
// input method and presses Enter to CONFIRM the candidate. The browser sends
// that keystroke to the page like any other, with `isComposing` set — and a
// handler that only looks at `e.key === "Enter"` commits whatever half-typed
// romaji or pinyin is in the box at that instant.
//
// The link composer was fixed when CodeRabbit caught it there
// (issue-code-links.test.tsx). These are the four sites that had the same gap
// and no test, and `TitleEditor` is the one that hurts: it PATCHes straight
// onto the issue, and a title change writes no `mission_activity` row, so
// there is no record of what the title used to be.
//
// Each case asserts BOTH halves. "Composing Enter does nothing" alone passes
// against an input that ignores Enter entirely, which is a different bug
// wearing this test as cover — so every case then ends composition and
// presses the same key again, and requires the commit.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

import { TitleEditor, AddRelationPicker, type IssueCardEdit } from "../issue-card-editors"
import { ProjectNameEditor } from "../project-card-editors"
import { LabelsDialog } from "../labels-dialog"
import type { IssueLabel } from "@/lib/types/mission"

vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() },
}))

function issueEdit(over: Partial<IssueCardEdit> = {}): IssueCardEdit {
  return {
    agents: [],
    labels: [],
    projects: [],
    routines: [],
    milestones: [],
    patch: vi.fn(async () => true),
    addRelation: vi.fn(async () => true),
    removeRelation: vi.fn(async () => {}),
    ...over,
  }
}

describe("TitleEditor — Enter confirms an IME candidate before it means save", () => {
  it("does not save the half-composed title", async () => {
    const onSave = vi.fn()
    render(<TitleEditor title="Original" onSave={onSave} />)
    fireEvent.click(screen.getByRole("button", { name: /edit title/i }))
    const box = screen.getByLabelText(/issue title/i)

    // "にほ" — the reader is mid-word, and this Enter picks a candidate.
    fireEvent.change(box, { target: { value: "Origina" } })
    fireEvent.keyDown(box, { key: "Enter", isComposing: true })
    expect(onSave).not.toHaveBeenCalled()

    // Confirming a candidate must also leave the editor OPEN. Closing it
    // would throw the draft away, which is the same data loss by another
    // route.
    expect(screen.getByLabelText(/issue title/i)).toBeInTheDocument()

    // The same key, composition over, still saves.
    fireEvent.change(box, { target: { value: "Original title, finished" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("Original title, finished"))
  })

  it("does not close the editor on a composing Escape either", () => {
    // Escape during composition means "cancel this candidate", not "discard
    // everything I have typed".
    const onSave = vi.fn()
    render(<TitleEditor title="Original" onSave={onSave} />)
    fireEvent.click(screen.getByRole("button", { name: /edit title/i }))
    const box = screen.getByLabelText(/issue title/i)

    fireEvent.change(box, { target: { value: "half typed" } })
    fireEvent.keyDown(box, { key: "Escape", isComposing: true })
    expect(screen.getByLabelText(/issue title/i)).toHaveValue("half typed")

    fireEvent.keyDown(box, { key: "Escape" })
    expect(screen.queryByLabelText(/issue title/i)).toBeNull()
  })
})

describe("AddRelationPicker — Enter confirms an IME candidate before it means add", () => {
  it("does not add the relation mid-composition", async () => {
    const edit = issueEdit()
    render(<AddRelationPicker edit={edit} />)
    fireEvent.click(screen.getByRole("button", { name: /add link/i }))
    const menu = screen.getByRole("dialog")
    const box = within(menu).getByLabelText(/target issue identifier/i)

    fireEvent.change(box, { target: { value: "EN" } })
    fireEvent.keyDown(box, { key: "Enter", isComposing: true })
    expect(edit.addRelation).not.toHaveBeenCalled()

    fireEvent.change(box, { target: { value: "ENG-5" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await waitFor(() => expect(edit.addRelation).toHaveBeenCalledWith("ENG-5", "relates_to"))
  })
})

describe("ProjectNameEditor — Enter confirms an IME candidate before it means save", () => {
  it("does not save the half-composed name", async () => {
    const onSave = vi.fn()
    render(<ProjectNameEditor name="Original" onSave={onSave} />)
    fireEvent.click(screen.getByRole("button", { name: /edit project name/i }))
    const box = screen.getByLabelText(/project name/i)

    fireEvent.change(box, { target: { value: "Origina" } })
    fireEvent.keyDown(box, { key: "Enter", isComposing: true })
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByLabelText(/project name/i)).toBeInTheDocument()

    fireEvent.change(box, { target: { value: "Original project, finished" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("Original project, finished"))
  })
})

describe("LabelsDialog — Enter confirms an IME candidate before it means create/rename", () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ id: "l1" }),
    })) as unknown as ReturnType<typeof vi.fn>
    global.fetch = fetchMock as unknown as typeof fetch
  })

  function labelsProps(over: Partial<React.ComponentProps<typeof LabelsDialog>> = {}) {
    const labels: IssueLabel[] = [
      { id: "l1", name: "bug", color: "#ef4444", label_group: null },
    ]
    return {
      open: true,
      onOpenChange: vi.fn(),
      labels,
      workspaceId: "ws1",
      onLabelsChanged: vi.fn(),
      ...over,
    }
  }

  /** POSTs and PATCHes only — the dialog issues no reads. */
  function writes() {
    return fetchMock.mock.calls.filter(([, init]) => {
      const method = (init as RequestInit | undefined)?.method
      return method === "POST" || method === "PATCH"
    })
  }

  it("does not create a label from a half-composed name", async () => {
    render(<LabelsDialog {...labelsProps()} />)
    const box = screen.getByPlaceholderText(/label name/i)

    fireEvent.change(box, { target: { value: "ba" } })
    fireEvent.keyDown(box, { key: "Enter", isComposing: true })
    await new Promise((r) => setTimeout(r, 0))
    expect(writes()).toHaveLength(0)

    fireEvent.change(box, { target: { value: "backend" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(String(writes()[0][1]?.body)).toContain("backend")
  })

  it("does not rename a label from a half-composed name", async () => {
    render(<LabelsDialog {...labelsProps()} />)
    fireEvent.click(screen.getByRole("button", { name: "Edit bug" }))
    const box = await screen.findByDisplayValue("bug")

    fireEvent.change(box, { target: { value: "bu" } })
    fireEvent.keyDown(box, { key: "Enter", isComposing: true })
    await new Promise((r) => setTimeout(r, 0))
    expect(writes()).toHaveLength(0)

    // Escape mid-composition cancels the candidate, not the edit.
    fireEvent.keyDown(box, { key: "Escape", isComposing: true })
    expect(screen.getByDisplayValue("bu")).toBeInTheDocument()

    fireEvent.change(box, { target: { value: "bugfix" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(String(writes()[0][1]?.body)).toContain("bugfix")
  })
})
