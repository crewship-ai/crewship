import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), push: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-dess", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }), usePathname: () => "/admin/backups" }))
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: { href: string; children: React.ReactNode }) => <a href={href} {...p}>{children}</a> }))

import { BackupsPage } from "../backups-page"
import { plansFixture, runsFixture } from "../__fixtures__/backups"

// Admin › Backups is built like People & workspaces and Security: a panel with
// one scope control, the views (Overview · Runs · Restore · Coverage), the
// plans and the instance settings. No workspace is listed in the panel; the
// scope picker and Coverage hold hundreds. Everything is kept in the URL.
function show(search = "?demo=1") {
  window.history.replaceState(null, "", `/admin/backups${search}`)
  return render(<BackupsPage />)
}
const panel = () => screen.getByRole("complementary", { name: "Backups navigation" })
const param = (k: string) => new URLSearchParams(window.location.search).get(k)
// While the picker is open the rest of the page is aria-hidden: read the control by its slot.
const scopeText = () => document.querySelector("[data-slot=scope-value]")?.textContent
/** Opens a filter menu above the runs and picks an option. */
async function pick(filter: RegExp, option: RegExp) {
  const trigger = screen.getByRole("button", { name: filter })
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
  fireEvent.click(await screen.findByRole("menuitem", { name: option }))
}
async function openScope() {
  fireEvent.click(await within(panel()).findByRole("button", { name: /^Showing:/ }))
  return screen.findByRole("dialog")
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

describe("BackupsPage panel", () => {
  it("leads back to Admin and offers the views, the plans and the instance settings, with no workspace list", async () => {
    show()
    const p = panel()
    expect(within(p).getByRole("link", { name: "Back to Admin" })).toHaveAttribute("href", "/admin")
    for (const v of ["Overview", "Runs", "Restore", "Coverage"]) expect(within(p).getByRole("button", { name: new RegExp(`^${v}`) })).toBeInTheDocument()
    for (const label of ["New plan", "Storage", "Keys & alerts"]) expect(within(p).getByRole("button", { name: new RegExp(label) })).toBeInTheDocument()
    expect(scopeText()).toBe("Whole instance")
    await within(p).findByRole("button", { name: /Complete recovery/ })
    for (const w of ["Dess", "Coolify", "Sandbox"]) expect(within(p).queryByRole("button", { name: new RegExp(`^${w}`) })).toBeNull()
    expect(within(p).queryByText("Selected workspaces")).toBeNull()
    expect(within(p).queryByText("Data retention")).toBeNull()
  })

  it("names the Overview attention and the coverage in the panel, each once", async () => {
    show()
    const p = panel()
    await waitFor(() => expect(within(p).getByRole("button", { name: /^Overview/ })).toHaveTextContent(/\d$/))
    await waitFor(() => expect(within(p).getByRole("button", { name: /^Coverage/ })).toHaveTextContent("2 of 4"))
  })

  it("opens Overview by default and another view from the panel, in the URL", async () => {
    show()
    expect(within(panel()).getByRole("button", { name: /^Overview/ })).toHaveAttribute("aria-current", "true")
    fireEvent.click(within(panel()).getByRole("button", { name: /Storage/ }))
    expect(param("section")).toBe("storage")
    fireEvent.click(within(panel()).getByRole("button", { name: /^Runs/ }))
    expect(param("section")).toBe("history")
    fireEvent.click(within(panel()).getByRole("button", { name: /^Coverage/ }))
    expect(param("section")).toBe("coverage")
  })

  it("lists each plan as a row; picking one opens it", async () => {
    show()
    fireEvent.click(await within(panel()).findByRole("button", { name: /Complete recovery/ }))
    expect(param("section")).toBe("schedules")
    expect(param("plan")).toBeTruthy()
  })

  it("lists at most eight plans, then All N plans…", async () => {
    const many = Array.from({ length: 11 }, (_, i) => ({ ...plansFixture()[0], id: `p${i}`, name: `Plan ${String(i).padStart(2, "0")}` }))
    h.apiFetch.mockImplementation(async (url: string) => (String(url).endsWith("/backups/plans") ? new Response(JSON.stringify({ data: many }), { status: 200 }) : new Response("{}", { status: 404 })))
    show("")
    const p = panel()
    await within(p).findByRole("button", { name: /Plan 00/ })
    expect(within(p).getAllByRole("button", { name: /^Plan \d\d/ })).toHaveLength(8)
    fireEvent.click(within(p).getByRole("button", { name: "All 11 plans…" }))
    expect(within(p).getAllByRole("button", { name: /^Plan \d\d/ })).toHaveLength(11)
  })

  it("search narrows the plans in the panel", async () => {
    show()
    await within(panel()).findByRole("button", { name: /Complete recovery/ })
    fireEvent.change(within(panel()).getByPlaceholderText(/Search/), { target: { value: "memory" } })
    expect(within(panel()).queryByRole("button", { name: /Complete recovery/ })).toBeNull()
    expect(within(panel()).getByRole("button", { name: /Memory every 6 h/ })).toBeInTheDocument()
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

describe("the scope: one control at the top of the panel", () => {
  it("says Whole instance, then a name, a count or all, kept in ?scope=&ws=", async () => {
    show()
    const dialog = await openScope()
    fireEvent.click(await within(dialog).findByRole("checkbox", { name: "Show Dess" }))
    expect(param("scope")).toBe("workspaces")
    expect(param("ws")).toBe("dess")
    expect(scopeText()).toBe("Dess")
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Show Coolify" }))
    expect(param("ws")?.split(",").sort()).toEqual(["coolify", "dess"])
    expect(scopeText()).toBe("2 workspaces")
    expect(document.querySelector("button[aria-label=\"Back up now\"][title]")).toHaveAttribute("title", "Back up 2 workspaces now")
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Show Dess" }))
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Show Coolify" }))
    expect(param("scope")).toBe("instance")
    expect(scopeText()).toBe("Whole instance")
  })

  it("searches a long list and ticks everything found at once", async () => {
    show()
    const dialog = await openScope()
    fireEvent.change(within(dialog).getByRole("searchbox", { name: "Search workspaces" }), { target: { value: "o" } })
    fireEvent.click(within(dialog).getByRole("button", { name: /^Tick all \d+ found/ }))
    expect(param("scope")).toBe("workspaces")
    expect(param("ws")?.split(",").sort()).toEqual(["coolify", "pages-demo", "sandbox"])
  })

  it("Whole instance clears the ticks and closes the picker", async () => {
    show("?demo=1&scope=workspaces&ws=dess")
    const dialog = await openScope()
    fireEvent.click(within(dialog).getByRole("radio", { name: /Whole instance/ }))
    expect(param("scope")).toBe("instance")
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("Runs and a new restore follow it: no Scope menu of their own", async () => {
    show("?demo=1&section=history")
    await screen.findByRole("button", { name: /^Kind:/ })
    expect(screen.queryByRole("button", { name: /^Scope:/ })).toBeNull()
    cleanup()
    show("?demo=1&section=recovery")
    await screen.findByRole("group", { name: "Restore" })
    expect(screen.queryByRole("button", { name: /^Scope:/ })).toBeNull()
  })
})

describe("Runs, like the Audit log", () => {
  const chip = (name: RegExp) => screen.getByRole("button", { name })
  it("opens on result chips with counts, and the head says what is shown", async () => {
    show("?demo=1&section=history")
    await waitFor(() => expect(chip(/^All\s*6$/)).toBeInTheDocument())
    expect(chip(/^Failed\s*0$/)).toBeInTheDocument()
    expect(chip(/^Incomplete\s*2$/)).toBeInTheDocument()
    expect(chip(/^Pinned\s*1$/)).toBeInTheDocument()
    expect(screen.getByText("Whole instance · 6 runs in the last 30 d")).toBeInTheDocument()
  })

  it("a chip narrows the table and is kept in ?status=", async () => {
    show("?demo=1&section=history")
    await screen.findByRole("button", { name: /^Pinned\s*1$/ })
    fireEvent.click(chip(/^Pinned/))
    expect(param("status")).toBe("pinned")
    await waitFor(() => expect(document.querySelectorAll("tr[data-run]")).toHaveLength(1))
  })

  it("Kind, Proof and Plan pick above the table and in the URL", async () => {
    show("?demo=1&section=history")
    await screen.findByRole("button", { name: /^Kind:/ })
    await pick(/^Kind:/, /^Environments/)
    expect(param("kind")).toBe("environments")
    await waitFor(() => expect(document.querySelectorAll("tr[data-run]")).toHaveLength(1))
    await pick(/^Kind:/, /^Any kind/)
    await pick(/^Proof:/, /^Test restore/)
    expect(param("proof")).toBe("3")
    await waitFor(() => expect(document.querySelectorAll("tr[data-run]")).toHaveLength(2))
    await pick(/^Proof:/, /^Any proof/)
    await pick(/^Plan:/, /^Complete recovery/)
    await waitFor(() => expect(document.querySelectorAll("tr[data-run]")).toHaveLength(5))
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

  it("asks the server for the newest 500 runs and pages them 25 at a time, saying when 500 came back", async () => {
    const base = runsFixture(new Date())[0]
    const five = Array.from({ length: 500 }, (_, i) => ({ ...base, id: `r${i}`, bundle_path: `/b/${i}`, started_at: new Date(Date.now() - i * 3_600_000).toISOString() }))
    h.apiFetch.mockImplementation(async (url: string) => (String(url).includes("/backups/runs?") ? new Response(JSON.stringify({ data: five }), { status: 200 }) : new Response("{}", { status: 404 })))
    show("?section=history&period=all")
    await waitFor(() => expect(document.querySelectorAll("tr[data-run]")).toHaveLength(25))
    expect(h.apiFetch.mock.calls.some(([u]) => String(u).includes("/backups/runs?") && String(u).includes("limit=500"))).toBe(true)
    expect(document.querySelector("[data-slot=runs-cap]")).toHaveTextContent("the newest 500 runs")
    expect(screen.getByText("25 of 500 runs shown")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Show 25 more" }))
    await waitFor(() => expect(document.querySelectorAll("tr[data-run]")).toHaveLength(50))
  })

  it("says nothing about a cap when fewer came back", async () => {
    show("?demo=1&section=history")
    await screen.findByRole("button", { name: /^Kind:/ })
    expect(document.querySelector("[data-slot=runs-cap]")).toBeNull()
  })
})

describe("Coverage", () => {
  it("lists every workspace, least protected first, and a row opens its workspace page", async () => {
    show("?demo=1&section=coverage")
    await waitFor(() => expect(document.querySelectorAll("tr[data-coverage]").length).toBe(5))
    expect([...document.querySelectorAll("tr[data-coverage]")].map((r) => r.getAttribute("data-coverage"))).toEqual(["never", "stale", "covered", "covered", "none"])
    fireEvent.click(document.querySelector("tr[data-coverage=stale]")!)
    expect(param("workspace")).toBe("pages-demo")
    await waitFor(() => expect(document.querySelector("[data-slot=workspace-backups-header]")).not.toBeNull())
  })

  it("narrows to a status from the URL, and says how many of how many", async () => {
    show("?demo=1&section=coverage&status=never")
    await waitFor(() => expect(document.querySelectorAll("tr[data-coverage]")).toHaveLength(1))
    expect(screen.getByText("1 of 5 workspaces")).toBeInTheDocument()
  })

  it("the Overview's counts open Coverage narrowed to them", async () => {
    show()
    fireEvent.click(await screen.findByRole("button", { name: /1 never backed up/ }))
    expect(param("section")).toBe("coverage")
    expect(param("status")).toBe("never")
  })

  it("Back up now on a row backs up that workspace (demo sends nothing)", async () => {
    show("?demo=1&section=coverage")
    fireEvent.click(await screen.findByRole("button", { name: "Back up Unify Lab now" }))
    await waitFor(() => expect(h.toast.message).toHaveBeenCalledWith("Demo data · nothing was sent"))
  })
})

describe("Back up now", () => {
  const topBar = () => document.querySelector("button[aria-label=\"Back up now\"][title]") as HTMLElement
  const open = async (search = "?demo=1") => {
    show(search)
    await waitFor(() => expect(topBar()).not.toBeNull())
    fireEvent.click(topBar())
    return screen.findByRole("dialog")
  }

  it("says what, like which plan, the options the run accepts, and how many runs will start", async () => {
    const d = await open("?demo=1&scope=workspaces&ws=dess,coolify,pages-demo")
    expect(within(d).getByText("3 workspaces")).toBeInTheDocument()
    expect(within(d).getByRole("combobox", { name: "Like plan" })).toBeInTheDocument()
    for (const g of ["Contents", "Environments", "Encrypt to"]) expect(within(d).getByRole("group", { name: g })).toBeInTheDocument()
    expect(within(d).getByLabelText("Note (optional)")).toBeInTheDocument()
    expect(within(d).getByRole("status")).toHaveTextContent("3 runs will start, one per workspace.")
    expect(within(d).getByRole("button", { name: "Back up 3 workspaces" })).toBeInTheDocument()
  })

  it("a whole-instance backup is one run, and always a full backup", async () => {
    const d = await open()
    expect(within(d).getByRole("status")).toHaveTextContent("1 run will start.")
    expect(within(d).queryByRole("button", { name: "Only some kinds" })).toBeNull()
  })

  it("sends the choices as the server reads them: a passphrase, no recipients, a note", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      const u = String(url)
      if (u.endsWith("/backups/recipients")) return new Response(JSON.stringify({ data: [{ id: "rcp-1", name: "ops", public_key: "age1x", holder: "", created_at: "" }] }), { status: 200 })
      if (u.endsWith("/backups/run") && init?.method === "POST") return new Response(JSON.stringify({ id: "r1", run_id: "r1", run_ids: ["r1"], status: "running" }), { status: 202 })
      return new Response("{}", { status: 404 })
    })
    const d = await open("")
    fireEvent.click(within(d).getByRole("button", { name: "A passphrase" }))
    fireEvent.change(within(d).getByLabelText("Passphrase"), { target: { value: "s3cret" } })
    fireEvent.change(within(d).getByLabelText("Note (optional)"), { target: { value: " before the upgrade " } })
    fireEvent.click(within(d).getByRole("button", { name: "Back up now" }))
    await waitFor(() => expect(h.apiFetch.mock.calls.some(([u, i]) => String(u).endsWith("/backups/run") && (i as RequestInit)?.method === "POST")).toBe(true))
    const call = h.apiFetch.mock.calls.find(([u, i]) => String(u).endsWith("/backups/run") && (i as RequestInit)?.method === "POST")!
    const sent = JSON.parse((call[1] as { body: string }).body)
    expect(sent).toMatchObject({ scope: "instance", passphrase: "s3cret", note: "before the upgrade" })
    expect(sent).not.toHaveProperty("recipient_ids")
  })

  it("without a key it opens the setup instead", async () => {
    h.apiFetch.mockImplementation(async (url: string) => (/\/backups\/(recipients|plans)$/.test(String(url)) ? new Response(JSON.stringify({ data: [] }), { status: 200 }) : new Response("{}", { status: 404 })))
    show("")
    await screen.findByRole("button", { name: "Set up backups" })
    fireEvent.click(topBar())
    expect(await screen.findByRole("dialog")).toHaveTextContent("Set up backups")
    expect(screen.queryByRole("combobox", { name: "Like plan" })).toBeNull()
  })
})

describe("Restore", () => {
  it("switches New restore, History and Drills above the wizard, in the URL", async () => {
    show("?demo=1&section=recovery")
    fireEvent.click(within(await screen.findByRole("group", { name: "Restore" })).getByRole("button", { name: "Drills" }))
    expect(param("view")).toBe("drills")
  })
})
