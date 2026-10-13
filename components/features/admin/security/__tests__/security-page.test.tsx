// Admin › Security as an instance admin uses it, built like People &
// workspaces: Overview · Workspaces · Activity, the instance settings in the
// panel, and a profile per workspace. The settings cards are the existing ones
// and have their own tests; here they are stubs, and what is pinned is where
// each appears, which workspace an editor writes to, and that a save for
// several workspaces never writes before the admin has seen what it overwrites.

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
    <div data-testid={`gov-${section ?? "policy"}`} data-workspace={instance?.row.workspace_id} />,
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
  defaults: { configured: false, enabled: false, deny_notify_min_risk: 7, effective_second_approver: { min_security_level: 4, min_security_level_label: "L4 · critical", source: "tier" } },
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
// The server's answer: filtered by workspace, kind and decision, counted the
// way the endpoint counts (decisions without the decision filter, kinds under
// the workspace filter alone), paged by limit/offset.
const requests = (url: string) => {
  const q = new URL(url, "http://x").searchParams
  const ws = q.get("workspace")?.split(",")
  const types = q.get("request_type")?.split(",")
  const dec = q.get("decision")
  const inWs = ITEMS.filter((i) => !ws || ws.includes(i.workspace_id))
  const inType = inWs.filter((i) => !types || types.includes(i.request_type))
  const all = inType.filter((i) => !dec || i.decision === dec)
  const offset = Number(q.get("offset") ?? 0), limit = Number(q.get("limit") ?? 100)
  const by_type: Record<string, number> = {}
  for (const i of inWs) by_type[i.request_type] = (by_type[i.request_type] ?? 0) + 1
  return {
  items: all.slice(offset, offset + limit), total: all.length, by_type,
  counts: { allow: inType.filter((i) => i.decision === "ALLOW").length, deny: inType.filter((i) => i.decision === "DENY").length, escalate: 0, pending: 0 },
  by_workspace: [{ workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", count: 1 }, { workspace_id: "ws-b", workspace_name: "Coolify", workspace_slug: "coolify", count: 2 }],
  }
}
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
    if (url.startsWith("/api/v1/admin/instance/keeper/governance/defaults")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body))
        return res({ applied: !body.dry_run, configured: true, preview_id: "d-1", defaults: { ...GOV.defaults, ...body.set },
          changes: Object.entries(body.set).map(([field, after]) => ({ field, before: (GOV.defaults as Record<string, unknown>)[field] ?? null, after })) })
      }
      return res({ applied: false, configured: false, defaults: GOV.defaults, changes: [], preview_id: "" })
    }
    if (url.startsWith("/api/v1/admin/instance/keeper/governance")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body))
        return res({
          applied: !body.dry_run, changed: 1, preview_id: "pv-1",
          workspaces: [
            { workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", changes: [] },
            { workspace_id: "ws-b", workspace_name: "Coolify", workspace_slug: "coolify", changes: [{ field: "enabled", before: false, after: true }] },
          ],
        })
      }
      return res(GOV)
    }
    if (url.startsWith("/api/v1/admin/instance/keeper/requests")) return res(requests(url))
    if (url.startsWith("/api/v1/admin/instance/keeper/health")) return res(HEALTH)
    return res({})
  })
})


/** The page's floating Save bar: every Security form saves from there. */
const saveBar = () => fireEvent.click(within(screen.getByRole("region", { name: "Unsaved changes" })).getByRole("button", { name: "Save" }))

