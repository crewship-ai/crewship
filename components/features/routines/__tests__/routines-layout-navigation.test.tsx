import type { ReactNode } from "react"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { RoutineFilterState } from "@/lib/routine-filters"
import { useAppStore } from "@/lib/store"
import { RoutinesLayout, CREATE_ROUTINE_REASON } from "../routines-layout"

const h = vi.hoisted(() => ({ mobile: false, role: "MANAGER" as string | null, refresh: vi.fn(), pipelines: [
  { slug: "nightly", name: "Nightly report", description: "Prepare report", invocation_count: 1, author_crew_id: "crew_ops" },
  { slug: "test-routines-fixture", name: "Fixture", description: "Test fixture", invocation_count: 5 },
] }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(window.location.search), usePathname: () => "/routines" }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role }) }))
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () => (String(url).startsWith("/api/v1/crews") ? [{ id: "crew_ops", name: "Ops" }] : String(url).endsWith("/pipelines/drafts") ? [] : {}),
  })),
}))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: h.pipelines, loading: false, error: null, refresh: h.refresh }) }))
vi.mock("@/hooks/use-routine-panel-index", async () => { const m = await import("@/lib/routine-panel-index"); return { useRoutinePanelIndex: () => m.EMPTY_PANEL_INDEX } })
vi.mock("@/hooks/use-pipeline-run-records", () => ({ usePipelineRunRecords: () => ({ records: [], loading: false, error: null, refresh: vi.fn() }) }))
vi.mock("motion/react", () => ({ AnimatePresence: ({ children }: { children: ReactNode }) => <>{children}</>, motion: { div: ({ children, className }: { children: ReactNode; className?: string }) => <div className={className}>{children}</div> } }))
vi.mock("../routines-explorer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../routines-explorer")>()),
  RoutinesExplorer: (p: { routines: { slug: string; name: string }[]; search: string; view: string | null; onViewChange: (v: string) => void; onSearchChange: (s: string) => void; onSelectRoutine: (s: string) => void; filters: RoutineFilterState; onChange: (f: RoutineFilterState) => void }) => <nav aria-label="Routine explorer" data-view={p.view ?? "none"}>
    <div data-routines-search><input aria-label="Routine search" value={p.search} onChange={e => p.onSearchChange(e.target.value)} /></div>
    {p.routines.map(r => <button key={r.slug} onClick={() => p.onSelectRoutine(r.slug)}>Select {r.name}</button>)}
    <button onClick={() => p.onViewChange("drafts")}>View drafts</button>
    <button onClick={() => p.onChange({ ...p.filters, showTestRoutines: true })}>Show test routines</button>
  </nav>,
}))
vi.mock("../routines-workspace", () => ({ RoutinesWorkspace: ({ search }: { search: string }) => <div>Overview search: {search}</div> }))
vi.mock("../routines-views", () => ({ needsYou: () => [], RoutinesViewPane: ({ view }: { view: string }) => <div>View pane {view}</div> }))
vi.mock("../routines-detail-panel", () => ({ RoutinesDetailPanel: (p: { slug: string; onClose: () => void; onChanged: () => void; onRunStarted: (s: string) => void }) => <section aria-label={`Detail ${p.slug}`}><button onClick={p.onClose}>Close detail</button><button onClick={p.onChanged}>Refresh detail</button><button onClick={() => p.onRunStarted("run-1")}>Start historical run</button></section> }))
vi.mock("../routine-run-detail", () => ({ RoutineRunDetail: ({ runId }: { runId: string }) => <div>Run {runId}</div> }))
vi.mock("../routine-new-wizard", () => ({ RoutineNewWizard: (p: { open: boolean; onOpenChange: (v: boolean) => void; onCreated: () => void; onOpenRoutine: (s: string) => void }) => p.open ? <section aria-label="New routine dialog"><button onClick={() => p.onOpenChange(false)}>Close creation</button><button onClick={() => { p.onCreated(); p.onOpenRoutine("created"); p.onOpenChange(false) }}>Open created routine</button></section> : null }))
// A desktop window: at tablet width the kit's SecondaryPanel starts closed
// and floats (COM-05), which the phone/desktop cases here do not exercise.
beforeEach(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
})
beforeEach(() => {
  window.history.replaceState(null, "", "/routines"); h.mobile = false; h.role = "MANAGER"; h.refresh.mockClear()
  window.localStorage.removeItem("crewship.pref.panel.routines.collapsed")
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
})
afterEach(cleanup)

