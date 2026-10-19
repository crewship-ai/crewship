import React from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { toPageView } from "@/hooks/use-pages"
import { DocumentCatalog } from "../document-catalog"

const row = (id: string, extra: Record<string, unknown> = {}) =>
  toPageView({ id, slug: id, name: `Doc ${id}`, owner: "user/ada", has_document: true, panels: [], ...extra })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("filters by scope and says when nothing matches", () => {
  const pages = [row("a", { private_audience: true }), row("b", { private_audience: false, folder: { slug: "ops", name: "Ops" } })]
  render(<DocumentCatalog pages={pages} onSelect={vi.fn()} filterable />)
  fireEvent.click(screen.getByRole("button", { name: /^In a folder/ }))
  expect(screen.queryByText("Doc a")).toBeNull()
  expect(screen.getByText("Doc b")).toBeTruthy()
})

it("never counts or previews an item it was not given", () => {
  // The server omits another human's private item; the catalog has no other source.
  render(<DocumentCatalog pages={[row("mine", { private_audience: true })]} onSelect={vi.fn()} filterable />)
  expect(screen.getByRole("button", { name: /^Private 1/ })).toBeTruthy()
  expect(screen.getByRole("button", { name: /^In a folder 0/ })).toBeTruthy()
  expect(screen.queryByText("Doc secret")).toBeNull()
  expect(document.body.textContent).not.toMatch(/secret/)
})

it("hides Duplicate until the copy flag is on", () => {
  const onDuplicate = vi.fn()
  const pages = [row("a", { private_audience: true })]
  const { rerender } = render(<DocumentCatalog pages={pages} onSelect={vi.fn()} onDuplicate={onDuplicate} />)
  expect(screen.queryByRole("button", { name: "Duplicate Doc a" })).toBeNull()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_COPY_MOVE", "true")
  rerender(<DocumentCatalog pages={pages} onSelect={vi.fn()} onDuplicate={onDuplicate} />)
  fireEvent.click(screen.getByRole("button", { name: "Duplicate Doc a" }))
  expect(onDuplicate).toHaveBeenCalledWith(pages[0])
})
