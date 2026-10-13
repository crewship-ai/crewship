import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), push: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-dess", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }) }))
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: { href: string; children: React.ReactNode }) => <a href={href} {...p}>{children}</a> }))

import { BackupsPage } from "../backups-page"

// Admin › Backups is built like People & workspaces and Security: a panel with
// three views (Overview · Runs · Restore), the plans, every workspace and the
// instance settings; Runs filters above its table like the Audit log, and a
// workspace opens its own backups. Everything is kept in the URL.
function show(search = "?demo=1") {
  window.history.replaceState(null, "", `/admin/backups${search}`)
  return render(<BackupsPage />)
}
const panel = () => screen.getByRole("complementary", { name: "Backups navigation" })
const views = () => within(panel()).getByRole("group", { name: "View" })
const param = (k: string) => new URLSearchParams(window.location.search).get(k)
/** Opens a filter menu above the runs and picks an option. */
async function pick(filter: RegExp, option: RegExp) {
  const trigger = screen.getByRole("button", { name: filter })
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
  fireEvent.click(await screen.findByRole("menuitem", { name: option }))
}

beforeEach(() => {
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(new Response("{}", { status: 404 }))
  h.push.mockReset()
  h.toast.message.mockReset()
})
afterEach(() => cleanup())

describe("BackupsPage panel, like People & workspaces", () => {
  it("leads back to Admin from the sub-bar and offers three views, the plans, the workspaces and the instance settings", async () => {
    show()
    const p = panel()
    expect(screen.getByRole("link", { name: "Back to Admin" })).toHaveAttribute("href", "/admin")
    expect(within(views()).getAllByRole("button").map((b) => b.textContent?.replace(/\d+$/, ""))).toEqual(["Overview", "Runs", "Restore"])
    for (const label of ["New plan", "Storage", "Keys & alerts"]) {
      expect(within(p).getByRole("button", { name: new RegExp(label) })).toBeInTheDocument()
    }
    expect(await within(p).findByRole("button", { name: /^Dess/ })).toBeInTheDocument()
    // No scope block and no data retention in the panel.
    expect(within(p).queryByText("Selected workspaces")).toBeNull()
    expect(within(p).queryByText("Data retention")).toBeNull()
  })

  it("opens Overview by default and another section from the panel, in the URL", async () => {
    show()
    expect(within(views()).getByRole("button", { name: /^Overview/ })).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(within(panel()).getByRole("button", { name: /Storage/ }))
    expect(param("section")).toBe("storage")
    fireEvent.click(within(views()).getByRole("button", { name: "Runs" }))
    expect(param("section")).toBe("history")
  })

  it("lists each plan as a row; picking one opens it", async () => {
    show()
    fireEvent.click(await within(panel()).findByRole("button", { name: /Complete recovery/ }))
    expect(param("section")).toBe("schedules")
    expect(param("plan")).toBeTruthy()
  })

  it("search narrows the plans and workspaces in the panel", async () => {
    show()
    await within(panel()).findByRole("button", { name: /Complete recovery/ })
    fireEvent.change(within(panel()).getByPlaceholderText(/Search/), { target: { value: "memory" } })
    expect(within(panel()).queryByRole("button", { name: /Complete recovery/ })).toBeNull()
    expect(within(panel()).getByRole("button", { name: /Memory every 6 h/ })).toBeInTheDocument()
  })

  it("opens a workspace's own backups from the panel", async () => {
    show()
    fireEvent.click(await within(panel()).findByRole("button", { name: /^Dess/ }))
    expect(param("workspace")).toBe("dess")
    const header = await waitFor(() => { const el = document.querySelector("[data-slot=workspace-backups-header]"); if (!el) throw new Error("no header"); return el })
    expect(header).toHaveTextContent("Dess")
    expect(within(header as HTMLElement).getByRole("button", { name: /Back up now/ })).toBeInTheDocument()
    expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toHaveTextContent("Overview")
  })
})

