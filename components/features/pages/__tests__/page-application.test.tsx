import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import type { PageApplication } from "@/hooks/use-page-application"
const state = vi.hoisted(() => ({ query: { isPending: false, data: null as PageApplication | null, isError: false, error: null as Error | null } }))
vi.mock("@/hooks/use-page-application", () => ({ usePageApplication: () => state }))
vi.mock("../page-preview", () => ({ PagePreviewFrame: ({ artifact }: { artifact: { javascript: string } }) => <div data-testid="application">{artifact.javascript}</div> }))
vi.mock("../use-application-actions", () => ({ useApplicationActions: () => ({ handleRequest: vi.fn(), confirmation: null }) }))
import { PageApplicationView } from "../page-application"
const props = { workspaceId: "ws", slug: "health", page: { slug: "health", panels: [] }, fallback: <p>Panel view</p> }
function release(version: number): PageApplication { return { can_publish: true, publication: { version, build_id: String(version), source_revision: version, artifact_digest: "sha", git_commit: "git", created_at: "2026-09-08" }, artifact: { format: "crewship-page-preview/v1", javascript: `Release ${version}`, css: "", toolchain: "test" }, runtime_url: "https://pages.example.net/api/v1/pages/runtime/bootstrap" } }
beforeEach(() => { vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 Chrome/130.0.0.0 Safari/537.36"); state.query.isPending = false; state.query.data = release(1); state.query.isError = false; state.query.error = null })
afterEach(() => { cleanup(); vi.restoreAllMocks() })
it("keeps the opened application until the reader explicitly loads a new publication", async () => {
  const { rerender } = render(<PageApplicationView {...props} />)
  expect(await screen.findByText("Release 1")).toBeTruthy()
  state.query.data = release(2)
  rerender(<PageApplicationView {...props} />)
  expect(screen.getByText("Release 1")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Load new version 2" }))
  expect(screen.getByText("Release 2")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Stop application / show panels" }))
  expect(screen.queryByTestId("application")).toBeNull()
  expect(screen.getByText("Panel view")).toBeTruthy()
})
it("removes cached executable content when authorization fails or Page data disappears", async () => {
  const { rerender } = render(<PageApplicationView {...props} />)
  expect(await screen.findByTestId("application")).toBeTruthy()
  state.query.isError = true; state.query.error = new Error("Page not found")
  rerender(<PageApplicationView {...props} />)
  expect(screen.queryByTestId("application")).toBeNull()
  state.query.isError = false; state.query.error = null
  rerender(<PageApplicationView {...props} page={null} />)
  expect(screen.queryByTestId("application")).toBeNull()
})

it("does not flash the panel fallback while publication metadata is loading", () => {
  state.query.data = null; state.query.isPending = true
  const { rerender } = render(<PageApplicationView {...props} />)
  expect(screen.getByRole("status").textContent).toContain("Loading application")
  expect(screen.queryByText("Panel view")).toBeNull()
  state.query.data = { publication: null, can_publish: false }; state.query.isPending = false
  rerender(<PageApplicationView {...props} />)
  expect(screen.getByText("Panel view")).toBeTruthy()
})

it("renders panels immediately when the detail says there is no active application", () => {
 state.query.data = null; state.query.isPending = true
 render(<PageApplicationView {...props} page={{ ...props.page, has_application: false, publication_version: 3 }} />)
 expect(screen.getByText("Panel view")).toBeTruthy()
 expect(screen.queryByRole("status")).toBeNull()
})
it("keeps an opened application on storage 503 but removes it on withdrawal", async () => {
 const { rerender } = render(<PageApplicationView {...props} />)
 expect(await screen.findByText("Release 1")).toBeTruthy()
 state.query.isError = true; state.query.error = Object.assign(new Error("busy"), { status: 503 })
 rerender(<PageApplicationView {...props} />)
 expect(screen.getByText("Release 1")).toBeTruthy()
 expect(screen.getByRole("status").textContent).toContain("temporarily busy")
 rerender(<PageApplicationView {...props} page={{ ...props.page, has_application: false }} />)
 expect(screen.queryByTestId("application")).toBeNull()
})

it("does not revive a withdrawn cached artifact when a different version is published", async () => {
 const { rerender } = render(<PageApplicationView {...props} page={{ ...props.page, has_application: true, publication_version: 1 }} />)
 expect(await screen.findByText("Release 1")).toBeTruthy()
 rerender(<PageApplicationView {...props} page={{ ...props.page, has_application: false, publication_version: 1 }} />)
 expect(screen.queryByTestId("application")).toBeNull()
 rerender(<PageApplicationView {...props} page={{ ...props.page, has_application: true, publication_version: 2 }} />)
 expect(screen.queryByText("Release 1")).toBeNull()
 state.query.data = release(2)
 rerender(<PageApplicationView {...props} page={{ ...props.page, has_application: true, publication_version: 2 }} />)
 expect(await screen.findByText("Release 2")).toBeTruthy()
})

it("shows panels on mismatched versions and opens only after metadata agrees", () => {
 const page = { ...props.page, has_application: true, publication_version: 2 }
 const { rerender } = render(<PageApplicationView {...props} page={page} />)
 expect(screen.getByText("Panel view")).toBeTruthy()
 expect(screen.getByRole("status")).toHaveTextContent("not synchronized")
 expect(screen.queryByText("Loading application…")).toBeNull()
 expect(screen.queryByTestId("application")).toBeNull()
 state.query.data = release(2)
 rerender(<PageApplicationView {...props} page={page} />)
 expect(screen.getByText("Release 2")).toBeTruthy()
 expect(screen.queryByText("Panel view")).toBeNull()
})

 it.each([
  ["Safari", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15"],
  ["Firefox", "Mozilla/5.0 Gecko/20100101 Firefox/130.0"],
  ["mobile Chrome", "Mozilla/5.0 (Linux; Android 14) Chrome/130.0.0.0 Mobile Safari/537.36"],
 ])("shows panels immediately in %s, with pending or cached application metadata", (_name, userAgent) => {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue(userAgent)
  state.query.isPending = true; state.query.data = null
  const page = { ...props.page, has_application: true, publication_version: 1 }
  const { rerender } = render(<PageApplicationView {...props} page={page} />)
  expect(screen.getByText("Panel view")).toBeTruthy()
  expect(screen.queryByText("Loading application…")).toBeNull()
  state.query.isPending = false; state.query.data = release(1)
  rerender(<PageApplicationView {...props} page={page} />)
  expect(screen.getByText("Panel view")).toBeTruthy()
  expect(screen.queryByTestId("application")).toBeNull()
  expect(screen.getByRole("status")).toHaveTextContent("desktop Chrome or Edge")
 })
