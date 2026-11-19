// Admin › Security as an instance admin uses it: a panel with three views
// (Overview, Workspaces, Activity), four instance settings, and the few
// workspaces whose watchdog is off. The settings cards are the existing ones
// and have their own tests; here they are stubs, and what is pinned is where
// each appears, which workspace an editor writes to, and that a save for
// several workspaces never writes before the admin has seen what it overwrites.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), resolve: null as null | ((u: string, i?: RequestInit) => unknown), ws: "ws-a" as string | null, admin: true as boolean | null }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: h.ws, loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => h.admin }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/app/(dashboard)/admin/hooks/use-admin-websocket", () => ({
  useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "connected" }),
}))
vi.mock("@/components/features/admin/keeper-judge-card", () => ({ KeeperJudgeCard: () => <div data-testid="card-judge" /> }))
vi.mock("@/components/features/admin/keeper-profile-card", () => ({ KeeperProfileCard: () => <div data-testid="card-rules" /> }))
vi.mock("@/components/features/admin/judge-models-card", () => ({ JudgeModelsCard: () => <div data-testid="card-background" /> }))
vi.mock("@/components/features/admin/keeper-health-card", () => ({ KeeperHealthCard: () => <div data-testid="card-health" /> }))
vi.mock("@/components/features/mcp/hooks/use-credentials", () => ({ useCredentials: () => ({ credentials: [{ id: "cr1", name: "POSTGRES_PASSWORD", type: "SECRET" }] }) }))
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
  workspaces: [gov("ws-a", "Dess", { enabled: true, behavior_sample_every: 10 }), gov("ws-b", "Coolify"), gov("ws-c", "Quiet", { gov_model_provider: "anthropic", gov_model_id: "claude-haiku-4-5" })],
}
const row = (id: string, ws: string, wsName: string, request_type: string, decision: string, extra: Record<string, unknown> = {}) => ({
  id, request_type, decision, workspace_id: ws, workspace_name: wsName, agent_id: "a1", agent_name: "Hlídač", crew_id: "c1",
  credential_id: request_type === "access" ? "cr1" : "", credential_name: request_type === "access" ? "POSTGRES_PASSWORD" : "",
  intent: "check the database", command: null, reason: "Bound credential. Low risk.", risk_score: 2, exit_code: null,
  ollama_prompt: "PROMPT key sk-ant-abcdefghijklmnopqrstuvwxyz0123", ollama_raw_response: "{\"decision\":\"ALLOW\"}",
  created_at: "2026-09-29T10:00:00Z", decided_at: null, ...extra,
})
const ITEMS = [row("k1", "ws-a", "Dess", "access", "ALLOW"), row("k2", "ws-b", "Coolify", "access", "DENY"), row("k3", "ws-b", "Coolify", "behavior", "ALLOW", { reason: "Reads look routine." }), row("k4", "ws-a", "Dess", "access", "ESCALATE")]
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
  counts: { allow: inType.filter((i) => i.decision === "ALLOW").length, deny: inType.filter((i) => i.decision === "DENY").length, escalate: inType.filter((i) => i.decision === "ESCALATE").length, pending: 0 },
  by_workspace: [{ workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", count: 1 }, { workspace_id: "ws-b", workspace_name: "Coolify", workspace_slug: "coolify", count: 2 }],
  }
}
const HEALTH = { workspaces: [
  { workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", samples: 40, allow: 30, deny: 6, escalate: 4, judge_failures: 0, min_samples: 20, progressed_rate: 0.85, judge_failure_rate: 0, p95_latency_ms: 900, alarm_judge_failure_rate: 0.3 },
  { workspace_id: "ws-b", workspace_name: "Coolify", workspace_slug: "coolify", samples: 40, allow: 20, deny: 12, escalate: 2, judge_failures: 6, min_samples: 20, progressed_rate: 0.5, judge_failure_rate: 0.15, p95_latency_ms: 9100, alarm_judge_failure_rate: 0.3,
    alarm: { kind: "judge failing", summary: "Judge failed 6 of 40 times" } },
  { workspace_id: "ws-c", workspace_name: "Quiet", workspace_slug: "quiet", samples: 3, min_samples: 20, progressed_rate: 1, judge_failure_rate: 0, p95_latency_ms: 700, alarm_judge_failure_rate: 0.3 },
] }

const res = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
const puts = () => h.apiFetch.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "PUT").map(([, init]) => JSON.parse(String((init as RequestInit).body)))

