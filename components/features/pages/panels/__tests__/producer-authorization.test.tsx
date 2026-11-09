/**
 * #5260: a panel whose agent producer the page's owner has not authorized
 * explains the state in plain words, says who can fix it, and gives only the
 * owner a working button. It never appears on a public page.
 */
import React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws1", workspace: null, workspaces: [], role: null, capabilities: null, loading: false, setWorkspaceId: () => {}, refresh: async () => {} }),
}))
vi.mock("@/hooks/use-active-routine-runs", () => ({
  useActiveRoutineRuns: () => ({ runs: [], activeCount: 0, awaitingApproval: 0, bySlug: new Map(), recentRuns: [], loading: false, error: null, refresh: () => {} }),
}))

import { PanelActionsProvider } from "@/components/features/pages/panels/panel-actions"
import { PanelRenderer } from "@/components/features/pages/panels/registry"
import { producerAuthorizeUrl } from "@/components/features/pages/panels/producer-authorization"
import { toPanelView } from "@/hooks/use-pages"
import type { PanelSpec } from "@/components/features/pages/panels/types"

const REASON =
  "Producer not authorized by the owner: agent \"writer\" may not push this panel's data, because the page's current owner did not name it as the producer."

function wire(canAuthorize: boolean) {
  return toPanelView({
    id: "sluzby",
    schema: "status.v1",
    title: "Services",
    producer: "agent/writer",
    state: "failed",
    reason: REASON,
    producer_authorization: { producer: "agent/writer", can_authorize: canAuthorize },
  })
}

function jsonResponse(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, headers: new Headers(), text: async () => JSON.stringify(body), json: async () => body } as unknown as Response
}

let mockFetch: ReturnType<typeof vi.fn>
let qc: QueryClient

beforeEach(() => {
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
})
afterEach(() => {
  vi.unstubAllGlobals()
  qc.clear()
})

function renderPanel(panel: PanelSpec, failure: string | null | undefined, opts: { publicView?: boolean; provider?: boolean } = {}) {
  const node = <PanelRenderer panel={panel} data={{ state: "failed", failure }} publicView={opts.publicView} />
  return render(
    <QueryClientProvider client={qc}>
      {opts.provider === false ? node : <PanelActionsProvider slug="fleet-201" workspaceId="ws1" actions={new Map()}>{node}</PanelActionsProvider>}
    </QueryClientProvider>,
  )
}

describe("producer authorization notice", () => {
  it("reads the wire field, and drops it on a sealed panel", () => {
    expect(wire(true).spec.producer_authorization).toEqual({ producer: "agent/writer", can_authorize: true })
    expect(toPanelView({ panel_id: "x", sealed: true, producer_authorization: { producer: "agent/writer", can_authorize: true } }).spec.producer_authorization).toBeNull()
    expect(toPanelView({ id: "y", schema: "status.v1" }).spec.producer_authorization).toBeNull()
  })

  it("tells a non-owner the state and who can fix it, with no button", () => {
    const view = wire(false)
    renderPanel(view.spec, view.snapshot.failure)
    expect(screen.getByText("Producer not authorized by the owner")).toBeTruthy()
    expect(screen.getByText(/Only the page's owner can authorize it; ask them to\./)).toBeTruthy()
    expect(screen.getByText("agent/writer")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Authorize producer" })).toBeNull()
  })

  it("gives the owner a button that posts the authorization and nothing else", async () => {
    mockFetch.mockResolvedValue(jsonResponse(200, { page: "fleet-201", panel: "sluzby", producer: "agent/writer", authorized: true, changed: true }))
    const view = wire(true)
    renderPanel(view.spec, view.snapshot.failure)
    expect(screen.getByText(/You own this page, so you can authorize it\./)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Authorize producer" }))
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    const [url, init] = mockFetch.mock.calls[0]
    expect(String(url)).toBe(producerAuthorizeUrl("fleet-201", "sluzby", "ws1"))
    expect(String(url)).toBe("/api/v1/pages/fleet-201/panels/sluzby/producer/authorize?workspace_id=ws1")
    expect((init as RequestInit).method).toBe("POST")
  })

  it("shows the server's refusal verbatim", async () => {
    mockFetch.mockResolvedValue(jsonResponse(403, { error: "only the artifact's owner can authorize an agent as a panel's producer" }))
    const view = wire(true)
    renderPanel(view.spec, view.snapshot.failure)
    fireEvent.click(screen.getByRole("button", { name: "Authorize producer" }))
    expect(await screen.findByRole("alert")).toBeTruthy()
    expect(screen.getByRole("alert").textContent).toContain("only the artifact's owner can authorize")
  })

  it("draws nothing on a public page, and no button outside the page grid", () => {
    const view = wire(true)
    const { container, unmount } = renderPanel(view.spec, view.snapshot.failure, { publicView: true })
    expect(container.querySelector('[data-slot="panel-producer-authorization"]')).toBeNull()
    expect(screen.queryByText(REASON)).toBeNull()
    unmount()
    renderPanel(view.spec, view.snapshot.failure, { provider: false })
    expect(screen.getByText("Producer not authorized by the owner")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Authorize producer" })).toBeNull()
  })
})
