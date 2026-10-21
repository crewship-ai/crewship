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

it("hides filter groups that cannot vary on the list", () => {
  render(<DocumentCatalog pages={[row("a", { private_audience: true })]} onSelect={vi.fn()} filterable />)
  expect(screen.queryByRole("group", { name: "Scope" })).toBeNull()
  expect(screen.queryByRole("group", { name: "Type" })).toBeNull()
  expect(screen.queryByRole("group", { name: "Owner" })).toBeNull()
})

it("offers selection and move only when the flag is on", () => {
  const pages = [row("a", { private_audience: true, id: "id-a" })]
  const { rerender } = render(<DocumentCatalog pages={pages} onSelect={vi.fn()} onShare={vi.fn()} />)
  expect(screen.queryByRole("button", { name: /Move to a working folder/ })).toBeNull()
  expect(screen.queryByRole("checkbox")).toBeNull()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_COPY_MOVE", "true")
  rerender(<DocumentCatalog pages={pages} onSelect={vi.fn()} onShare={vi.fn()} />)
  expect(screen.getByRole("checkbox", { name: "Select Doc a" })).toBeTruthy()
})

it("hides Duplicate until the copy flag is on", () => {
  const onDuplicate = vi.fn()
  const pages = [row("a", { private_audience: true, id: "id-a" })]
  const { rerender } = render(<DocumentCatalog pages={pages} onSelect={vi.fn()} onDuplicate={onDuplicate} />)
  expect(screen.queryByRole("button", { name: "Duplicate Doc a" })).toBeNull()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_COPY_MOVE", "true")
  rerender(<DocumentCatalog pages={pages} onSelect={vi.fn()} onDuplicate={onDuplicate} />)
  fireEvent.click(screen.getByRole("button", { name: "Duplicate Doc a" }))
  expect(onDuplicate).toHaveBeenCalledWith(pages[0])
})
