import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"

/**
 * Settings › Lifecycle hooks as its own page (/settings/hooks).
 *
 * What it must keep true:
 *   - every member can READ the registry; every write is ADMIN+, and a shell
 *     handler OWNER only (internal/api/hooks_write.go validateHandlerKind) —
 *     so the page never offers a control the server would refuse;
 *   - the "subagent" handler is never wired in this build, so it is shown but
 *     not selectable;
 *   - toggling, creating and deleting hit the same endpoints the CLI does.
 */

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast }))
const h = vi.hoisted(() => ({ role: "ADMIN" as string | null, mobile: false }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws1", role: h.role, loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings/hooks",
}))

import { HooksPage } from "../hooks-page"

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.releasePointerCapture = vi.fn()
})

const HOOKS = [
  { id: "hk-slack", workspace_id: "ws1", event: "on_approval_requested", handler_kind: "http", handler_config: { url: "https://hooks.slack.com/services/T/B/x" }, enabled: true, blocking: false, created_at: "2026-10-12T10:00:00Z", updated_at: "2026-10-12T10:00:00Z" },
  { id: "hk-gate", workspace_id: "ws1", crew_id: "c-fin", event: "pre_llm_call", handler_kind: "shell", handler_config: { command: "/usr/local/bin/gate.sh" }, enabled: true, blocking: true, created_at: "2026-10-12T10:00:00Z", updated_at: "2026-10-12T10:00:00Z" },
]
const JOURNAL = { entries: [
  { ts: new Date(Date.now() - 120_000).toISOString(), entry_type: "hook.fired", payload: { hook_id: "hk-slack", outcome: "pass", latency_ms: 184 } },
  { ts: new Date(Date.now() - 3_600_000).toISOString(), entry_type: "hook.fired", payload: { hook_id: "hk-gate", outcome: "error", message: "exit status 1" } },
] }
const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })
let hooks: unknown[] = HOOKS
const writes = () => api.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method)

beforeEach(() => {
  api.mockReset(); toast.success.mockReset(); toast.error.mockReset()
  hooks = HOOKS
  h.role = "ADMIN"; h.mobile = false
  window.history.replaceState(null, "", "/settings/hooks")
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method) return ok({ id: "hk-new" })
    if (url.startsWith("/api/v1/hooks")) return ok({ count: hooks.length, rows: hooks })
    if (url.startsWith("/api/v1/journal")) return ok(JOURNAL)
    if (url.startsWith("/api/v1/crews")) return ok([{ id: "c-fin", name: "Finance", slug: "finance" }])
    return ok({})
  })
})
afterEach(() => cleanup())

/** The open hook's profile card, e.g. "Before every model call hook". */
const detail = () => waitFor(() => {
  const el = document.querySelector<HTMLElement>("[data-slot=hook-detail] > section")
  if (!el) throw new Error("no hook open")
  return el
})
const openAt = (id: string) => window.history.replaceState(null, "", `/settings/hooks?hook=${id}`)
const rows = () => Array.from(document.querySelectorAll<HTMLElement>("nav button[title]"))

