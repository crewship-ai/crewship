import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import type { PageFolderView } from "@/hooks/use-page-folders"
import { libraryItems, withLibraryState } from "../library-items"
import { EMPTY_FILTERS, type LibraryView } from "../library-filter"
import { LibraryPanel } from "../library-panel"

const page = (over: Partial<WirePage>): WirePage => ({ id: over.slug ?? "p", slug: "p", name: "P", owner: "user/u1", updated_at: "2026-10-27T10:00:00Z", reach: ["owner"], ...over } as WirePage)
const lab = { slug: "lab", name: "Lab" }
const items = withLibraryState(libraryItems([
  page({ slug: "site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 2, folder: lab }),
  page({ slug: "brief", name: "Partner brief", artifact_kind: "word", folder: lab } as Partial<WirePage>),
  page({ slug: "probe", name: "Probe", artifact_kind: "project", artifact_shared_version: 1 }),
].map(toPageView)), { archived: new Set(), test: new Set(["probe"]) })
const folders = [{ id: "f1", slug: "lab", name: "Lab", pageCount: 2 }] as PageFolderView[]

describe("library panel", () => {
  // A folder counts what All artifacts holds, so choosing it from a view that
  // shows only hidden items must not land on an empty list under that count.
  it.each<LibraryView>(["home", "test", "archive", "series", "empty"])("choosing a folder from %s shows All artifacts in that folder", view => {
    const change = vi.fn()
    render(<LibraryPanel items={items} folders={folders} personal={{}} filters={{ ...EMPTY_FILTERS, view }} onFiltersChange={change} />)
    fireEvent.click(screen.getByText("Lab"))
    expect(change).toHaveBeenCalledWith(expect.objectContaining({ view: "all", folder: "lab" }))
  })

  it("choosing a folder keeps a personal view", () => {
    const change = vi.fn()
    render(<LibraryPanel items={items} folders={folders} personal={{}} filters={{ ...EMPTY_FILTERS, view: "mine" }} onFiltersChange={change} />)
    fireEvent.click(screen.getByText("Lab"))
    expect(change).toHaveBeenCalledWith(expect.objectContaining({ view: "mine", folder: "lab" }))
  })
})
