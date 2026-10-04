import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { SkillCardData } from "../skill-card"
const mock = vi.hoisted(() => ({ fetch: vi.fn(), changed: vi.fn(), close: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: mock.fetch }))
vi.mock("streamdown", () => ({ Streamdown: ({ children }: { children: string }) => <article>{children}</article> }))
import { SkillsDetailPanel } from "../skills-detail-panel"
const skill: SkillCardData = { id: "skill-a", name: "Compiler", slug: "compiler", display_name: null, description: null, version: null, author: null, category: "CODING", source: "BUNDLED", icon: null, downloads: null, featured: false }
const agents = [{ id: "a", name: "Alice", slug: "coder", crew_id: "c", crew: { id: "c", name: "Builders", slug: "builders" } }, { id: "b", name: "Bob", slug: "editor", crew_id: null }]
const crews = [{ id: "c", name: "Builders", slug: "builders", _count: { agents: 1 } }, { id: "d", name: "Writers", slug: "writers" }]
const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => { resolve = r }); return { promise, resolve } }
function setupFetch() {
  mock.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method) return reply({})
    if (url.startsWith("/api/v1/skills/")) return reply({ ...skill, content: "Build safe binaries" })
    if (url.startsWith("/api/v1/crews?")) return reply(crews)
    if (url.startsWith("/api/v1/agents?")) return reply(agents)
    if (url.includes("/agents/a/skills")) return reply([{ skill_id: skill.id }])
    if (url.includes("/agents/b/skills")) return reply([])
    throw new Error(`unexpected request ${url}`)
  })
}
function show(workspaceId: string | null = "ws-a", selected: SkillCardData | null = skill) {
  return render(<SkillsDetailPanel skill={selected} workspaceId={workspaceId} onChanged={mock.changed} onClose={mock.close} />)
}
async function install() {
  fireEvent.click(screen.getByRole("button", { name: "Install to agent…" }))
  await screen.findByRole("button", { name: "Alice Builders" })
}
async function uninstall() {
  fireEvent.click(screen.getByRole("button", { name: "Uninstall this skill from agents" }))
  await screen.findByRole("button", { name: "Alice Builders" })
}
beforeEach(() => { vi.clearAllMocks(); setupFetch() })
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks() })

it("shows the unselected placeholder without fetching", () => {
  show(null, null)
  expect(screen.getByText("Select a skill to see details")).toBeVisible()
  expect(mock.fetch).not.toHaveBeenCalled()
})
it("renders detail, flagged reason, full-page link, close and copy command", async () => {
  const write = vi.fn().mockResolvedValue(undefined)
  vi.spyOn(navigator, "clipboard", "get").mockReturnValue({ writeText: write } as unknown as Clipboard)
  show("ws-a", { ...skill, vendor: "Vendor", display_name: "Safe compiler", source: "UNKNOWN", scan_status: "FLAGGED", description_quality: "Review required" })
  await screen.findByText("Build safe binaries")
  expect(screen.getByRole("heading", { name: "Safe compiler" })).toBeVisible()
  expect(screen.getByText("Review required")).toBeVisible()
  expect(screen.getByRole("link", { name: "Vendor/compiler" })).toHaveAttribute("href", "/skills/skill-a")
  vi.useFakeTimers()
  fireEvent.click(screen.getByRole("button", { name: "Copy install command" }))
  await act(async () => {})
  expect(write).toHaveBeenCalledWith("crewship skill install Vendor/compiler")
  expect(screen.getByRole("button", { name: "Copy install command" }).querySelector(".lucide-check")).not.toBeNull()
  act(() => vi.advanceTimersByTime(1500))
  expect(screen.getByRole("button", { name: "Copy install command" }).querySelector(".lucide-copy")).not.toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Close skill detail" }))
  expect(mock.close).toHaveBeenCalledOnce()
})
it("falls back to list metadata and ignores an obsolete detail response", async () => {
  const old = deferred<Response>()
  mock.fetch.mockReturnValueOnce(old.promise)
  const view = show()
  const next = { ...skill, id: "next", name: "Next" }
  mock.fetch.mockResolvedValueOnce(reply({}, 503))
  view.rerender(<SkillsDetailPanel skill={next} workspaceId={null} />)
  await screen.findByText("No body content available for this skill.")
  await act(async () => old.resolve(reply({ ...skill, content: "Obsolete body" })))
  expect(screen.queryByText("Obsolete body")).toBeNull()
  expect(mock.fetch).toHaveBeenLastCalledWith("/api/v1/skills/next")
})
it("filters installation targets by name, slug and crew and submits only selected agents", async () => {
  show("ws & a")
  await install()
  const filter = screen.getByPlaceholderText("Filter by name, slug, or crew…")
  for (const term of ["Ali", "coder", "builders"]) {
    fireEvent.change(filter, { target: { value: term } })
    expect(screen.getByRole("button", { name: "Alice Builders" })).toBeVisible()
    expect(screen.queryByRole("button", { name: "Bob" })).toBeNull()
  }
  fireEvent.change(filter, { target: { value: "missing" } })
  expect(screen.getByText("No agents found.")).toBeVisible()
  fireEvent.change(filter, { target: { value: "" } })
  fireEvent.click(screen.getByRole("button", { name: "Bob" }))
  fireEvent.click(screen.getByRole("button", { name: "Bob" }))
  fireEvent.click(screen.getByRole("button", { name: "Alice Builders" }))
  fireEvent.click(screen.getByRole("button", { name: "Install (1)" }))
  await waitFor(() => expect(mock.changed).toHaveBeenCalledOnce())
  expect(mock.fetch).toHaveBeenCalledWith("/api/v1/agents/a/skills?workspace_id=ws%20%26%20a", expect.objectContaining({ method: "POST", body: JSON.stringify({ skill_id: "skill-a" }) }))
})