beforeEach(() => {
  cleanup()
  h.ws = "ws-a"; h.admin = true; h.resolve = null
  window.history.replaceState(null, "", "/admin/security")
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(defaultImpl)
})

async function defaultImpl(u: string, init?: RequestInit) {
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
    if (/\/api\/v1\/admin\/keeper\/requests\/[^/]+\/events/.test(url)) return res([
      { seq: 1, state: "PENDING", actor_type: "agent", recorded_at: "2026-09-29T10:00:00Z" },
      { seq: 2, state: "ALLOW", actor_type: "keeper", recorded_at: "2026-09-29T10:00:02Z" },
    ])
    if (/\/api\/v1\/admin\/keeper\/requests\/[^/]+\/resolve/.test(url)) return h.resolve ? h.resolve(url, init) : res({ ok: true })
    if (url.startsWith("/api/v1/crews")) return res([{ id: "c1", name: "Research" }])
    if (url.startsWith("/api/v1/agents")) return res([{ id: "a1", name: "Scout", crew_id: "c1" }])
    if (url.startsWith("/api/v1/admin/keeper/ask")) return res({ request_id: "kr9", decision: "ALLOW", risk_score: 2, reason: "Bound credential." })
    if (url.startsWith("/api/v1/admin/instance/keeper/health")) return res(HEALTH)
    return res({})
}



/** A workspace in the panel (the phone list in the middle has a button too). */
const panelButtons = (name: RegExp) => screen.queryAllByRole("button", { name }).filter((b) => !b.closest("[data-slot=workspaces-list]"))
const inPanel = async (name: RegExp) => { await screen.findAllByRole("button", { name }); return panelButtons(name)[0] }
/** The page's floating Save bar: every Security form saves from there. */
const saveBar = () => fireEvent.click(within(screen.getByRole("region", { name: "Unsaved changes" })).getByRole("button", { name: "Save" }))
const reqURLs = () => h.apiFetch.mock.calls.map(([u]) => String(u)).filter((u) => u.includes("/instance/keeper/requests"))

describe("the panel", () => {
  it("names three views with their counts, four instance settings, and only the workspaces with the watchdog off", async () => {
    render(<SecurityPage />)
    const coolify = await inPanel(/^Coolify/)
    for (const [name, n] of [["Overview", "2"], ["Workspaces", "3"], ["Activity", "4"]] as const) {
      expect(screen.getByRole("button", { name: new RegExp(`^${name}`) })).toHaveTextContent(n)
    }
    expect(screen.getByRole("button", { name: /^Credential judge/ })).toHaveTextContent("qwen2.5:7b · answering")
    for (const name of ["Decision rules", "Background checks", "New workspaces"]) expect(screen.getByRole("button", { name: new RegExp(`^${name}`) })).toBeInTheDocument()
    // Watchdog off: Coolify and Quiet, not Dess (on), then the way to the whole list.
    expect(coolify).toBeInTheDocument()
    expect(panelButtons(/^Dess/)).toHaveLength(0)
    expect(screen.getByRole("button", { name: "All 3 workspaces…" })).toBeInTheDocument()
    // No segmented control for the views, no stream rows in the panel.
    expect(screen.queryByRole("group", { name: "View" })).toBeNull()
    expect(screen.queryByRole("button", { name: /^Behaviour/ })).toBeNull()
  })

  it("lists at most five workspaces with the watchdog off", async () => {
    const many = Array.from({ length: 9 }, (_, i) => gov(`w${i}`, `Off ${i}`))
    GOV.workspaces.push(...many)
    try {
      render(<SecurityPage />)
      await inPanel(/^Off 0/)
      expect(panelButtons(/^Off \d/)).toHaveLength(5)
      expect(screen.getByRole("button", { name: /^All 12 workspaces…/ })).toBeInTheDocument()
    } finally {
      GOV.workspaces.length = 3
    }
  })

  it("All N workspaces… opens the Workspaces view filtered to the watchdog off", async () => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^All 3 workspaces…/ }))
    expect(await screen.findByRole("table", { name: "Workspaces" })).toBeInTheDocument()
    expect(window.location.search).toContain("view=workspaces")
    expect(window.location.search).toContain("watch=off")
    const table = screen.getByRole("table", { name: "Workspaces" })
    expect(within(table).queryByRole("row", { name: "Dess" })).toBeNull()
    expect(within(table).getByRole("row", { name: "Coolify" })).toBeInTheDocument()
  })

  it("searching a name from another view lands on the Workspaces view with the match", async () => {
    render(<SecurityPage />)
    await inPanel(/^Coolify/)
    fireEvent.change(screen.getByPlaceholderText("Search workspaces…"), { target: { value: "dess" } })
    const table = await screen.findByRole("table", { name: "Workspaces" })
    expect(within(table).getByRole("row", { name: "Dess" })).toBeInTheDocument()
    expect(within(table).queryByRole("row", { name: "Coolify" })).toBeNull()
    expect(window.location.search).toContain("q=dess")
  })
})

