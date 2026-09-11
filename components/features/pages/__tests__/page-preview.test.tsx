import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import type { PagePreview } from "@/hooks/use-page-preview"

const state = vi.hoisted(() => ({
  query: { data: null as PagePreview | null, isError: false, error: null as Error | null, refetch: vi.fn() },
  build: { isPending: false, error: null, mutate: vi.fn() },
}))
vi.mock("@/hooks/use-page-preview", () => ({ usePagePreview: () => state }))
vi.mock("@/hooks/use-page-application", () => ({ usePageApplication: () => ({ query: { data: { publication: null, can_publish: false } }, check: {}, publish: {} }) }))
import { PagePreviewDialog } from "../page-preview"

beforeEach(() => {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 Chrome/140.0.0.0 Safari/537.36")
  const w = window as unknown as { happyDOM?: { settings: { disableIframePageLoading: boolean } } }
  if (w.happyDOM) w.happyDOM.settings.disableIframePageLoading = true
  state.query.data = { revision: 1, runtime_url: "https://pages.example.net/api/v1/pages/runtime/bootstrap", build: { id: "build1", source_revision: 1, state: "ready" }, artifact: { format: "crewship-page-preview/v1", javascript: "void 0", css: "", toolchain: "test" } }
  state.query.error = null
  state.query.isError = false
})
afterEach(cleanup)
const props = { workspaceId: "ws", slug: "health", page: { slug: "health", panels: [] }, onClose: vi.fn() }
it("removes cached executable content when preview authorization fails", () => {
  const { rerender } = render(<PagePreviewDialog {...props} />)
  expect(screen.getByTitle("Application preview")).toBeTruthy()
  state.query.error = new Error("You need permission to edit this Page")
  state.query.isError = true
  rerender(<PagePreviewDialog {...props} />)
  expect(screen.queryByTitle("Application preview")).toBeNull()
  expect(screen.getByRole("alert").textContent).toMatch(/permission/)
})
it("stops executing content when Page data disappears and permits manual stop", () => {
  const { rerender } = render(<PagePreviewDialog {...props} />)
  rerender(<PagePreviewDialog {...props} page={null} />)
  expect(screen.queryByTitle("Application preview")).toBeNull()
  rerender(<PagePreviewDialog {...props} />)
  fireEvent.click(screen.getByRole("button", { name: "Stop preview" }))
  expect(screen.queryByTitle("Application preview")).toBeNull()
  expect(screen.getByText("Preview stopped.")).toBeTruthy()
})
it("reports invalid runtime configuration inside the dialog", () => {
  state.query.data!.runtime_url = "about:srcdoc"
  render(<PagePreviewDialog {...props} />)
  expect(screen.queryByTitle("Application preview")).toBeNull()
  expect(screen.getByRole("alert").textContent).toMatch(/separate application domain/)
})

it("requires the server development flag and keeps the opaque iframe sandbox", () => {
 state.query.data!.runtime_url = window.location.origin + "/api/v1/pages/runtime/bootstrap"
 const { rerender } = render(<PagePreviewDialog {...props} />)
 expect(screen.queryByTitle("Application preview")).toBeNull()
 state.query.data!.development_same_origin = true
 rerender(<PagePreviewDialog {...props} />)
 expect(screen.getByTitle("Application preview").getAttribute("sandbox")).toBe("allow-scripts")
 state.query.data!.development_same_origin = false
 rerender(<PagePreviewDialog {...props} />)
 expect(screen.queryByTitle("Application preview")).toBeNull()
})

it("does not execute an application in an unsupported browser", () => {
 vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 Firefox/140.0")
 render(<PagePreviewDialog workspaceId="ws" slug="health" page={{ slug:"health", panels:[] }} open onOpenChange={() => {}} />)
 expect(screen.queryByTitle("Application preview")).toBeNull()
 expect(screen.getByRole("alert").textContent).toContain("desktop Chrome or Edge")
})
