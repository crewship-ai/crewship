import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import PagesPage from "../page"
const replace = vi.hoisted(() => vi.fn())
vi.mock("next/navigation", () => ({ useRouter: () => ({ replace }), useSearchParams: () => new URLSearchParams(window.location.search) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "human" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "workspace", loading: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "standard" }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/restricted/restricted-pages", () => ({ RestrictedPages: () => <p>Restricted catalog</p> }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <p>Pages overview</p> }))
beforeEach(() => { replace.mockReset(); window.history.replaceState({}, "", "/pages?ref=saved-id&workspace_id=workspace") })
afterEach(cleanup)
it("preserves immutable identity through the native editor URL", async () => {
  render(<PagesPage />)
  await waitFor(() => expect(replace).toHaveBeenCalledWith("/pages/%40saved-id?workspace_id=workspace"))
  expect(screen.queryByText("Pages overview")).toBeNull()
})
it("responds to query-only navigation without reusing the previous identity", async () => {
  const view = render(<PagesPage />)
  await waitFor(() => expect(replace).toHaveBeenCalledWith("/pages/%40saved-id?workspace_id=workspace"))
  replace.mockClear()
  window.history.replaceState({}, "", "/pages?ref=second-id&workspace_id=workspace")
  view.rerender(<PagesPage />)
  await waitFor(() => expect(replace).toHaveBeenCalledWith("/pages/%40second-id?workspace_id=workspace"))
  expect(replace).not.toHaveBeenCalledWith("/pages/%40saved-id?workspace_id=workspace")
})
it.each(["ref=one&ref=two&workspace_id=workspace", "ref=saved-id&workspace_id=other", "ref=&workspace_id=workspace", "ref=%40invalid&workspace_id=workspace", "ref=saved-id&workspace_id=workspace&workspace_id=workspace"])("refuses ambiguous or invalid intent: %s", async query => {
  window.history.replaceState({}, "", "/pages?" + query)
  render(<PagesPage />)
  expect(await screen.findByText("Document unavailable.")).toBeTruthy()
  expect(replace).not.toHaveBeenCalled()
})
it("preserves the ordinary Pages entrypoint", async () => {
  window.history.replaceState({}, "", "/pages")
  render(<PagesPage />)
  expect(await screen.findByText("Pages overview")).toBeTruthy()
  expect(replace).not.toHaveBeenCalled()
})
