import { useState } from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { IssuesToolbarStrip, issuesShowingLabel, formatCount, type IssuesToolbarStripProps } from "../issues-toolbar-strip"
import type { SavedView } from "@/lib/types/mission"
afterEach(cleanup)
const saved: SavedView = { id: "v1", name: "Mine", filters_json: "{}", sort_json: null, view_type: "list", is_default: false, shared: true, created_at: "" }
const defaults: IssuesToolbarStripProps = { issueViewMode: "board", onViewModeChange: vi.fn(), savedViews: [], savedViewsOpen: false, onSavedViewsOpenChange: vi.fn(), activeViewId: null, onActiveViewChange: vi.fn() }
it("exposes current view and forwards both mode controls", () => {
  const change = vi.fn()
  const { rerender } = render(<IssuesToolbarStrip {...defaults} onViewModeChange={change} />)
  expect(screen.getByRole("button", { name: "Board view" })).toHaveAttribute("aria-pressed", "true")
  fireEvent.click(screen.getByRole("button", { name: "List view" }))
  expect(change).toHaveBeenLastCalledWith("list")
  rerender(<IssuesToolbarStrip {...defaults} issueViewMode="list" onViewModeChange={change} />)
  expect(screen.getByRole("button", { name: "List view" })).toHaveAttribute("aria-pressed", "true")
  fireEvent.click(screen.getByRole("button", { name: "Board view" }))
  expect(change).toHaveBeenLastCalledWith("board")
  expect(screen.queryByText("Saved Views")).not.toBeInTheDocument()
})
it.each(["board", "list", "future"])("applies saved %s view then clears it without retaining a stale mode", async mode => {
  const change = vi.fn()
  function Harness() {
    const [open, setOpen] = useState(false)
    const [active, setActive] = useState<string | null>(null)
    return <IssuesToolbarStrip {...defaults} savedViews={[{ ...saved, view_type: mode as SavedView["view_type"] }]} savedViewsOpen={open} onSavedViewsOpenChange={setOpen} activeViewId={active} onActiveViewChange={(id, type) => { change(id, type); setActive(id) }} />
  }
  render(<Harness />)
  fireEvent.pointerDown(screen.getByRole("button", { name: "Saved Views" }), { button: 0, ctrlKey: false })
  const item = await screen.findByRole("menuitem", { name: /Mine/ })
  expect(item).toHaveTextContent("shared")
  fireEvent.click(item)
  expect(change).toHaveBeenLastCalledWith("v1", mode === "future" ? undefined : mode)
  fireEvent.pointerDown(screen.getByRole("button", { name: "Mine" }), { button: 0, ctrlKey: false })
  fireEvent.click(await screen.findByRole("menuitem", { name: "All Issues" }))
  expect(change).toHaveBeenLastCalledWith(null, undefined)
  expect(screen.getByRole("button", { name: "Saved Views" })).toBeInTheDocument()
})
it("falls back when the active view is absent and omits unshared badges", () => {
  render(<IssuesToolbarStrip {...defaults} savedViews={[{ ...saved, shared: false }]} activeViewId="missing" savedViewsOpen />)
  expect(screen.getByRole("menu", { name: "Saved Views" })).toBeInTheDocument()
  expect(screen.getByRole("menuitem", { name: "Mine" })).not.toHaveTextContent("shared")
})
it("presents server totals and prevents another page request while loading", () => {
  const load = vi.fn()
  const { rerender } = render(<IssuesToolbarStrip {...defaults} loaded={100} total={1015} hasMore onLoadMore={load} />)
  expect(screen.getByTestId("issues-showing")).toHaveTextContent("Showing newest 100 of 1 015")
  fireEvent.click(screen.getByRole("button", { name: "Load 100 more" }))
  expect(load).toHaveBeenCalledTimes(1)
  rerender(<IssuesToolbarStrip {...defaults} loaded={100} total={1015} hasMore onLoadMore={load} loadingMore />)
  expect(screen.getByRole("button", { name: "Load 100 more" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Load 100 more" }))
  expect(load).toHaveBeenCalledTimes(1)
})
it.each([{ hasMore: false, onLoadMore: vi.fn() }, { hasMore: true, onLoadMore: undefined }])("keeps a partial-count label without inventing a page action", props => {
  render(<IssuesToolbarStrip {...defaults} loaded={100} total={200} {...props} />)
  expect(screen.getByTestId("issues-showing")).toHaveTextContent("Showing newest 100 of 200")
  expect(screen.queryByRole("button", { name: "Load 100 more" })).not.toBeInTheDocument()
})
it.each([null, 0, 99, 100])("does not misrepresent a complete/unknown total %s as a partial page", total => {
  expect(issuesShowingLabel(100, total)).toBeNull()
  render(<IssuesToolbarStrip {...defaults} loaded={100} total={total} />)
  expect(screen.queryByTestId("issues-showing")).not.toBeInTheDocument()
})
it("formats large totals consistently without locale dependence", () => {
  expect(formatCount(1000000)).toBe("1 000 000")
  expect(formatCount(0)).toBe("0")
})
