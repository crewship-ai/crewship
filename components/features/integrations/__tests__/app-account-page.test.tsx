import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/lib/toast", () => ({ toast: { success: h.success, error: h.error, info: vi.fn(), message: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"
import { AppAccountPage, type AppPane } from "../tools-space"
import { accountRows } from "../tools-model"
import type { ComposioStatus } from "../composio-integrations"
import type { AgentLite, ConnectedAccount } from "../composio/types"

const gh: ConnectedAccount = { id: "ca_gh", user_id: "u1", status: "ACTIVE", toolkit: { slug: "github" } }
const a1: AgentLite = { id: "a1", name: "Reviewer", slug: "reviewer", crew: { name: "Code review" } }
const a2: AgentLite = { id: "a2", name: "Fixer", slug: "fixer", crew: { name: "Code review" } }
const a3: AgentLite = { id: "a3", name: "Scout", slug: "scout", crew: { name: "Research" } }
const bindings = {
  a1: [{ toolkit: "github", mode: "full" as const, user_id: "u1", endpoint: "" }],
  a3: [{ toolkit: "github", mode: "read" as const, user_id: "u1", endpoint: "" }],
}
const status: ComposioStatus = {
  loading: false,
  configured: true,
  keyLabel: "Team",
  counts: { apps: 1, accounts: 1, users: 1, agentsBound: 2, agentsTotal: 3, endpoints: 0 },
  toolkits: [],
  users: [],
  accounts: [gh],
  agents: [a1, a2, a3],
  bindings,
}

function show(pane: AppPane = "access", toolFocus?: string) {
  const onChanged = vi.fn()
  render(
    <PageSaveProvider>
      <AppAccountPage
        workspaceId="ws"
        row={accountRows([gh], status.agents, bindings)[0]}
        status={status}
        pane={pane}
        onPane={vi.fn()}
        canManage
        onBack={vi.fn()}
        onChanged={onChanged}
        onGone={vi.fn()}
        onTriggers={vi.fn()}
        toolFocus={toolFocus}
      />
      <PageSaveBar />
    </PageSaveProvider>,
  )
  return onChanged
}
const segment = (agent: string) => screen.getByRole("group", { name: `Github access for ${agent}` })
const choose = (agent: string, level: string) => fireEvent.click(within(segment(agent)).getByRole("button", { name: level }))
const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })
const calls = () => vi.mocked(apiFetch).mock.calls.map(([url, init]) => [String(url), init?.method ?? "GET", init?.body ? JSON.parse(String(init.body)) : undefined])

beforeEach(() => {
  h.error.mockReset()
  h.success.mockReset()
  vi.mocked(apiFetch).mockReset().mockResolvedValue(new Response("{}", { status: 200 }))
})
afterEach(cleanup)

// INT-03: app access is a draft on the page's Save bar, not a write per click.
describe("AppAccountPage › Access on the Save bar", () => {
  it("a click changes the draft only; the bar counts the agents changed", () => {
    show()
    choose("Reviewer", "Read-only")
    choose("Fixer", "Full")
    expect(within(segment("Reviewer")).getByRole("button", { name: "Read-only" })).toHaveAttribute("aria-pressed", "true")
    expect(bar()).toHaveTextContent("2 unsaved changes")
    expect(apiFetch).not.toHaveBeenCalled()
    expect(h.success).not.toHaveBeenCalled()
  })

  it("choosing the saved level again is no change", () => {
    show()
    choose("Reviewer", "Read-only")
    choose("Reviewer", "Full")
    expect(bar()).toBeNull()
  })

  it("Save writes one bind per changed agent and Off unbinds just this app", async () => {
    const onChanged = show()
    choose("Reviewer", "Read-only")
    choose("Fixer", "Full")
    choose("Scout", "Off")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(3))
    expect(calls()).toEqual([
      ["/api/v1/integrations/composio/agents/a1/bind?workspace_id=ws", "POST", { user_id: "u1", apps: [{ toolkit: "github", mode: "read" }] }],
      ["/api/v1/integrations/composio/agents/a2/bind?workspace_id=ws", "POST", { user_id: "u1", apps: [{ toolkit: "github", mode: "full" }] }],
      ["/api/v1/integrations/composio/agents/a3/bind?workspace_id=ws&toolkit=github", "DELETE", undefined],
    ])
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    expect(h.success).not.toHaveBeenCalled()
  })

  it("Discard puts every agent back", () => {
    show()
    choose("Reviewer", "Off")
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(within(segment("Reviewer")).getByRole("button", { name: "Full" })).toHaveAttribute("aria-pressed", "true")
    expect(bar()).toBeNull()
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("a refused agent keeps its draft and is named in the corner; the others are saved", async () => {
    vi.mocked(apiFetch).mockImplementation(async (url) =>
      String(url).includes("/agents/a1/") ? new Response(JSON.stringify({ detail: "agent is archived" }), { status: 409 }) : new Response("{}", { status: 200 }),
    )
    const onChanged = show()
    choose("Reviewer", "Read-only")
    choose("Fixer", "Full")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(h.error).toHaveBeenCalled())
    expect(h.error.mock.calls[0][0]).toBe("Couldn’t save Github access")
    expect(h.error.mock.calls[0][1].description).toContain("Reviewer: agent is archived")
    expect(onChanged).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(bar()).toHaveTextContent("1 unsaved change"))
    expect(within(segment("Reviewer")).getByRole("button", { name: "Read-only" })).toHaveAttribute("aria-pressed", "true")
  })
})

// INT-04: a tool found from the panel search opens on the app's Tools tab.
describe("AppAccountPage › Tools with a focused tool", () => {
  it("searches the app's tools for the found tool and marks its row", async () => {
    vi.mocked(apiFetch).mockResolvedValue(
      new Response(JSON.stringify({ enabled: true, total: 1, tools: [{ slug: "GITHUB_CREATE_PULL_REQUEST", name: "Create a pull request", description: "", toolkit: { slug: "github" } }] }), { status: 200 }),
    )
    show("tools", "GITHUB_CREATE_PULL_REQUEST")
    const row = await screen.findByRole("row", { name: /GITHUB_CREATE_PULL_REQUEST/ })
    expect(row).toHaveAttribute("aria-current", "true")
    expect(String(vi.mocked(apiFetch).mock.calls[0][0])).toContain("search=GITHUB_CREATE_PULL_REQUEST")
  })
})
