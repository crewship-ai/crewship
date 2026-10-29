import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { PageDetailClient } from "../[slug]/page-client"

const test = vi.hoisted(() => ({
  state: { workspaceId: "previous", workspaces: [{ id: "previous" }, { id: "owner" }], loading: false, error: false, setWorkspaceId: vi.fn() },
  document: vi.fn(),
  metadata: vi.fn(),
}))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(window.location.search) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => test.state }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => "document" }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "standard" }))
vi.mock("@/hooks/use-pages", () => ({ usePage: (...args: unknown[]) => { test.metadata(...args); return { page: {}, loading: false, error: null, accessDenied: false } } }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/restricted/restricted-pages", () => ({ RestrictedPages: () => null }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: (props: unknown) => { test.document(props); return <p>Editor</p> } }))

beforeEach(() => {
  test.state = { workspaceId: "previous", workspaces: [{ id: "previous" }, { id: "owner" }], loading: false, error: false, setWorkspaceId: vi.fn() }
  test.document.mockClear()
  test.metadata.mockClear()
  window.history.replaceState({}, "", "/pages/document?workspace_id=owner")
})
afterEach(cleanup)

it("opens a document link in its accessible workspace without requesting the previous workspace's document", () => {
  const view = render(<PageDetailClient />)
  expect(test.state.setWorkspaceId).toHaveBeenCalledWith("owner")
  expect(test.document).not.toHaveBeenCalled()
  expect(test.metadata).not.toHaveBeenCalled()
  test.state = { ...test.state, workspaceId: "owner" }
  view.rerender(<PageDetailClient />)
  expect(test.document).toHaveBeenCalledWith({ workspaceId: "owner", slug: "document" })
})

it("does not fetch content while the existing unsaved-work guard holds the workspace selection", () => {
  const view = render(<PageDetailClient />)
  view.rerender(<PageDetailClient />)
  expect(test.state.setWorkspaceId).toHaveBeenCalledTimes(1)
  expect(test.document).not.toHaveBeenCalled()
  expect(test.metadata).not.toHaveBeenCalled()
})

it.each(["workspace_id=unreachable", "workspace_id=owner&workspace_id=previous", "workspace_id="])("refuses an unavailable or ambiguous workspace: %s", query => {
  window.history.replaceState({}, "", "/pages/document?" + query)
  render(<PageDetailClient />)
  expect(screen.getByRole("alert").textContent).toBe("Document unavailable.")
  expect(test.state.setWorkspaceId).not.toHaveBeenCalled()
  expect(test.document).not.toHaveBeenCalled()
  expect(test.metadata).not.toHaveBeenCalled()
})

it("waits for the authenticated directory before selecting or requesting a linked document", () => {
  test.state = { ...test.state, loading: true, workspaces: [] }
  render(<PageDetailClient />)
  expect(test.state.setWorkspaceId).not.toHaveBeenCalled()
  expect(test.document).not.toHaveBeenCalled()
  expect(test.metadata).not.toHaveBeenCalled()
})

it("preserves the selected workspace for an ordinary link", () => {
  window.history.replaceState({}, "", "/pages/document")
  render(<PageDetailClient />)
  expect(test.state.setWorkspaceId).not.toHaveBeenCalled()
  expect(test.document).toHaveBeenCalledWith({ workspaceId: "previous", slug: "document" })
})
