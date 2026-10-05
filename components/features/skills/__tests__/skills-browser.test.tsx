import type { ReactNode } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { SkillCardData } from "../skill-card"

const state = vi.hoisted(() => ({
  workspaceId: "ws-a" as string | null, loading: false, mobile: false,
  fetch: vi.fn(), width: 380, setWidth: vi.fn(), imported: null as (() => void) | null,
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: state.fetch }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: state.workspaceId, loading: state.loading }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => state.mobile }))
vi.mock("@/hooks/use-user-preference", () => ({ useUserPreference: () => [state.width, state.setWidth] }))
vi.mock("react-virtuoso", () => ({ VirtuosoGrid: ({ data, itemContent }: { data: SkillCardData[]; itemContent: (i: number, s: SkillCardData) => ReactNode }) => <div>{data.map((s, i) => <div key={s.id}>{itemContent(i, s)}</div>)}</div> }))
vi.mock("../skill-card", () => ({ SkillCard: ({ skill, onSelect }: { skill: SkillCardData; onSelect: (s: SkillCardData) => void }) => <button onClick={() => onSelect(skill)}>Card {skill.name}</button> }))
vi.mock("../skills-detail-panel", () => ({ SkillsDetailPanel: ({ skill, onClose, onChanged }: { skill: SkillCardData; onClose: () => void; onChanged: () => void }) => <section aria-label="Skill detail">{skill.name}<button onClick={onClose}>Close detail</button><button onClick={onChanged}>Reload skills</button></section> }))
vi.mock("@/components/skills/import-dialog", () => ({ ImportSkillDialog: ({ onImported }: { onImported: () => void }) => { state.imported = onImported; return <button onClick={onImported}>Import skills</button> } }))
import { SkillsBrowser } from "../skills-browser"

