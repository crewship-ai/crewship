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
vi.mock("sonner", () => ({ toast: sonner }))

import { RealtimeToasts } from "../realtime-toasts"

const fire = (type: string, payload: Record<string, unknown>) => handlers[type]({ payload })

beforeEach(() => {
  for (const f of Object.values(sonner)) f.mockReset()
  nav.pathname = "/"
  nav.push.mockReset()
})
afterEach(() => { cleanup() })

describe("realtime toasts — event table", () => {
  it("a failed run names the agent, stays until closed and folds repeats into ×N", () => {
    render(<RealtimeToasts />)
    fire("run.failed", { agent_slug: "scout", error: "exit 1" })
    expect(sonner.error).toHaveBeenLastCalledWith("Scout couldn’t finish the run", expect.objectContaining({ id: "run-failed:scout", duration: Infinity, description: "exit 1" }))
    fire("run.failed", { agent_slug: "scout" })
    fire("run.failed", { agent_slug: "scout" })
    expect(sonner.error).toHaveBeenLastCalledWith("Scout couldn’t finish the run ×3", expect.objectContaining({ id: "run-failed:scout" }))
    // Closing it starts the count again.
    const { onDismiss } = sonner.error.mock.calls.at(-1)![1] as { onDismiss: () => void }
    onDismiss()
    fire("run.failed", { agent_slug: "scout" })
    expect(sonner.error).toHaveBeenLastCalledWith("Scout couldn’t finish the run", expect.anything())
  })

  it("a cancelled run is not a failure", () => {
    render(<RealtimeToasts />)
    fire("run.failed", { agent_slug: "scout", status: "CANCELLED" })
    fire("pipeline.run.failed", { pipeline_slug: "weekly-scan", status: "CANCELLED" })
    expect(sonner.error).not.toHaveBeenCalled()
  })

  it("a failed routine stays until closed", () => {
    render(<RealtimeToasts />)
    fire("pipeline.run.failed", { pipeline_slug: "weekly-scan" })
    expect(sonner.error).toHaveBeenCalledWith("Routine weekly-scan couldn’t finish", expect.objectContaining({ duration: Infinity, id: "routine-failed:weekly-scan" }))
  })

  it("an escalation points to the Inbox, and does not toast while on it", () => {
    render(<RealtimeToasts />)
    fire("escalation.created", { agent_slug: "scout" })
    expect(sonner.info).toHaveBeenCalledWith("Scout needs your decision", expect.objectContaining({ action: expect.objectContaining({ label: "Open" }) }))
    const { action } = sonner.info.mock.calls[0][1] as { action: { onClick: () => void } }
    action.onClick()
    expect(nav.push).toHaveBeenCalledWith("/inbox")
    expect(sonner.warning).not.toHaveBeenCalled()
    cleanup()

    sonner.info.mockReset()
    nav.pathname = "/inbox"
    render(<RealtimeToasts />)
    fire("escalation.created", { agent_slug: "scout" })
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
