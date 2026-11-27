import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, within } from "@testing-library/react"
import type { ReactNode } from "react"

const usePage = vi.fn()
vi.mock("@/hooks/use-pages", () => ({ usePage: (...args: unknown[]) => usePage(...args) }))
vi.mock("../../projects/artifact-viewer-context", () => ({ useArtifactViewer: () => ({ workspaceId: "ws1" }) }))
const apiFetch = vi.fn()
// Undo goes through the open editor of the Sheet (its session and lease); `apiFetch` stands for that editor's answer.
vi.mock("@/lib/mooring/native-undo", () => ({ requestSheetUndo: (...args: unknown[]) => apiFetch(...args) }))
const published = vi.fn()
vi.mock("@/lib/mooring/native-agent-version", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/mooring/native-agent-version")>(), publishAgentVersion: (...args: unknown[]) => published(...args) }))

import { SheetChatCard } from "../sheet-chat-card"
import { SheetPanelProvider, type SheetPanelValue } from "../sheet-panel-context"

const done = { state: "done" as const, pageId: "pg_1", slug: "plan" }
const sheetPage = { id: "pg_1", slug: "plan", name: "Quarterly plan", artifactKind: "sheet" }
const wrap = (value: SheetPanelValue | null, node: ReactNode) => <SheetPanelProvider value={value}>{node}</SheetPanelProvider>
const panel = () => ({ open: vi.fn<SheetPanelValue["open"]>(), noteChanged: vi.fn<SheetPanelValue["noteChanged"]>(), activePageId: null } satisfies SheetPanelValue)

describe("SheetChatCard", () => {
  beforeEach(() => { cleanup(); usePage.mockReset() })

  it("shows the name the server gives, never anything from the tool text", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    render(<SheetChatCard activity={done} turnStreaming={false} />)
    expect(screen.getByText("Quarterly plan")).toBeTruthy()
  })

  it("says only that it is not available without access, and shows no name", () => {
    usePage.mockReturnValue({ page: null, loading: false, notFound: true })
    render(<SheetChatCard activity={done} turnStreaming={false} />)
    expect(screen.getByText("Not available")).toBeTruthy()
    expect(screen.queryByText("plan")).toBeNull()
  })

  it("says why the agent could not change the Sheet, in words, when the server gave a reason", () => {
    usePage.mockReturnValue({ page: null, loading: false, notFound: false })
    render(<SheetChatCard activity={{ state: "failed", reason: "agent_policy" }} turnStreaming={false} />)
    expect(screen.getByTestId("sheet-refusal").textContent).toContain("not allowed to change artifacts")
    cleanup()
    render(<SheetChatCard activity={{ state: "failed", reason: "busy" }} turnStreaming={false} />)
    expect(screen.getByTestId("sheet-refusal").textContent).toContain("busy with other Sheets; nothing was changed")
    cleanup()
    const { container } = render(<SheetChatCard activity={{ state: "failed" }} turnStreaming={false} />)
    expect(container.textContent).toBe("")
  })

  // A second chip beside the file read as if the Sheet were missing.
  it("when the message did not let the agent change a Sheet, says how to allow it, as a plain line and not a second Sheet chip", () => {
    usePage.mockReturnValue({ page: null, loading: false, notFound: false })
    render(<SheetChatCard activity={{ state: "failed", reason: "not_attached", pageId: "pg_1" }} turnStreaming={false} />)
    const line = screen.getByTestId("sheet-refusal")
    expect(line.textContent).toBe("Allow edits in your next message.")
    expect(line.className).not.toMatch(/\bborder\b/)
    expect(screen.queryByText(/No Sheet of yours/)).toBeNull()
  })
  // Under the agent's reply, what it changed, with Undo and a way to see it.
  const changed = { state: "done" as const, pageId: "pg_1", operationId: "op-9", undoable: true, baseVersion: "6", saved: { version: "7" }, summary: { cellsChanged: 570, sheets: ["L"], ranges: ["'L'!L2:L571"], truncated: false } }
  it("names the change in words, with Open and Undo", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    const p = panel()
    render(wrap(p, <SheetChatCard activity={changed} turnStreaming={false} />))
    const card = screen.getByRole("article", { name: "Quarterly plan" })
    // The card says where, as the server counted it.
    expect(card).toHaveTextContent("Updated Quarterly plan · 570 cells")
    fireEvent.click(within(card).getByRole("button", { name: "Open" }))
    expect(p.open).toHaveBeenCalledWith("pg_1")
    expect(within(card).getByRole("button", { name: "Undo" })).toBeInTheDocument()
  })
  // Undo applies the agent's inverse to the head, only in cells nobody changed since.
  // No Undo the server cannot do; it says so in the result once it can.
  it("offers no Undo, and no sentence about it, while the server does not say the change can be undone", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    render(wrap(panel(), <SheetChatCard activity={{ ...changed, undoable: undefined }} turnStreaming={false} />))
    expect(screen.getByRole("article", { name: "Quarterly plan" })).toHaveTextContent("Updated Quarterly plan · 570 cells")
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull()
    expect(screen.getByRole("button", { name: "Open" })).toBeInTheDocument()
  })
  // A change the server cannot undo says why and where the version before it is.
  it("explains a change too large to undo here, with the version before it in version history", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    render(wrap(panel(), <SheetChatCard activity={{ ...changed, undoable: false }} turnStreaming={false} />))
    const card = screen.getByRole("article", { name: "Quarterly plan" })
    expect(within(card).queryByRole("button", { name: "Undo" })).toBeNull()
    expect(card).toHaveTextContent("Undo unavailable · Version history.")
    const link = within(card).getByRole("link", { name: "Version history" })
    expect(link.getAttribute("href")).toMatch(/[?&]version=6(&|$)/)
  })
  it("undoes by reverting the agent's cells as a new version, and shows it in the open Sheet", async () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    apiFetch.mockResolvedValue(new Response(JSON.stringify({ version: 8 }), { status: 201 }))
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Undo" })) })
    expect(apiFetch, "the open editor of that Sheet is asked, with the operation").toHaveBeenCalledWith("pg_1", "op-9")
    expect(screen.getByRole("status")).toHaveTextContent("Undone")
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull()
    expect(published).toHaveBeenCalledWith({ pageId: "pg_1", version: "8" })
  })
  it("says which cells it left because they were changed since, and offers the history", async () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    apiFetch.mockResolvedValue(new Response(JSON.stringify({ version: "9", skipped: [{ sheet: "L", cell: "B9" }, { sheet: "L", cell: "B10" }] }), { status: 201 }))
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Undo" })) })
    expect(screen.getByRole("status")).toHaveTextContent("Undone · 2 later edits kept.")
    expect(screen.getByRole("link", { name: "Version history" })).toHaveAttribute("href", "/artifacts/pg_1?workspace_id=ws1")
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull()
  })
  // Skipped is a count; 200 for a repeat or for nothing left to revert.
  it("names up to three ranges and counts the rest, and shows Open as a quiet button", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    const many = { ...changed, summary: { ...changed.summary, ranges: ["'L'!A1:A2", "'L'!B1", "'Q 1'!C1:C9", "'L'!D1", "'L'!E1"] } }
    render(wrap(panel(), <SheetChatCard activity={many} turnStreaming={false} />))
    expect(screen.getByRole("article")).toHaveTextContent("Version 7 in 'L'!A1:A2, 'L'!B1, 'Q 1'!C1:C9 and 2 more")
    // Dark in the light theme: the same quiet look as Undo.
    expect(screen.getByRole("button", { name: "Open" })).toHaveAttribute("data-variant", "outline")
  })
  it("says how many cells it left when the server gives their number", async () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    for (const [status, body, text] of [
      [201, { version: "9", skipped: 3 }, "Undone · 3 later edits kept."],
      [201, { version: "9", skipped: 0 }, "Undone"],
      [200, { version: "9", skipped: 570 }, "Undone · 570 later edits kept."],
    ] as const) {
      cleanup(); apiFetch.mockReset(); apiFetch.mockResolvedValue(new Response(JSON.stringify(body), { status }))
      render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
      await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Undo" })) })
      expect(screen.getByRole("status")).toHaveTextContent(text)
    }
  })
  // Undo needs the editor's session and lease: without an editor beside the chat, or without editing, nothing is sent.
  it("asks the person to edit the Sheet beside the chat when Undo cannot be sent from here", async () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    for (const answer of ["no_editor", "not_editing"] as const) {
      cleanup(); apiFetch.mockReset(); apiFetch.mockResolvedValue(answer)
      const p = panel()
      render(wrap(p, <SheetChatCard activity={changed} turnStreaming={false} />))
      await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Undo" })) })
      expect(screen.getByRole("status"), answer).toHaveTextContent("Undo unavailable.")
      expect(screen.getByRole("button", { name: "Undo" }), "it can be pressed again").toBeInTheDocument()
      if (answer === "no_editor") expect(p.open, "the Sheet opens beside the chat").toHaveBeenCalledWith("pg_1")
      else expect(p.open).not.toHaveBeenCalled()
    }
  })
  it("names each refusal of the undo endpoint in words", async () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    for (const [status, code, text] of [
      [404, "not_found", "Undo unavailable."],
      [409, "not_undoable", "Undo unavailable."],
      [409, "moved", "Couldn’t undo. Try again."],
      [422, "fidelity_loss", "Couldn’t undo safely."],
    ] as const) {
      cleanup(); apiFetch.mockReset(); apiFetch.mockResolvedValue(new Response(JSON.stringify({ code }), { status }))
      render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
      await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Undo" })) })
      expect(screen.getByRole("status"), code).toHaveTextContent(text)
      expect(screen.queryByText(new RegExp(code)), code).toBeNull()
    }
  })
  it("says in words why an undo could not be done now, and never shows a code", async () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    for (const [status, code, text] of [[409, "busy", "Couldn’t undo. Try again."], [403, "forbidden", "Undo unavailable."], [500, "boom", "Couldn’t undo. Try again."]] as const) {
      cleanup(); apiFetch.mockReset(); apiFetch.mockResolvedValue(new Response(JSON.stringify({ code }), { status }))
      render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
      await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Undo" })) })
      expect(screen.getByRole("status")).toHaveTextContent(text)
      expect(screen.queryByText(new RegExp(code))).toBeNull()
    }
  })
  it("is not a Sheet card for something that is not a Sheet", () => {
    usePage.mockReturnValue({ page: { ...sheetPage, artifactKind: "project" }, loading: false, notFound: false })
    render(<SheetChatCard activity={done} turnStreaming={false} />)
    expect(screen.getByText("Not available")).toBeTruthy()
  })

  it("opens beside the chat when there is a side pane, otherwise links to the page", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    const value = panel()
    const { unmount } = render(wrap(value, <SheetChatCard activity={done} turnStreaming={false} />))
    fireEvent.click(screen.getByRole("button", { name: "Open Quarterly plan" }))
    expect(value.open).toHaveBeenCalledWith("pg_1")
    unmount()
    render(<SheetChatCard activity={done} turnStreaming={false} />)
    expect(screen.getByRole("link", { name: "Open Quarterly plan" }).getAttribute("href")).toContain("/artifacts/pg_1")
  })

  it("asks the pane to open on a live change, never for a turn read back from history", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    const live = panel()
    render(wrap(live, <SheetChatCard activity={done} turnStreaming={true} />))
    expect(live.noteChanged).toHaveBeenCalledWith("pg_1")
    cleanup()
    const history = panel()
    render(wrap(history, <SheetChatCard activity={done} turnStreaming={false} />))
    expect(history.noteChanged).not.toHaveBeenCalled()
  })

  it("on a phone, where nothing opens beside the chat, offers the Sheet for the next message besides the link", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    const choose = vi.fn()
    render(<SheetPanelProvider value={null} workspaceId="ws1" pick={{ choose, chosenPageId: null }}><SheetChatCard activity={done} turnStreaming={false} /></SheetPanelProvider>)
    expect(screen.getByRole("link", { name: "Open Quarterly plan" })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Use Quarterly plan with the next message" }))
    expect(choose).toHaveBeenCalledWith("pg_1")
  })

  it("offers nothing to pick where nothing can be picked, or for a Sheet the reader does not see", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    render(<SheetPanelProvider value={null} workspaceId="ws1"><SheetChatCard activity={done} turnStreaming={false} /></SheetPanelProvider>)
    expect(screen.queryByRole("button", { name: /with the next message/ })).toBeNull()
    cleanup()
    usePage.mockReturnValue({ page: null, loading: false, notFound: true })
    render(<SheetPanelProvider value={null} workspaceId="ws1" pick={{ choose: vi.fn(), chosenPageId: null }}><SheetChatCard activity={done} turnStreaming={false} /></SheetPanelProvider>)
    expect(screen.queryByRole("button", { name: /with the next message/ })).toBeNull()
  })
})

// The person who holds the Sheet was told "Someone is editing this Sheet now."
describe("a change refused because the person edits the Sheet", () => {
  const refused = { state: "failed" as const, pageId: "pg_1", reason: "holder_human" as const }
  it("keeps the reason in Details and offers no handoff", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    render(wrap({ ...panel(), editingPageId: "pg_1" }, <SheetChatCard activity={refused} turnStreaming={false} />))
    expect(screen.getByTestId("sheet-refusal")).toHaveTextContent("Couldn’t update the Sheet.")
    expect(screen.queryByRole("button", { name: "Hand back" })).toBeNull()
  })
  it("says someone else edits it when the person does not", () => {
    usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false })
    render(wrap({ ...panel(), editingPageId: null }, <SheetChatCard activity={refused} turnStreaming={false} />))
    expect(screen.getByTestId("sheet-refusal")).toHaveTextContent("Someone is editing this Sheet now.")
    expect(screen.queryByRole("button", { name: "Hand back" })).toBeNull()
  })
})
