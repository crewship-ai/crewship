// Admin › Security as an instance admin uses it: Posture, Keeper and Keeper
// reviews on one page. The settings cards are the existing ones and have their
// own tests; here they are stubs, and what is pinned is where each appears,
// what the Overview says, and that the activity list holds every kind of row.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-a", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/app/(dashboard)/admin/hooks/use-admin-websocket", () => ({
  useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "connected" }),
}))
vi.mock("@/components/features/admin/keeper-judge-card", () => ({ KeeperJudgeCard: () => <div data-testid="card-judge" /> }))
vi.mock("@/components/features/admin/keeper-profile-card", () => ({ KeeperProfileCard: () => <div data-testid="card-rules" /> }))
vi.mock("@/components/features/admin/judge-models-card", () => ({ JudgeModelsCard: () => <div data-testid="card-background" /> }))
vi.mock("@/components/features/admin/keeper-health-card", () => ({ KeeperHealthCard: () => <div data-testid="card-health" /> }))
vi.mock("@/components/features/admin/keeper-governance-panel", () => ({
  KeeperGovernancePanel: ({ section }: { section: string }) => <div data-testid={`gov-${section}`} />,
}))

import { SecurityPage } from "../security-page"

const STATUS = { enabled: true, ollama_url: "http://x:11434", model: "qwen2.5:7b", ollama_online: true, gatekeeper_configured: true, total_requests: 4, allow_count: 3, deny_count: 1, escalate_count: 0 }
const POSTURE = {
  environment: "", encryption_key_configured: true, plaintext_secrets_allowed: false, private_endpoints_ceiling: false,
  signup_open: false, oauth_configured: false, email_configured: false, rate_limit_disabled: false, rate_limit_effectively_disabled: false,
  warnings: [{ key: "no_backup_recorded", severity: "medium", message: "No backup has ever been recorded on this instance. The database holds every credential." }],
}
const row = (id: string, request_type: string, decision: string, extra: Record<string, unknown> = {}) => ({
  id, request_type, decision, agent_id: "a1", agent_name: "Hlídač", crew_id: "c1", credential_id: request_type === "access" || request_type === "execute" ? "cr1" : "",
  credential_name: request_type === "access" || request_type === "execute" ? "POSTGRES_PASSWORD" : "", intent: "check the database", command: null,
  reason: "Bound credential. Low risk.", risk_score: 2, exit_code: null, ollama_prompt: "PROMPT key sk-ant-abcdefghijklmnopqrstuvwxyz0123", ollama_raw_response: "{\"decision\":\"ALLOW\"}",
  created_at: "2026-09-29T10:00:00Z", decided_at: null, ...extra,
})
const ENTRIES = [row("k1", "access", "ALLOW"), row("k2", "execute", "DENY"), row("k3", "behavior", "ALLOW", { reason: "Reads look routine." })]

const res = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })

beforeEach(() => {
  cleanup()
  window.history.replaceState(null, "", "/admin/security")
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) => {
    const url = String(u)
    if (url.startsWith("/api/v1/system/keeper")) return res(STATUS)
    if (url.startsWith("/api/v1/admin/security-posture")) return res(POSTURE)
    if (url.startsWith("/api/v1/admin/keeper/requests")) return res(ENTRIES)
    return res({})
  })
})

describe("Overview", () => {
  it("says in one line whether Keeper and its judge work", async () => {
    render(<SecurityPage />)
    const line = await screen.findByText(/Judge answering · qwen2.5:7b/)
    expect(line.closest("[data-slot=security-summary]")).toHaveTextContent("Keeper on")
  })

  it("lists what needs attention with a way to act on it", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Needs attention" })
    expect(within(card).getByText("No backup recorded")).toBeInTheDocument()
    expect(within(card).getByRole("link", { name: /Create a backup/ })).toHaveAttribute("href", "/admin?tab=backups")
  })

  it("shows the server's setup read-only, an unset value named as such", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Server setup" })
    expect(within(card).getByText("(unset)")).toBeInTheDocument()
    expect(within(card).getByText("refused")).toBeInTheDocument()
    expect(within(card).queryByRole("button")).toBeNull()
  })

  it("says Keeper is off, and what that costs, above everything else", async () => {
    h.apiFetch.mockImplementation(async (u: string) => {
      const url = String(u)
      if (url.startsWith("/api/v1/system/keeper")) return res({ ...STATUS, enabled: false })
      if (url.startsWith("/api/v1/admin/security-posture")) return res(POSTURE)
      return res([])
    })
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Needs attention" })
    const first = card.querySelector("[data-finding]")
    expect(first).toHaveAttribute("data-finding", "keeper_off")
    expect(within(card).getByRole("link", { name: /Turn it on/ })).toHaveAttribute("href", "/admin/security?section=judge")
  })
})

describe("Activity", () => {
  it("holds credential requests and reviews in one list, narrowed from the panel", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Behavior/ }))
    const table = await screen.findByRole("region", { name: "Activity" })
    expect(within(table).getAllByRole("row")).toHaveLength(2)
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("Behavior")
    expect(window.location.search).toContain("stream=behavior")
  })

  it("filters by decision and opens a record with secrets redacted", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Credential requests/ }))
    const table = await screen.findByRole("region", { name: "Activity" })
    fireEvent.click(screen.getByRole("button", { name: "Deny" }))
    expect(within(table).getAllByRole("row")).toHaveLength(2)
    fireEvent.click(within(table).getByText("POSTGRES_PASSWORD"))
    const prompt = await screen.findByRole("region", { name: "Judge prompt" })
    expect(prompt).not.toHaveTextContent("abcdefghijklmnopqrstuvwxyz0123")
    expect(prompt).toHaveTextContent("REDACTED")
  })
})

describe("Settings", () => {
  it.each([
    ["Credential judge", "card-judge", "Instance"],
    ["Decision rules", "card-rules", "Instance"],
    ["This workspace", "gov-judge", "Workspace"],
    ["Background checks", "card-background", "Instance"],
    ["Watchdog", "gov-watchdog", "Workspace"],
    ["Alerts & approvals", "gov-alerts", "Workspace"],
    ["Credential leases", "gov-leases", "Workspace"],
  ])("%s shows its own card and says how far it reaches", async (label, testId, scope) => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${label}`) }))
    await waitFor(() => expect(screen.getByTestId(testId)).toBeInTheDocument())
    expect(document.querySelector("[data-slot=security-scope]")).toHaveTextContent(scope)
  })

  it("opens where the link points", async () => {
    window.history.replaceState(null, "", "/admin/security?section=watchdog")
    render(<SecurityPage />)
    expect(await screen.findByTestId("gov-watchdog")).toBeInTheDocument()
  })
})
