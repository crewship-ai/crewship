import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act } from "@testing-library/react"
import { createRef } from "react"
import type { NativeCraftHostStatus } from "@/components/features/pages/native-craft-editor-host"

let reportStatus: (status: NativeCraftHostStatus) => void = () => {}
let editorProps: { toolbar?: HTMLElement | null; historyHref?: string; placeLease?: unknown } = {}
vi.mock("@/components/features/artifacts-library/native-editor", () => ({
  ArtifactNativeEditor: (props: { onStatus?: (s: NativeCraftHostStatus) => void; toolbar?: HTMLElement | null; historyHref?: string; placeLease?: unknown }) => { reportStatus = props.onStatus ?? (() => {}); editorProps = props; return <div data-testid="editor" /> },
}))
vi.mock("@/hooks/use-pages", () => ({ usePage: () => ({ page: { id: "pg_1", name: "Quarterly plan" } }) }))
const confirmLeave = vi.fn()
vi.mock("../confirm-unsaved-sheet", () => ({ confirmLeaveUnsavedSheet: () => confirmLeave() }))

import { SheetSidePanel } from "../sheet-side-panel"

const status = (over: Partial<NativeCraftHostStatus> = {}): NativeCraftHostStatus =>
  ({ ready: true, dirty: false, canEdit: true, editing: true, holder: null, holderIsMe: false, openProposals: 0, recoveredDrafts: 0, ...over })

const renderPanel = (onClose = vi.fn(), dirtyRef = createRef<boolean>() as unknown as { current: boolean }) => {
  dirtyRef.current = false
  render(<SheetSidePanel workspaceId="ws1" pageId="pg_1" hidden={false} expanded={false} onExpandedChange={() => {}} onClose={onClose} dirtyRef={dirtyRef} />)
  return { onClose, dirtyRef }
}

