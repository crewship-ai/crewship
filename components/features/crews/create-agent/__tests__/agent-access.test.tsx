// =============================================================================
// Tools & notifications on New agent.
//
// An agent's integrations and notification channels are per-agent, not
// per-crew — a Security Analyst and a Copywriter in the same container should
// not necessarily hold the same tools. Neither is a field of
// POST /api/v1/agents: both endpoints are keyed on an agent that already
// exists, so the form collects intent and spends it after create.
//
// What this file mostly guards is the failure half. The agent is created
// before any binding is attempted, so a rejected grant is a partial result
// that has to be reported — an agent silently missing the tool it was created
// for is the bug worth a test.
// =============================================================================

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { CreateAgentDialog } from "../create-agent-dialog"
import { channelLabel } from "../agent-access"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

const CREWS = [{ id: "c1", slug: "engineering", name: "Engineering" }]

const INTEGRATIONS = [
  // Nobody bound yet → resolves for EVERY agent today, and the first binding
  // takes it away from all of them.
  { id: "i1", name: "github", display_name: "GitHub", transport: "http", enabled: true, agent_binding_count: 0 },
  { id: "i2", name: "jira", display_name: "Jira", transport: "stdio", enabled: false, agent_binding_count: 0 },
  // Already opt-in — granting it changes nothing for anyone else.
  { id: "i3", name: "linear", display_name: "Linear", transport: "http", enabled: true, agent_binding_count: 3 },
]
const CHANNELS = [
  { id: "ch1", type: "shoutrrr", provider: "slack", url: "https://hooks.slack.com/services/T/B/xoxb-secret", enabled: true, scope: "workspace" },
  { id: "ch2", type: "email", to: "ops@example.com", enabled: true, scope: "user" },
]

interface Calls { url: string; method: string; body: Record<string, unknown> | undefined }

/** Route-aware fetch. `bindFails` makes every post-create grant 500. */
function setupFetch({ bindFails = false } = {}) {
  const calls: Calls[] = []
  const spy = vi.spyOn(global, "fetch").mockImplementation(async (url, init) => {
    const u = String(url)
    const method = (init as RequestInit | undefined)?.method ?? "GET"
    let body: Record<string, unknown> | undefined
    const raw = (init as RequestInit | undefined)?.body
    if (typeof raw === "string") { try { body = JSON.parse(raw) } catch { /* not JSON */ } }
    calls.push({ url: u, method, body })

    if (u.includes("/api/v1/integrations") && !u.includes("/agents/")) {
      return new Response(JSON.stringify(INTEGRATIONS), { status: 200 })
    }
    if (u.includes("/api/v1/notification-channels") && !u.includes("/agents")) {
      return new Response(JSON.stringify({ channels: CHANNELS }), { status: 200 })
    }
    if (u.includes("/agents/") && u.includes("/integrations")) {
      return new Response("{}", { status: bindFails ? 500 : 201 })
    }
    if (u.includes("/notification-channels/") && u.includes("/agents")) {
      return new Response("{}", { status: bindFails ? 500 : 200 })
    }
    if (u.includes("/api/v1/agents") && method === "POST") {
      return new Response(JSON.stringify({ id: "a1", name: "Filip", slug: "filip" }), { status: 201 })
    }
    return new Response("{}", { status: 200 })
  })
  return { calls, spy }
}

function renderDialog() {
  return render(
    <CreateAgentDialog
      workspaceId="ws-1"
      open
      onOpenChange={vi.fn()}
      defaultCrewSlug="engineering"
      crews={CREWS}
      onCreated={vi.fn()}
    />,
  )
}

