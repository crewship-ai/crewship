import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

const h = vi.hoisted(() => ({ pages: {} as Record<string, unknown>, apiFetch: vi.fn() }))
vi.mock("@/hooks/use-pages", async () => ({ ...await vi.importActual("@/hooks/use-pages"), usePage: (_ws: string, ref: string) => { const page = h.pages[ref.slice(1)]; return page === undefined ? { page: null, loading: false, notFound: true } : { page, loading: false, notFound: false } } }))
vi.mock("@/lib/api-fetch", async () => ({ ...await vi.importActual("@/lib/api-fetch"), apiFetch: h.apiFetch }))

import { UnfinishedImportsNotice } from "../unfinished-imports-notice"
import { noteUnfinishedImport, unfinishedImports } from "@/components/features/chat/artifact-context/sheet/unfinished-imports"
import { installMemoryStorage } from "@/components/features/chat/artifact-context/sheet/__tests__/memory-storage"

const scope = { userId: "u1", workspaceId: "ws1" }
const show = () => render(<QueryClientProvider client={new QueryClient()}><UnfinishedImportsNotice workspaceId="ws1" userId="u1" /></QueryClientProvider>)

describe("UnfinishedImportsNotice", () => {
  installMemoryStorage()
  beforeEach(() => {
    cleanup(); h.pages = {}; h.apiFetch.mockReset()
  })

  it("labels an empty Sheet whose import was not finished", () => {
    h.pages.pg_9 = { id: "pg_9", name: "Budget" }
    noteUnfinishedImport(scope, "pg_9")
    show()
    expect(screen.getByRole("group", { name: "Budget" })).toHaveTextContent("Import not finished")
  })

  it("deletes it only after the person confirms", async () => {
    h.pages.pg_9 = { id: "pg_9", name: "Budget" }
    noteUnfinishedImport(scope, "pg_9")
    h.apiFetch.mockResolvedValue(new Response(null, { status: 204 }))
    show()
    fireEvent.click(screen.getByRole("button", { name: "Delete Budget" }))
    expect(h.apiFetch).not.toHaveBeenCalled()
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Delete the empty Sheet" })) })
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/pages/%40pg_9?workspace_id=ws1", { method: "DELETE" })
    await waitFor(() => expect(unfinishedImports(scope)).toEqual([]))
  })

  it("keeps the label and says so when the delete was refused", async () => {
    h.pages.pg_9 = { id: "pg_9", name: "Budget" }
    noteUnfinishedImport(scope, "pg_9")
    h.apiFetch.mockResolvedValue(new Response(null, { status: 403 }))
    show()
    fireEvent.click(screen.getByRole("button", { name: "Delete Budget" }))
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Delete the empty Sheet" })) })
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be deleted")
    expect(unfinishedImports(scope)).toEqual(["pg_9"])
  })

  it("forgets a note the server no longer confirms: gone, or no longer empty", () => {
    h.pages.pg_2 = { id: "pg_2", name: "Filled", hasWorkbook: true }
    noteUnfinishedImport(scope, "pg_1"); noteUnfinishedImport(scope, "pg_2")
    show()
    expect(screen.queryByText("Import not finished")).toBeNull()
    expect(unfinishedImports(scope)).toEqual([])
  })

  it("puts the keyboard on Keep when it asks, and back on Delete when the person keeps it", () => {
    h.pages.pg_9 = { id: "pg_9", name: "Budget" }
    noteUnfinishedImport(scope, "pg_9")
    show()
    fireEvent.click(screen.getByRole("button", { name: "Delete Budget" }))
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Keep" }))
    fireEvent.click(screen.getByRole("button", { name: "Keep" }))
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Delete Budget" }))
  })
})