describe("the panel, like People & workspaces", () => {
  it("offers three views, the instance settings, the activity and every workspace with how it is watched", async () => {
    render(<SecurityPage />)
    const coolify = await screen.findByRole("button", { name: /^Coolify/ })
    expect(within(screen.getByRole("group", { name: "View" })).getAllByRole("button").map((b) => b.textContent)).toEqual(["Overview", "Workspaces", "Activity"])
    expect(screen.getByRole("button", { name: /^Credential judge/ })).toHaveTextContent("qwen2.5:7b")
    expect(screen.getByRole("button", { name: /^New workspaces/ })).toBeInTheDocument()
    expect(coolify).toHaveTextContent("Watchdog off")
    expect(screen.getByRole("button", { name: /^Dess/ })).toHaveTextContent("Watchdog on · 1 in 10")
    // The old tick list of workspaces is gone.
    expect(document.querySelector("[data-slot=workspace-scope]")).toBeNull()
  })

  it("finds a workspace by name", async () => {
    render(<SecurityPage />)
    await screen.findByRole("button", { name: /^Coolify/ })
    fireEvent.change(screen.getByPlaceholderText("Search workspaces…"), { target: { value: "dess" } })
    expect(screen.queryByRole("button", { name: /^Coolify/ })).toBeNull()
    expect(screen.getByRole("button", { name: /^Dess/ })).toBeInTheDocument()
    expect(window.location.search).toContain("q=dess")
  })
})

describe("Overview", () => {
  it("says in one line whether Keeper and its judge work, with totals over every workspace", async () => {
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
    expect(within(card).getByRole("link", { name: /Create a backup/ })).toHaveAttribute("href", "/admin/backups")
  })

  it("shows the server's setup read-only, an unset value named as such", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Server setup" })
    expect(within(card).getByText("(unset)")).toBeInTheDocument()
    expect(within(card).queryByRole("button")).toBeNull()
  })

  it("shows each workspace's judge health, and says when there is too little to judge", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Judge health" })
    expect(within(card).getByText("Dess")).toBeInTheDocument()
    expect(within(card).getByText(/too few decisions/)).toBeInTheDocument()
  })

  it("shows how each workspace is watched, and a row opens that workspace", async () => {
    render(<SecurityPage />)
    const table = await screen.findByRole("table", { name: "What's on where" })
    fireEvent.click(within(table).getByRole("row", { name: "Coolify" }))
    expect(await screen.findByTestId("gov-policy")).toHaveAttribute("data-workspace", "ws-b")
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

  it("narrows to one workspace through the server", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Credential requests/ }))
    await screen.findByRole("region", { name: "Activity" })
    fireEvent.change(screen.getByRole("combobox", { name: "Workspace" }), { target: { value: "coolify" } })
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith(expect.stringContaining("workspace=ws-b")))
    const table = screen.getByRole("region", { name: "Activity" })
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(2))
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("Coolify")
    expect(window.location.search).toContain("in=coolify")
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

describe("Activity on the server (review R6)", () => {
  const reqURLs = () => h.apiFetch.mock.calls.map(([u]) => String(u)).filter((u) => u.includes("/keeper/requests"))

  it("asks the server for the kind and decision picked, and pages through the rest", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Credential requests/ }))
    await waitFor(() => expect(reqURLs().some((u) => u.includes("request_type=access%2Cexecute"))).toBe(true))
    fireEvent.click(await screen.findByRole("button", { name: "Deny" }))
    await waitFor(() => expect(reqURLs().some((u) => u.includes("decision=DENY"))).toBe(true))
    const table = screen.getByRole("region", { name: "Activity" })
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(2))
    expect(document.querySelector("[data-slot=activity-paging]")).toHaveTextContent("Showing 1 of 1")
  })

  it("counts each kind in the panel from the server, not from what was read", async () => {
    render(<SecurityPage />)
    await waitFor(() => expect(screen.getByRole("button", { name: /^Behavior/ })).toHaveTextContent("1"))
    expect(screen.getByRole("button", { name: /^Credential requests/ })).toHaveTextContent("2")
  })
})