async function createWith(grants: RegExp[]) {
  renderDialog()
  fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
  for (const g of grants) {
    fireEvent.click(await screen.findByRole("switch", { name: g }))
  }
  fireEvent.click(screen.getByRole("button", { name: /create agent/i }))
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe("channelLabel", () => {
  it("names a webhook by host, never by path", () => {
    // For Slack and Discord the path IS the credential, and this is a
    // picker, not the channel's detail page.
    expect(channelLabel(CHANNELS[0])).toBe("slack · hooks.slack.com")
    expect(channelLabel(CHANNELS[0])).not.toContain("xoxb-secret")
  })

  it("names an email channel by its destination", () => {
    expect(channelLabel(CHANNELS[1])).toBe("email · ops@example.com")
  })

  it("falls back to the kind when a URL will not parse", () => {
    expect(channelLabel({ id: "x", type: "webhook", url: "not a url", enabled: true })).toBe("webhook")
  })
})

describe("<CreateAgentDialog> — Tools & notifications", () => {
  it("offers what the workspace has, keyed on the agent rather than the crew", async () => {
    setupFetch()
    renderDialog()

    expect(await screen.findByRole("switch", { name: "GitHub" })).toBeInTheDocument()
    expect(screen.getByRole("switch", { name: "slack · hooks.slack.com" })).toBeInTheDocument()
    expect(screen.getByText(/what this agent may reach/i)).toBeInTheDocument()
  })

  // integration_resolve.go: `if !hasBind && serversWithBindings[id] { continue }`.
  // A server with zero bindings resolves for every agent; the first binding
  // flips it to opt-in and silently revokes it from all the others. Before
  // this form existed you needed `crewship integration bind` to do that — now
  // a switch does, so the switch has to say so.
  it("warns that the first grant revokes the integration from every other agent", async () => {
    setupFetch()
    renderDialog()
    fireEvent.click(await screen.findByRole("switch", { name: "GitHub" }))

    const warning = await screen.findByText(/available to every agent/i)
    expect(warning).toHaveTextContent(/GitHub/)
    expect(warning).toHaveTextContent(/every OTHER agent loses access/i)

    // It APPEARS in response to a switch, so it needs a live region —
    // CreateSurfaceNotice gives role="alert" only to tone="error", and warn is
    // right here because nothing is blocked. Without this a screen-reader user
    // flips the switch and is told nothing about what it cost.
    expect(warning.closest('[role="status"]')).not.toBeNull()
    expect(warning.closest('[aria-live="polite"]')).not.toBeNull()
  })

  it("says nothing when the integration is already opt-in", async () => {
    // Linear has 3 bindings, so granting a 4th takes nothing from anyone.
    setupFetch()
    renderDialog()
    fireEvent.click(await screen.findByRole("switch", { name: "Linear" }))

    await waitFor(() => expect(screen.getByRole("switch", { name: "Linear" })).toBeChecked())
    expect(screen.queryByText(/available to every agent/i)).toBeNull()
  })

  it("drops the warning again when the grant is taken back", async () => {
    setupFetch()
    renderDialog()
    const sw = await screen.findByRole("switch", { name: "GitHub" })
    fireEvent.click(sw)
    await screen.findByText(/available to every agent/i)

    fireEvent.click(sw)
    await waitFor(() => expect(screen.queryByText(/available to every agent/i)).toBeNull())
  })

  it("will not grant an integration the workspace has switched off", async () => {
    setupFetch()
    renderDialog()
    // Granting it would be a promise the platform breaks: a disabled
    // workspace integration reaches nothing.
    expect(await screen.findByRole("switch", { name: "Jira" })).toBeDisabled()
  })

  it("binds after create, because neither endpoint accepts an agent that does not exist", async () => {
    const { calls } = setupFetch()
    await createWith([/GitHub/, /ops@example\.com/])

    await waitFor(() => expect(toast.success).toHaveBeenCalled())

    const create = calls.findIndex((c) => c.url.includes("/api/v1/agents?") && c.method === "POST")
    const bindTool = calls.findIndex((c) => c.url.includes("/agents/a1/integrations"))
    const bindChan = calls.findIndex((c) => c.url.includes("/notification-channels/ch2/agents"))

    expect(create).toBeGreaterThanOrEqual(0)
    expect(bindTool).toBeGreaterThan(create)
    expect(bindChan).toBeGreaterThan(create)

    // The two routes are mirrors of each other — one takes the server in the
    // body, the other takes the agent.
    expect(calls[bindTool].body).toMatchObject({ mcp_server_id: "i1", mcp_server_scope: "workspace" })
    expect(calls[bindChan].body).toEqual({ agent_id: "a1" })
  })

  it("makes no grant calls when nothing was picked", async () => {
    const { calls } = setupFetch()
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
    fireEvent.click(screen.getByRole("button", { name: /create agent/i }))

    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(calls.filter((c) => c.url.includes("/agents/a1/"))).toHaveLength(0)
  })

  it("says which grants did not apply rather than reporting a clean create", async () => {
    setupFetch({ bindFails: true })
    await createWith([/GitHub/, /ops@example\.com/])

    // The agent exists — this is a partial result, not a failure to unwind.
    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(toast.success).not.toHaveBeenCalled()

    const [headline, opts] = (toast.warning as unknown as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(headline).toMatch(/2 grants did not apply/)
    expect(opts.description).toMatch(/GitHub/)
    expect(opts.description).toMatch(/ops@example\.com/)
    expect(opts.description).toMatch(/agent's canvas/)
  })

  it("still creates the agent when the catalogues cannot be read", async () => {
    vi.spyOn(global, "fetch").mockImplementation(async (url, init) => {
      const u = String(url)
      if (u.includes("/api/v1/integrations") || u.includes("/api/v1/notification-channels")) {
        return new Response("nope", { status: 500 })
      }
      if ((init as RequestInit | undefined)?.method === "POST") {
        return new Response(JSON.stringify({ id: "a1", name: "Filip", slug: "filip" }), { status: 201 })
      }
      return new Response("{}", { status: 200 })
    })
    renderDialog()

    // A warn notice, not an error one: the catalogue failing does not stop
    // anyone creating an agent, so it must not claim the surface's alert role.
    expect(await screen.findByText(/did not load/i)).toBeInTheDocument()
    expect(screen.queryByRole("alert")).toBeNull()

    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
    fireEvent.click(screen.getByRole("button", { name: /create agent/i }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})