describe("SheetSidePanel", () => {
  beforeEach(() => { cleanup(); confirmLeave.mockReset(); vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  // Take over editing covered Expand. jsdom does not lay out, so the classes that keep them apart are pinned.
  it("keeps Close and Expand their full size and lets the editing controls wrap instead of covering them", () => {
    renderPanel()
    for (const name of ["Close Sheet", "Expand Sheet"]) expect(screen.getByRole("button", { name }).className, name).toMatch(/(^|\s)shrink-0(\s|$)/)
    expect(screen.getByTestId("sheet-panel-bar").className).toMatch(/(^|\s)flex-wrap(\s|$)/)
  })
  // At 1440 px the one bar overflowed and slid sideways, hiding Close and the name.
  it("puts close, name, state and Expand on one row and who edits with the actions on a second, never wider than the panel", () => {
    renderPanel()
    const header = screen.getByTestId("sheet-panel-header")
    expect(header.className).toMatch(/(^|\s)overflow-x-hidden(\s|$)/)
    const first = screen.getByTestId("sheet-panel-title-row"), bar = screen.getByTestId("sheet-panel-bar")
    for (const name of ["Close Sheet", "Expand Sheet"]) expect(first).toContainElement(screen.getByRole("button", { name }))
    expect(first).toContainElement(screen.getByRole("heading"))
    expect(first).toContainElement(screen.getByTestId("sheet-panel-state"))
    expect(first, "the editing controls have their own row").not.toContainElement(bar)
    expect(header).toContainElement(bar)
  })
  // During a save conflict the header said "Not saved yet", as if a save were only pending.
  it("says Conflict while a save was refused because the Sheet changed on the server", () => {
    renderPanel()
    act(() => reportStatus(status({ editing: true, dirty: true, phase: "draft", conflict: true })))
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent(/^Conflict$/)
    expect(screen.getByTestId("sheet-panel-state")).toHaveAttribute("data-tone", "warn")
  })
  // The state says in one sentence what it means, on hover and on keyboard focus.
  it("explains Viewing · version N in one sentence, on keyboard focus", () => {
    renderPanel()
    act(() => reportStatus(status({ editing: false, version: "3" })))
    const state = screen.getByTestId("sheet-panel-state")
    expect(state).toHaveTextContent(/^Viewing · version 3$/)
    act(() => state.focus())
    expect(screen.getByRole("tooltip")).toHaveTextContent("You are looking at saved version 3. Click a cell to start editing.")
  })
  it("explains Conflict in one sentence, on keyboard focus, and stays a status", () => {
    renderPanel()
    act(() => reportStatus(status({ editing: true, dirty: true, phase: "draft", conflict: true })))
    const state = screen.getByTestId("sheet-panel-state")
    expect(state).toHaveAttribute("role", "status")
    act(() => state.focus())
    expect(screen.getByRole("tooltip")).toHaveTextContent("The workbook changed meanwhile. Your edits are kept here; nothing is lost.")
  })
  it("closes at once when nothing is unsaved", async () => {
    const { onClose } = renderPanel()
    act(() => reportStatus(status()))
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Close Sheet" })) })
    expect(confirmLeave).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it("asks first when something is unsaved, and keeps the Sheet on a no", async () => {
    confirmLeave.mockResolvedValue(false)
    const { onClose, dirtyRef } = renderPanel()
    act(() => reportStatus(status({ dirty: true })))
    expect(dirtyRef.current).toBe(true)
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Close Sheet" })) })
    expect(confirmLeave).toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
    confirmLeave.mockResolvedValue(true)
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Close Sheet" })) })
    expect(onClose).toHaveBeenCalled()
  })

  it("says it is opening for five seconds, then that it calculates the workbook, and stops once it is ready", () => {
    renderPanel()
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent("Opening the Sheet…")
    act(() => { vi.advanceTimersByTime(4900) })
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent("Opening the Sheet…")
    act(() => { vi.advanceTimersByTime(200) })
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent("Calculating the workbook… a large one can take a minute.")
    act(() => reportStatus(status()))
    expect(screen.getByTestId("sheet-panel-state").textContent).not.toMatch(/Opening|Calculating/)
  })

  it("shows it is working while opening, saving and while the agent changes the Sheet, in a bar that never moves the layout", () => {
    renderPanel()
    const bar = screen.getByTestId("sheet-panel-progress")
    expect(bar).toHaveAttribute("data-active", "true")
    act(() => reportStatus(status({ phase: "saved", savedVersion: "3" })))
    expect(screen.getByTestId("sheet-panel-progress")).toBe(bar)
    expect(bar).toHaveAttribute("data-active", "false")
    act(() => reportStatus(status({ phase: "saving" })))
    expect(bar).toHaveAttribute("data-active", "true")
    act(() => reportStatus(status({ holder: { kind: "agent", name: "Analyst" }, holderIsMe: false })))
    expect(bar).toHaveAttribute("data-active", "true")
  })

  it("counts a save's seconds, says after ten that a large workbook can take half a minute, and never covers the editor", () => {
    renderPanel()
    act(() => reportStatus(status({ phase: "saving" })))
    const state = () => screen.getByTestId("sheet-panel-state")
    expect(state()).toHaveTextContent(/^Saving…$/)
    act(() => { vi.advanceTimersByTime(4000) })
    expect(state()).toHaveTextContent("Saving… 4 s")
    expect(state().textContent).not.toMatch(/half a minute/)
    act(() => { vi.advanceTimersByTime(6000) })
    expect(state()).toHaveTextContent("Saving… 10 s · A large workbook can take up to half a minute to save.")
    expect(screen.getByTestId("editor")).toBeVisible()
    act(() => reportStatus(status({ phase: "saved", savedVersion: "4" })))
    expect(state()).toHaveTextContent("Saved · version 4")
    act(() => reportStatus(status({ phase: "saving" })))
    expect(state()).toHaveTextContent(/^Saving…$/)
  })

  it("says the agent is editing while the agent holds the lock", () => {
    renderPanel()
    act(() => reportStatus(status({ holder: { kind: "agent", name: "Analyst" }, holderIsMe: false })))
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent("The agent is changing the Sheet…")
  })

  it("stays mounted but hidden while another pane is shown", () => {
    render(<SheetSidePanel workspaceId="ws1" pageId="pg_1" hidden expanded={false} onExpandedChange={() => {}} onClose={() => {}} />)
    expect(screen.getByTestId("editor")).toBeTruthy()
    expect(screen.getByTestId("sheet-side-panel").hasAttribute("hidden")).toBe(true)
  })

  it.each([
    [{ phase: "saved", savedVersion: "3" }, "Saved · version 3"], [{ phase: "saving" }, "Saving…"], [{ dirty: true }, "Not saved yet"],
    [{ phase: "uncertain" }, "Save not confirmed"], [{ editing: false }, "Viewing"], [{ editing: false, version: "7" }, "Viewing · version 7"],
  ])("says whether the work is saved (%#), in one line that keeps its place", (over, text) => {
    renderPanel()
    act(() => reportStatus(status(over)))
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent(text)
  })

  it("keeps the state line before the editor answers, so nothing moves when it does", () => {
    renderPanel()
    expect(screen.getByTestId("sheet-panel-state")).toBeInTheDocument()
  })

  it("gives the editor one bar in its header for who edits and the menu, with the way to the Sheet's history", () => {
    renderPanel()
    // One bar above the grid; history and downloads are in the editor's menu there.
    const bar = screen.getByTestId("sheet-panel-bar")
    expect(editorProps.toolbar).toBe(bar)
    expect(bar.closest("header")).not.toBeNull()
    expect(editorProps.historyHref).toContain("pg_1")
    expect(editorProps.placeLease, "no lock row above the grid").toBeUndefined()
  })

  it("names a revision only once it was saved: never while saving or before the answer", () => {
    renderPanel()
    act(() => reportStatus(status({ phase: "saving", savedVersion: "3", version: "3" })))
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent("Saving…")
    expect(screen.getByTestId("sheet-panel-state").textContent).not.toMatch(/version/)
  })

  it("says it waits for the agent after a message let it change the Sheet, until the agent takes it", () => {
    render(<SheetSidePanel workspaceId="ws1" pageId="pg_1" hidden={false} expanded={false} onExpandedChange={() => {}} onClose={() => {}} agentPending />)
    act(() => reportStatus(status({ editing: false, version: "3" })))
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent("Waiting for the agent…")
    act(() => reportStatus(status({ editing: false, holder: { kind: "agent", name: "Analyst" }, holderIsMe: false })))
    expect(screen.getByTestId("sheet-panel-state")).toHaveTextContent("The agent is changing the Sheet…")
  })

  it("moves the keyboard to the Sheet when it opens from a button, never away from where the person types", () => {
    const opener = document.createElement("button"); document.body.append(opener); opener.focus()
    const first = render(<SheetSidePanel workspaceId="ws1" pageId="pg_1" hidden={false} expanded={false} onExpandedChange={() => {}} onClose={() => {}} opener={opener} />)
    expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2 }))
    first.unmount()
    const field = document.createElement("textarea"); document.body.append(field); field.focus()
    render(<SheetSidePanel workspaceId="ws1" pageId="pg_1" hidden={false} expanded={false} onExpandedChange={() => {}} onClose={() => {}} opener={opener} />)
    expect(document.activeElement).toBe(field)
    opener.remove(); field.remove()
  })

  it("takes no keyboard without an opener, as when the agent's change opened it", () => {
    const field = document.createElement("textarea"); document.body.append(field); field.focus()
    render(<SheetSidePanel workspaceId="ws1" pageId="pg_1" hidden={false} expanded={false} onExpandedChange={() => {}} onClose={() => {}} opener={null} />)
    expect(document.activeElement).toBe(field)
    field.remove()
  })
})
