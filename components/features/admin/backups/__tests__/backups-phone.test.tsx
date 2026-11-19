import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// At phone width (390 px) tables become stacked rows: no <table>, so no
// horizontal scroll, and every row is a 44 px target. The scope control and
// the four views stay reachable on the page, since the panel is a sheet there.

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-dess", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => true }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }), usePathname: () => "/admin/backups" }))
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: { href: string; children: React.ReactNode }) => <a href={href} {...p}>{children}</a> }))

import { BackupsPage } from "../backups-page"

function show(search: string) {
  window.history.replaceState(null, "", `/admin/backups${search}`)
  return render(<BackupsPage />)
}

beforeEach(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(new Response("{}", { status: 404 }))
})
afterEach(cleanup)

describe("phone layout", () => {
  it("Runs are stacked rows with the time, what and size, and the result at the end", async () => {
    show("?demo=1&section=history")
    const list = await screen.findByRole("list", { name: "Runs" })
    expect(document.querySelector("table")).toBeNull()
    const first = within(list).getAllByRole("listitem")[0]
    expect(first).toHaveTextContent("Whole instance · Complete recovery")
    expect(first.querySelector("[data-slot=status-pill]")).toHaveTextContent("done · incomplete")
    expect(first.className).toContain("min-h-[44px]")
  })

  it("a stacked run opens its sheet, and its tick is out of that click", async () => {
    show("?demo=1&section=history")
    const list = await screen.findByRole("list", { name: "Runs" })
    fireEvent.click(within(list).getAllByRole("checkbox")[0])
    expect(screen.queryByRole("dialog")).toBeNull()
    fireEvent.click(within(list).getAllByRole("button")[0])
    expect(await screen.findByRole("dialog")).toBeInTheDocument()
  })

  it("Coverage is stacked too, and a row opens its workspace", async () => {
    show("?demo=1&section=coverage")
    const list = await screen.findByRole("list", { name: "Workspaces" })
    expect(document.querySelector("table")).toBeNull()
    fireEvent.click(within(list).getAllByRole("button", { name: /Pages demo/ })[0])
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("workspace")).toBe("pages-demo"))
  })

  it("keeps the scope and the four views on the page", async () => {
    show("?demo=1")
    const tabs = await screen.findByRole("tablist", { name: "View" })
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent)).toEqual(["Overview", "Runs", "Restore", "Coverage"])
    expect(screen.getAllByRole("button", { name: /^Showing:/ }).length).toBeGreaterThan(0)
  })
})
