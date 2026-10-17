import type { ReactNode } from "react"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { useAppStore } from "@/lib/store"
import { SkillsLayout } from "../skills-layout"

// Harbor 2 M3 (#3350): the skills explorer sits in the shared SecondaryPanel.

const h = vi.hoisted(() => ({
  mobile: false,
  skills: [{ id: "s1", name: "a", slug: "a", display_name: null, description: null, version: null, author: null, category: "CODING", source: "BUNDLED", icon: null }],
}))
vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/skills",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("@/lib/api-fetch", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/api-fetch")>()), apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER", loading: false }) }))
vi.mock("@/hooks/use-skills", () => ({
  useSkillsList: () => ({ data: h.skills, isLoading: false, isError: false, refetch: vi.fn() }),
  useSkillsAgents: () => ({ data: [] }),
  useSkillsCrews: () => ({ data: [] }),
  useProposedSkills: () => ({ proposed: [] }),
  useReviewProposedSkill: () => ({ mutateAsync: vi.fn(), isPending: false }),
}))
vi.mock("motion/react", () => ({
  AnimatePresence: ({ children }: { children: ReactNode }) => <>{children}</>,
  motion: { div: ({ children, className }: { children: ReactNode; className?: string }) => <div className={className}>{children}</div> },
}))
vi.mock("../add-skill-dialog", () => ({ AddSkillDialog: (p: { open: boolean }) => (p.open ? <div role="dialog" aria-label="Add skill" /> : null) }))
vi.mock("../skills-cli-dialog", () => ({ SkillsCliDialog: (p: { open: boolean }) => (p.open ? <div role="dialog" aria-label="Skills from the command line" /> : null) }))
vi.mock("../give-to-picker", () => ({ GiveToPicker: () => null }))
vi.mock("../skills-explorer", () => ({
  visibleViews: () => ["library", "used", "unused", "attention", "proposed", "archived"],
  SkillsExplorer: (p: { onPicked?: () => void }) => (
    <nav aria-label="Skill views">
      <div data-skills-search><input aria-label="Search skills" /></div>
      <button type="button" onClick={p.onPicked}>Pick a view</button>
    </nav>
  ),
}))
vi.mock("../skills-library", () => ({ SkillsLibrary: () => <div>Library</div> }))
vi.mock("../skill-page", () => ({ SkillPage: () => null }))
vi.mock("../skill-editor", () => ({ SkillEditor: () => null }))

beforeEach(() => {
  window.history.replaceState(null, "", "/skills")
  h.mobile = false
  window.localStorage.removeItem("crewship.pref.panel.skills.collapsed")
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
})
afterEach(cleanup)

it("renders the explorer inside the skills SecondaryPanel, which collapses", () => {
  render(<SkillsLayout />)
  const panel = document.querySelector('[data-slot="secondary-panel"]')
  expect(panel).toHaveAttribute("data-panel-id", "skills")
  expect(panel).toContainElement(screen.getByRole("navigation", { name: "Skill views" }))
  fireEvent.click(screen.getByRole("button", { name: "Hide Skills explorer" }))
  expect(panel).toHaveAttribute("data-collapsed", "true")
  expect(screen.queryByRole("navigation", { name: "Skill views" })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Show Skills explorer" }))
  expect(screen.getByRole("navigation", { name: "Skill views" })).toBeInTheDocument()
})

it("keeps `/` focusing the explorer search", () => {
  render(<SkillsLayout />)
  fireEvent.keyDown(window, { key: "/" })
  expect(screen.getByRole("textbox", { name: "Search skills" })).toHaveFocus()
})

it("on a phone shows the explorer in the panel sheet and closes it after a pick", () => {
  h.mobile = true
  render(<SkillsLayout />)
  expect(document.querySelector('[data-slot="secondary-panel"]')).toBeNull()
  expect(screen.queryByRole("button", { name: "Filters" })).not.toBeInTheDocument()
  expect(screen.queryByRole("navigation", { name: "Skill views" })).not.toBeInTheDocument()
  act(() => useAppStore.getState().setSecondaryPanelOpen(true))
  const sheet = screen.getByRole("dialog", { name: "Skills explorer" })
  expect(sheet).toContainElement(screen.getByRole("navigation", { name: "Skill views" }))
  fireEvent.click(screen.getByRole("button", { name: "Pick a view" }))
  expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
})

it("puts Command line and Add skill in the page actions", () => {
  render(<SkillsLayout />)
  fireEvent.click(screen.getByRole("button", { name: "Command line" }))
  expect(screen.getByRole("dialog", { name: "Skills from the command line" })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Add skill" }))
  expect(screen.getByRole("dialog", { name: "Add skill" })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: /^Import/ })).not.toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "New skill" })).not.toBeInTheDocument()
})

it("has one visible h1 naming the view and the library line", () => {
  render(<SkillsLayout />)
  expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1)
  expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Library")
  expect(screen.getByText(/Instructions agents load when a task needs them · 1 in the library from 1 source/)).toBeInTheDocument()
})

it("a legacy ?view= lands on its new name and keeps the other parameters", () => {
  window.history.replaceState(null, "", "/skills?view=assigned&crew=c1")
  render(<SkillsLayout />)
  const q = new URLSearchParams(window.location.search)
  expect(q.get("view")).toBe("used")
  expect(q.get("crew")).toBe("c1")
})

it("number keys switch views", () => {
  render(<SkillsLayout />)
  fireEvent.keyDown(window, { key: "2" })
  expect(new URLSearchParams(window.location.search).get("view")).toBe("used")
  fireEvent.keyDown(window, { key: "1" })
  expect(new URLSearchParams(window.location.search).get("view")).toBeNull()
})