describe("Overview", () => {
  it("says in one line whether Keeper and its judge work, and how many wait for a person", async () => {
    render(<SecurityPage />)
    const line = await screen.findByText(/Judge answering · qwen2.5:7b/)
    const summary = line.closest("[data-slot=security-summary]")!
    expect(summary).toHaveTextContent("Keeper on")
    await waitFor(() => expect(summary).toHaveTextContent("1 waiting for a person"))
    // Allowed and denied are Activity's, not repeated here.
    expect(summary).not.toHaveTextContent("allowed")
  })

  it("lists what needs attention with a fix for each, a posture link and an in-page one", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Needs attention" })
    expect(within(card).getByText("No backup recorded")).toBeInTheDocument()
    expect(within(card).getByRole("link", { name: /Create a backup/ })).toHaveAttribute("href", "/admin/backups")
    expect(within(card).getByText("2 workspaces have the watchdog off")).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: /Show them/ }))
    expect(await screen.findByRole("table", { name: "Workspaces" })).toBeInTheDocument()
    expect(window.location.search).toContain("watch=off")
  })

  it("shows the server's setup read-only, an unset value named as such", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Server setup" })
    expect(within(card).getByText("(unset)")).toBeInTheDocument()
    expect(within(card).queryByRole("button")).toBeNull()
    expect(card).toHaveTextContent("Whole instance")
  })

  it("shows judge health only for workspaces with an alarm or a degraded own judge, with the full numbers", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Judge health" })
    await waitFor(() => expect(card.querySelector("[data-health=coolify]")).not.toBeNull())
    const coolify = card.querySelector("[data-health=coolify]")!
    expect(coolify).toHaveTextContent("Judge failed 6 of 40 times")
    expect(coolify).toHaveTextContent("40 decisions · allow 20 · deny 12 · escalate 2 · judge failed 6 (15 %) · p95 9100 ms")
    expect(card.querySelector("[data-health=dess]")).toBeNull()
    expect(card.querySelector("[data-health=quiet]")).toBeNull()
    expect(card.querySelector("[data-slot=no-alarm]")).toHaveTextContent("2 workspaces have no alarm")
  })

  it("an own judge that fails more often than the alarm line is reported without a standing alarm", async () => {
    const bad = { ...HEALTH.workspaces[2], samples: 40, judge_failure_rate: 0.5, judge_failures: 20 }
    h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) => String(u).startsWith("/api/v1/admin/instance/keeper/health") ? res({ workspaces: [HEALTH.workspaces[0], bad] }) : defaultImpl(u, init))
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Judge health" })
    await waitFor(() => expect(card.querySelector("[data-health=quiet]")).not.toBeNull())
  })

  it("shows how workspaces are watched in four rows, each opening Workspaces with that filter", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "How workspaces are watched" })
    expect(card).toHaveTextContent("1 watched")
    expect(card).toHaveTextContent("1 with their own judge")
    expect(card).toHaveTextContent("0 need a second approver")
    expect(card).toHaveTextContent("3 keep access until revoked")
    fireEvent.click(within(card).getByRole("button", { name: "Show: 1 with their own judge" }))
    const table = await screen.findByRole("table", { name: "Workspaces" })
    expect(window.location.search).toContain("judge=own")
    expect(within(table).getByRole("row", { name: "Quiet" })).toBeInTheDocument()
    expect(within(table).queryByRole("row", { name: "Dess" })).toBeNull()
  })

  it("lists the latest five decisions, and one opens its record", async () => {
    render(<SecurityPage />)
    const card = await screen.findByRole("region", { name: "Latest decisions" })
    await waitFor(() => expect(within(card).getAllByRole("button", { name: /POSTGRES_PASSWORD|Behaviour/ }).length).toBeGreaterThan(1))
    fireEvent.click(within(card).getAllByRole("button", { name: /POSTGRES_PASSWORD/ })[0])
    expect(await screen.findByRole("region", { name: "Judge prompt" })).toBeInTheDocument()
  })
})

