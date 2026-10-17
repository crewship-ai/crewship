import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ search: "" }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), prefetch: vi.fn(), back: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(h.search),
  usePathname: () => "/crews",
}))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => ({ detail: [] }) }))
vi.mock("@/hooks/use-credential-readiness", () => ({ useCredentialReadiness: () => ({ gapsByCredential: new Map() }) }))
vi.mock("@/hooks/use-user-preference", () => ({ useUserPreference: (_k: string, d: unknown) => [d, vi.fn(), { ready: true }] }))
vi.mock("@/components/features/crews/crew-canvas", () => ({ CrewCanvas: ({ crewSlug }: { crewSlug: string }) => <p data-testid="crew-canvas">{crewSlug}</p> }))
vi.mock("@/components/features/crews/agent-canvas", () => ({ AgentCanvas: () => <p>agent</p> }))
vi.mock("@/components/features/crews/bottom-panel", () => ({ BottomPanel: () => null }))
vi.mock("../create-crew-dialog", () => ({ CreateCrewDialog: () => null }))
vi.mock("../create-agent", () => ({ CreateAgentDialog: () => null }))

import { CrewsLayout } from "@/components/features/crews/crews-layout"

const crews = [{ id: "c1", name: "Ops", slug: "ops", description: null, color: null, icon: null, created_at: "", _count: { agents: 0, members: 0 } }]

function setWidth(w: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: w })
  window.matchMedia = ((q: string) => ({
    matches: /max-width: (\d+)px/.test(q) ? w <= Number(/max-width: (\d+)px/.exec(q)![1]) : false,
    media: q, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, onchange: null, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

function renderLayout() {
  return render(<CrewsLayout crews={crews} agents={[]} missions={[]} workspaceId="ws" loaded onRefresh={vi.fn()} />)
}

beforeEach(() => {
  h.search = ""
  window.history.replaceState(null, "", "/crews")
})
afterEach(cleanup)

describe("/crews without a selection", () => {
  it("opens the first crew on a desktop", async () => {
    setWidth(1440)
    renderLayout()
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("crew")).toBe("ops"))
  })

  it("shows the list on a phone, and does not open a crew before the width is known", async () => {
    setWidth(390)
    const view = renderLayout()
    await waitFor(() => expect(view.container.querySelector("[data-slot=secondary-panel][data-phone=inline]")).not.toBeNull())
    expect(new URLSearchParams(window.location.search).has("crew")).toBe(false)
    expect(view.container.querySelector("[data-crew-row=ops]")).not.toBeNull()
  })
})
