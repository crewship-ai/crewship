// Admin › Security as an instance admin uses it: the Keeper of every
// workspace on one page. The settings cards are the existing ones and have
// their own tests; here they are stubs, and what is pinned is where each
// appears, that the panel's workspace ticks decide what the page covers, and
// that a save for several workspaces never writes before the admin has seen
// what it overwrites.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-a", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }))
vi.mock("@/app/(dashboard)/admin/hooks/use-admin-websocket", () => ({
  useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "connected" }),
}))
vi.mock("@/components/features/admin/keeper-judge-card", () => ({ KeeperJudgeCard: () => <div data-testid="card-judge" /> }))
vi.mock("@/components/features/admin/keeper-profile-card", () => ({ KeeperProfileCard: () => <div data-testid="card-rules" /> }))
vi.mock("@/components/features/admin/judge-models-card", () => ({ JudgeModelsCard: () => <div data-testid="card-background" /> }))
vi.mock("@/components/features/admin/keeper-health-card", () => ({ KeeperHealthCard: () => <div data-testid="card-health" /> }))
vi.mock("@/components/features/admin/keeper-governance-panel", () => ({
  KeeperGovernancePanel: ({ section, instance }: { section: string; instance?: { row: { workspace_id: string } } }) =>
    <div data-testid={`gov-${section}`} data-workspace={instance?.row.workspace_id} />,
}))

import { SecurityPage } from "../security-page"

const STATUS = { enabled: true, ollama_url: "http://x:11434", model: "qwen2.5:7b", ollama_online: true, gatekeeper_configured: true, total_requests: 4, allow_count: 3, deny_count: 1, escalate_count: 0 }
const POSTURE = {
  environment: "", encryption_key_configured: true, plaintext_secrets_allowed: false, private_endpoints_ceiling: false,
  signup_open: false, oauth_configured: false, email_configured: false, rate_limit_disabled: false, rate_limit_effectively_disabled: false,
  warnings: [{ key: "no_backup_recorded", severity: "medium", message: "No backup has ever been recorded on this instance. The database holds every credential." }],
}
const gov = (id: string, name: string, extra: Record<string, unknown> = {}) => ({
  workspace_id: id, workspace_name: name, workspace_slug: name.toLowerCase(), configured: true,
  enabled: false, security_contact_user_id: "", deny_notify_min_risk: 7, watch_spec: "", watch_presets: null,
  require_second_approver: false, gov_model_provider: "", gov_model_id: "", gov_model_credential_id: "",
  auto_lease_seconds: 0, behavior_sample_every: 0, ...extra,
})
const GOV = {
  defaults: { configured: false, enabled: false, deny_notify_min_risk: 7 },
  workspaces: [gov("ws-a", "Dess", { enabled: true, behavior_sample_every: 10 }), gov("ws-b", "Coolify")],
}
const row = (id: string, ws: string, wsName: string, request_type: string, decision: string, extra: Record<string, unknown> = {}) => ({
  id, request_type, decision, workspace_id: ws, workspace_name: wsName, agent_id: "a1", agent_name: "Hlídač", crew_id: "c1",
  credential_id: request_type === "access" ? "cr1" : "", credential_name: request_type === "access" ? "POSTGRES_PASSWORD" : "",
  intent: "check the database", command: null, reason: "Bound credential. Low risk.", risk_score: 2, exit_code: null,
  ollama_prompt: "PROMPT key sk-ant-abcdefghijklmnopqrstuvwxyz0123", ollama_raw_response: "{\"decision\":\"ALLOW\"}",
  created_at: "2026-09-29T10:00:00Z", decided_at: null, ...extra,
})
const ITEMS = [row("k1", "ws-a", "Dess", "access", "ALLOW"), row("k2", "ws-b", "Coolify", "access", "DENY"), row("k3", "ws-b", "Coolify", "behavior", "ALLOW", { reason: "Reads look routine." })]
const requests = (items: typeof ITEMS) => ({
  items, total: items.length,
  counts: { allow: items.filter((i) => i.decision === "ALLOW").length, deny: items.filter((i) => i.decision === "DENY").length, escalate: 0, pending: 0 },
  by_workspace: [{ workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", count: 1 }, { workspace_id: "ws-b", workspace_name: "Coolify", workspace_slug: "coolify", count: 2 }],
})
const HEALTH = { workspaces: [
  { workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", samples: 40, min_samples: 20, progressed_rate: 0.8, judge_failure_rate: 0, p95_latency_ms: 900 },
  { workspace_id: "ws-b", workspace_name: "Coolify", workspace_slug: "coolify", samples: 3, min_samples: 20, progressed_rate: 1, judge_failure_rate: 0, p95_latency_ms: 700 },
] }

const res = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
const puts = () => h.apiFetch.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "PUT").map(([, init]) => JSON.parse(String((init as RequestInit).body)))

beforeEach(() => {
  cleanup()
  window.history.replaceState(null, "", "/admin/security")
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) => {
    const url = String(u)
    if (url.startsWith("/api/v1/system/keeper")) return res(STATUS)
    if (url.startsWith("/api/v1/admin/security-posture")) return res(POSTURE)
    if (url.startsWith("/api/v1/admin/instance/keeper/governance")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body))
        return res({
          applied: !body.dry_run, changed: 1, defaults_updated: !!body.all,
          workspaces: [
            { workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", changes: [] },
            { workspace_id: "ws-b", workspace_name: "Coolify", workspace_slug: "coolify", changes: [{ field: "enabled", before: false, after: true }] },
          ],
        })
      }
      return res(GOV)
    }
    if (url.startsWith("/api/v1/admin/instance/keeper/requests")) {
      const ws = new URL(url, "http://x").searchParams.get("workspace")
      return res(requests(ws ? ITEMS.filter((i) => ws.split(",").includes(i.workspace_id)) : ITEMS))
    }
    if (url.startsWith("/api/v1/admin/instance/keeper/health")) return res(HEALTH)
    return res({})
  })
})

