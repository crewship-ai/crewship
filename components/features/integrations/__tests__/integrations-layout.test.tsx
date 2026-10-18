import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { NotificationChannel, NotificationProvider } from "@/hooks/use-notification-channels"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"
import type { ComponentProps } from "react"
import type { IncomingTarget } from "../incoming-model"
import type { AgentLite, ConnectedAccount } from "../composio/types"
import type { ComposioStatus, ComposioIntegrationsProps } from "../composio-integrations"
const mocks = vi.hoisted(() => ({ admin: true, mobile: false, success: vi.fn(), error: vi.fn(), refreshIncoming: vi.fn(), invalidate: vi.fn(), channels: [] as NotificationChannel[], providers: [] as NotificationProvider[], deliveries: [] as NotificationDelivery[], configured: false, accounts: [] as ConnectedAccount[], targets: [] as IncomingTarget[], incomingError: null as string | null, incomingLoading: false, agents: [] as AgentLite[] }))
vi.mock("@/lib/toast", () => ({ toast: { success: mocks.success, error: mocks.error } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ abilities: { can: () => mocks.admin } }) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: "me" } } }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mocks.mobile }))
vi.mock("@/lib/stale-cache", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/stale-cache")>(), invalidate: mocks.invalidate }))
vi.mock("@/lib/api-fetch", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: vi.fn() }))
vi.mock("../use-incoming-endpoints", async importOriginal => ({ ...await importOriginal<typeof import("../use-incoming-endpoints")>(), useIncomingEndpoints: () => ({ rows: [], targets: mocks.targets, loading: mocks.incomingLoading, error: mocks.incomingError, refresh: mocks.refreshIncoming }) }))
vi.mock("../notification-prefs-section", () => ({ NotificationPrefsSection: ({ workspaceId }: { workspaceId: string }) => <p>Preferences for {workspaceId}</p> }))
vi.mock("../composio-integrations", async () => {
  const React = await import("react")
  return { ComposioIntegrations: ({ onStatus, apiKeyOpen, onApiKeyOpenChange, section }: ComposioIntegrationsProps) => {
    React.useEffect(() => { const state: ComposioStatus = { loading: false, configured: mocks.configured, keyLabel: mocks.configured ? "Team" : null, counts: { apps: 10, accounts: mocks.accounts.length, users: mocks.accounts.length ? 1 : 0, agentsBound: 1, agentsTotal: 2, endpoints: 1 }, toolkits: [{ slug: "github", count: 1 }, { slug: "slack", count: 1 }], users: [{ id: "me", count: 1 }, { id: "someone", count: 1 }], accounts: mocks.accounts, agents: mocks.agents, bindings: {} }; onStatus?.(state) }, [onStatus])
    return <div><p hidden>{section}</p>{apiKeyOpen && <button onClick={() => onApiKeyOpenChange?.(false)}>Close API key editor</button>}</div>
  } }
})
vi.mock("../views/incoming-webhooks-view", () => ({ RecentReceipts: () => <p>Receipts</p>, IncomingWebhooksView: (props: ComponentProps<typeof import("../views/incoming-webhooks-view").IncomingWebhooksView>) => <div><p>Incoming section: {props.section}</p><p>Selected incoming: {props.targetId ?? "none"}</p>{props.data.targets.map(target => <button key={target.id} onClick={() => props.onSelect(target)}>Open incoming {target.name}</button>)}<button onClick={props.onBack}>Back to incoming endpoints</button><button onClick={() => props.onAdd(props.data.targets[0])}>Add incoming endpoint</button></div> }))
vi.mock("../new-trigger-dialog", () => ({ NewTriggerDialog: (props: ComponentProps<typeof import("../new-trigger-dialog").NewTriggerDialog>) => <div role="dialog" aria-label="Incoming creation"><button onClick={props.onClose}>Close incoming creation</button><button onClick={() => props.onCreated(props.data.targets[0])}>Finish incoming creation</button></div> }))
vi.mock("../views/crew-tools-view", () => ({ CrewToolsView: (props: ComponentProps<typeof import("../views/crew-tools-view").CrewToolsView>) => <div><p>Crew tools for {props.workspaceId}</p><p>Linked server: {props.initialServerId ?? "none"}</p><button onClick={props.onServerConsumed}>Consume linked server</button></div> }))
import { apiFetch } from "@/lib/api-fetch"
import { useAppStore } from "@/lib/store"
import { IntegrationsLayout } from "../integrations-layout"
const ok = (body: unknown) => new Response(JSON.stringify(body))
const channel = (overrides: Partial<NotificationChannel> = {}): NotificationChannel => ({ id: "email", workspace_id: "ws", type: "email", to: "team@example.test", enabled: true, events: [], scope: "workspace", ...overrides })
beforeEach(() => {
  window.history.replaceState({}, "", "/integrations")
  try { window.localStorage.removeItem("crewship.pref.panel.integrations.collapsed") } catch { /* no storage */ }
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
  mocks.admin = true; mocks.mobile = false; mocks.configured = false; mocks.accounts = []; mocks.targets = []; mocks.incomingError = null; mocks.incomingLoading = false; mocks.agents = []; mocks.success.mockReset(); mocks.error.mockReset(); mocks.refreshIncoming.mockReset(); mocks.invalidate.mockReset()
  mocks.channels = [channel()]; mocks.providers = [{ provider: "slack", label: "Slack", blurb: "Team messaging", category: "chat", enabled: true, fields: [] }]; mocks.deliveries = []
  vi.mocked(apiFetch).mockReset().mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://localhost")
    if (url.pathname.endsWith("/test")) return ok({})
    if (url.pathname.startsWith("/api/v1/me/preferences")) return ok({})
    if (init?.method === "DELETE") { mocks.channels = []; return new Response(null, { status: 204 }) }
    if (init?.method === "PATCH") { mocks.channels = mocks.channels.map(c => ({ ...c, ...JSON.parse(String(init.body)) })); return ok({}) }
    if (url.pathname === "/api/v1/notification-channels") return ok({ channels: mocks.channels })
    if (url.pathname === "/api/v1/notification-providers") return ok({ providers: mocks.providers, categories: [{ key: "chat", label: "Chat" }] })
    if (url.pathname.endsWith("/agents")) return ok({ agents: [] })
    if (url.pathname === "/api/v1/notification-deliveries") return ok({ deliveries: mocks.deliveries })
    throw new Error(`Unhandled test request ${input}`)
  })
})
afterEach(cleanup)

