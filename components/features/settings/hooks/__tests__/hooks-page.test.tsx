import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"

/**
 * Settings › Lifecycle hooks as its own page (/settings/hooks): a map of the
 * run's steps, before / after, with each hook in its slot.
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

const map = () => waitFor(() => {
  const el = document.querySelector<HTMLElement>("[data-slot=hooks-map]")
  if (!el) throw new Error("no map")
  return el
})
const openAt = (id: string) => window.history.replaceState(null, "", `/settings/hooks?hook=${id}`)
const slot = (event: string) => document.querySelector<HTMLElement>(`[data-slot=hooks-map] [data-event="${event}"]`)!
const detail = () => waitFor(() => {
  const el = document.querySelector<HTMLElement>("[data-slot=hook-detail] > section")
  if (!el) throw new Error("no hook open")
  return el
})
const openHook = async (id: string) => {
  await map()
  fireEvent.click(await waitFor(() => {
    const el = document.querySelector<HTMLElement>(`[data-hook="${id}"]`)
    if (!el) throw new Error("no card")
    return el
  }))
  return detail()
}

describe("Hooks page — the lifecycle map", () => {
  it("puts each hook in the slot of its event, before or after its step", async () => {
    render(<HooksPage />)
    await map()
    await waitFor(() => expect(within(slot("pre_llm_call")).getByTitle(/gate\.sh/)).toBeInTheDocument())
    expect(within(slot("on_approval_requested")).getByTitle("hooks.slack.com", { exact: false })).toBeInTheDocument()
    expect(within(slot("pre_llm_call")).getByText("STOPS")).toBeInTheDocument()
    expect(screen.getByRole("row", { name: "Tool call" })).toHaveTextContent("Not possible — the tool already ran inside the agent.")
    const summary = document.querySelector("[data-slot=hooks-summary]")!
    expect(summary).toHaveTextContent("2 hooks")
    expect(summary).toHaveTextContent("1 can stop an action")
    expect(summary).toHaveTextContent("1 failed last time")
  })

  it("opens a hook with what it runs, its scope, that it stops the action, and its runs", async () => {
    render(<HooksPage />)
    const d = await openHook("hk-gate")
    expect(d).toHaveAccessibleName("Before every model call hook")
    const panel = document.querySelector<HTMLElement>("[data-slot=hook-detail]")!
    expect(within(panel).getByText("/usr/local/bin/gate.sh")).toBeInTheDocument()
    expect(within(panel).getByText("Finance")).toBeInTheDocument()
    expect(within(panel).getByText("STOPS THE ACTION")).toBeInTheDocument()
    expect(within(panel).getByText(/outside any container/)).toBeInTheDocument()
    expect(within(panel).getByText("exit status 1")).toBeInTheDocument()
  })

  it("toggles a hook with the enable/disable endpoints", async () => {
    render(<HooksPage />)
    await openHook("hk-slack")
    fireEvent.click(screen.getByRole("switch", { name: "Disable the on_approval_requested hook" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/hooks/hk-slack/disable?workspace_id=ws1")
  })

  it("lets an ADMIN edit a webhook but not a shell hook (OWNER only)", async () => {
    render(<HooksPage />)
    await openHook("hk-gate")
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
    expect(screen.queryByRole("button", { name: /delete hook/i })).toBeNull()
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
    cleanup()
    render(<HooksPage />)
    await openHook("hk-slack")
    expect(await screen.findByRole("button", { name: "Edit" })).toBeInTheDocument()
  })

  it("is read-only below ADMIN", async () => {
    h.role = "MEMBER"
    render(<HooksPage />)
    await map()
    expect(screen.queryByRole("button", { name: /new hook/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /^Add a hook/ })).toBeNull()
    expect(screen.getByText(/Read-only — admins add and change hooks/)).toBeInTheDocument()
    await openHook("hk-slack")
    expect(screen.getByRole("switch", { name: /the on_approval_requested hook/ })).toBeDisabled()
  })

  it("deletes after confirmation with DELETE", async () => {
    render(<HooksPage />)
    await openHook("hk-slack")
    fireEvent.click(screen.getByRole("button", { name: /delete hook/i }))
    fireEvent.click(await screen.findByRole("button", { name: /^Delete$/ }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/hooks/hk-slack?workspace_id=ws1")
    expect((writes()[0][1] as RequestInit).method).toBe("DELETE")
  })

  it("explains the map and offers a slot to start from when there are no hooks", async () => {
    hooks = []
    render(<HooksPage />)
    await map()
    expect(await screen.findByText(/No hooks yet/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Add a hook: Before every model call" })).toBeInTheDocument()
  })

  it("opens the hook a ?hook= link names", async () => {
    openAt("hk-gate")
    render(<HooksPage />)
    expect(await detail()).toHaveAccessibleName("Before every model call hook")
  })

  it("shows only the hooks that failed last time", async () => {
    render(<HooksPage />)
    await map()
    fireEvent.click(await screen.findByRole("button", { name: "Failed last time" }))
    await waitFor(() => expect(document.querySelectorAll("[data-hook]")).toHaveLength(1))
    expect(document.querySelector("[data-hook]")).toHaveAttribute("data-hook", "hk-gate")
  })

  it("lists the same hooks in run order", async () => {
    render(<HooksPage />)
    await map()
    fireEvent.click(screen.getByRole("button", { name: "List" }))
    const list = await waitFor(() => {
      const el = document.querySelector<HTMLElement>("[data-slot=hooks-list]")
      if (!el) throw new Error("no list")
      return el
    })
    expect(within(list).getAllByRole("button").map((b) => b.getAttribute("title"))).toEqual([
      "Before every model call · /usr/local/bin/gate.sh",
      "An agent waits for approval · https://hooks.slack.com/services/T/B/x",
    ])
  })
})

describe("New hook", () => {
  async function openEditor() {
    render(<HooksPage />)
    await map()
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

  it("starts from a slot with its event already chosen", async () => {
    render(<HooksPage />)
    await map()
    fireEvent.click(screen.getByRole("button", { name: "Add a hook: After every model call" }))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText("post_llm_call")).toBeInTheDocument()
    expect(within(dialog).queryByRole("radio", { name: "A guardrail trips" })).toBeNull()
    fireEvent.change(within(dialog).getByLabelText("URL"), { target: { value: "https://cost.example/llm" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Create hook" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String((writes()[0][1] as RequestInit).body)).event).toBe("post_llm_call")
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
  it("stacks the steps that have hooks first, the rest behind Show all", async () => {
    h.mobile = true
    render(<HooksPage />)
    const m = await map()
    await waitFor(() => expect(within(m).getAllByRole("region").map((r) => r.getAttribute("aria-label"))).toEqual(["Model call", "Policy alerts"]))
    fireEvent.click(within(m).getByRole("button", { name: /Show the 6 steps with no hooks/ }))
    expect(within(m).getAllByRole("region")).toHaveLength(8)
    fireEvent.click(within(m).getByTitle("hooks.slack.com", { exact: false }))
    expect(await detail()).toHaveAccessibleName("An agent waits for approval hook")
  })
})
