import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import type { ProvisioningCrewState, ProvisioningStatus } from "@/hooks/use-provisioning-status"

// PAR-001: the top bar ends with the Activity and Inbox bells and nothing
// else (v67). The connection, the fleet and the container builds moved into
// the Activity bell's panel — its "Now" status summary — and stay reachable.

const h = vi.hoisted(() => ({
  engine: "connected" as string,
  ws: "connected" as string,
  crews: { total: 11, running: 3, error: 0, idle: 8, queued: 0 } as Record<string, number> | null,
  provisioning: null as unknown,
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => [] })) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: h.ws }) }))
vi.mock("@/hooks/use-engine-status", () => ({ useEngineStatus: () => ({ status: h.engine }) }))
vi.mock("@/hooks/use-crews-status", () => ({ useCrewsStatus: () => h.crews }))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => h.provisioning }))
vi.mock("@/hooks/use-access-mode", () => ({
  useAccessMode: () => "trusted",
  useTrustedWorkspaceId: () => "ws-1",
}))

import { ActivitySystemStatus } from "../activity-system-status"

function crew(o: Partial<ProvisioningCrewState> = {}): ProvisioningCrewState {
  return { id: "c1", slug: "quality", name: "Quality", status: "needs_provision", featureIds: [], ...o }
}
function prov(o: Partial<ProvisioningStatus> = {}): ProvisioningStatus {
  return { total: 0, building: 0, failed: 0, needsProvision: 0, pendingRestart: 0, recentlyCompleted: 0, detail: [], acknowledge: vi.fn(), ...o }
}

beforeEach(() => {
  h.engine = "connected"
  h.ws = "connected"
  h.crews = { total: 11, running: 3, error: 0, idle: 8, queued: 0 }
  h.provisioning = prov()
})
afterEach(cleanup)

describe("the Activity panel's status summary (PAR-001)", () => {
  it("states the connection and the fleet in one line", () => {
    render(<ActivitySystemStatus onNavigate={() => {}} />)
    const line = screen.getByTestId("activity-status-summary")
    expect(line).toHaveTextContent("Online")
    expect(line).toHaveTextContent("11 agents · 3 running · 8 idle")
  })

  it("says Offline, and drops the fleet, while the link is down", () => {
    h.engine = "offline"
    h.ws = "disconnected"
    render(<ActivitySystemStatus onNavigate={() => {}} />)
    const line = screen.getByTestId("activity-status-summary")
    expect(line).toHaveTextContent("Offline")
    expect(line).not.toHaveTextContent("running")
  })

  it("keeps the full system status one click away", () => {
    render(<ActivitySystemStatus onNavigate={() => {}} />)
    expect(screen.queryByTestId("system-status-panel")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /system status/i }))
    const panel = screen.getByTestId("system-status-panel")
    expect(within(panel).getByText("Engine")).toBeInTheDocument()
    expect(within(panel).getByText("Real-time")).toBeInTheDocument()
  })

  it("lists container builds that need attention, with their actions", () => {
    h.provisioning = prov({ needsProvision: 1, total: 1, detail: [crew()] })
    render(<ActivitySystemStatus onNavigate={() => {}} />)
    expect(screen.getByText("Container builds")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Quality" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /build/i })).toBeInTheDocument()
  })

  it("shows no builds section when nothing needs a build", () => {
    render(<ActivitySystemStatus onNavigate={() => {}} />)
    expect(screen.queryByText("Container builds")).toBeNull()
  })
})
