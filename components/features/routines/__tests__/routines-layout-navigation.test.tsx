import type { ReactNode } from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { RoutineFilterState } from "@/lib/routine-filters"
import { RoutinesLayout } from "../routines-layout"

const h = vi.hoisted(() => ({ mobile: false, refresh: vi.fn(), pipelines: [
  { slug: "nightly", name: "Nightly report", description: "Prepare report", invocation_count: 1 },
  { slug: "test-routines-fixture", name: "Fixture", description: "Test fixture", invocation_count: 5 },
] }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(window.location.search) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: h.pipelines, loading: false, error: null, refresh: h.refresh }) }))
vi.mock("motion/react", () => ({ AnimatePresence: ({ children }: { children: ReactNode }) => <>{children}</>, motion: { div: ({ children, className }: { children: ReactNode; className?: string }) => <div className={className}>{children}</div> } }))
vi.mock("../routines-explorer", () => ({ RoutinesExplorer: (p: { routines: { slug: string; name: string }[]; search: string; onSearchChange: (s: string) => void; onSelectRoutine: (s: string) => void; filters: RoutineFilterState; onChange: (f: RoutineFilterState) => void; onToggleCollapse: () => void }) => <nav aria-label="Routine explorer">
  <div data-routines-search><input aria-label="Routine search" value={p.search} onChange={e => p.onSearchChange(e.target.value)} /></div>
  {p.routines.map(r => <button key={r.slug} onClick={() => p.onSelectRoutine(r.slug)}>Select {r.name}</button>)}
  <button onClick={() => p.onChange({ ...p.filters, showTestRoutines: true })}>Show test routines</button>
  <button onClick={p.onToggleCollapse}>Collapse explorer</button>
</nav> }))
vi.mock("../routines-workspace", () => ({ RoutinesWorkspace: ({ search }: { search: string }) => <div>Overview search: {search}</div> }))
vi.mock("../routines-detail-panel", () => ({ RoutinesDetailPanel: (p: { slug: string; onClose: () => void; onChanged: () => void; onRunStarted: (s: string) => void }) => <section aria-label={`Detail ${p.slug}`}><button onClick={p.onClose}>Close detail</button><button onClick={p.onChanged}>Refresh detail</button><button onClick={() => p.onRunStarted("run-1")}>Start historical run</button></section> }))
vi.mock("../routine-run-detail", () => ({ RoutineRunDetail: ({ runId }: { runId: string }) => <div>Run {runId}</div> }))
vi.mock("../routine-new-dialog", () => ({ RoutineNewDialog: (p: { open: boolean; onOpenChange: (v: boolean) => void; onCreated: () => void; onOpenRoutine: (s: string) => void }) => p.open ? <section aria-label="New routine dialog"><button onClick={() => p.onOpenChange(false)}>Close creation</button><button onClick={() => { p.onCreated(); p.onOpenRoutine("created"); p.onOpenChange(false) }}>Open created routine</button></section> : null }))
beforeEach(() => { window.history.replaceState(null, "", "/routines"); h.mobile = false; h.refresh.mockClear() })
afterEach(cleanup)

it("keeps navigation in the URL, clears stale run/edit selection and preserves explorer search", () => {
  render(<RoutinesLayout workspaceId="workspace" />)
  expect(screen.getByText("1 routine · 1 run")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Select Fixture" })).not.toBeInTheDocument()
  fireEvent.change(screen.getByRole("textbox", { name: "Routine search" }), { target: { value: "night" } })
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(new URLSearchParams(window.location.search).get("slug")).toBe("nightly")
  expect(screen.getByRole("region", { name: "Detail nightly" })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Refresh detail" }))
  expect(h.refresh).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole("button", { name: "Start historical run" }))
  expect(screen.getByText("Run run-1")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(new URLSearchParams(window.location.search).has("run")).toBe(false)
  fireEvent.click(screen.getByRole("button", { name: "Back to routines" }))
  expect(window.location.search).toBe("")
  expect(screen.getByRole("textbox", { name: "Routine search" })).toHaveValue("night")
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(screen.queryByRole("region", { name: "Detail nightly" })).not.toBeInTheDocument()
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

it("normalizes a linked draft and keeps the phone explorer as a dismissible overlay", () => {
  h.mobile = true
  window.history.replaceState(null, "", "/routines?draft=nightly&draft_id=draft-1&workspace=w&view=edit&run=old")
  render(<RoutinesLayout workspaceId="workspace" />)
  expect(window.location.search).toBe("?slug=nightly")
  expect(screen.getByRole("region", { name: "Detail nightly" })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }))
  fireEvent.click(screen.getByRole("button", { name: "Close routine list" }))
  expect(screen.queryByRole("navigation", { name: "Routine explorer" })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }))
  fireEvent.click(screen.getByRole("button", { name: "Select Nightly report" }))
  expect(screen.queryByRole("navigation", { name: "Routine explorer" })).not.toBeInTheDocument()
})

it.each(["edit", "settings"])("hides the unguarded back action in the %s view", (view) => {
  window.history.replaceState(null, "", `/routines?routine=nightly&view=${view}`)
  render(<RoutinesLayout workspaceId="workspace" />)
  expect(screen.queryByRole("button", { name: "Back to routines" })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Close detail" }))
  expect(window.location.search).toBe("")
})
