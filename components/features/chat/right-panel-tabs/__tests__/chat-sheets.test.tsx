// The chat's Artifacts panel lists the Sheets of this chat and opens them.
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { SheetPanelProvider } from "../../artifact-context/sheet/sheet-panel-context"
import { ChatSheets } from "../chat-sheets"

const h = vi.hoisted(() => ({ ids: [] as string[], pages: {} as Record<string, { id: string; name: string; artifactKind?: string; nativeCraftApplication?: string } | null>, args: [] as unknown[] }))
vi.mock("../../artifact-context/sheet/use-conversation-sheets", () => ({ useConversationSheets: (...args: unknown[]) => { h.args = args; return h.ids } }))
vi.mock("../../artifact-context/sheet/conversation-sheets", () => ({ conversationSheets: () => ["pg_local"] }))
vi.mock("@/hooks/use-pages", () => ({ usePage: (_w: string, ref: string) => { const page = h.pages[ref.slice(1)]; return { page: page ?? undefined, loading: false, notFound: page === null } } }))

const sheet = (id: string, name: string) => ({ id, name, artifactKind: "sheet", nativeCraftApplication: "gridcraft" })
afterEach(() => { cleanup(); h.ids = []; h.pages = {} })

describe("Sheets in this chat, in the Artifacts panel", () => {
  it("lists each Sheet of the chat by name and opens it beside the chat", () => {
    h.ids = ["pg_1", "pg_2"]; h.pages = { pg_1: sheet("pg_1", "Loan_plan"), pg_2: sheet("pg_2", "Budget") }
    const open = vi.fn()
    render(<SheetPanelProvider value={{ open, noteChanged: vi.fn(), activePageId: "pg_2" }}><ChatSheets workspaceId="ws" chatId="chat_1" viewerId="u1" /></SheetPanelProvider>)
    expect(h.args).toEqual(["ws", "chat_1", ["pg_local"]])
    const list = screen.getByRole("list", { name: "Sheets in this chat" })
    expect(list.querySelectorAll("li")).toHaveLength(2)
    fireEvent.click(screen.getByRole("button", { name: "Open Loan_plan beside the chat" }))
    expect(open).toHaveBeenCalledWith("pg_1")
    expect(screen.getByRole("button", { name: "Open Budget beside the chat" })).toHaveAttribute("aria-current", "true")
    expect(screen.getByRole("link", { name: "Loan_plan in Artifacts" })).toHaveAttribute("href", "/artifacts/pg_1?workspace_id=ws")
  })
  it("never names, counts or hints at a Sheet the reader cannot see", () => {
    h.ids = ["pg_1", "pg_hidden"]; h.pages = { pg_1: sheet("pg_1", "Mine"), pg_hidden: null }
    render(<ChatSheets workspaceId="ws" chatId="chat_1" viewerId="u1" />)
    expect(screen.getAllByRole("listitem")).toHaveLength(1)
    expect(screen.queryByText(/Not available|hidden|2/)).toBeNull()
  })
  it("without a panel beside the chat (a phone), opens the Sheet in Artifacts", () => {
    h.ids = ["pg_1"]; h.pages = { pg_1: sheet("pg_1", "Mine") }
    render(<ChatSheets workspaceId="ws" chatId="chat_1" viewerId="u1" />)
    expect(screen.queryByRole("button", { name: /beside the chat/ })).toBeNull()
    expect(screen.getByRole("link", { name: "Mine in Artifacts" })).toHaveAttribute("href", "/artifacts/pg_1?workspace_id=ws")
  })
  it("shows nothing while the chat has no Sheet", () => {
    const { container } = render(<ChatSheets workspaceId="ws" chatId="chat_1" viewerId="u1" />)
    expect(container.textContent).toBe("")
  })
})
