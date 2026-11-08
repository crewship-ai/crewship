import { act, cleanup, render, renderHook, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"

const state = vi.hoisted(() => ({ wire: null as Record<string, unknown> | null, loading: false, error: null as string | null, denied: false, reads: [] as string[], grants: [] as (string | null)[], shell: [] as { slug: string; capabilities: unknown }[] }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePage: (_ws: string, ref: string) => {
    state.reads.push(ref)
    const page = state.wire && !state.denied ? toPageView(state.wire as unknown as WirePage) : null
    return { page, raw: page ? state.wire : null, loading: state.loading, error: state.error, accessDenied: state.denied, notFound: false, live: "live" }
  },
}))
vi.mock("@/hooks/use-page-grants", () => ({ usePageGrants: (_ws: string, slug: string | null) => { state.grants.push(slug); return { refusal: null } } }))
vi.mock("@/components/features/pages/editor/use-page-capabilities", () => ({ usePageCapabilities: (raw: unknown, overrides: unknown) => ({ raw, overrides }) }))
vi.mock("@/components/features/pages/editor/page-editor-shell", () => ({
  PageEditorShell: ({ slug, capabilities, navigation }: { slug: string; capabilities: unknown; navigation: { setMode: (m: "view") => void; openPage: (s: null) => void } }) => {
    state.shell.push({ slug, capabilities })
    return <div data-testid="shell" data-slug={slug}>
      <button type="button" onClick={() => navigation.setMode("view")}>Back to the artifact</button>
      <button type="button" onClick={() => navigation.openPage(null)}>Deleted</button>
    </div>
  },
}))

import { FramePageEditor, panelEditorApplies, useFrameEditorNavigation } from "../frame-page-editor"

const page = (extra: Record<string, unknown> = {}) => ({ id: "page-weekly", slug: "weekly", name: "Weekly", owner: "user/u1", reach: ["owner"], panel_count: 2, ...extra })
beforeEach(() => { Object.assign(state, { wire: page(), loading: false, error: null, denied: false, reads: [], grants: [], shell: [] }) })
afterEach(cleanup)

it.each<[string, Record<string, unknown>, boolean]>([
  ["a panel Page", {}, true],
  ["a document", { has_document: true }, false],
  ["a deck", { has_slides: true }, false],
  ["a workbook", { has_workbook: true }, false],
  ["a Mooring page", { has_mooring: true }, false],
  ["a collaboration page", { has_collaboration: true }, false],
  ["a typed artifact", { artifact_kind: "project" }, false],
])("applies the panel editor to %s exactly as the Pages route does: %s", (_name, extra, applies) => {
  expect(panelEditorApplies(toPageView(page(extra) as unknown as WirePage))).toBe(applies)
  expect(panelEditorApplies(null)).toBe(false)
})

it("reads the Page by its ID and edits it under the slug the server reports for that ID, with its grants", () => {
  render(<FramePageEditor workspaceId="ws1" pageId="page-weekly" onLeave={vi.fn()} />)
  expect(state.reads).toContain("@page-weekly")
  expect(screen.getByTestId("shell").dataset.slug).toBe("weekly")
  expect(state.grants).toContain("weekly")
  expect(state.shell.at(-1)?.capabilities).toMatchObject({ overrides: { mayManageAccess: true } })
})

it.each<[string, () => void, RegExp]>([
  ["refused", () => { state.denied = true }, /can't be edited right now/],
  ["failing", () => { state.error = "boom" }, /can't be edited right now/],
  ["a document with its own editor", () => { state.wire = page({ has_document: true }) }, /edited in its own view/],
])("opens no editor for a page that is %s", (_name, setup, message) => {
  setup()
  render(<FramePageEditor workspaceId="ws1" pageId="page-weekly" onLeave={vi.fn()} />)
  expect(screen.queryByTestId("shell")).toBeNull()
  expect(screen.getByText(message)).toBeTruthy()
})

it("hands back to the frame when the editor closes, and to the library when the page was deleted", () => {
  const onLeave = vi.fn()
  render(<FramePageEditor workspaceId="ws1" pageId="page-weekly" onLeave={onLeave} />)
  act(() => screen.getByRole("button", { name: "Back to the artifact" }).click())
  expect(onLeave).toHaveBeenLastCalledWith("done")
  act(() => screen.getByRole("button", { name: "Deleted" }).click())
  expect(onLeave).toHaveBeenLastCalledWith("deleted")
})

it("asks before leaving with unsaved work, and leaves only on Discard", () => {
  const onLeave = vi.fn()
  const { result } = renderHook(() => useFrameEditorNavigation("weekly", onLeave))
  expect(result.current).toMatchObject({ slug: "weekly", mode: "edit", section: "content", pane: "section", dirty: false, pending: null })
  act(() => result.current.setSection("access"))
  expect(result.current.section).toBe("access")
  act(() => result.current.setDirty(true))
  act(() => result.current.setMode("view"))
  expect(onLeave).not.toHaveBeenCalled()
  expect(result.current.pending).not.toBeNull()
  act(() => result.current.pending!.keep())
  expect(result.current.pending).toBeNull()
  expect(onLeave).not.toHaveBeenCalled()
  act(() => result.current.setMode("view"))
  act(() => result.current.pending!.discard())
  expect(onLeave).toHaveBeenCalledWith("done")
  expect(result.current.dirty).toBe(false)
})