beforeEach(() => {
  try { window.localStorage.removeItem("crewship.pref.integrations.space") } catch { /* no storage */ }
})

describe("Integrations on four spaces", () => {
  it("opens Tools by default and shows its primary action", async () => {
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "Tools" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Connect an app" })).toBeInTheDocument()
    expect(new URL(window.location.href).searchParams.get("space")).toBe("tools")
  })

  it("Z12: an old Notifications link lands on Channels and is rewritten", async () => {
    window.history.replaceState({}, "", "/integrations?tab=notifications&section=connections")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "Channels" })).toBeInTheDocument()
    expect(await screen.findByRole("row", { name: "Open team@example.test" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Add channel" })).toBeInTheDocument()
    const url = new URL(window.location.href)
    expect(url.searchParams.get("space")).toBe("channels")
    expect(url.searchParams.get("tab")).toBeNull()
  })

  it("Z13: the preferences link lands on My notifications", async () => {
    window.history.replaceState({}, "", "/integrations?tab=notifications&section=preferences")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("Preferences for ws")).toBeInTheDocument()
    expect(screen.getByRole("heading", { level: 1, name: "My notifications" })).toBeInTheDocument()
  })

  it("type=webhook rows go to Webhooks, others to Channels", async () => {
    mocks.channels.push(channel({ id: "wh", type: "webhook", url: "https://example.test/hook", to: undefined }))
    window.history.replaceState({}, "", "/integrations?space=channels")
    render(<IntegrationsLayout workspaceId="ws" />)
    await screen.findByRole("row", { name: "Open team@example.test" })
    expect(screen.queryByRole("row", { name: "Open example.test/hook" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("tab", { name: /Webhooks/ }))
    expect(await screen.findByRole("row", { name: "Open example.test/hook" })).toBeInTheDocument()
    expect(screen.queryByRole("row", { name: "Open team@example.test" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Add webhook" })).toBeInTheDocument()
  })

  it("N02: ?id= opens the channel page; a reload keeps it; back returns to the list", async () => {
    window.history.replaceState({}, "", "/integrations?space=channels&id=email")
    const view = render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "team@example.test" })).toBeInTheDocument()
    view.unmount()
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "team@example.test" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Channels" }))
    expect(await screen.findByRole("row", { name: "Open team@example.test" })).toBeInTheDocument()
    expect(new URL(window.location.href).searchParams.get("id")).toBeNull()
  })

  it("opening a row puts the channel in the URL", async () => {
    window.history.replaceState({}, "", "/integrations?space=channels")
    render(<IntegrationsLayout workspaceId="ws" />)
    fireEvent.click(await screen.findByRole("row", { name: "Open team@example.test" }))
    await screen.findByRole("heading", { level: 1, name: "team@example.test" })
    expect(new URL(window.location.href).searchParams.get("id")).toBe("email")
  })

  it("N01: a MEMBER sees workspace channels read-only and can still add one", async () => {
    mocks.admin = false
    window.history.replaceState({}, "", "/integrations?space=channels&id=email&pane=settings")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("switch", { name: "Paused" })).toBeDisabled()
    expect(screen.queryByRole("button", { name: "Remove…" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Add channel" })).toBeEnabled()
  })

  it("removes a channel only after confirmation", async () => {
    window.history.replaceState({}, "", "/integrations?space=channels&id=email&pane=settings")
    render(<IntegrationsLayout workspaceId="ws" />)
    fireEvent.click(await screen.findByRole("button", { name: "Remove…" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
    expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: "Remove…" }))
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Remove channel" }))
    await waitFor(() => expect(mocks.success).toHaveBeenCalledWith("Channel removed"))
  })

  it("legacy incoming deep link opens filtered triggers", async () => {
    window.history.replaceState({}, "", "/integrations?tab=incoming&section=routine&target=review-pr")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "Triggers of routines" })).toBeInTheDocument()
    const url = new URL(window.location.href)
    expect(url.searchParams.get("space")).toBe("triggers")
    expect(url.searchParams.get("starts")).toBe("routine")
    expect(url.searchParams.get("target")).toBe("review-pr")
    expect(screen.getAllByRole("button", { name: "New trigger" }).length).toBeGreaterThan(0)
  })

  it("N15: a crew-tools link with a server reaches Crew MCP servers", async () => {
    window.history.replaceState({}, "", "/integrations?tab=tools&section=crew-tools&server=srv_1")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("Linked server: srv_1")).toBeInTheDocument()
  })

  it("N14: ?section=mcp lands on Tools without error", async () => {
    window.history.replaceState({}, "", "/integrations?tab=tools&section=mcp")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "MCP endpoints" })).toBeInTheDocument()
  })

  it("PAR-083: without a Composio key, Tools shows v67's empty state with one way to add the key", async () => {
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { name: "Nothing connected" })).toBeInTheDocument()
    expect(screen.getByText(/Give agents tools, start work from outside and reach people\./)).toBeInTheDocument()
    // The banner and the empty card used to ask for the same thing twice.
    expect(screen.queryByText("Add a Composio API key to connect apps")).toBeNull()
    const adds = screen.getAllByRole("button", { name: /Add (Composio )?key/ })
    expect(adds).toHaveLength(1)
    fireEvent.click(adds[0])
    expect(await screen.findByRole("button", { name: "Close API key editor" })).toBeInTheDocument()
  })

  it("PAR-083: a person who cannot manage sees the empty state without a key button", async () => {
    mocks.admin = false
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { name: "Nothing connected" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Add (Composio )?key/ })).toBeNull()
    expect(screen.getByText(/Ask an admin to add the workspace's Composio API key/)).toBeInTheDocument()
  })

  it("PAR-083: with accounts the KPI strip and the table carry real figures", async () => {
    mocks.configured = true
    mocks.accounts = [
      { id: "ca_1", user_id: "me", status: "ACTIVE", toolkit: { slug: "github" } },
      { id: "ca_2", user_id: "me", status: "EXPIRED", toolkit: { slug: "slack" } },
    ]
    render(<IntegrationsLayout workspaceId="ws" />)
    const strip = await screen.findByRole("group", { name: "Tools at a glance" })
    expect(within(strip).getByText("Connected apps").closest("button")).toHaveTextContent("2")
    expect(within(strip).getByText("Signed out").closest("button")).toHaveTextContent("1")
    expect(screen.getByText("2 apps")).toBeInTheDocument()
    const head = screen.getAllByRole("columnheader").map((c) => c.textContent)
    expect(head).toEqual(["App", "Account", "Access", "State"])
  })

  it("PAR-084: the breadcrumb's space is a dropdown that switches space", async () => {
    render(<IntegrationsLayout workspaceId="ws" />)
    await screen.findByRole("heading", { level: 1, name: "Tools" })
    const trigger = screen.getByRole("button", { name: "Integrations space: Tools" })
    fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
    fireEvent.click(trigger)
    const items = await screen.findAllByRole("menuitemradio")
    expect(items.map((i) => i.textContent)).toEqual(["Tools", "Triggers", "Channels", "Webhooks"])
    fireEvent.click(items[2])
    expect(await screen.findByRole("heading", { level: 1, name: "Channels" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Integrations space: Channels" })).toBeInTheDocument()
  })

  it("lists connected accounts with per-agent access", async () => {
    mocks.configured = true
    mocks.accounts = [{ id: "ca_1", user_id: "me", status: "ACTIVE", toolkit: { slug: "github" } }]
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("row", { name: "Open Github" })).toHaveTextContent("Not given to any agent")
    fireEvent.click(screen.getByRole("row", { name: "Open Github" }))
    expect(await screen.findByRole("heading", { level: 1, name: "Github" })).toBeInTheDocument()
  })

  it("N12: the panel search finds a tool across apps and opens the app on Tools", async () => {
    mocks.configured = true
    mocks.accounts = [
      { id: "ca_1", user_id: "me", status: "ACTIVE", toolkit: { slug: "github" } },
      { id: "ca_2", user_id: "me", status: "ACTIVE", toolkit: { slug: "slack" } },
    ]
    const base = vi.mocked(apiFetch).getMockImplementation()!
    vi.mocked(apiFetch).mockImplementation(async (input, init) => {
      const url = new URL(String(input), "http://localhost")
      if (url.pathname === "/api/v1/integrations/composio/tools") {
        const github = url.searchParams.get("toolkit") === "github"
        return ok({ enabled: true, total: github ? 1 : 0, tools: github ? [{ slug: "GITHUB_CREATE_PULL_REQUEST", name: "Create a pull request", description: "", toolkit: { slug: "github" } }] : [] })
      }
      return base(input, init)
    })
    render(<IntegrationsLayout workspaceId="ws" />)
    await screen.findByRole("row", { name: "Open Github" })
    fireEvent.change(screen.getByLabelText("Search apps and tools"), { target: { value: "pull request" } })
    fireEvent.click(await screen.findByRole("button", { name: "Open GITHUB_CREATE_PULL_REQUEST in Github" }))
    expect(await screen.findByRole("heading", { level: 1, name: "Github" })).toBeInTheDocument()
    const url = new URL(window.location.href)
    expect(url.searchParams.get("id")).toBe("ca_1")
    expect(url.searchParams.get("pane")).toBe("tools")
    expect(await screen.findByRole("row", { name: /GITHUB_CREATE_PULL_REQUEST/ })).toHaveAttribute("aria-current", "true")
  })

  it("INT-03: app access edits wait on the page Save bar", async () => {
    mocks.configured = true
    mocks.accounts = [{ id: "ca_1", user_id: "me", status: "ACTIVE", toolkit: { slug: "github" } }]
    window.history.replaceState({}, "", "/integrations?space=tools&view=connected&id=ca_1&pane=access")
    mocks.agents = [{ id: "a1", name: "Reviewer", slug: "reviewer", crew: { name: "Code review" } }]
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { level: 1, name: "Github" })).toBeInTheDocument()
    const before = vi.mocked(apiFetch).mock.calls.length
    fireEvent.click(within(screen.getByRole("group", { name: "Github access for Reviewer" })).getByRole("button", { name: "Full" }))
    expect(await screen.findByRole("region", { name: "Unsaved changes" })).toHaveTextContent("1 unsaved change")
    expect(vi.mocked(apiFetch).mock.calls.slice(before).filter(([, init]) => init?.method === "POST")).toEqual([])
  })

  it("keeps the panel in the integrations SecondaryPanel with the four spaces", async () => {
    render(<IntegrationsLayout workspaceId="ws" />)
    const panel = await waitFor(() => document.querySelector<HTMLElement>("[data-slot=secondary-panel]")!)
    expect(panel).toHaveAttribute("data-panel-id", "integrations")
    const spaces = within(panel).getByRole("tablist", { name: "Spaces" })
    expect(within(spaces).getAllByRole("tab").map((t) => t.textContent?.replace(/\d+$/, ""))).toEqual(["Tools", "Triggers", "Channels", "Webhooks"])
  })

  it("on a phone the spaces are a segment above the content", async () => {
    mocks.mobile = true
    render(<IntegrationsLayout workspaceId="ws" />)
    const seg = await screen.findByRole("tablist", { name: "Spaces" })
    fireEvent.click(within(seg).getByRole("tab", { name: "Channels" }))
    expect(await screen.findByRole("heading", { level: 1, name: "Channels" })).toBeInTheDocument()
  })

  it("an empty space has its own call to action beside the one top-bar primary", async () => {
    mocks.channels = []
    window.history.replaceState({}, "", "/integrations?space=channels")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByRole("heading", { name: "No channels yet" })).toBeInTheDocument()
    const primaries = document.querySelectorAll("[data-action=space-primary]")
    expect(primaries).toHaveLength(1)
    expect(primaries[0]).toHaveAccessibleName("Add channel")
    fireEvent.click(primaries[0])
    expect(await screen.findByRole("navigation", { name: "Add channel steps" })).toBeInTheDocument()
  })

  it("PAR-139: artifact webhooks are rows of the Triggers table, not a card of their own", async () => {
    mocks.targets = [{ id: "pg1", slug: "pricing", name: "Pricing", kind: "page" }]
    window.history.replaceState({}, "", "/integrations?space=triggers")
    render(<IntegrationsLayout workspaceId="ws" />)
    const row = await screen.findByRole("row", { name: "Open webhooks of Pricing" })
    expect(row).toHaveTextContent("Artifact")
    expect(row).toHaveTextContent("Token in the URL")
    expect(screen.queryByText(/webhooks are kept per artifact/)).toBeNull()
  })

  it("keys 1–4 switch spaces", async () => {
    render(<IntegrationsLayout workspaceId="ws" />)
    await screen.findByRole("heading", { level: 1, name: "Tools" })
    fireEvent.keyDown(window, { key: "4" })
    expect(await screen.findByRole("heading", { level: 1, name: "Webhooks" })).toBeInTheDocument()
  })
})
