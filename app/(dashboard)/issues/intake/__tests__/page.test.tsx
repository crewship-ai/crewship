import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

const h = vi.hoisted(() => ({ mode: "full", tab: null as string | null, workspaceId: "ws" as string | null, push: vi.fn(), setTab: vi.fn(), mobile: false }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => h.mode }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: h.workspaceId, loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("@/hooks/use-shallow-search-param", () => ({ useShallowSearchParam: () => [h.tab, h.setTab] }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }), useSearchParams: () => new URLSearchParams() }))
vi.mock("@/components/features/issues/restricted-issues", () => ({ RestrictedIssues: () => <div>restricted issues</div> }))
vi.mock("@/components/features/issues/intake-surface", () => ({
  IntakeSurface: ({ tab, onTabChange }: { tab: string; onTabChange: (t: string) => void }) => (
    <div>
      <span>surface:{tab}</span>
      <button onClick={() => onTabChange("recurring")}>to recurring</button>
    </div>
  ),
}))

import IntakePage from "../page"

describe("/issues/intake", () => {
  beforeEach(() => {
    h.mode = "full"
    h.tab = null
    h.workspaceId = "ws"
    h.mobile = false
    h.push.mockReset()
    h.setTab.mockReset()
  })

  it("opens on Triage rules and keeps the tab in the URL", () => {
    render(<IntakePage />)
    expect(screen.getByText("surface:triage")).toBeInTheDocument()
    fireEvent.click(screen.getByText("to recurring"))
    expect(h.setTab).toHaveBeenCalledWith("recurring")
  })

  it("opens a deep-linked tab and ignores an unknown one", () => {
    h.tab = "recurring"
    const { unmount } = render(<IntakePage />)
    expect(screen.getByText("surface:recurring")).toBeInTheDocument()
    unmount()
    h.tab = "nonsense"
    render(<IntakePage />)
    expect(screen.getByText("surface:triage")).toBeInTheDocument()
  })

  it("has Back to issues", () => {
    render(<IntakePage />)
    fireEvent.click(screen.getByRole("button", { name: "Back to issues" }))
    expect(h.push).toHaveBeenCalledWith("/issues")
  })

  it("shows the restricted view, not intake, to a restricted reader", () => {
    h.mode = "restricted"
    render(<IntakePage />)
    expect(screen.getByText("restricted issues")).toBeInTheDocument()
    expect(screen.queryByText(/surface:/)).toBeNull()
  })

  it("renders nothing while the access mode is unknown", () => {
    h.mode = "loading"
    const { container } = render(<IntakePage />)
    expect(container).toBeEmptyDOMElement()
  })

  it("is the same single column on a phone", () => {
    h.mobile = true
    render(<IntakePage />)
    expect(screen.getByText("surface:triage")).toBeInTheDocument()
  })
})