describe("the Workspaces view", () => {
  it("shows every workspace's watch in one table, what is enforced included (review R8)", async () => {
    GOV.workspaces[1] = gov("ws-b", "Coolify", { effective_second_approver: { min_security_level: 4, min_security_level_label: "L4 · critical", source: "tier" } })
    try {
      window.history.replaceState(null, "", "/admin/security?view=workspaces")
      render(<SecurityPage />)
      const table = await screen.findByRole("table", { name: "Workspaces" })
      const coolify = within(table).getByRole("row", { name: "Coolify" })
      expect(coolify.querySelector("[data-col=watchdog]")).toHaveTextContent("Off")
      expect(coolify.querySelector("[data-col=four-eyes]")).toHaveTextContent("L4 required")
      expect(coolify.querySelector("[data-col=judge]")).toHaveTextContent("Instance judge (inherited)")
      expect(within(table).getByRole("row", { name: "Dess" }).querySelector("[data-col=review]")).toHaveTextContent("1 in 10")
    } finally {
      GOV.workspaces[1] = gov("ws-b", "Coolify")
    }
  })

  it("ends with what a new workspace starts with, which opens its own editor", async () => {
    window.history.replaceState(null, "", "/admin/security?view=workspaces")
    render(<SecurityPage />)
    const table = await screen.findByRole("table", { name: "Workspaces" })
    const rows = within(table).getAllByRole("row")
    expect(rows[rows.length - 1]).toHaveAccessibleName("New workspaces")
    // Four-eyes as enforced for a new workspace, like every row.
    expect(rows[rows.length - 1].querySelector("[data-col=four-eyes]")).toHaveTextContent("L4 required")
    fireEvent.click(rows[rows.length - 1])
    await waitFor(() => expect(window.location.search).toContain("section=defaults"))
  })

  it("opens a workspace's profile with every setting of its own, through the instance route", async () => {
    window.history.replaceState(null, "", "/admin/security?view=workspaces")
    render(<SecurityPage />)
    fireEvent.click(within(await screen.findByRole("table", { name: "Workspaces" })).getByRole("row", { name: "Coolify" }))
    expect(await screen.findByTestId("gov-policy")).toHaveAttribute("data-workspace", "ws-b")
    expect(screen.getByTestId("gov-judge")).toHaveAttribute("data-workspace", "ws-b")
    expect(document.querySelector("[data-slot=workspace-security-header]")).toHaveTextContent("Watchdog off")
    expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toHaveTextContent("Workspaces")
    expect(window.location.search).toContain("ws=coolify")
  })
})

