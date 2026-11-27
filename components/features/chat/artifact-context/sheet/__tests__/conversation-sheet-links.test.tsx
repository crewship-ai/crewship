import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

const pages = vi.hoisted(() => ({ value: {} as Record<string, { id: string; name: string; artifactKind?: string; nativeCraftApplication?: string } | null> }))
vi.mock("@/hooks/use-pages", async () => ({ ...await vi.importActual("@/hooks/use-pages"), usePage: (_ws: string, ref: string) => { const page = pages.value[ref.slice(1)] ?? null; return { page, loading: false, notFound: page === null } } }))

import { ConversationSheetLinks } from "../conversation-sheet-links"

describe("links to the Sheets of a conversation", () => {
  it("shows only Sheets the server confirms for this reader, and names nothing it does not", () => {
    pages.value = { mine: { id: "mine", name: "My plan", artifactKind: "sheet" }, doc: { id: "doc", name: "Memo", nativeCraftApplication: "wordcraft" }, hidden: null }
    render(<ConversationSheetLinks workspaceId="ws1" pageIds={["mine", "doc", "hidden"]} onOpen={() => {}} />)
    expect(screen.getAllByRole("listitem")).toHaveLength(1)
    expect(screen.getByRole("button", { name: "Open My plan" })).toBeInTheDocument()
    expect(screen.queryByText(/Memo|hidden/)).toBeNull()
  })
})
