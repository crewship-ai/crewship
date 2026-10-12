import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { NotificationChannel, NotificationProvider } from "@/hooks/use-notification-channels"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"
import type { ComponentProps } from "react"
import type { IncomingTarget } from "../incoming-model"
import type { ConnectedAccount } from "../composio/types"
import type { ComposioStatus, ComposioIntegrationsProps } from "../composio-integrations"
const mocks = vi.hoisted(() => ({ admin: true, mobile: false, success: vi.fn(), error: vi.fn(), refreshIncoming: vi.fn(), invalidate: vi.fn(), channels: [] as NotificationChannel[], providers: [] as NotificationProvider[], deliveries: [] as NotificationDelivery[], configured: false, accounts: [] as ConnectedAccount[], targets: [] as IncomingTarget[], incomingError: null as string | null, incomingLoading: false }))
vi.mock("sonner", () => ({ toast: { success: mocks.success, error: mocks.error } }))
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
    React.useEffect(() => { const state: ComposioStatus = { loading: false, configured: mocks.configured, keyLabel: mocks.configured ? "Team" : null, counts: { apps: 10, accounts: mocks.accounts.length, users: mocks.accounts.length ? 1 : 0, agentsBound: 1, agentsTotal: 2, endpoints: 1 }, toolkits: [{ slug: "github", count: 1 }, { slug: "slack", count: 1 }], users: [{ id: "me", count: 1 }, { id: "someone", count: 1 }], accounts: mocks.accounts, agents: [], bindings: {} }; onStatus?.(state) }, [onStatus])
    return <div><p>Managed tools section: {section ?? "setup"}</p>{apiKeyOpen && <button onClick={() => onApiKeyOpenChange?.(false)}>Close API key editor</button>}</div>
  } }
})
vi.mock("../views/incoming-webhooks-view", () => ({ IncomingWebhooksView: (props: ComponentProps<typeof import("../views/incoming-webhooks-view").IncomingWebhooksView>) => <div><p>Incoming section: {props.section}</p><p>Selected incoming: {props.targetId ?? "none"}</p>{props.data.targets.map(target => <button key={target.id} onClick={() => props.onSelect(target)}>Open incoming {target.name}</button>)}<button onClick={props.onBack}>Back to incoming endpoints</button><button onClick={() => props.onAdd(props.data.targets[0])}>Add incoming endpoint</button></div> }))
vi.mock("../views/incoming-credentials", () => ({ IncomingCreateDialog: (props: ComponentProps<typeof import("../views/incoming-credentials").IncomingCreateDialog>) => <div role="dialog" aria-label="Incoming creation"><button onClick={props.onClose}>Close incoming creation</button><button onClick={() => props.onCreated(props.data.targets[0])}>Finish incoming creation</button></div> }))
vi.mock("../views/crew-tools-view", () => ({ CrewToolsView: (props: ComponentProps<typeof import("../views/crew-tools-view").CrewToolsView>) => <div><p>Crew tools for {props.workspaceId}</p><p>Linked server: {props.initialServerId ?? "none"}</p><button onClick={props.onServerConsumed}>Consume linked server</button></div> }))
import { apiFetch } from "@/lib/api-fetch"
import { IntegrationsLayout } from "../integrations-layout"
const ok = (body: unknown) => new Response(JSON.stringify(body))
const channel = (overrides: Partial<NotificationChannel> = {}): NotificationChannel => ({ id: "email", workspace_id: "ws", type: "email", to: "team@example.test", enabled: true, events: [], scope: "workspace", ...overrides })
beforeEach(() => {
  window.history.replaceState({}, "", "/integrations")
  mocks.admin = true; mocks.mobile = false; mocks.configured = false; mocks.accounts = []; mocks.targets = []; mocks.incomingError = null; mocks.incomingLoading = false; mocks.success.mockReset(); mocks.error.mockReset(); mocks.refreshIncoming.mockReset(); mocks.invalidate.mockReset()
  mocks.channels = [channel()]; mocks.providers = [{ provider: "slack", label: "Slack", blurb: "Team messaging", category: "chat", enabled: true, fields: [] }]; mocks.deliveries = []
  vi.mocked(apiFetch).mockReset().mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://localhost")
    if (url.pathname.endsWith("/test")) return ok({})
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

describe("Integrations workspace layout", () => {
  it("normalizes channels and narrows the connection list by search", async () => {
    mocks.channels.push(channel({ id: "webhook", type: "webhook", url: "https://example.test/hook", to: undefined }), channel({ id: "chat", type: "shoutrrr", provider: "slack", to: undefined }))
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByTitle("Open team@example.test")).toBeInTheDocument()
    expect(screen.getByTitle("Open https://example.test/hook")).toBeInTheDocument()
    expect(screen.getByTitle("Open slack")).toBeInTheDocument()
    fireEvent.change(screen.getByRole("textbox", { name: "Search connections and deliveries" }), { target: { value: "team@example" } })
    expect(screen.queryByTitle("Open https://example.test/hook")).not.toBeInTheDocument()
    expect(screen.getByTitle("Open team@example.test")).toBeInTheDocument()
  })
  it("sends tests, toggles a connection, and refreshes every feed", async () => {
    render(<IntegrationsLayout workspaceId="ws" />); await screen.findByTitle("Open team@example.test")
    fireEvent.click(screen.getByRole("button", { name: "Test" }))
    await waitFor(() => expect(mocks.success).toHaveBeenCalledWith("Test sent", { description: "to team@example.test" }))
    fireEvent.click(screen.getByRole("switch", { name: "Disable team@example.test" }))
    await screen.findByRole("switch", { name: "Enable team@example.test" })
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
    expect(mocks.refreshIncoming).toHaveBeenCalled(); expect(mocks.invalidate).toHaveBeenCalledWith("composio:ws:")
  })
  it("cancels deletion without removing the connection, then deletes only after confirmation", async () => {
    render(<IntegrationsLayout workspaceId="ws" />); await screen.findByTitle("Open team@example.test")
    fireEvent.click(screen.getByRole("button", { name: "Delete team@example.test" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
    expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: "Delete team@example.test" }))
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete connection" }))
    await waitFor(() => expect(mocks.success).toHaveBeenCalledWith("Connection deleted"))
    expect(screen.queryByTitle("Open team@example.test")).not.toBeInTheDocument()
  })
  it("discards an open delete confirmation when the workspace changes", async () => {
    const view = render(<IntegrationsLayout workspaceId="ws" />); await screen.findByTitle("Open team@example.test")
    fireEvent.click(screen.getByRole("button", { name: "Delete team@example.test" })); await screen.findByRole("alertdialog")
    mocks.channels = [channel({ id: "other", to: "other@example.test", workspace_id: "new" })]
    view.rerender(<IntegrationsLayout workspaceId="new" />)
    await screen.findByTitle("Open other@example.test")
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
    expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false)
  })
  it.each([true, false])("ignores a test-send completion from the previous workspace (success=%s)", async success => {
    const view = render(<IntegrationsLayout workspaceId="ws" />); await screen.findByTitle("Open team@example.test")
    let resolve!: (response: Response) => void
    vi.mocked(apiFetch).mockReturnValueOnce(new Promise<Response>(yes => { resolve = yes }))
    fireEvent.click(screen.getByRole("button", { name: "Test" }))
    mocks.channels = [channel({ id: "other", to: "other@example.test", workspace_id: "new" })]
    view.rerender(<IntegrationsLayout workspaceId="new" />)
    await screen.findByTitle("Open other@example.test")
    await act(async () => resolve(success ? ok({}) : new Response(JSON.stringify({ error: "Old refusal" }), { status: 403 })))
    expect(mocks.success).not.toHaveBeenCalled(); expect(mocks.error).not.toHaveBeenCalled()
  })

  it("opens a connection from preferences, returns to the list and keeps URL sections shareable", async () => {
    window.history.replaceState({}, "", "/integrations?section=preferences")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("Preferences for ws")).toBeInTheDocument()
    fireEvent.click(await screen.findByRole("button", { name: /team@example.test/ }))
    expect(await screen.findByRole("button", { name: "Back to connections" })).toBeInTheDocument()
    expect(new URL(window.location.href).searchParams.get("section")).toBe("connections")
    fireEvent.click(screen.getByRole("button", { name: "Back to connections" }))
    expect(await screen.findByTitle("Open team@example.test")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Deliveries/ }))
    await waitFor(() => expect(new URL(window.location.href).searchParams.get("section")).toBe("deliveries"))
  })
  it("treats other members' personal connections as read-only and includes them only on request", async () => {
    mocks.channels = [channel({ scope: "user", owner_user_id: "other", to: "" })]
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("not yours to edit")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Test" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("switch", { name: "Include everyone's personal connections" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([input]) => String(input).includes("scope=all"))).toBe(true))
  })
  it("opens the mobile rail as an overlay and closes it when tapping outside", async () => {
    mocks.mobile = true; render(<IntegrationsLayout workspaceId="ws" />)
    await screen.findByTitle("Open team@example.test")
    fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: "Close the integrations list" }))
    expect(screen.queryByRole("button", { name: "Close the integrations list" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeInTheDocument()
  })
  it("opens unconfigured tools setup and the API-key editor from its rail", async () => {
    window.history.replaceState({}, "", "/integrations?tab=tools")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("Managed tools section: setup")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Setup/ }))
    fireEvent.click(await screen.findByRole("button", { name: "Close API key editor" }))
    expect(screen.queryByRole("button", { name: "Close API key editor" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Crew tools/ }))
    expect(await screen.findByText("Crew tools for ws")).toBeInTheDocument()
  })
  it("honors a crew-tool deep link and consumes it once", async () => {
    window.history.replaceState({}, "", "/integrations?tab=tools&section=crew-tools&server=srv-one")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("Linked server: srv-one")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Consume linked server" }))
    expect(screen.getByText("Linked server: none")).toBeInTheDocument()
  })
  it("shows configured tool counts and controls the key editor", async () => {
    mocks.configured = true; window.history.replaceState({}, "", "/integrations?tab=tools")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("Connectable apps")).toBeInTheDocument()
    expect(screen.getByText("across 0 users")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /API key.*Team/ }))
    expect(screen.getByRole("button", { name: "Close API key editor" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /App catalog/ }))
    expect(await screen.findByText("Managed tools section: catalog")).toBeInTheDocument()
  })
  it("filters managed accounts by toolkit and user text, and opens their details", async () => {
    mocks.configured = true; mocks.accounts = [ { id: "a", user_id: "me", status: "ACTIVE", toolkit: { slug: "github", logo: "https://example.test/github.svg" } }, { id: "b", user_id: "someone", status: "expired", toolkit: { slug: "slack" } } ]
    window.history.replaceState({}, "", "/integrations?tab=tools")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("across 1 user")).toBeInTheDocument()
    const search = screen.getByRole("textbox", { name: "Search apps, tools and agents" })
    fireEvent.change(search, { target: { value: "someone" } })
    expect(screen.queryByRole("button", { name: /github.*me/ })).not.toBeInTheDocument()
    fireEvent.change(search, { target: { value: "github" } })
    fireEvent.click(screen.getByRole("button", { name: /github.*me/ }))
    expect(await screen.findByRole("button", { name: /Back to/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Back to/ }))
    expect(await screen.findByText("Managed tools section: accounts")).toBeInTheDocument()
  })
  it("opens incoming targets, updates the deep link, and starts and closes creation", async () => {
    mocks.targets = [{ id: "routine-one", slug: "routine", name: "Routine", kind: "routine" }]
    window.history.replaceState({}, "", "/integrations?tab=incoming")
    render(<IntegrationsLayout workspaceId="ws" />)
    fireEvent.click(await screen.findByRole("button", { name: "Open incoming Routine" }))
    expect(await screen.findByText("Selected incoming: routine-one")).toBeInTheDocument()
    expect(new URL(window.location.href).searchParams.get("target")).toBe("routine-one")
    fireEvent.click(screen.getByRole("button", { name: "Add incoming endpoint" }))
    fireEvent.click(await screen.findByRole("button", { name: "Close incoming creation" }))
    expect(screen.queryByRole("dialog", { name: "Incoming creation" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Back to incoming endpoints" }))
    expect(await screen.findByText("Incoming section: endpoints")).toBeInTheDocument()
    expect(new URL(window.location.href).searchParams.has("target")).toBe(false)
  })

  it.each(["E-mail", "Outgoing webhook", "Slack"])("opens the correct notification form for %s", async service => {
    render(<IntegrationsLayout workspaceId="ws" />); await screen.findByTitle("Open team@example.test")
    fireEvent.click(screen.getByRole("button", { name: "Add integration" }))
    const wizard = await screen.findByRole("dialog")
    fireEvent.click(within(wizard).getByRole("button", { name: /Outgoing notifications/ }))
    fireEvent.click(await within(wizard).findByTitle(`Connect ${service}`))
    expect(await screen.findByText(`Connect ${service === "Outgoing webhook" ? "outgoing webhook" : service}`)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Close" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  })
  it.each([true, false])("opens tools from the add wizard (configured=%s)", async configured => {
    mocks.configured = configured
    window.history.replaceState({}, "", "/integrations?tab=tools")
    render(<IntegrationsLayout workspaceId="ws" />)
    await screen.findByText(configured ? "Managed tools section: accounts" : "Managed tools section: setup")
    fireEvent.click(screen.getByRole("button", { name: "Add integration" }))
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /Tools & MCP/ }))
    if (configured) expect(await screen.findByText("Managed tools section: catalog")).toBeInTheDocument()
    else expect(await screen.findByRole("button", { name: "Close API key editor" })).toBeInTheDocument()
  })
  it("opens incoming creation from the add wizard and selects the created target", async () => {
    mocks.targets = [{ id: "a", name: "Worker", slug: "worker", kind: "agent" }]
    render(<IntegrationsLayout workspaceId="ws" />); await screen.findByTitle("Open team@example.test")
    fireEvent.click(screen.getByRole("button", { name: "Add integration" }))
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /Incoming webhook/ }))
    fireEvent.click(await screen.findByRole("button", { name: "Finish incoming creation" }))
    expect(await screen.findByText("Selected incoming: a")).toBeInTheDocument()
  })
  it.each(["toggle", "test", "delete"])("reports a refused %s and keeps the connection", async action => {
    render(<IntegrationsLayout workspaceId="ws" />); await screen.findByTitle("Open team@example.test")
    vi.mocked(apiFetch).mockRejectedValueOnce("Disconnected")
    if (action === "toggle") fireEvent.click(screen.getByRole("switch", { name: "Disable team@example.test" }))
    if (action === "test") fireEvent.click(screen.getByRole("button", { name: "Test" }))
    if (action === "delete") {
      fireEvent.click(screen.getByRole("button", { name: "Delete team@example.test" }))
      fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete connection" }))
    }
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith(...(action === "test" ? ["Test send failed", { description: undefined }] : [action === "toggle" ? "Failed to update the connection" : "Failed to delete the connection"])))
    expect(screen.getByTitle("Open team@example.test")).toBeInTheDocument()
  })
  it("derives recent delivery counts and failure status from the full channel history", async () => {
    mocks.channels = [channel(), channel({ id: "second", type: "shoutrrr", provider: undefined, to: undefined, categories: ["security"], enabled: false })]
    const delivery = (id: string, status: string, age: number): NotificationDelivery => ({ id, workspace_id: "ws", channel_id: "email", user_id: "me", category: "security", dedup_key: id, source_kind: "run", source_id: "run", title: id, status, attempts: 1, created_at: new Date(Date.now() - age).toISOString(), updated_at: new Date().toISOString() })
    mocks.deliveries = [delivery("old", "sent", 48 * 3600000), delivery("new", "sent", 2000), delivery("failed", "failed", 1000)]
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByTitle("Open team@example.test")).toBeInTheDocument()
    expect(screen.getByTitle("Open unknown")).toBeInTheDocument()
    expect(screen.getAllByText(/failing/i).length).toBeGreaterThan(0)
  })
  it("does not pretend a forbidden delivery log is empty", async () => {
    mocks.admin = false
    const normal = vi.mocked(apiFetch).getMockImplementation()!
    vi.mocked(apiFetch).mockImplementation(async (input, init) => String(input).includes("notification-deliveries") ? new Response(null, { status: 403 }) : normal(input, init))
    render(<IntegrationsLayout workspaceId="ws" />)
    await screen.findByTitle("Open team@example.test")
    expect(screen.queryByRole("switch", { name: "Include everyone's personal connections" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Deliveries/ }))
    await waitFor(() => expect(document.body.textContent).toMatch(/admin/i))
  })
  it.each(["loading", "error", "empty"])("explains incoming targets in state %s", async state => {
    mocks.incomingLoading = state === "loading"; mocks.incomingError = state === "error" ? "Unavailable" : null
    window.history.replaceState({}, "", "/integrations?tab=incoming")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText(state === "loading" ? "Loading targets…" : state === "error" ? "Targets unavailable. Use Refresh to retry." : "No matching targets. Create a routine, agent or Page first.")).toBeInTheDocument()
  })
  it("resolves an incoming target slug to its id and closes the mobile rail on selection", async () => {
    mocks.mobile = true; mocks.targets = [{ id: "a", name: "Worker", slug: "worker", kind: "agent" }]
    window.history.replaceState({}, "", "/integrations?tab=incoming&section=agent&target=worker")
    render(<IntegrationsLayout workspaceId="ws" />)
    expect(await screen.findByText("Selected incoming: a")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: /Endpoints/ }))
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }))
    fireEvent.click(within(document.querySelector("aside")!).getByRole("button", { name: /Worker/ }))
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeInTheDocument()
  })

})
