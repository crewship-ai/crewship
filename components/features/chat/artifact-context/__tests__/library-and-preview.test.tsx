import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ConversationLibrary } from "../library/conversation-library"
import { buildLibrary, type LibraryItem } from "../library/library-model"
import { fileActions, previewKind } from "../preview/file-preview-kind"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
const items: LibraryItem[] = [
  { status: "readable", linkId: "1", kind: "files", name: "data.csv", revisionKey: "f1@1" },
  { status: "readable", linkId: "2", kind: "files", name: "data.csv", revisionKey: "f1@1" },
  { status: "readable", linkId: "3", kind: "files", name: "data.csv", revisionKey: "f1@2" },
  { status: "readable", linkId: "4", kind: "artifacts", name: "Plan", revisionKey: "p@1" },
  { status: "restricted", linkId: "5" },
]
it("dedupes the same revision, keeps other revisions, counts restricted only under All", () => {
  const view = buildLibrary(items, "all")
  expect(view.counts).toEqual({ all: 4, files: 2, artifacts: 1, tools: 0, work: 0 })
  expect(buildLibrary(items, "files").entries.map(entry => entry.linkId)).toEqual(["1", "3"])
  expect(buildLibrary(items, "tools").entries).toEqual([])
})
it("renders a restricted link without a name and filters by kind", () => {
  const onOpen = vi.fn(), onFilterChange = vi.fn()
  render(<ConversationLibrary items={items} filter="all" onFilterChange={onFilterChange} onOpen={onOpen} />)
  expect(screen.getByText("Restricted item")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Restricted item" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Artifacts (1)" }))
  expect(onFilterChange).toHaveBeenCalledWith("artifacts")
  fireEvent.click(screen.getByRole("button", { name: "Plan" }))
  expect(onOpen).toHaveBeenCalledWith("4")
})
it("shows an empty state", () => {
  render(<ConversationLibrary items={[]} filter="all" onFilterChange={() => {}} onOpen={() => {}} />)
  expect(screen.getByRole("status").textContent).toMatch(/Nothing linked/)
})
it.each([
  ["text/csv", "a.csv", "table"], ["text/tab-separated-values; charset=utf-8", "a.tsv", "table"],
  ["text/plain", "notes.txt", "text"], ["text/plain", "main.go", "code"], ["application/json", "a.json", "code"],
  ["image/png", "a.png", "image"], ["image/svg+xml", "a.svg", "none"], ["application/pdf", "a.pdf", "pdf"],
  ["application/octet-stream", "a.bin", "none"],
  ["text/javascript", "a.js", "code"], ["text/html", "a.html", "code"], ["text/css", "a.css", "code"], ["application/xml", "a.xml", "code"], ["application/x-yaml", "a.yml", "code"],
  ["text/plain", "a.svg", "text"], ["image/png", "a.svg", "image"], ["image/svg+xml", "a.png", "none"],
] as const)("%s %s previews as %s", (mime, name, kind) => { expect(previewKind(mime, name)).toBe(kind) })
it("offers only granted Open/Download", () => {
  expect(fileActions({ open: true }, "pdf")).toEqual({ open: true, download: false })
  expect(fileActions({ open: true, download: true }, "none")).toEqual({ open: false, download: true })
  expect(fileActions({}, "image")).toEqual({ open: false, download: false })
})
it("a restricted link is hidden under a kind filter and an empty kind says so", () => {
  render(<ConversationLibrary items={items} filter="tools" onFilterChange={() => {}} onOpen={() => {}} />)
  expect(screen.queryByText("Restricted item")).toBeNull()
  expect(screen.getByRole("status").textContent).toMatch(/No linked items of this kind/)
})
it("shows loading and a temporary failure that is not a revoke, keeping loaded items", () => {
  const onRetry = vi.fn()
  const view = render(<ConversationLibrary items={[]} filter="all" status="loading" onFilterChange={() => {}} onOpen={() => {}} />)
  expect(screen.getByRole("status").textContent).toMatch(/Loading/)
  view.unmount()
  render(<ConversationLibrary items={items} filter="all" status="unavailable" onRetry={onRetry} onFilterChange={() => {}} onOpen={() => {}} />)
  expect(screen.getByRole("alert").textContent).toMatch(/not a removal of access/)
  expect(screen.getByRole("button", { name: "Plan" })).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Try again" }))
  expect(onRetry).toHaveBeenCalled()
})
it("renders nothing with the flag off", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "")
  expect(render(<ConversationLibrary items={items} filter="all" onFilterChange={() => {}} onOpen={() => {}} />).container.innerHTML).toBe("")
})