describe("ticking workspaces in the panel", () => {
  it("picks what the page covers, several at once, kept in ?scope=&ws=; none is the whole instance", async () => {
    show()
    const p = panel()
    fireEvent.click(await within(p).findByRole("button", { name: "Select Dess" }))
    expect(param("scope")).toBe("workspaces")
    fireEvent.click(within(p).getByRole("button", { name: "Select Coolify" }))
    expect(param("ws")?.split(",").sort()).toEqual(["coolify", "dess"])
    expect(document.querySelector("[data-slot=backups-scope-summary]")).toHaveTextContent("2 of")
    expect(screen.getByRole("button", { name: "Back up now" })).toHaveAttribute("title", "Back up 2 workspaces now")
    fireEvent.click(within(p).getByRole("button", { name: "Select Dess" }))
    fireEvent.click(within(p).getByRole("button", { name: "Select Coolify" }))
    expect(param("scope")).toBe("instance")
    expect(screen.getByRole("button", { name: "Back up now" })).toHaveAttribute("title", "Back up the whole instance now")
  })

  it("ticks every workspace at once, and a name still opens the workspace", async () => {
    show()
    fireEvent.click(await within(panel()).findByRole("button", { name: "Select every workspace" }))
    expect(param("scope")).toBe("workspaces")
    expect(document.querySelector("[data-slot=backups-scope-summary]")).toHaveTextContent(/^All of \d+ ticked/)
    fireEvent.click(within(panel()).getByRole("button", { name: /^Dess/ }))
    expect(param("workspace")).toBe("dess")
  })

  it("Restore from a workspace's page restores that workspace", async () => {
    show("?demo=1&workspace=dess")
    const header = await waitFor(() => { const el = document.querySelector("[data-slot=workspace-backups-header]"); if (!el) throw new Error("no header"); return el as HTMLElement })
    fireEvent.click(within(header).getByRole("button", { name: /Restore/ }))
    expect(param("section")).toBe("recovery")
    expect(param("scope")).toBe("workspaces")
    expect(param("ws")).toBe("dess")
  })
})

describe("Runs, like the Audit log", () => {
  it("filters above the table and in the URL, the count is what is shown", async () => {
    show("?demo=1&section=history")
    await waitFor(() => expect(document.querySelector("[data-slot=runs-scope]")).toHaveTextContent("5 runs in range"))
    await pick(/^Kind:/, /^Environments/)
    expect(param("kind")).toBe("environments")
    await waitFor(() => expect(document.querySelector("[data-slot=runs-scope]")).toHaveTextContent("1 run in range"))
  })

  it("Proof keeps only runs proven that far", async () => {
    show("?demo=1&section=history")
    await waitFor(() => expect(document.querySelector("[data-slot=runs-scope]")).toHaveTextContent("5 runs in range"))
    await pick(/^Proof:/, /^Test restore/)
    expect(param("proof")).toBe("3")
    await waitFor(() => expect(document.querySelector("[data-slot=runs-scope]")).toHaveTextContent("2 runs in range"))
  })

  it("Result narrows to failed or pinned runs", async () => {
    show("?demo=1&section=history")
    await screen.findByRole("button", { name: /^Result:/ })
    await pick(/^Result:/, /^Pinned/)
    expect(param("status")).toBe("pinned")
  })

  it("Scope switches between the whole instance, every workspace and one, kept in ?scope=", async () => {
    show("?demo=1&section=history")
    await screen.findByRole("button", { name: /^Scope: Whole instance/ })
    await pick(/^Scope:/, /^All workspaces/)
    expect(param("scope")).toBe("workspaces")
    await pick(/^Scope:/, /^Whole instance/)
    expect(param("scope")).toBe("instance")
  })

  it("draws the Crew Journal's chart and a time range in the sub-bar", async () => {
    show("?demo=1&section=history")
    expect(await screen.findByRole("combobox", { name: "Time range" })).toBeInTheDocument()
    expect(document.querySelector("[data-slot=runs-histogram]")).not.toBeNull()
  })

  it("ticked runs are pinned from the green selection bar (demo sends nothing)", async () => {
    show("?demo=1&section=history")
    const box = (await screen.findAllByRole("checkbox", { name: /^Select the run of/ }))[0]
    fireEvent.click(box)
    const bar = screen.getByRole("toolbar", { name: "Change the selected runs" })
    expect(bar).toHaveTextContent("1 selected")
    fireEvent.click(within(bar).getByRole("button", { name: /^Pin$/ }))
    await waitFor(() => expect(h.toast.message).toHaveBeenCalledWith("Demo data · nothing was sent"))
  })
})

describe("Restore", () => {
  it("switches New restore, History and Drills above the wizard, in the URL", async () => {
    show("?demo=1&section=recovery")
    fireEvent.click(within(await screen.findByRole("group", { name: "Restore" })).getByRole("button", { name: "Drills" }))
    expect(param("view")).toBe("drills")
  })
})