const skill = (id: string, overrides: Partial<SkillCardData> = {}): SkillCardData => ({
  id, name: id, slug: id, display_name: null, description: null, version: null, author: null,
  category: "CODING", source: "BUNDLED", icon: null, downloads: null, featured: false, ...overrides,
})
const rows = [skill("compiler", { description: "Compile Rust applications", maturity: "OFFICIAL", runtime: "SCRIPT" }), skill("writer", { category: "WRITING", source: "GENERATED", vendor: "Publisher", display_name: "Writing Assistant" }), skill("database", { category: "DATA", source: "CUSTOM", maturity: "CURATED", runtime: "MCP" })]
const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => { resolve = r }); return { promise, resolve } }
beforeEach(() => {
  vi.clearAllMocks()
  state.workspaceId = "ws-a"; state.loading = false; state.mobile = false; state.width = 380
  state.fetch.mockImplementation(async () => reply(rows))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it("loads the workspace catalog, opens a selection and resizes and closes details", async () => {
  render(<SkillsBrowser />)
  expect(await screen.findByText("Showing 3 of 3")).toBeVisible()
  expect(state.fetch).toHaveBeenCalledWith("/api/v1/skills?workspace_id=ws-a")
  expect(screen.getByText(/Bundled \(1\)/)).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Card compiler" }))
  expect(screen.getByRole("region", { name: "Skill detail" })).toHaveTextContent("compiler")
  fireEvent.mouseDown(screen.getByRole("separator"), { clientX: 500 })
  fireEvent.mouseMove(window, { clientX: 300 })
  expect(state.setWidth).toHaveBeenLastCalledWith(580)
  fireEvent.mouseMove(window, { clientX: 1500 })
  expect(state.setWidth).toHaveBeenLastCalledWith(280)
  fireEvent.mouseMove(window, { clientX: -1500 })
  expect(state.setWidth).toHaveBeenLastCalledWith(720)
  fireEvent.mouseUp(window)
  fireEvent.click(screen.getByRole("button", { name: "Close detail" }))
  await waitFor(() => expect(screen.queryByRole("region", { name: "Skill detail" })).toBeNull())
})

it("combines facets, removes chips and clears an empty intersection", async () => {
  render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  fireEvent.click(screen.getByText("Coding"))
  expect(screen.getByText("Showing 1 of 3")).toBeVisible()
  fireEvent.click(screen.getByText("Community"))
  expect(screen.getByText("No skills match the current filters.")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Source: Community" }))
  expect(screen.getByRole("button", { name: "Card compiler" })).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Domain: Coding" }))
  fireEvent.click(screen.getByText("Runtime"))
  fireEvent.click(screen.getByText("Script"))
  expect(screen.getByText("Showing 1 of 3")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Runtime: Script" }))
  fireEvent.click(screen.getByText("Maturity"))
  fireEvent.click(screen.getByText("Curated"))
  expect(screen.getByRole("button", { name: "Card database" })).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Maturity: Curated" }))
  fireEvent.click(screen.getByText("Data"))
  fireEvent.click(screen.getByText("Script"))
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
  expect(screen.getByText("Showing 3 of 3")).toBeVisible()
})

it("searches the real Orama index and clears the query", async () => {
  render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  fireEvent.change(screen.getByRole("textbox", { name: "Search skills" }), { target: { value: "Rust" } })
  await waitFor(() => expect(screen.getByText("Showing 1 of 3")).toBeVisible())
  expect(screen.getByRole("button", { name: "Card compiler" })).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Clear search" }))
  await screen.findByText("Showing 3 of 3")
})

it("filters generated skills locally and requests the installed lens from the server", async () => {
  render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  fireEvent.click(screen.getByRole("tab", { name: "Generated" }))
  await screen.findByText("Showing 1 of 3")
  expect(screen.getByRole("button", { name: "Card writer" })).toBeVisible()
  state.fetch.mockImplementation(async () => reply([rows[2]]))
  fireEvent.click(screen.getByRole("tab", { name: "Installed" }))
  await screen.findByText("Showing 1 of 1")
  expect(state.fetch).toHaveBeenLastCalledWith("/api/v1/skills?workspace_id=ws-a&installed=1")
  fireEvent.click(screen.getByRole("button", { name: "Import skills" }))
  await waitFor(() => expect(state.fetch).toHaveBeenCalledTimes(4))
  expect(state.fetch).toHaveBeenLastCalledWith("/api/v1/skills?workspace_id=ws-a&installed=1")
})

it("recovers from failed loads and reloads and rebuilds search after import", async () => {
  state.fetch.mockResolvedValueOnce(reply({}, 503))
  render(<SkillsBrowser />)
  await screen.findByText("Failed to load skills")
  fireEvent.click(screen.getByRole("button", { name: "Import skills" }))
  await screen.findByRole("button", { name: "Card compiler" })
  state.fetch.mockResolvedValueOnce(reply({}, 500))
  fireEvent.click(screen.getByRole("button", { name: "Import skills" }))
  await screen.findByText("Failed to reload skills")
  state.fetch.mockImplementation(async () => reply([skill("new", { description: "Nebula" })]))
  fireEvent.click(screen.getByRole("button", { name: "Import skills" }))
  await screen.findByRole("button", { name: "Card new" })
  fireEvent.change(screen.getByRole("textbox", { name: "Search skills" }), { target: { value: "Nebula" } })
  await screen.findByText("Showing 1 of 1")
})

it("ignores a prior workspace response and clears its selected detail when workspace disappears", async () => {
  const old = deferred<Response>()
  state.fetch.mockReturnValueOnce(old.promise)
  const view = render(<SkillsBrowser />)
  state.workspaceId = "ws-b"
  view.rerender(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  await act(async () => old.resolve(reply([skill("obsolete")])) )
  expect(screen.queryByRole("button", { name: "Card obsolete" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Card compiler" }))
  state.workspaceId = null
  view.rerender(<SkillsBrowser />)
  await waitFor(() => expect(screen.queryByRole("region", { name: "Skill detail" })).toBeNull())
  expect(screen.queryByRole("button", { name: "Card compiler" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Import skills" })).toBeNull()
})

it("waits for workspace loading and renders an empty missing catalog", async () => {
  state.workspaceId = null; state.loading = true
  const view = render(<SkillsBrowser />)
  expect(screen.getByText("Loading skills…")).toBeVisible()
  expect(state.fetch).not.toHaveBeenCalled()
  state.loading = false
  view.rerender(<SkillsBrowser />)
  expect(screen.getByText("No skills match the current filters.")).toBeVisible()
  state.workspaceId = "ws-a"; state.fetch.mockResolvedValueOnce(reply(null))
  view.rerender(<SkillsBrowser />)
  await screen.findByText("Showing 0 of 0")
})

it("collapses the rail on mobile and allows expanding and collapsing it", async () => {
  state.mobile = true
  render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  fireEvent.click(screen.getByRole("button", { name: "Expand sidebar", hidden: true }))
  expect(screen.getByRole("textbox", { name: "Search skills" })).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Collapse sidebar" }))
  expect(screen.queryByRole("textbox", { name: "Search skills" })).toBeNull()
})

it("clamps saved widths to the observed viewport even when the rail leaves less than the usual minimum", async () => {
  let resized!: ResizeObserverCallback
  const disconnect = vi.fn()
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: ResizeObserverCallback) { resized = callback }
    observe() {}
    disconnect = disconnect
  })
  const view = render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  fireEvent.click(screen.getByRole("button", { name: "Card compiler" }))
  const grid = view.container.querySelector<HTMLElement>("[data-panel-id=skills-grid]")!.parentElement!
  const resize = (width: number) => act(() => resized([{ contentRect: { width } } as ResizeObserverEntry], {} as ResizeObserver))
  resize(800)
  expect(grid.style.gridTemplateColumns).toBe("280px 1fr 160px")
  fireEvent.click(screen.getByRole("button", { name: "Collapse sidebar" }))
  expect(grid.style.gridTemplateColumns).toBe("44px 1fr 380px")
  resize(700)
  expect(grid.style.gridTemplateColumns).toBe("44px 1fr 296px")
  act(() => resized([], {} as ResizeObserver))
  expect(grid.style.gridTemplateColumns).toBe("44px 1fr 380px")
  view.unmount()
  expect(disconnect).toHaveBeenCalledOnce()
})

it.each(["success", "failure"])("ignores %s from an import reload after changing workspace", async (result) => {
  const view = render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  const old = deferred<Response>()
  state.fetch.mockReturnValueOnce(old.promise)
  fireEvent.click(screen.getByRole("button", { name: "Import skills" }))
  state.workspaceId = "ws-b"
  state.fetch.mockImplementation(async () => reply([skill("current")]))
  view.rerender(<SkillsBrowser />)
  await screen.findByRole("button", { name: "Card current" })
  await act(async () => old.resolve(result === "success" ? reply([skill("obsolete")]) : reply({}, 503)))
  expect(screen.getByRole("button", { name: "Card current" })).toBeVisible()
  expect(screen.queryByText("Failed to reload skills")).toBeNull()
  expect(screen.queryByRole("button", { name: "Card obsolete" })).toBeNull()
})

it("reapplies an existing search to newly imported catalog entries", async () => {
  render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  fireEvent.change(screen.getByRole("textbox", { name: "Search skills" }), { target: { value: "Rust" } })
  await screen.findByText("Showing 1 of 3")
  state.fetch.mockImplementation(async () => reply([skill("replacement", { description: "Rust build" })]))
  fireEvent.click(screen.getByRole("button", { name: "Import skills" }))
  expect(await screen.findByRole("button", { name: "Card replacement" })).toBeVisible()
  expect(screen.getByText("Showing 1 of 1")).toBeVisible()
})

it("reloads an empty catalog and keeps collapsed facet controls interactive", async () => {
  render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  fireEvent.click(screen.getByText("Domain"))
  fireEvent.click(screen.getByText("Domain"))
  fireEvent.click(screen.getByText("Source"))
  fireEvent.click(screen.getByText("Source"))
  fireEvent.click(screen.getByText("Writing"))
  fireEvent.click(screen.getByRole("button", { name: "Clear all" }))
  expect(screen.getByText("Showing 3 of 3")).toBeVisible()
  state.fetch.mockResolvedValueOnce(reply(null))
  fireEvent.click(screen.getByRole("button", { name: "Import skills" }))
  await screen.findByText("Showing 0 of 0")
})


it("ignores a completion callback retained by an old workspace import dialog", async () => {
  const view = render(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  const oldImported = state.imported!
  state.workspaceId = "ws-b"
  view.rerender(<SkillsBrowser />)
  await screen.findByText("Showing 3 of 3")
  const requests = state.fetch.mock.calls.length
  act(() => oldImported())
  expect(state.fetch).toHaveBeenCalledTimes(requests)
  const currentImported = state.imported!
  view.unmount()
  act(() => currentImported())
  expect(state.fetch).toHaveBeenCalledTimes(requests)
})
