import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup, waitFor, fireEvent, screen } from "@testing-library/react"

// "Open terminal" on a phone did nothing: the dock that shows Terminal and
// Files was never mounted below the phone breakpoint, while the button that
// asks for it stayed on the Environment tab (#5621).

const h = vi.hoisted(() => ({ search: "" }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), prefetch: vi.fn(), back: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(h.search),
  usePathname: () => "/crews",
}))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => ({ detail: [] }) }))
vi.mock("@/hooks/use-credential-readiness", () => ({ useCredentialReadiness: () => ({ gapsByCredential: new Map() }) }))
vi.mock("@/hooks/use-user-preference", () => ({ useUserPreference: (_k: string, d: unknown) => [d, vi.fn(), { ready: true }] }))
vi.mock("@/components/features/crews/crew-canvas", () => ({
  CrewCanvas: ({ onOpenTerminal, onOpenFiles }: { onOpenTerminal: () => void; onOpenFiles?: () => void }) => (
    <div data-testid="crew-canvas">
      <button type="button" onClick={onOpenTerminal}>Open terminal</button>
      <button type="button" onClick={onOpenFiles}>Open files</button>
    </div>
  ),
}))
vi.mock("@/components/features/crews/agent-canvas", () => ({ AgentCanvas: () => <p>agent</p> }))
vi.mock("@/components/features/crews/bottom-panel", () => ({
  BottomPanel: ({ request, initialOpen, onOpenChange }: { request: { tab: string } | null; initialOpen?: boolean; onOpenChange?: (open: boolean) => void }) => (
    <div data-testid="dock" data-tab={request?.tab ?? ""} data-initial-open={String(!!initialOpen)}>
      <button type="button" onClick={() => onOpenChange?.(false)}>close-dock</button>
    </div>
  ),
}))
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

function openCrew() {
  h.search = "crew=ops"
  window.history.replaceState(null, "", "/crews?crew=ops")
  return render(<CrewsLayout crews={crews} agents={[]} missions={[]} workspaceId="ws" loaded onRefresh={vi.fn()} />)
}

beforeEach(() => { h.search = "" })
afterEach(cleanup)

describe("crew Terminal and Files dock", () => {
  it("is always there on a desktop", async () => {
    setWidth(1440)
    openCrew()
    await waitFor(() => expect(screen.getByTestId("crew-canvas")).toBeTruthy())
    expect(screen.getByTestId("dock")).toBeTruthy()
  })

  it("is absent on a phone until Terminal is asked for, then opens on that tab", async () => {
    setWidth(390)
    openCrew()
    await waitFor(() => expect(screen.getByTestId("crew-canvas")).toBeTruthy())
    expect(screen.queryByTestId("dock")).toBeNull()

    fireEvent.click(screen.getByText("Open terminal"))
    const dock = await screen.findByTestId("dock")
    expect(dock.getAttribute("data-tab")).toBe("terminal")
    // Mounted open: a closed strip would look like nothing happened.
    expect(dock.getAttribute("data-initial-open")).toBe("true")
  })

  it("leaves again on a phone when it is closed, and Files opens it too", async () => {
    setWidth(390)
    openCrew()
    await waitFor(() => expect(screen.getByTestId("crew-canvas")).toBeTruthy())
    fireEvent.click(screen.getByText("Open files"))
    expect((await screen.findByTestId("dock")).getAttribute("data-tab")).toBe("files")
    fireEvent.click(screen.getByText("close-dock"))
    await waitFor(() => expect(screen.queryByTestId("dock")).toBeNull())
  })
})
