import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

// Harbor 2 G16: realtime events → which toast, for how long. A failure stays
// until it is closed, repeats fold into one toast, an escalation does not
// toast while the Inbox is on screen, and a mission start does not toast.

const handlers: Record<string, (e: { payload: Record<string, unknown> }) => void> = {}
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (type: string, cb: (e: { payload: Record<string, unknown> }) => void) => { handlers[type] = cb },
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-current" }) }))
const nav = vi.hoisted(() => ({ pathname: "/", push: vi.fn() }))
vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
  useRouter: () => ({ push: nav.push, replace: vi.fn() }),
}))
const sonner = vi.hoisted(() => ({ error: vi.fn(), warning: vi.fn(), success: vi.fn(), info: vi.fn() }))
// The routine's name is read from the server; these tests use the slug's words.
vi.mock("@/lib/api-fetch", () => ({ apiFetch: async () => ({ ok: false, json: async () => ({}) }) }))
vi.mock("sonner", () => ({ toast: sonner }))
const viewing = vi.hoisted(() => new Set<string>())
vi.mock("@/lib/chat-viewing", () => ({ isViewingAgentChat: (id: string) => viewing.has(id) }))

import { RealtimeToasts } from "../realtime-toasts"

const fire = (type: string, payload: Record<string, unknown>) => handlers[type]({ payload })

beforeEach(() => {
  for (const f of Object.values(sonner)) f.mockReset()
  nav.pathname = "/"
  nav.push.mockReset()
  viewing.clear()
})
afterEach(() => { cleanup() })

describe("realtime toasts — event table", () => {
  // Payloads exactly as the server broadcasts them (internal/api/internal_runs.go:650).
  const failed = (extra: Record<string, unknown> = {}) => ({ run_id: "run-1", agent_id: "cmwmhev0x0001", agent_name: "Scout", status: "FAILED", ...extra })

  it("a failed run names the agent, stays until closed and folds repeats into ×N", () => {
    render(<RealtimeToasts />)
    fire("run.failed", failed())
    expect(sonner.error).toHaveBeenLastCalledWith("Scout couldn’t finish the run", expect.objectContaining({ id: "run-failed:cmwmhev0x0001", duration: Infinity }))
    fire("run.failed", failed({ run_id: "run-2" }))
    fire("run.failed", failed({ run_id: "run-3" }))
    expect(sonner.error).toHaveBeenLastCalledWith("Scout couldn’t finish the run ×3", expect.objectContaining({ id: "run-failed:cmwmhev0x0001" }))
    // Closing it starts the count again.
    const { onDismiss } = sonner.error.mock.calls.at(-1)![1] as { onDismiss: () => void }
    onDismiss()
    fire("run.failed", failed())
    expect(sonner.error).toHaveBeenLastCalledWith("Scout couldn’t finish the run", expect.anything())
  })

  it("never shows an agent ID or a raw error to the reader", () => {
    render(<RealtimeToasts />)
    fire("run.failed", { run_id: "run-1", agent_id: "cmwmhev0x0001", agent_name: "", status: "FAILED", error: "exec: open /var/lib/x: permission denied" })
    const [title, options] = sonner.error.mock.calls.at(-1)! as [string, { description: string }]
    expect(title).toBe("An agent couldn’t finish the run")
    expect(`${title} ${options.description}`).not.toMatch(/cmwmhev|permission denied|\/var\//i)
    expect(options.description).toMatch(/Activity/)
  })

  it("a timed-out run says so plainly", () => {
    render(<RealtimeToasts />)
    fire("run.failed", failed({ status: "TIMEOUT" }))
    expect(sonner.error).toHaveBeenLastCalledWith("Scout ran out of time", expect.objectContaining({ duration: Infinity }))
  })

  it("a finished run does not toast while its agent's chat is open, and names the agent elsewhere", () => {
    nav.pathname = "/chat/scout"
    viewing.add("cmwmhev0x0001")
    render(<RealtimeToasts />)
    fire("run.completed", { run_id: "run-1", agent_id: "cmwmhev0x0001", agent_name: "Scout", status: "COMPLETED" })
    expect(sonner.success).not.toHaveBeenCalled()
    viewing.delete("cmwmhev0x0001")
    fire("run.completed", { run_id: "run-2", agent_id: "cmwmhev0x0001", agent_name: "Scout", status: "COMPLETED" })
    expect(sonner.success).toHaveBeenLastCalledWith("Scout finished a run", expect.objectContaining({ duration: 4000 }))
    const titles = sonner.success.mock.calls.map((c) => String(c[0])).join(" ")
    expect(titles).not.toMatch(/@|Run completed/)
  })

  it("a cancelled run is not a failure", () => {
    render(<RealtimeToasts />)
    fire("run.failed", { run_id: "r", agent_id: "a1", agent_name: "Scout", status: "CANCELLED" })
    fire("pipeline.run.failed", { pipeline_slug: "weekly-scan", status: "CANCELLED" })
    expect(sonner.error).not.toHaveBeenCalled()
  })

  it("a failed routine stays until closed", () => {
    render(<RealtimeToasts />)
    fire("pipeline.run.failed", { pipeline_slug: "weekly-scan" })
    expect(sonner.error).toHaveBeenCalledWith("Routine Weekly scan couldn’t finish", expect.objectContaining({ duration: Infinity, id: "routine-failed:weekly-scan" }))
  })

  it("an escalation points to the Inbox, and does not toast while on it", () => {
    render(<RealtimeToasts />)
    // Server payload: internal/api/escalation_handler.go:487.
    fire("escalation.created", { id: "e1", crew_id: "c1", from_slug: "scout", reason: "Approve the refund?" })
    expect(sonner.info).toHaveBeenCalledWith("Scout needs your decision", expect.objectContaining({ action: expect.objectContaining({ label: "Open" }) }))
    const { action } = sonner.info.mock.calls[0][1] as { action: { onClick: () => void } }
    action.onClick()
    expect(nav.push).toHaveBeenCalledWith("/inbox")
    expect(sonner.warning).not.toHaveBeenCalled()
    cleanup()

    sonner.info.mockReset()
    nav.pathname = "/inbox"
    render(<RealtimeToasts />)
    fire("escalation.created", { id: "e1", crew_id: "c1", from_slug: "scout" })
    expect(sonner.info).not.toHaveBeenCalled()
  })

  it("a mission start does not toast; its outcome does", () => {
    render(<RealtimeToasts />)
    fire("mission.updated", { status: "STARTED", title: "Q3 audit" })
    expect(sonner.info).not.toHaveBeenCalled()
    fire("mission.updated", { status: "FAILED", title: "Q3 audit" })
    expect(sonner.error).toHaveBeenCalledWith("Mission failed", expect.objectContaining({ duration: Infinity }))
  })
})