it("keeps navigation in the URL, clears stale run/edit selection and preserves explorer search", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  expect(screen.getByText("1 routine · 1 run")).toBeInTheDocument()
  expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Overview")
  expect(screen.queryByRole("button", { name: "Select Fixture" })).not.toBeInTheDocument()
  fireEvent.change(screen.getByRole("textbox", { name: "Routine search" }), { target: { value: "night" } })
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(new URLSearchParams(window.location.search).get("slug")).toBe("nightly")
  expect(screen.getByRole("region", { name: "Detail nightly" })).toBeInTheDocument()
  // The breadcrumb names the routine; there is no in-content back bar.
  expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Nightly report")
  expect(screen.queryByRole("button", { name: "Back to routines" })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Refresh detail" }))
  expect(h.refresh).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole("button", { name: "Start historical run" }))
  expect(screen.getByText("Run run-1")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(new URLSearchParams(window.location.search).has("run")).toBe(false)
  fireEvent.click(screen.getByRole("button", { name: "Close detail" }))
  expect(window.location.search).toBe("")
  expect(screen.getByRole("textbox", { name: "Routine search" })).toHaveValue("night")
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(screen.queryByRole("region", { name: "Detail nightly" })).not.toBeInTheDocument()
})

it("keeps the list view in ?view= and switches views with 1–5", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  fireEvent.click(screen.getByRole("button", { name: "View drafts" }))
  expect(window.location.search).toBe("?view=drafts")
  expect(screen.getByText("View pane drafts")).toBeInTheDocument()
  expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Drafts")
  fireEvent.keyDown(window, { key: "2" })
  expect(window.location.search).toBe("?view=needs")
  fireEvent.keyDown(window, { key: "1" })
  expect(window.location.search).toBe("")
  expect(screen.getByText(/Overview search/)).toBeInTheDocument()
  // Opening a routine leaves the list view; the panel marks no view.
  fireEvent.keyDown(window, { key: "4" })
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(window.location.search).toBe("?slug=nightly")
  expect(screen.getByRole("navigation", { name: "Routine explorer" })).toHaveAttribute("data-view", "none")
})

it("focuses search and resets filters with shortcuts while leaving typed shortcuts alone", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  fireEvent.keyDown(window, { key: "/" })
  const search = screen.getByRole("textbox", { name: "Routine search" })
  expect(search).toHaveFocus()
  fireEvent.keyDown(search, { key: "c" })
  expect(screen.queryByRole("region", { name: "New routine dialog" })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Show test routines" }))
  expect(screen.getByRole("button", { name: "Select Fixture" })).toBeInTheDocument()
  fireEvent.keyDown(window, { key: "Escape" })
  expect(screen.queryByRole("button", { name: "Select Fixture" })).not.toBeInTheDocument()
  fireEvent.change(search, { target: { value: "clear me" } })
  fireEvent.keyDown(window, { key: "Escape" })
  expect(search).toHaveValue("")
  fireEvent.keyDown(window, { key: "c", ctrlKey: true })
  expect(screen.queryByRole("region", { name: "New routine dialog" })).not.toBeInTheDocument()
  fireEvent.keyDown(window, { key: "c" })
  fireEvent.click(screen.getByRole("button", { name: "Open created routine" }))
  expect(screen.getByRole("region", { name: "Detail created" })).toBeInTheDocument()
  expect(h.refresh).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole("button", { name: "Close detail" }))
  expect(window.location.search).toBe("")
})

