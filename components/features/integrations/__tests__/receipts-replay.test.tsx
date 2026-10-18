// INT-08: a trigger's Calls offer Replay where the backend can (the work item
// replay route) and say when a call is kept for replay or not. Outbound
// "Send again" has no route and is not drawn.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { RecentReceipts } from "../views/incoming-webhooks-view"
import type { IncomingEndpoint, IncomingTarget } from "../incoming-model"

vi.mock("@/lib/api-fetch", async (original) => ({ ...(await original<typeof import("@/lib/api-fetch")>()), apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtime: () => ({}) }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

const agent: IncomingTarget = { id: "a1", slug: "pepa", name: "Pepa", kind: "agent", crew_id: "c1", webhook_secret_set: true }
const routine: IncomingTarget = { id: "r1", slug: "review", name: "Review", kind: "routine" }
const row = (target: IncomingTarget) => ({ id: target.id, name: "ep", target, sender: "GitHub", signed: true, enabled: true, path: "" }) as unknown as IncomingEndpoint

const delivery = (id: string, over: Record<string, unknown> = {}) => ({
  id, received_at: "2026-10-17T10:00:00Z", work_id: `w-${id}`, filter_decision: "accepted", raw_body_available: true, raw_body_expires_at: "2026-10-24T10:00:00Z", ...over,
})
const workItem = {
  id: "w-d1", workspace_id: "ws", source: "webhook", source_ref: "d1", domain_kind: "assignment", state: "succeeded", input_sha256: "abc", target_revision: "r1", attempts: [], events: [],
  created_at: "", updated_at: "", terminal_at: "", generation: 1, attempt_count: 1, priority: 0, eligible_at: "", deadline_at: null, replay_of: null, replay_reason: "", agent_id: "", crew_id: "", session_id: "", class: "chat", authorized_by_user_id: "", domain_id: "", state_reason: "",
}
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status })

function mount(target: IncomingTarget) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RecentReceipts workspaceId="ws" target={target} rows={[row(target)]} />
    </QueryClientProvider>,
  )
}
beforeEach(() => {
  vi.mocked(apiFetch).mockImplementation(async (url: string | URL | Request, init?: RequestInit) => {
    const u = String(url)
    if (u.includes("/webhook-deliveries")) return json({ items: [delivery("d1"), delivery("d2", { raw_body_available: false, raw_body_expires_at: null }), delivery("d3", { work_id: null, filter_decision: "ignored", raw_body_available: false })] })
    if (u.endsWith("/work-items/w-d1/replay") && init?.method === "POST") return json({ ...workItem, id: "w-new" }, 201)
    if (u.endsWith("/work-items/w-d1")) return json(workItem)
    return json({ items: [] })
  })
})
afterEach(cleanup)

describe("Calls › replay", () => {
  it("says which calls are kept for replay and which payloads were dropped", async () => {
    mount(agent)
    const first = (await screen.findByTestId("receipt-d1")) as HTMLElement
    expect(first).toHaveTextContent(/Kept for replay until/)
    expect(within(first).getByRole("button", { name: "Replay" })).toBeEnabled()
    expect(screen.getByTestId("receipt-d2")).toHaveTextContent("Replay unavailable · payload dropped")
    expect(within(screen.getByTestId("receipt-d2")).queryByRole("button", { name: "Replay" })).toBeNull()
    expect(within(screen.getByTestId("receipt-d3")).queryByRole("button", { name: "Replay" })).toBeNull()
  })

  it("replays through the work item replay route after the confirm dialog", async () => {
    mount(agent)
    fireEvent.click(await screen.findByRole("button", { name: "Replay" }))
    const create = await screen.findByRole("button", { name: /Create new work/ })
    await waitFor(() => expect(create).toBeEnabled())
    fireEvent.click(create)
    await waitFor(() =>
      expect(vi.mocked(apiFetch)).toHaveBeenCalledWith("/api/v1/workspaces/ws/work-items/w-d1/replay", expect.objectContaining({ method: "POST" })),
    )
  })

  it("draws no replay or send-again for routine endpoints, which have no work item to replay", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => json({ items: [{ id: "x1", received_at: "2026-10-17T10:00:00Z", run_id: "run1", run_status: "completed" }] }))
    mount(routine)
    await screen.findByText("run1")
    expect(screen.queryByRole("button", { name: /Replay|Send again/ })).toBeNull()
    expect(screen.queryByText(/Kept for replay/)).toBeNull()
  })
})