describe("several workspaces at once, from the selection bar", () => {
  const pick = async () => {
    window.history.replaceState(null, "", "/admin/security?view=workspaces")
    render(<SecurityPage />)
    const table = await screen.findByRole("table", { name: "Workspaces" })
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select Dess" }))
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select Coolify" }))
    return screen.getByRole("toolbar", { name: "Change the selected workspaces" })
  }

  it("marks what differs, and writes nothing until the admin has seen what is overwritten", async () => {
    const bar = await pick()
    expect(bar).toHaveTextContent("2 selected")
    fireEvent.click(within(bar).getByRole("button", { name: /^Watchdog/ }))
    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent("Watchdog for 2 workspaces")
    const enabled = dialog.querySelector("[data-field=enabled]") as HTMLElement
    expect(within(enabled).getByText(/MIXED · On \/ Off/)).toBeInTheDocument()
    fireEvent.click(within(enabled).getByRole("radio", { name: "On" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Review 1 change" }))

    const confirm = await screen.findByRole("alertdialog")
    expect(puts()).toEqual([{ workspaces: ["ws-a", "ws-b"], dry_run: true, set: { enabled: true } }])
    const table = within(confirm).getByRole("table")
    expect(within(table).getByText("Coolify").closest("tr")).toHaveTextContent("Watchdog: Off → On")
    expect(within(table).getByText("Dess").closest("tr")).toHaveTextContent("no change")
    expect(confirm).toHaveTextContent("New workspaces are not changed")
    fireEvent.click(within(confirm).getByRole("button", { name: "Overwrite 1 workspace" }))
    await waitFor(() => expect(puts()).toHaveLength(2))
    expect(puts()[1]).toEqual({ workspaces: ["ws-a", "ws-b"], dry_run: false, expect_preview: "pv-1", set: { enabled: true } })
    await waitFor(() => expect(screen.queryByRole("toolbar", { name: "Change the selected workspaces" })).toBeNull())
  })

  it("sends nothing on cancel", async () => {
    const bar = await pick()
    fireEvent.click(within(bar).getByRole("button", { name: /^Lease/ }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(dialog).getByRole("radio", { name: "15 min" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Review 1 change" }))
    const confirm = await screen.findByRole("alertdialog")
    expect(puts()[0]).toEqual({ workspaces: ["ws-a", "ws-b"], dry_run: true, set: { auto_lease_seconds: 900 } })
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(puts()).toHaveLength(1)
  })
})

describe("instance settings", () => {
  it.each([
    ["Credential judge", "card-judge"],
    ["Decision rules", "card-rules"],
    ["Background checks", "card-background"],
  ])("%s shows its own card and says it reaches every workspace", async (label, testId) => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${label}`) }))
    await waitFor(() => expect(screen.getByTestId(testId)).toBeInTheDocument())
    expect(document.querySelector("[data-slot=security-scope]")).toHaveTextContent("Instance")
    expect(document.querySelector("[data-slot=security-scope]")).toHaveTextContent("Applies to every workspace on this server")
  })
})

describe("old links still land", () => {
  it.each([
    ["/admin/security?section=watchdog&ws=dess", "ws-a"],
    ["/admin/security?section=alerts&ws=coolify", "ws-b"],
  ])("%s opens that workspace's profile", async (url, id) => {
    window.history.replaceState(null, "", url)
    render(<SecurityPage />)
    expect(await screen.findByTestId("gov-policy")).toHaveAttribute("data-workspace", id)
  })

  it("a per-workspace section with several workspaces opens the Workspaces view", async () => {
    window.history.replaceState(null, "", "/admin/security?section=leases&ws=dess,coolify")
    render(<SecurityPage />)
    expect(await screen.findByRole("table", { name: "Workspaces" })).toBeInTheDocument()
    expect(screen.queryByTestId("gov-policy")).toBeNull()
  })
})

describe("Defaults for new workspaces, an operation of its own", () => {
  const defaultsPuts = () => h.apiFetch.mock.calls
    .filter(([u, init]) => String(u).includes("/governance/defaults") && (init as RequestInit | undefined)?.method === "PUT")
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)))

  it("previews, says no existing workspace changes, and confirms exactly the preview", async () => {
    window.history.replaceState(null, "", "/admin/security?section=defaults")
    render(<SecurityPage />)
    await waitFor(() => expect(document.querySelector("[data-field=enabled]")).not.toBeNull())
    const enabled = document.querySelector("[data-field=enabled]") as HTMLElement
    expect(document.querySelector("[data-slot=security-scope]")).toHaveTextContent("existing workspaces do not change")
    fireEvent.click(within(enabled).getByRole("radio", { name: "On" }))
    saveBar()
    const dialog = await screen.findByRole("alertdialog")
    expect(defaultsPuts()[0]).toEqual({ dry_run: true, set: { enabled: true } })
    expect(dialog).toHaveTextContent("Watchdog: Off → On")
    expect(dialog).toHaveTextContent("No existing workspace changes")
    expect(puts().some((b) => "all" in b || "workspaces" in b)).toBe(false)
    fireEvent.click(within(dialog).getByRole("button", { name: "Save defaults" }))
    await waitFor(() => expect(defaultsPuts()).toHaveLength(2))
    expect(defaultsPuts()[1]).toEqual({ dry_run: false, expect_preview: "d-1", set: { enabled: true } })
  })
})
