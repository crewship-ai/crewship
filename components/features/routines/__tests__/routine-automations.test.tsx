// The routine page (Harbor 2): its header wording, the tabs and the Simple /
// Full detail switch, older `?view=` links, and the three facts the page has
// to answer — what starts it (schedules, automations), what it writes back to
// Crewship, and what it can reach.
//
// A routine with no automations must render NOTHING extra about them: empty
// scaffolding costs every routine a permanent slot for a feature most of them
// never use.

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, within, waitFor, cleanup } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { Automation } from "@/lib/automations"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({
  automations: [] as unknown[],
  records: [] as unknown[],
  schedules: [] as unknown[],
  webhooks: [] as unknown[],
  replace: vi.fn(),
}))

vi.mock("next/link", () => ({
  default: ({ children, href, ...rest }: { children: React.ReactNode; href: string } & React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: h.replace }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/routines",
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () => (String(url).endsWith("/draft") ? { id: "", slug: "daily-triage", revision: 0, base_pipeline_id: "pipe-1", base_revision: 1, document: {} } : {}),
  })),
  broadcastSessionExpired: vi.fn(),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({ useWorkspaceAgentDirectory: () => ({ agents: [], error: false }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER", capabilities: [] }) }))
vi.mock("@/hooks/use-access-me", () => ({
  useAccessMe: () => ({ access: { actions: { read: { state: "allowed", reason: "workspace_visible" }, run: { state: "conditional", reason: "runtime_preflight_required" } } }, loading: false, error: false, refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-pending-approval", () => ({ usePendingApproval: () => ({ waitpoint: null, deciding: false, decide: vi.fn(), refresh: vi.fn() }) }))
vi.mock("@/hooks/use-agent-reach", () => ({ useAgentReach: () => ({ toolkits: [], channels: [], loading: false }) }))
vi.mock("../routine-definition-canvas", () => ({ RoutineDefinitionCanvas: () => <div data-testid="canvas" /> }))
vi.mock("../routine-step-spine", async (importOriginal) => ({ ...(await importOriginal<typeof import("../routine-step-spine")>()), RoutineStepSpine: () => <div data-testid="spine" /> }))
vi.mock("../routine-schedules-tab", async (importOriginal) => ({ ...(await importOriginal<typeof import("../routine-schedules-tab")>()), RoutineSchedulesTab: () => <div data-testid="schedules-card" /> }))
vi.mock("../routine-webhooks-tab", () => ({ RoutineWebhooksTab: () => <div data-testid="webhooks-card" /> }))
vi.mock("../routine-versions-tab", () => ({ RoutineVersionsTab: () => <div data-testid="versions-tab" /> }))
vi.mock("../routine-comparison", () => ({ RoutineComparison: () => null }))
vi.mock("../routine-budget-card", () => ({ RoutineBudgetCard: () => <div /> }))
vi.mock("../routine-publish-dialog", () => ({ RoutinePublishDialog: () => null }))
vi.mock("@/components/features/settings/resource-share-dialog", () => ({ ResourceShareDialog: () => <button type="button">Share</button> }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({ schedules: h.schedules, loading: false, error: null, refresh: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(), preview: vi.fn() }),
}))
vi.mock("@/hooks/use-pipeline-webhooks", () => ({ usePipelineWebhooks: () => ({ webhooks: h.webhooks, loading: false, error: null }) }))
vi.mock("@/hooks/use-automations", () => ({ useAutomations: () => ({ automations: h.automations, loading: false, error: null }) }))

import { inOneLook } from "../routine-card-detail"
import { apiFetch } from "@/lib/api-fetch"
import { RoutinePage } from "../routine-page"

function routine(over: Partial<RoutineDetail> = {}): RoutineDetail {
  return {
    id: "pipe-1",
    slug: "daily-triage",
    name: "Daily triage",
    dsl_version: "1.0",
    definition: { steps: [{ id: "s1", type: "agent_run" }] },
    definition_hash: "abc123",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 0,
    authored_via: "ui",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    head_version: 3,
    ...over,
  } as RoutineDetail
}

function rule(over: Partial<Automation> = {}): Automation {
  return {
    id: "a1",
    workspace_id: "ws1",
    name: "Triage new bugs",
    enabled: true,
    event_type: "mission.status_change",
    matcher: {},
    action_kind: "routine",
    action: { routine_slug: "daily-triage" },
    debounce_seconds: 10,
    max_per_hour: 60,
    created_at: "2026-08-07T10:00:00Z",
    updated_at: "2026-08-07T10:00:00Z",
    ...over,
  }
}

function renderPage(r: RoutineDetail = routine()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <RoutinePage
      routine={r}
      workspaceId="ws1"
      crewName="Ops"
      records={h.records as never[]}
      lifecycle="active"
      canEdit
      canManage
      canRun
      onChanged={vi.fn()}
      onRun={vi.fn()}
      onOpenRun={vi.fn()}
      onGovernance={vi.fn()}
      onDelete={vi.fn()}
    />,
    { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> },
  )
}
const tab = (name: string) => fireEvent.click(screen.getByRole("tab", { name: new RegExp(`^${name}`) }))

beforeEach(() => {
  h.automations = []
  h.records = []
  h.schedules = []
  h.webhooks = []
  h.replace.mockReset()
  window.localStorage.removeItem("crewship.pref.routines.detailDepth")
  window.history.replaceState(null, "", "/routines?slug=daily-triage")
})
afterEach(cleanup)

describe("header and tabs", () => {
  it("Live / changes-not-live wording; versions only in Full detail", async () => {
    renderPage(routine({ draft: { id: "d1", revision: 2, updated_at: "2026-08-01T00:00:00Z" } }))
    const state = screen.getByTestId("routine-state-line")
    expect(state).toHaveTextContent("Live")
    expect(state).toHaveTextContent("Changes not live")
    expect(state).not.toHaveTextContent("v3")
    expect(screen.getByTestId("routine-meta-line")).toHaveTextContent("Ops crew")
    expect(screen.getAllByRole("tab").map((t) => t.textContent?.replace(/\d+$/, ""))).toEqual(["Overview", "Steps", "Triggers", "Settings"])
    fireEvent.click(screen.getByRole("radio", { name: "Full detail" }))
    expect(state).toHaveTextContent("Live v3")
    expect(state).toHaveTextContent("Changes not live · draft r2")
    expect(screen.getByRole("tab", { name: "Versions" })).toBeInTheDocument()
    // Remembered for the person (synced as a user preference).
    await waitFor(() => expect(vi.mocked(apiFetch)).toHaveBeenCalledWith("/api/v1/me/preferences/routines.detailDepth", expect.objectContaining({ method: "PUT" })), { timeout: 2000 })
  })

  it("the tab lives in ?tab=", () => {
    renderPage()
    tab("Settings")
    expect(new URLSearchParams(window.location.search).get("tab")).toBe("settings")
    tab("Overview")
    expect(new URLSearchParams(window.location.search).has("tab")).toBe(false)
  })

  it.each([
    ["plan", "triggers"],
    ["edit", "settings"],
    ["settings", "settings"],
    ["versions", "versions"],
  ])("?view=%s lands on the %s tab and keeps the slug", async (view, expected) => {
    window.history.replaceState(null, "", `/routines?slug=daily-triage&view=${view}`)
    renderPage()
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("tab")).toBe(expected))
    const params = new URLSearchParams(window.location.search)
    expect(params.get("slug")).toBe("daily-triage")
    expect(params.has("view")).toBe(false)
    if (view === "versions") expect(screen.getByRole("radio", { name: "Full detail" })).toHaveAttribute("aria-checked", "true")
  })

  it("?view=history goes to the routine's runs in Activity", async () => {
    window.history.replaceState(null, "", "/routines?slug=daily-triage&view=history")
    renderPage()
    await waitFor(() => expect(h.replace).toHaveBeenCalledWith("/activity?pipeline=daily-triage"))
  })
})

describe("automations bound to a routine", () => {
  it("renders nothing extra when no automation targets this routine", () => {
    h.automations = [rule({ id: "other", action: { routine_slug: "some-other-routine" } })]
    renderPage()
    tab("Triggers")
    expect(screen.queryByTestId("routine-automations")).toBeNull()
    expect(within(screen.getByTestId("routine-trigger-list")).queryByText("Event")).toBeNull()
  })

  it("lists each rule in the one list of triggers and names the event it watches", () => {
    h.automations = [rule({ id: "a1", name: "Triage new bugs", event_type: "mission.status_change" }), rule({ id: "a2", name: "Escalate stalls", event_type: "assignment.failed" })]
    renderPage()
    tab("Triggers")
    // RT-08: the rules are rows of the one list — no second Automations card.
    const list = screen.getByTestId("routine-trigger-list")
    const events = () => Array.from(screen.getByTestId("routine-trigger-list").querySelectorAll('[data-kind="event"]'))
    const row = (name: string) => events().find((el) => el.textContent?.includes(name))!
    expect(events()).toHaveLength(2)
    expect(row("Triage new bugs")).toHaveTextContent("When mission status change happens")
    expect(row("Escalate stalls")).toHaveTextContent("When assignment failed happens")
    expect(row("Triage new bugs")).toHaveTextContent("crewship automation")
    expect(list).toBeInTheDocument()
    expect(screen.queryByTestId("routine-automations")).not.toBeInTheDocument()
    // Full detail names the raw event type.
    fireEvent.click(screen.getByRole("radio", { name: "Full detail" }))
    expect(row("Triage new bugs")).toHaveTextContent("event mission.status_change")
    expect(row("Escalate stalls")).toHaveTextContent("event assignment.failed")
  })

  it("marks a disabled rule as off rather than hiding it", () => {
    h.automations = [rule({ id: "a1", name: "Triage new bugs", enabled: false })]
    renderPage()
    tab("Triggers")
    const row = screen.getByTestId("routine-trigger-list").querySelector('[data-kind="event"]')!
    expect(row).toHaveTextContent("Triage new bugs")
    expect(row).toHaveTextContent("Off")
  })
})

describe("what starts it", () => {
  it("does not claim zero starts for a schedule bound by slug", () => {
    h.schedules = [{ id: "schedule-1", name: "Mornings", target_pipeline_slug: "daily-triage", enabled: true, cron_expr: "0 9 * * *", timezone: "UTC", consecutive_failures: 0, max_consecutive_failures: 5 }]
    renderPage()
    const glance = screen.getByTestId("routine-at-a-glance")
    expect(within(glance).getByRole("button", { name: "Schedule" })).toBeInTheDocument()
    tab("Triggers")
    const list = screen.getByTestId("routine-trigger-list")
    expect(list).toHaveTextContent("Every day at 09:00")
    expect(list).toHaveTextContent("follows the published version")
  })

  it("says it runs only by hand when nothing else starts it", () => {
    renderPage()
    tab("Triggers")
    expect(within(screen.getByTestId("routine-trigger-list")).getAllByRole("listitem")).toHaveLength(1)
    expect(screen.getByText("Runs only when someone presses Run.")).toBeInTheDocument()
    expect(screen.getByText("crewship routine run daily-triage")).toBeInTheDocument()
  })
})

describe("what a routine writes back to Crewship", () => {
  it("stays quiet for a routine with no crewship steps", () => {
    renderPage()
    tab("Settings")
    expect(screen.queryByTestId("routine-crewship-actions")).toBeNull()
  })

  it("finds a crewship step, also buried in a foreach body", () => {
    renderPage(routine({ definition: { steps: [{ id: "fan", type: "foreach", foreach: { items: "{{ steps.report.output }}", steps: [{ id: "file", type: "crewship", action: "issue.create" }] } }] } }))
    tab("Settings")
    expect(screen.getByTestId("routine-crewship-actions")).toHaveTextContent("issue.create")
  })
})

describe("overview", () => {
  it("answers the six questions without revealing defaults; What it does is written from the definition", () => {
    renderPage(
      routine({
        definition: {
          inputs: [
            { name: "max_stale_hours", type: "number", required: true, default: 24, description: "How old a page may be" },
            { name: "token", type: "string", default: "private-value" },
          ],
          outputs: [{ name: "change_report", type: "string" }],
          steps: [{ id: "probe", type: "script" }],
        },
      } as Partial<RoutineDetail>),
    )
    expect(screen.getByTestId("routine-look-what-you-provide")).toHaveTextContent("Max stale hours, Token (optional)")
    expect(screen.getByTestId("routine-look-what-you-get")).toHaveTextContent("Change report")
    expect(screen.getByTestId("routine-look-who-does-the-work")).toHaveTextContent("1 script on the crew share")
    expect(screen.getByTestId("routine-look-what-it-can-touch")).toHaveTextContent("Runs scripts on the crew. Stopping does not undo what already happened.")
    expect(screen.getByText("Written from the definition by the server, not by a model.")).toBeInTheDocument()
    expect(screen.getByTestId("routine-inputs")).toHaveTextContent("max_stale_hours")
  })

  it("puts a failed last run on top with what ran and what did not", () => {
    h.records = [{ id: "run-9", pipeline_id: "pipe-1", pipeline_slug: "daily-triage", status: "failed", mode: "run", started_at: "2026-08-07T09:00:00Z", cost_usd: 0, duration_ms: 10, triggered_via: "schedule", failed_at_step: "b", error_message: "HTTP 401" }]
    renderPage(routine({ definition: { steps: [{ id: "a", type: "transform", name: "Read settings" }, { id: "b", type: "http", name: "Read new issues" }, { id: "c", type: "notify", name: "Tell" }] } }))
    const failure = screen.getByTestId("routine-failure")
    expect(failure).toHaveTextContent("Could not finish")
    expect(failure).toHaveTextContent("step “Read new issues”")
    expect(within(failure).getByRole("button", { name: "Run again" })).toBeInTheDocument()
    expect(screen.getByTestId("routine-run-bars").children).toHaveLength(1)
  })

  it("PAR-128: one run draws one capped tick, not a slab as wide as the card", () => {
    h.records = [{ id: "run-1", pipeline_id: "pipe-1", pipeline_slug: "daily-triage", status: "completed", mode: "run", started_at: "2026-08-07T09:00:00Z", cost_usd: 0, duration_ms: 10, triggered_via: "schedule" }]
    renderPage(routine())
    const tick = screen.getByTestId("routine-run-bars").children[0]
    expect(tick.className).toMatch(/(^|\s)max-w-\d/)
  })

  it("PAR-127: Runs and history carries the run count, as in v67 (Runs and history · 5)", () => {
    renderPage(routine({ invocation_count: 5 } as Partial<RoutineDetail>))
    const link = screen.getByRole("link", { name: /Runs and history/ })
    expect(link).toHaveTextContent(/Runs and history\s*·\s*5/)
  })

  it("links credential requirements to accounts without inventing an assigned credential (Settings › Access)", () => {
    renderPage(routine({ manifest: { agents: ["morgan"], credentials: [{ type: "CLI_TOKEN", scope: "github" }, { type: "AI_CLI_TOKEN", scope: "gitlab" }], egress: ["api.github.com"] } } as Partial<RoutineDetail>))
    tab("Settings")
    expect(screen.getAllByRole("link", { name: /github|gitlab/i }).filter((link) => link.getAttribute("href") === "/credentials")).toHaveLength(2)
    expect(screen.getByText(/accounts are resolved when the run starts/)).toBeInTheDocument()
    const host = screen.getByText("api.github.com")
    expect(host.closest("details")).not.toHaveAttribute("open")
  })
})

describe("human decision summary", () => {
  it.each([3600, 0, undefined])("uses the engine step timeout (%s), not a wait-specific field", (timeout_seconds) => {
    const rows = inOneLook(routine({ definition: { steps: [{ id: "approve", name: "Finance approval", type: "wait", timeout_seconds, wait: { kind: "approval", timeout_sec: 30 } }] } }), [])
    expect(rows.find((row) => row.label === "When it needs you")?.text).toBe(`At “Finance approval”${timeout_seconds ? " · answer within 1 h" : ""}`)
  })
})
