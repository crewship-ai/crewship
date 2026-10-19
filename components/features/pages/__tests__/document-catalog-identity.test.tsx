import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { toPageView } from "@/hooks/use-pages"
import { DocumentCatalog } from "../document-catalog"

afterEach(cleanup)
it("retains only an actual validated wire identity independently of the Page key fallback", () => {
 expect(toPageView({ id: "immutable-A", slug: "reused-address" }).immutablePageId).toBe("immutable-A")
 for (const id of [undefined, "", " ", "bad/id", " spaced ", 123]) {
  expect(toPageView({ id: id as never, slug: "synthetic-key" }).immutablePageId).toBeUndefined()
 }
 expect(toPageView({ slug: "synthetic-key" }).id).toBe("synthetic-key")
})
it("opens the displayed immutable Document even when its address can be reused", () => {
 const select = vi.fn()
 const page = toPageView({ id: "immutable-A", slug: "reused-address", name: "Saved original", has_document: true })
 render(<DocumentCatalog pages={[page]} onSelect={select} immutableTargets />)
 fireEvent.click(screen.getByRole("button", { name: "Saved original Doc" }))
 expect(select).toHaveBeenCalledWith("@immutable-A")
 expect(select).not.toHaveBeenCalledWith("reused-address")
})
it("refuses a missing wire identity instead of promoting its synthetic key into an address", () => {
 const select = vi.fn()
 const page = toPageView({ slug: "synthetic-key", name: "Lost identity", has_document: true })
 render(<DocumentCatalog pages={[page]} onSelect={select} immutableTargets />)
 const button = screen.getByRole("button", { name: "Lost identity Doc" })
 expect(button).toBeDisabled()
 fireEvent.click(button)
 expect(select).not.toHaveBeenCalled()
 expect(screen.getByText("A Document cannot be opened because its original identity is unavailable.")).toBeTruthy()
})