for (const operation of ["install", "uninstall", "assign"] as const) {
  it.each(["detail", "error", "raw", "empty", "unreadable", "network", "idempotent"])(`${operation} reports %s failures and treats idempotent responses as success`, async (kind) => {
    show()
    if (operation === "install") await install()
    else if (operation === "uninstall") await uninstall()
    else { fireEvent.click(screen.getByRole("button", { name: "Assign to crew…" })); await screen.findByRole("button", { name: "Builders 1 agents" }) }
    fireEvent.click(screen.getByRole("button", { name: operation === "assign" ? "Builders 1 agents" : "Alice Builders" }))
    const normal = mock.fetch.getMockImplementation()!
    mock.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (!init?.method) return normal(url, init)
      if (kind === "network") throw new Error("offline")
      if (kind === "idempotent") return reply({}, operation === "uninstall" ? 404 : 409)
      const body = kind === "detail" ? '{"detail":"Policy denied"}' : kind === "error" ? '{"error":"Policy denied"}' : kind === "raw" ? "Policy denied" : ""
      const res = new Response(body, { status: 503, statusText: "Unavailable" })
      if (kind === "unreadable") vi.spyOn(res, "text").mockRejectedValue(new Error("stream broken"))
      return res
    })
    fireEvent.click(screen.getByRole("button", { name: operation === "install" ? "Install (1)" : operation === "uninstall" ? "Uninstall (1)" : "Apply to crew" }))
    if (kind === "idempotent") {
      await waitFor(() => expect(mock.changed).toHaveBeenCalledOnce())
      expect(screen.queryByRole("dialog")).toBeNull()
    } else {
      await screen.findByText(kind === "network" && operation === "assign" ? "Failed to assign skill: offline" : /Failed for 1 of 1:/)
      expect(mock.changed).not.toHaveBeenCalled()
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
      expect(screen.queryByRole("dialog")).toBeNull()
    }
  })
}

it("uninstalls only matching assignments and deselects rows", async () => {
  show()
  await uninstall()
  expect(screen.queryByRole("button", { name: "Bob" })).toBeNull()
  const row = screen.getByRole("button", { name: "Alice Builders" })
  fireEvent.click(row); fireEvent.click(row)
  expect(screen.getByRole("button", { name: "Uninstall (0)" })).toBeDisabled()
  fireEvent.click(row)
  fireEvent.click(screen.getByRole("button", { name: "Uninstall (1)" }))
  await waitFor(() => expect(mock.changed).toHaveBeenCalledOnce())
  expect(mock.fetch).toHaveBeenCalledWith("/api/v1/agents/a/skills/skill-a?workspace_id=ws-a", { method: "DELETE" })
})

it.each(["install", "uninstall", "assign"])("shows failed and empty %s pickers", async (operation) => {
  show()
  await screen.findByText("Build safe binaries")
  mock.fetch.mockResolvedValueOnce(reply({}, 503))
  const trigger = operation === "install" ? "Install to agent…" : operation === "uninstall" ? "Uninstall this skill from agents" : "Assign to crew…"
  fireEvent.click(screen.getByRole("button", { name: trigger }))
  await screen.findByText(operation === "install" ? "Failed to load agents" : operation === "assign" ? "Failed to load crews" : "HTTP 503")
  fireEvent.click(screen.getByRole("button", { name: "Close" }))
  mock.fetch.mockResolvedValueOnce(reply(operation === "uninstall" ? [] : null))
  fireEvent.click(screen.getByRole("button", { name: trigger }))
  await screen.findByText(operation === "install" ? "No agents found." : operation === "assign" ? "No crews in this workspace." : "No agents currently have this skill installed.")
})

it.each(["empty", "failed"])("handles a crew with %s agent lookup without reporting success", async (kind) => {
  show()
  fireEvent.click(screen.getByRole("button", { name: "Assign to crew…" }))
  await screen.findByRole("button", { name: "Builders 1 agents" })
  fireEvent.click(screen.getByRole("button", { name: "Builders 1 agents" }))
  mock.fetch.mockResolvedValueOnce(reply(kind === "empty" ? [agents[1]] : {}, kind === "empty" ? 200 : 500))
  fireEvent.click(screen.getByRole("button", { name: "Apply to crew" }))
  await screen.findByText(kind === "empty" ? "Crew has no agents to install on." : "Failed to load crew agents")
  expect(mock.changed).not.toHaveBeenCalled()
})