it("C opens New routine for a Manager; a Member gets the buttons disabled with the reason", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  expect(screen.getByRole("button", { name: "New routine" })).toBeEnabled()
  fireEvent.click(screen.getByRole("button", { name: "New routine" }))
  expect(screen.getByRole("region", { name: "New routine dialog" })).toBeInTheDocument()
  cleanup()
  h.role = "MEMBER"
  render(<RoutinesLayout workspaceId="workspace" />)
  const create = screen.getByRole("button", { name: "New routine" })
  expect(create).toBeDisabled()
  expect(create).toHaveAccessibleDescription(CREATE_ROUTINE_REASON)
  expect(screen.getByRole("button", { name: "Import" })).toBeDisabled()
  fireEvent.keyDown(window, { key: "c" })
  expect(screen.queryByRole("region", { name: "New routine dialog" })).not.toBeInTheDocument()
})

it("sits in the shared SecondaryPanel, which collapses to a rail", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  const panel = document.querySelector('[data-slot="secondary-panel"]')
  expect(panel).toHaveAttribute("data-panel-id", "routines")
  expect(panel).toContainElement(screen.getByRole("navigation", { name: "Routine explorer" }))
  fireEvent.click(screen.getByRole("button", { name: "Hide Routine list" }))
  expect(panel).toHaveAttribute("data-collapsed", "true")
  expect(screen.queryByRole("navigation", { name: "Routine explorer" })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Show Routine list" }))
  expect(screen.getByRole("navigation", { name: "Routine explorer" })).toBeInTheDocument()
})

it("normalizes a linked draft and puts the phone explorer in the panel sheet", () => {
  h.mobile = true
  window.history.replaceState(null, "", "/routines?draft=nightly&draft_id=draft-1&workspace=w&view=edit&run=old")
  render(<RoutinesLayout workspaceId="workspace" />)
  expect(window.location.search).toBe("?slug=nightly")
  expect(screen.getByRole("region", { name: "Detail nightly" })).toBeInTheDocument()
  expect(document.querySelector('[data-slot="secondary-panel"]')).toBeNull()
  expect(screen.queryByRole("navigation", { name: "Routine explorer" })).not.toBeInTheDocument()
  // The phone's way to the list: a chip above the content.
  fireEvent.click(screen.getByRole("button", { name: /All routines|Ops/ }))
  const sheet = screen.getByRole("dialog", { name: "Routine list" })
  expect(sheet).toContainElement(screen.getByRole("navigation", { name: "Routine explorer" }))
  // Picking a routine means "show me that": the sheet closes.
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
})

it.each(["edit", "settings"])("a routine opened in the %s view keeps its slug and closes cleanly", (view) => {
  window.history.replaceState(null, "", `/routines?routine=nightly&view=${view}`)
  render(<RoutinesLayout workspaceId="workspace" />)
  expect(screen.getByRole("region", { name: "Detail nightly" })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Close detail" }))
  expect(window.location.search).toBe("")
})

it("How routines work in the top bar opens the intro as ?view=intro (RT-09)", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  fireEvent.click(screen.getByRole("button", { name: "How routines work" }))
  expect(window.location.search).toBe("?view=intro")
  expect(screen.getByRole("heading", { name: "Work that repeats, done by your crew." })).toBeInTheDocument()
  expect(screen.getByRole("heading", { name: "From a sentence to a routine in three steps" })).toBeInTheDocument()
  // No template gallery and no workspace figures (ODCH-RT-08).
  expect(screen.queryByText(/template/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/In this workspace this week/)).not.toBeInTheDocument()
  // The intro is not one of the five views in the panel.
  expect(screen.getByRole("navigation", { name: "Routine explorer" })).toHaveAttribute("data-view", "none")
  fireEvent.keyDown(window, { key: "1" })
  expect(window.location.search).toBe("")
})

it("PAR-051: New routine carries the C shortcut chip, hidden from assistive names and touch screens", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  const button = screen.getByRole("button", { name: "New routine" })
  const chip = button.querySelector("kbd")
  expect(chip).not.toBeNull()
  expect(chip).toHaveTextContent("C")
  expect(chip).toHaveAttribute("aria-hidden", "true")
  expect(chip!.className).toMatch(/coarse:hidden/)
})