const scope = () => document.querySelector("[data-slot=workspace-scope]") as HTMLElement
const tick = (name: RegExp | string) => fireEvent.click(within(scope()).getByRole("button", { name }))

describe("the Workspaces section of the panel", () => {
  it("starts on every workspace, marks the one the admin sits in, and counts each", async () => {
    render(<SecurityPage />)
    await screen.findByRole("button", { name: /^Coolify/ })
    expect(within(scope()).getByRole("button", { name: "All workspaces" })).toHaveAttribute("aria-pressed", "true")
    expect(within(scope()).getByRole("button", { name: /^Dess/ })).toHaveTextContent("here")
    expect(within(scope()).getByRole("button", { name: /^Coolify/ })).toHaveTextContent("2")
  })

  it("keeps the ticks in the URL, so a link shows what its sender saw", async () => {
    render(<SecurityPage />)
    await screen.findByRole("button", { name: /^Coolify/ })
    tick(/^Dess/)
    expect(window.location.search).toContain("ws=coolify")
    tick(/^Coolify/)
    expect(window.location.search).toContain("ws=none")
  })

  it("greys out for an instance-wide setting", async () => {
    window.history.replaceState(null, "", "/admin/security?section=judge")
    render(<SecurityPage />)
    await screen.findByTestId("card-judge")
    expect(scope()).toHaveAttribute("aria-disabled", "true")
    expect(screen.getByText("Instance setting · applies to every workspace")).toBeInTheDocument()
  })
})

describe("Overview", () => {
  it("says in one line whether Keeper and its judge work, with totals over the ticked workspaces", async () => {
    render(<SecurityPage />)
    const line = await screen.findByText(/Judge answering · qwen2.5:7b/)
    const summary = line.closest("[data-slot=security-summary]")!
    expect(summary).toHaveTextContent("Keeper on")
    await waitFor(() => expect(summary).toHaveTextContent("requests · 2 allowed · 1 denied"))
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
    expect(within(card).queryByRole("button")).toBeNull()
  })

  it("shows each ticked workspace's judge health, and says when there is too little to judge", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Judge health" })
    expect(within(card).getByText("Dess")).toBeInTheDocument()
    expect(within(card).getByText(/too few decisions/)).toBeInTheDocument()
  })
})

describe("Activity", () => {
  it("lists every workspace's decisions with the workspace on each row", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Credential requests/ }))
    const table = await screen.findByRole("region", { name: "Activity" })
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(3))
    expect(within(table).getByRole("columnheader", { name: "Workspace" })).toBeInTheDocument()
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent(/Dess|Coolify/)
  })

  it("narrows to the ticked workspaces through the server", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Credential requests/ }))
    await screen.findByRole("region", { name: "Activity" })
    tick(/^Dess/)
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith(expect.stringContaining("workspace=ws-b")))
    const table = screen.getByRole("region", { name: "Activity" })
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(2))
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("Coolify")
  })

  it("opens a record with secrets redacted", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Credential requests/ }))
    const table = await screen.findByRole("region", { name: "Activity" })
    await waitFor(() => expect(within(table).getAllByRole("row").length).toBeGreaterThan(1))
    fireEvent.click(within(table).getAllByText("POSTGRES_PASSWORD")[0])
    const prompt = await screen.findByRole("region", { name: "Judge prompt" })
    expect(prompt).not.toHaveTextContent("abcdefghijklmnopqrstuvwxyz0123")
    expect(prompt).toHaveTextContent("REDACTED")
  })
})

