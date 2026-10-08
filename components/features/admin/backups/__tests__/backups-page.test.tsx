import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), push: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-dess", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }) }))
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: { href: string; children: React.ReactNode }) => <a href={href} {...p}>{children}</a> }))

import { BackupsPage } from "../backups-page"

// Admin › Backups is a nested page like Security and People: the side panel
// holds the scope, the Runs facets, the plans and the settings; the content is
// one section at a time, kept in the URL.
function show(search = "?demo=1") {
  window.history.replaceState(null, "", `/admin/backups${search}`)
  return render(<BackupsPage />)
}
const panel = () => screen.getByRole("complementary", { name: "Backups navigation" })
const param = (k: string) => new URLSearchParams(window.location.search).get(k)

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(new Response("{}", { status: 404 }))
  h.push.mockReset()
})
afterEach(() => cleanup())

describe("BackupsPage panel", () => {
  it("is headed by ← Admin and lists Scope, Status, Runs, Plans, Recovery and Settings", async () => {
    show()
    const p = panel()
    expect(within(p).getByRole("link", { name: /Admin/ })).toHaveAttribute("href", "/admin")
    for (const label of ["Scope", "Status", "Runs", "Plans", "Recovery", "Settings"]) expect(within(p).getByText(label)).toBeInTheDocument()
    expect(within(p).queryByText("Data retention")).toBeNull()
  })

  it("opens Overview by default and another section from the panel, in the URL", async () => {
    show()
    expect(within(panel()).getByRole("button", { name: /Overview/ })).toHaveAttribute("aria-current", "true")
    fireEvent.click(within(panel()).getByRole("button", { name: /Storage/ }))
    expect(param("section")).toBe("storage")
  })

  it("a Runs facet opens Backup history filtered by it", async () => {
    show()
    fireEvent.click(await within(panel()).findByRole("button", { name: /^Failed/ }))
    expect(param("section")).toBe("history")
    expect(param("status")).toBe("failed")
  })

  it("lists each plan as a row; picking one opens it in Schedules", async () => {
    show()
    fireEvent.click(await within(panel()).findByRole("button", { name: /Complete recovery/ }))
    expect(param("section")).toBe("schedules")
    expect(param("plan")).toBeTruthy()
  })

  it("greys the workspace list out on an instance-only page", async () => {
    show("?demo=1&section=keys")
    const list = (await screen.findAllByText("Selected workspaces"))[0].closest("[data-slot=backups-scope]") as HTMLElement
    expect(list.querySelector("[data-slot=workspace-scope]")).toHaveAttribute("aria-disabled", "true")
  })

  it("switches between the whole instance and selected workspaces, kept in ?scope=", async () => {
    show()
    fireEvent.click(within(panel()).getByRole("button", { name: /Selected workspaces/ }))
    expect(param("scope")).toBe("workspaces")
    fireEvent.click(within(panel()).getByRole("button", { name: /Whole instance/ }))
    expect(param("scope")).toBe("instance")
  })
})

// The panel's toolbar: a search over plans and runs, and a Filter with the
// dimensions the Runs facets do not cover (kind, proof, when). The facet
// counts and Backup history apply the same filters.
describe("BackupsPage toolbar", () => {
  it("search narrows the plans in the panel", async () => {
    show()
    await within(panel()).findByRole("button", { name: /Complete recovery/ })
    fireEvent.change(within(panel()).getByPlaceholderText("Search runs, plans…"), { target: { value: "memory" } })
    expect(within(panel()).queryByRole("button", { name: /Complete recovery/ })).toBeNull()
    expect(within(panel()).getByRole("button", { name: /Memory every 6 h/ })).toBeInTheDocument()
  })

  it("Filter › Kind narrows the runs the facets count and History shows, kept in the URL", async () => {
    show("?demo=1&section=history")
    await within(panel()).findByRole("button", { name: /^All runs\s*5/ })
    fireEvent.click(within(panel()).getByRole("button", { name: /Filter/ }))
    fireEvent.click(screen.getByRole("button", { name: /Environments/ }))
    expect(param("kind")).toBe("environments")
    expect(within(panel()).getByRole("button", { name: /^All runs\s*1/ })).toBeInTheDocument()
  })

  it("Filter › Proof keeps only runs proven that far", async () => {
    show("?demo=1&section=history")
    await within(panel()).findByRole("button", { name: /^All runs\s*5/ })
    fireEvent.click(within(panel()).getByRole("button", { name: /Filter/ }))
    fireEvent.click(screen.getByRole("button", { name: /Test restore/ }))
    expect(param("proof")).toBe("3")
    expect(within(panel()).getByRole("button", { name: /^All runs\s*2/ })).toBeInTheDocument()
  })
})