describe("Activity", () => {
  const open = async () => { render(<SecurityPage />); fireEvent.click(await screen.findByRole("button", { name: /^Activity/ })); return await screen.findByRole("region", { name: "Activity" }) }

  it("lists every workspace's decisions with the workspace on each row, and the kinds as chips with counts", async () => {
    const table = await open()
    await waitFor(() => expect(within(table).getAllByRole("row").length).toBeGreaterThan(3))
    expect(within(table).getByRole("columnheader", { name: "Workspace" })).toBeInTheDocument()
    const chips = screen.getByRole("group", { name: "Kind" })
    expect(within(chips).getByRole("button", { name: /^Credential requests/ })).toHaveTextContent("3")
    expect(within(chips).getByRole("button", { name: /^Behaviour/ })).toHaveTextContent("1")
    for (const n of ["Skill review", "Memory health", "Lessons from failures"]) expect(within(chips).getByRole("button", { name: new RegExp(`^${n}`) })).toBeInTheDocument()
  })

  it("asks the server for the kind and decision picked: Waiting is pending", async () => {
    await open()
    fireEvent.click(screen.getByRole("button", { name: /^Credential requests/ }))
    await waitFor(() => expect(reqURLs().some((u) => u.includes("request_type=access%2Cexecute"))).toBe(true))
    const decision = screen.getByRole("radiogroup", { name: "Decision" })
    expect(within(decision).getAllByRole("radio").map((r) => r.textContent)).toEqual(["All", "Allowed", "Denied", "Escalated", "Waiting"])
    fireEvent.click(within(decision).getByRole("radio", { name: "Denied" }))
    await waitFor(() => expect(reqURLs().some((u) => u.includes("decision=DENY"))).toBe(true))
    fireEvent.click(within(decision).getByRole("radio", { name: "Waiting" }))
    await waitFor(() => expect(reqURLs().some((u) => u.includes("decision=PENDING"))).toBe(true))
  })

  it("narrows to several workspaces through the server, picked by name", async () => {
    await open()
    fireEvent.click(screen.getByRole("combobox", { name: "Workspaces" }))
    fireEvent.change(await screen.findByLabelText("Search workspaces"), { target: { value: "co" } })
    fireEvent.click(screen.getByRole("checkbox", { name: /Coolify/ }))
    fireEvent.change(screen.getByLabelText("Search workspaces"), { target: { value: "" } })
    fireEvent.click(screen.getByRole("checkbox", { name: /Dess/ }))
    await waitFor(() => expect(reqURLs().some((u) => u.includes("workspace=ws-b%2Cws-a") || u.includes("workspace=ws-a%2Cws-b"))).toBe(true))
    expect(window.location.search).toContain("in=coolify%2Cdess")
  })

  it("pages through the history 25 at a time from the server", async () => {
    const all = Array.from({ length: 30 }, (_, i) => row(`p${i}`, "ws-a", "Dess", "access", "ALLOW"))
    h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) => {
      if (String(u).startsWith("/api/v1/admin/instance/keeper/requests")) {
        const q = new URL(String(u), "http://x").searchParams
        const off = Number(q.get("offset") ?? 0), lim = Number(q.get("limit") ?? 25)
        return res({ items: all.slice(off, off + lim), total: 30, by_type: { access: 30 }, counts: { allow: 30, deny: 0, escalate: 0, pending: 0 }, by_workspace: [] })
      }
      return defaultImpl(u, init)
    })
    await open()
    const more = await screen.findByRole("button", { name: "Show 5 more" })
    expect(reqURLs().some((u) => u.includes("limit=25") && u.includes("offset=0"))).toBe(true)
    fireEvent.click(more)
    await waitFor(() => expect(reqURLs().some((u) => u.includes("offset=25"))).toBe(true))
    await waitFor(() => expect(screen.getByText("All 30 shown")).toBeInTheDocument())
  })

  it("opens a record with secrets redacted and the history of that request, asked in the request's own workspace", async () => {
    await open()
    const table = await screen.findByRole("region", { name: "Activity" })
    await waitFor(() => expect(within(table).getAllByText("POSTGRES_PASSWORD").length).toBeGreaterThan(0))
    fireEvent.click(within(table).getAllByText("POSTGRES_PASSWORD")[0])
    const prompt = await screen.findByRole("region", { name: "Judge prompt" })
    expect(prompt).not.toHaveTextContent("abcdefghijklmnopqrstuvwxyz0123")
    expect(prompt).toHaveTextContent("REDACTED")
    const history = await screen.findByRole("region", { name: "History of this request" })
    await waitFor(() => expect(history).toHaveTextContent("Waiting"))
    expect(history).toHaveTextContent("Allowed")
    expect(h.apiFetch.mock.calls.map(([u]) => String(u)).some((u) => /\/keeper\/requests\/k\d\/events\?workspace_id=ws-/.test(u))).toBe(true)
  })

  describe("deciding from the sheet", () => {
    const openEscalated = async () => {
      const table = await open()
      fireEvent.click(await screen.findByRole("radio", { name: "Escalated" }))
      await waitFor(() => expect(within(table).getAllByText("POSTGRES_PASSWORD").length).toBeGreaterThan(0))
      fireEvent.click(within(table).getAllByText("POSTGRES_PASSWORD")[0])
      return await screen.findByRole("region", { name: "Decide" })
    }
    const resolves = () => h.apiFetch.mock.calls.filter(([u]) => String(u).includes("/resolve"))

    it("offers Allow and Deny for an escalated request, with a reason, through the resolve call of that workspace", async () => {
      const decide = await openEscalated()
      fireEvent.change(within(decide).getByLabelText("Reason"), { target: { value: "Checked with the owner" } })
      fireEvent.click(within(decide).getByRole("button", { name: "Allow" }))
      await waitFor(() => expect(resolves()).toHaveLength(1))
      const [url, init] = resolves()[0]
      expect(String(url)).toContain("/requests/k4/resolve?workspace_id=ws-a")
      expect(JSON.parse(String((init as RequestInit).body))).toEqual({ decision: "ALLOW", reason: "Checked with the owner" })
    })

    it("shows the server's sentence when a second approver is required, and when it was already settled", async () => {
      const decide = await openEscalated()
      h.resolve = () => res({ detail: "A second approver is required: you own the requesting agent." }, 403)
      fireEvent.click(within(decide).getByRole("button", { name: "Deny" }))
      expect(await within(decide).findByRole("alert")).toHaveTextContent("A second approver is required")
      h.resolve = () => res({ detail: "This request was already decided: ALLOW." }, 409)
      fireEvent.click(within(decide).getByRole("button", { name: "Allow" }))
      await waitFor(() => expect(within(decide).getByRole("alert")).toHaveTextContent("already decided"))
    })

    it("offers nothing to decide on a request that is settled", async () => {
      const table = await open()
      await waitFor(() => expect(within(table).getAllByText("POSTGRES_PASSWORD").length).toBeGreaterThan(0))
      fireEvent.click(await screen.findByRole("radio", { name: "Allowed" }))
      await waitFor(() => expect(reqURLs().some((u) => u.includes("decision=ALLOW"))).toBe(true))
      fireEvent.click(within(table).getAllByText("POSTGRES_PASSWORD")[0])
      await screen.findByRole("region", { name: "Judge prompt" })
      expect(screen.queryByRole("region", { name: "Decide" })).toBeNull()
    })

    it("does not let anyone but an instance administrator decide", async () => {
      h.admin = false
      const decide = await openEscalated()
      expect(within(decide).queryByRole("button", { name: "Allow" })).toBeNull()
      expect(decide).toHaveTextContent("Only an instance administrator")
    })
  })

  describe("Try a request", () => {
    it("is offered in the admin's current workspace, which the dialog names, with the warning that it is real", async () => {
      await open()
      await waitFor(() => expect(screen.getByRole("button", { name: /Try a request/ })).toBeEnabled())
      fireEvent.click(screen.getByRole("button", { name: /Try a request/ }))
      const dialog = await screen.findByRole("dialog")
      expect(dialog).toHaveTextContent("Asks the real judge in Dess.")
      expect(within(dialog).getByTestId("ask-warning")).toHaveTextContent("Runs the real judge and writes a real decision")
      fireEvent.change(within(dialog).getByLabelText("Crew"), { target: { value: "c1" } })
      fireEvent.change(within(dialog).getByLabelText("Agent"), { target: { value: "a1" } })
      fireEvent.change(within(dialog).getByLabelText("Credential"), { target: { value: "cr1" } })
      fireEvent.change(within(dialog).getByLabelText(/What the agent says/), { target: { value: "Check the database" } })
      fireEvent.click(within(dialog).getByRole("button", { name: "Ask the judge" }))
      expect(await within(dialog).findByTestId("ask-answer")).toHaveTextContent("Allowed")
      const call = h.apiFetch.mock.calls.find(([u]) => String(u).startsWith("/api/v1/admin/keeper/ask"))!
      expect(String(call[0])).toContain("workspace_id=ws-a")
      expect(JSON.parse(String((call[1] as RequestInit).body))).toMatchObject({ requesting_agent_id: "a1", requesting_crew_id: "c1", credential_id: "cr1", intent: "Check the database" })
    })

    it("is left out, with the reason, when the admin has no workspace", async () => {
      h.ws = null
      await open()
      expect(screen.queryByRole("button", { name: /Try a request/ })).toBeNull()
      expect(screen.getByTestId("ask-unavailable")).toHaveTextContent("needs a workspace")
    })
  })
})