describe("What's on where", () => {
  it("shows every workspace's settings in one table and opens a cell for that workspace", async () => {
    window.history.replaceState(null, "", "/admin/security?section=matrix")
    render(<SecurityPage />)
    const table = await screen.findByRole("region", { name: "What's on where" })
    expect(within(table).getByRole("button", { name: "Watchdog in Coolify: Off" })).toBeInTheDocument()
    expect(within(table).getByRole("button", { name: "Review in Dess: 1 in 10" })).toBeInTheDocument()
    fireEvent.click(within(table).getByRole("button", { name: "Watchdog in Coolify: Off" }))
    const card = await screen.findByTestId("gov-watchdog")
    expect(card).toHaveAttribute("data-workspace", "ws-b")
    expect(window.location.search).toContain("section=watchdog")
    expect(window.location.search).toContain("ws=coolify")
  })
})

describe("Settings", () => {
  it.each([
    ["Credential judge", "card-judge", "Instance"],
    ["Decision rules", "card-rules", "Instance"],
    ["Background checks", "card-background", "Instance"],
  ])("%s shows its own card and says it reaches every workspace", async (label, testId, scope) => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${label}`) }))
    await waitFor(() => expect(screen.getByTestId(testId)).toBeInTheDocument())
    expect(document.querySelector("[data-slot=security-scope]")).toHaveTextContent(scope)
  })

  it.each([
    ["Judge per workspace", "gov-judge"],
    ["Watchdog", "gov-watchdog"],
    ["Alerts & approvals", "gov-alerts"],
    ["Credential leases", "gov-leases"],
  ])("%s edits the one ticked workspace with the existing card, through the instance route", async (label, testId) => {
    window.history.replaceState(null, "", "/admin/security?ws=coolify")
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${label}`) }))
    const card = await screen.findByTestId(testId)
    expect(card).toHaveAttribute("data-workspace", "ws-b")
    expect(document.querySelector("[data-slot=security-scope]")).toHaveTextContent("Editing Coolify")
  })

  it("opens where the link points", async () => {
    window.history.replaceState(null, "", "/admin/security?section=watchdog&ws=dess")
    render(<SecurityPage />)
    expect(await screen.findByTestId("gov-watchdog")).toHaveAttribute("data-workspace", "ws-a")
  })
})

describe("several workspaces at once", () => {
  it("warns, marks what differs, and writes nothing until the admin has seen what is overwritten", async () => {
    window.history.replaceState(null, "", "/admin/security?section=watchdog")
    render(<SecurityPage />)
    expect(await screen.findByRole("alert")).toHaveTextContent("All 2 workspaces selected")
    expect(screen.queryByTestId("gov-watchdog")).toBeNull()
    const enabled = document.querySelector("[data-field=enabled]") as HTMLElement
    expect(within(enabled).getByText(/MIXED · On \/ Off/)).toBeInTheDocument()

    fireEvent.click(within(enabled).getByRole("radio", { name: "On" }))
    fireEvent.click(screen.getByRole("button", { name: "Overwrite all 2 workspaces…" }))

    const dialog = await screen.findByRole("alertdialog")
    expect(puts()).toEqual([{ all: true, dry_run: true, set: { enabled: true } }])
    const table = within(dialog).getByRole("table")
    expect(within(table).getByText("Coolify").closest("tr")).toHaveTextContent("OffOn")
    expect(within(table).getByText("Dess").closest("tr")).toHaveTextContent("no change")
    expect(dialog).toHaveTextContent("New workspaces will start with these settings")

    fireEvent.click(within(dialog).getByRole("button", { name: "Overwrite 1 workspace" }))
    await waitFor(() => expect(puts()).toHaveLength(2))
    expect(puts()[1]).toEqual({ all: true, dry_run: false, set: { enabled: true } })
  })

  it("sends only the ticked workspaces when not all are ticked, and nothing on cancel", async () => {
    GOV.workspaces.push(gov("ws-c", "Sandbox"))
    try {
      window.history.replaceState(null, "", "/admin/security?section=leases&ws=dess,coolify")
      render(<SecurityPage />)
      expect(await screen.findByRole("alert")).toHaveTextContent("2 workspaces selected")
      fireEvent.click(screen.getByRole("radio", { name: "15 min" }))
      fireEvent.click(screen.getByRole("button", { name: "Overwrite 2 workspaces…" }))
      const dialog = await screen.findByRole("alertdialog")
      expect(puts()[0]).toEqual({ workspaces: ["ws-a", "ws-b"], dry_run: true, set: { auto_lease_seconds: 900 } })
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
      await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
      expect(puts()).toHaveLength(1)
    } finally {
      GOV.workspaces.pop()
    }
  })
})