describe("Hooks page", () => {
  it("lists hooks by what they react to, and opens the first with its last run", async () => {
    render(<HooksPage />)
    // Grouped in event order: model calls before policy alerts.
    expect(await detail()).toHaveAccessibleName("Before every model call hook")
    expect(rows().map((r) => r.getAttribute("title"))).toEqual([
      "Before every model call · /usr/local/bin/gate.sh",
      "An agent waits for approval · https://hooks.slack.com/services/T/B/x",
    ])
    fireEvent.click(rows()[1])
    await waitFor(async () => expect(await detail()).toHaveAccessibleName("An agent waits for approval hook"))
    expect(screen.getByText(/pass · 2m ago/)).toBeInTheDocument()
    const summary = document.querySelector("[data-slot=hooks-summary]")!
    expect(summary).toHaveTextContent("2 hooks")
    expect(summary).toHaveTextContent("1 can block an action")
    expect(summary).toHaveTextContent("1 failed last time")
  })

  it("shows a shell hook's command, scope and that it can block, with the host warning", async () => {
    render(<HooksPage />)
    const d = await detail()
    expect(d).toHaveAccessibleName("Before every model call hook")
    expect(screen.getAllByText("/usr/local/bin/gate.sh").length).toBeGreaterThan(0)
    expect(within(d).getAllByText("Finance").length).toBeGreaterThan(0)
    expect(within(d).getByText("Can block")).toBeInTheDocument()
    expect(screen.getByText(/outside any container/)).toBeInTheDocument()
    expect(screen.getByText("exit status 1")).toBeInTheDocument()
  })

  it("toggles a hook with the enable/disable endpoints", async () => {
    openAt("hk-slack")
    render(<HooksPage />)
    await detail()
    fireEvent.click(screen.getByRole("switch", { name: "Disable the on_approval_requested hook" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/hooks/hk-slack/disable?workspace_id=ws1")
  })

  it("lets an ADMIN edit a webhook but not a shell hook (OWNER only)", async () => {
    render(<HooksPage />)
    expect(await detail()).toHaveAccessibleName("Before every model call hook")
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
    expect(screen.queryByRole("button", { name: /delete hook/i })).toBeNull()
    fireEvent.click(rows()[1])
    expect(await screen.findByRole("button", { name: "Edit" })).toBeInTheDocument()
  })

  it("is read-only below ADMIN", async () => {
    h.role = "MEMBER"
    openAt("hk-slack")
    render(<HooksPage />)
    await detail()
    expect(screen.queryByRole("button", { name: /new hook/i })).toBeNull()
    expect(screen.getByRole("switch", { name: /the on_approval_requested hook/ })).toBeDisabled()
    expect(screen.getByText(/Read-only — admins add and change hooks/)).toBeInTheDocument()
  })

  it("deletes after confirmation with DELETE", async () => {
    openAt("hk-slack")
    render(<HooksPage />)
    await detail()
    fireEvent.click(screen.getByRole("button", { name: /delete hook/i }))
    fireEvent.click(await screen.findByRole("button", { name: /^Delete$/ }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/hooks/hk-slack?workspace_id=ws1")
    expect((writes()[0][1] as RequestInit).method).toBe("DELETE")
  })

  it("explains hooks and offers a first one when there are none", async () => {
    hooks = []
    render(<HooksPage />)
    expect(await screen.findByRole("region", { name: "No hooks yet" })).toBeInTheDocument()
    expect(screen.getAllByRole("button", { name: /new hook/i }).length).toBeGreaterThan(0)
  })
})

describe("New hook", () => {
  async function openEditor() {
    render(<HooksPage />)
    await detail()
    fireEvent.click(screen.getByRole("button", { name: /new hook/i }))
    return screen.findByRole("dialog")
  }

  it("creates a webhook with the event, URL and scope", async () => {
    const dialog = await openEditor()
    fireEvent.click(within(dialog).getByRole("radio", { name: "A guardrail trips" }))
    fireEvent.change(within(dialog).getByLabelText("URL"), { target: { value: "https://pager.example/hook" } })
    fireEvent.change(within(dialog).getByLabelText("Scope"), { target: { value: "c-fin" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "critical" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Create hook" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(url).toBe("/api/v1/hooks?workspace_id=ws1")
    expect(JSON.parse(String(init.body))).toEqual({
      event: "on_guardrail_triggered", handler_kind: "http", handler_config: { url: "https://pager.example/hook" },
      crew_id: "c-fin", matcher: { severities: ["critical"] }, blocking: false, enabled: true,
    })
  })

  it("offers blocking only on an event that can block", async () => {
    const dialog = await openEditor()
    fireEvent.click(within(dialog).getByRole("radio", { name: "After an agent stops" }))
    expect(within(dialog).queryByRole("switch", { name: "Block the action" })).toBeNull()
    fireEvent.click(within(dialog).getByRole("radio", { name: "Before every model call" }))
    expect(within(dialog).getByRole("switch", { name: "Block the action" })).toBeInTheDocument()
  })

  it("keeps Shell for the OWNER and never offers the unwired agent handler", async () => {
    const dialog = await openEditor()
    expect(within(dialog).getByRole("radio", { name: "Run a command on the server" })).toBeDisabled()
    expect(within(dialog).getByRole("radio", { name: "Hand to an agent" })).toBeDisabled()
    cleanup()
    h.role = "OWNER"
    const owner = await openEditor()
    expect(within(owner).getByRole("radio", { name: "Run a command on the server" })).toBeEnabled()
    expect(within(owner).getByRole("radio", { name: "Hand to an agent" })).toBeDisabled()
  })

  it("says what is missing instead of sending a half-filled hook", async () => {
    const dialog = await openEditor()
    fireEvent.click(within(dialog).getByRole("button", { name: "Create hook" }))
    expect(await within(dialog).findByText("Pick when the hook runs.")).toBeInTheDocument()
    expect(writes()).toHaveLength(0)
  })
})

describe("On a phone", () => {
  it("puts New hook in the page and picks the hook from a list", async () => {
    h.mobile = true
    render(<HooksPage />)
    await detail()
    expect(document.querySelector("[data-slot=hooks-actions]")).toContainElement(screen.getByRole("button", { name: /new hook/i }))
    fireEvent.change(screen.getByRole("combobox", { name: "Hook" }), { target: { value: "hk-slack" } })
    await waitFor(async () => expect(await detail()).toHaveAccessibleName("An agent waits for approval hook"))
  })
})