describe("the Workspaces view", () => {
  it("shows every workspace's watch in one table, the second approver as enforced (review R8)", async () => {
    GOV.workspaces[1] = gov("ws-b", "Coolify", { effective_second_approver: { min_security_level: 4, min_security_level_label: "L4 · critical", source: "tier" } })
    try {
      window.history.replaceState(null, "", "/admin/security?view=workspaces")
      render(<SecurityPage />)
      const table = await screen.findByRole("table", { name: "Workspaces" })
      expect(within(table).getAllByRole("columnheader").map((c) => c.textContent).filter(Boolean)).toEqual(["Workspace", "Watchdog", "Checks", "Alert from risk", "Second approver", "Access lasts", "Judge"])
      const coolify = within(table).getByRole("row", { name: "Coolify" })
      expect(coolify.querySelector("[data-col=watchdog]")).toHaveTextContent("Off")
      expect(coolify.querySelector("[data-col=second-approver]")).toHaveTextContent("From level 4")
      expect(coolify.querySelector("[data-col=judge]")).toHaveTextContent("Instance")
      expect(coolify.querySelector("[data-col=access]")).toHaveTextContent("Until revoked")
      expect(within(table).getByRole("row", { name: "Dess" }).querySelector("[data-col=checks]")).toHaveTextContent("1 in 10")
      expect(within(table).getByRole("row", { name: "Quiet" }).querySelector("[data-col=judge]")).toHaveTextContent("Its own")
    } finally {
      GOV.workspaces[1] = gov("ws-b", "Coolify")
    }
  })

  it("pages 25 workspaces at a time and starts over when a filter changes", async () => {
    GOV.workspaces.push(...Array.from({ length: 40 }, (_, i) => gov(`x${i}`, `Wk ${String(i).padStart(2, "0")}`)))
    try {
      window.history.replaceState(null, "", "/admin/security?view=workspaces")
      render(<SecurityPage />)
      const table = await screen.findByRole("table", { name: "Workspaces" })
      await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(26))
      fireEvent.click(screen.getByRole("button", { name: "Show 18 more" }))
      await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(44))
      fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
      const block = await screen.findByRole("group", { name: "Filter workspaces" })
      fireEvent.click(within(block).getByRole("button", { name: /Watchdog/ }))
      fireEvent.click(await within(block).findByRole("button", { name: /^On/ }))
      await waitFor(() => expect(within(screen.getByRole("table", { name: "Workspaces" })).getAllByRole("row")).toHaveLength(2))
      expect(window.location.search).toContain("watch=on")
    } finally {
      GOV.workspaces.length = 3
    }
  })

  it("opens the defaults editor from Defaults for new ones", async () => {
    window.history.replaceState(null, "", "/admin/security?view=workspaces")
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /Defaults for new ones/ }))
    await waitFor(() => expect(window.location.search).toContain("section=defaults"))
  })

  it("opens a workspace's page with its own settings, through the instance route, and no tiles", async () => {
    window.history.replaceState(null, "", "/admin/security?view=workspaces")
    render(<SecurityPage />)
    fireEvent.click(within(await screen.findByRole("table", { name: "Workspaces" })).getByRole("row", { name: "Coolify" }))
    expect(await screen.findByTestId("gov-policy")).toHaveAttribute("data-workspace", "ws-b")
    expect(screen.getByTestId("gov-judge")).toHaveAttribute("data-workspace", "ws-b")
    expect(screen.queryByRole("region", { name: "Protection at a glance" })).toBeNull()
    expect(window.location.search).toContain("ws=coolify")
    fireEvent.click(screen.getByRole("button", { name: /Workspaces$/ }))
    expect(await screen.findByRole("table", { name: "Workspaces" })).toBeInTheDocument()
  })

  it("its activity link opens Activity for that workspace only", async () => {
    window.history.replaceState(null, "", "/admin/security?ws=coolify")
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: /Its activity/ }))
    await waitFor(() => expect(window.location.search).toContain("in=coolify"))
    await waitFor(() => expect(reqURLs().some((u) => u.includes("workspace=ws-b"))).toBe(true))
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

  it("takes any valid number beside the quick picks, and the watch presets, for several workspaces", async () => {
    const bar = await pick()
    fireEvent.click(within(bar).getByRole("button", { name: /^Watchdog/ }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByTestId("bulk-number-behavior_sample_every"), { target: { value: "37" } })
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Network egress" }))
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Secret files" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Review 2 changes" }))
    await screen.findByRole("alertdialog")
    expect(puts()[0]).toEqual({ workspaces: ["ws-a", "ws-b"], dry_run: true, set: { behavior_sample_every: 37, watch_presets: ["egress", "secret_files"] } })
  })

  it("refuses a number outside the range without setting it", async () => {
    const bar = await pick()
    fireEvent.click(within(bar).getByRole("button", { name: /^Alerts/ }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByTestId("bulk-number-deny_notify_min_risk"), { target: { value: "11" } })
    expect(within(dialog).getByTestId("bulk-number-invalid-deny_notify_min_risk")).toBeInTheDocument()
    expect(within(dialog).getByRole("button", { name: "Review changes" })).toBeDisabled()
    fireEvent.change(within(dialog).getByTestId("bulk-number-deny_notify_min_risk"), { target: { value: "10" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Review 1 change" }))
    await screen.findByRole("alertdialog")
    expect(puts()[0].set).toEqual({ deny_notify_min_risk: 10 })
  })

  it("sends nothing on cancel", async () => {
    const bar = await pick()
    fireEvent.click(within(bar).getByRole("button", { name: /^Access time/ }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(dialog).getByRole("radio", { name: "15 min" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Review 1 change" }))
    const confirm = await screen.findByRole("alertdialog")
    expect(puts()[0]).toEqual({ workspaces: ["ws-a", "ws-b"], dry_run: true, set: { auto_lease_seconds: 900 } })
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(puts()).toHaveLength(1)
  })

  it("does not let a person who is not an instance administrator tick workspaces", async () => {
    h.admin = false
    window.history.replaceState(null, "", "/admin/security?view=workspaces")
    render(<SecurityPage />)
    const table = await screen.findByRole("table", { name: "Workspaces" })
    expect(within(table).getByRole("checkbox", { name: "Select Dess" })).toBeDisabled()
  })
})

describe("instance settings", () => {
  it.each([
    ["Credential judge", "card-judge"],
    ["Decision rules", "card-rules"],
    ["Background checks", "card-background"],
  ])("%s is its own page with a way back and the mark that it reaches every workspace", async (label, testId) => {
    render(<SecurityPage />)
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${label}`) }))
    await waitFor(() => expect(screen.getByTestId(testId)).toBeInTheDocument())
    expect(document.querySelector("[data-slot=whole-instance]")).toHaveTextContent("Whole instance")
    fireEvent.click(screen.getByRole("button", { name: "Overview" , hidden: false }))
    expect(await screen.findByRole("region", { name: "Needs attention" })).toBeInTheDocument()
  })
})

describe("old links still land", () => {
  it.each([
    ["/admin/security?section=watchdog&ws=dess", "ws-a"],
    ["/admin/security?section=alerts&ws=coolify", "ws-b"],
  ])("%s opens that workspace's page", async (url, id) => {
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

  it("?view=activity&stream=behavior opens that kind", async () => {
    window.history.replaceState(null, "", "/admin/security?view=activity&stream=behavior")
    render(<SecurityPage />)
    await waitFor(() => expect(reqURLs().some((u) => u.includes("request_type=behavior"))).toBe(true))
    expect(screen.getByRole("button", { name: /^Behaviour/ })).toHaveAttribute("aria-pressed", "true")
  })
})

describe("New workspaces, an operation of its own", () => {
  const defaultsPuts = () => h.apiFetch.mock.calls
    .filter(([u, init]) => String(u).includes("/governance/defaults") && (init as RequestInit | undefined)?.method === "PUT")
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)))

  it("previews, says no existing workspace changes, and confirms exactly the preview", async () => {
    window.history.replaceState(null, "", "/admin/security?section=defaults")
    render(<SecurityPage />)
    await waitFor(() => expect(document.querySelector("[data-field=enabled]")).not.toBeNull())
    const enabled = document.querySelector("[data-field=enabled]") as HTMLElement
    expect(document.querySelector("[data-slot=whole-instance]")).toBeInTheDocument()
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

  it("carries watch presets too, which the server accepts for defaults", async () => {
    window.history.replaceState(null, "", "/admin/security?section=defaults")
    render(<SecurityPage />)
    await waitFor(() => expect(document.querySelector("[data-field=watch_presets]")).not.toBeNull())
    fireEvent.click(within(document.querySelector("[data-field=watch_presets]") as HTMLElement).getByRole("checkbox", { name: "Credentials" }))
    saveBar()
    await screen.findByRole("alertdialog")
    expect(defaultsPuts()[0]).toEqual({ dry_run: true, set: { watch_presets: ["credentials"] } })
  })
})
