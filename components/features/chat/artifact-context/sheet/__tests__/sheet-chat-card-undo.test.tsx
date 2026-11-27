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


// The Undo card under the agent's reply: the answers the main suite does not reach.
const changed = { state: "done" as const, pageId: "pg_1", operationId: "op-9", undoable: true, baseVersion: "6", saved: { version: "7" }, summary: { cellsChanged: 1, sheets: ["L", "Q 1"], ranges: [], truncated: false } }
const answer = (body: unknown, status = 200) => apiFetch.mockResolvedValueOnce(new Response(body === undefined ? "not json" : JSON.stringify(body), { status }))
const undo = async () => { await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Undo" })) }) }

describe("SheetChatCard Undo", () => {
  beforeEach(() => { cleanup(); usePage.mockReset(); apiFetch.mockReset(); published.mockReset(); usePage.mockReturnValue({ page: sheetPage, loading: false, notFound: false }) })

  it("names one cell and several sheets in words", () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    expect(screen.getByRole("article")).toHaveTextContent("Changed Quarterly plan · version 7 · 1 cell on sheets L, Q 1")
  })
  it("offers no Undo for a change without an operation id", () => {
    render(wrap(panel(), <SheetChatCard activity={{ ...changed, operationId: undefined }} turnStreaming={false} />))
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull()
    expect(screen.getByRole("button", { name: "Show in Sheet" })).toBeInTheDocument()
  })
  it("takes the new version as a decimal string as well as a number", async () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    answer({ version: "12", skipped: [] }); await undo()
    expect(screen.getByRole("status")).toHaveTextContent("Undone · version 12")
    expect(published).toHaveBeenCalledWith({ pageId: "pg_1", version: "12" })
    expect(screen.queryByRole("button", { name: "Undo" }), "Undo is gone once done").toBeNull()
  })
  it("treats a success without a usable version as not done, and publishes nothing", async () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    published.mockClear() // the agent's own version 7, published on mount
    for (const body of [{}, { version: 0 }, { version: "08" }, { version: 1.5 }, undefined]) {
      answer(body); await undo()
      expect(screen.getByRole("status"), JSON.stringify(body)).toHaveTextContent("Undo did not go through. Try again.")
    }
    expect(published).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "Undo" }), "Undo can be tried again").toBeEnabled()
  })
  it("says the generic line when the request itself fails", async () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    apiFetch.mockRejectedValueOnce(new TypeError("Failed to fetch")); await undo()
    expect(screen.getByRole("status")).toHaveTextContent("Undo did not go through. Try again.")
  })
  it("says busy and forbidden in words, and never a code", async () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    answer({ code: "busy" }, 409); await undo()
    expect(screen.getByRole("status")).toHaveTextContent("Someone is editing this Sheet now. Try Undo again when they are done.")
    answer({ code: "whatever" }, 403); await undo()
    expect(screen.getByRole("status")).toHaveTextContent("You may not change this Sheet.")
    // Undo C: 404 not_found means the operation is unknown, so trying again cannot help.
    answer({ code: "not_found" }, 404); await undo()
    expect(screen.getByRole("status")).toHaveTextContent("This change can no longer be undone here.")
    expect(screen.getByRole("status").textContent).not.toMatch(/not_found|busy|whatever/)
  })
  it("sends one Undo while one is running", async () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    let finish!: (r: Response) => void
    apiFetch.mockReturnValueOnce(new Promise<Response>(r => { finish = r }))
    await undo()
    expect(screen.getByRole("button", { name: "Undo" })).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Undo" }))
    expect(apiFetch).toHaveBeenCalledTimes(1)
    await act(async () => { finish(new Response(JSON.stringify({ version: 8 }), { status: 201 })) })
    expect(screen.getByRole("status")).toHaveTextContent("Undone · version 8")
  })
  it("names at most 20 left cells, quotes sheet names, and drops entries that are not a cell", async () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    const skipped = [{ sheet: "Bob's", cell: "A1" }, { sheet: "L", cell: "A1:B2" }, null, { sheet: 1, cell: "A2" }, ...Array.from({ length: 21 }, (_, i) => ({ sheet: "L", cell: `B${i + 1}` }))]
    answer({ version: 8, skipped }); await undo()
    const status = screen.getByRole("status")
    expect(status).toHaveTextContent("Undone · version 8. 22 cells were left as they are because someone changed them since: 'Bob''s'!A1, 'L'!B1")
    expect(status).toHaveTextContent("'L'!B19, …")
    expect(status).not.toHaveTextContent("A1:B2")
    expect(within(status).getByRole("link", { name: "Open history" })).toHaveAttribute("href", expect.stringContaining("pg_1"))
  })
  it("says one left cell in the singular", async () => {
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    answer({ version: 8, skipped: [{ sheet: "L", cell: "C3" }] }); await undo()
    expect(screen.getByRole("status")).toHaveTextContent("1 cell was left as it is because someone changed it since: 'L'!C3.")
  })
  it("links Show in Sheet to the page where nothing opens beside the chat", () => {
    render(wrap(null, <SheetChatCard activity={changed} turnStreaming={false} />))
    expect(screen.getByRole("link", { name: "Show in Sheet" })).toHaveAttribute("href", expect.stringContaining("pg_1"))
  })
  it("shows a placeholder while the agent is still changing the Sheet, and nothing for a turn read back", () => {
    render(wrap(panel(), <SheetChatCard activity={{ state: "running", pageId: "pg_1" }} turnStreaming />))
    expect(screen.getByLabelText("Changing the Sheet")).toHaveAttribute("aria-busy", "true")
    cleanup()
    const { container } = render(wrap(panel(), <SheetChatCard activity={{ state: "running", pageId: "pg_1" }} turnStreaming={false} />))
    expect(container.textContent).toBe("")
  })
  it("shows a placeholder while the Sheet loads", () => {
    usePage.mockReturnValue({ page: null, loading: true, notFound: false })
    render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    expect(screen.getByLabelText("Loading Sheet")).toBeInTheDocument()
  })
  it("publishes the agent's saved version to the open editor once per version, only when done", () => {
    const { rerender } = render(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    rerender(wrap(panel(), <SheetChatCard activity={changed} turnStreaming={false} />))
    expect(published).toHaveBeenCalledTimes(1)
    expect(published).toHaveBeenCalledWith({ pageId: "pg_1", version: "7" })
  })
})
