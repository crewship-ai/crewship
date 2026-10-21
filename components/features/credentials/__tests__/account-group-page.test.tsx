// An account group's page (Harbor 2): Accounts · Order · Settings. The two
// writes that matter: Order saves with the revision it read (If-Match) and a
// 412 is shown as a conflict, not a silent overwrite; Delete goes through the
// shared confirm dialog.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { AccountGroupPage } from "../account-group-page"
import type { LoginCredential } from "@/lib/credentials/provider-logins"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }), usePathname: () => "/credentials" }))

function account(id: string, name: string): LoginCredential {
  return {
    id, name, provider: "ANTHROPIC", status: "ACTIVE",
    login: {
      mode: "subscription", provider: "ANTHROPIC", plan: null, plan_label: "Max", owner_user_id: null, owner_email: `${id}@example.test`,
      expires_at: null, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null }, quota: null,
      delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" }, pays_for: { agents: 1, crews: 0 },
    },
  }
}
const ACCOUNTS = [account("a1", "Claude Max · Pavel"), account("a2", "Claude Team · Jana")]
const POOL = {
  id: "p1", name: "Claude seats", provider: "ANTHROPIC", mode: "subscription", revision: 4, allow_cross_owner: true, member_count: 2,
  members: [{ credential_id: "a1", priority: 0 }, { credential_id: "a2", priority: 1 }],
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
}

function renderPage(over: Partial<React.ComponentProps<typeof AccountGroupPage>> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const props = {
    workspaceId: "ws1", poolId: "p1", tab: null, onTabChange: vi.fn(), accounts: ACCOUNTS, canManage: true,
    onBack: vi.fn(), onOpenAccount: vi.fn(), onDeleted: vi.fn(), ...over,
  }
  render(<QueryClientProvider client={client}><AccountGroupPage {...props} /></QueryClientProvider>)
  return props
}

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url === "/api/v1/provider-logins/pools/p1" && (!init?.method || init.method === "GET")) return json(POOL)
    return json({})
  })
})

describe("AccountGroupPage", () => {
  it("shows the group's accounts and says groups do not switch yet", async () => {
    renderPage()
    expect(await screen.findByRole("heading", { level: 1, name: "Claude seats" })).toBeInTheDocument()
    expect(screen.getAllByText(/Groups do not assign accounts or enable automatic switching yet/).length).toBeGreaterThan(0)
    const table = screen.getByRole("table", { name: "AI provider accounts" })
    expect(within(table).getByText("Claude Max · Pavel")).toBeInTheDocument()
    expect(within(table).getByText("Claude Team · Jana")).toBeInTheDocument()
  })

  it("order saves with If-Match", async () => {
    renderPage({ tab: "order" })
    fireEvent.click(await screen.findByRole("button", { name: "Move Claude Team · Jana up" }))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/provider-logins/pools/p1", expect.objectContaining({ method: "PUT" })))
    const [, init] = h.apiFetch.mock.calls.find(([, i]) => i?.method === "PUT")!
    expect(new Headers(init.headers).get("If-Match")).toBe('"4"')
    expect(JSON.parse(init.body)).toEqual({
      name: "Claude seats", allow_cross_owner: true,
      members: [{ credential_id: "a2", priority: 0 }, { credential_id: "a1", priority: 1 }],
    })
  })

  it("412 shows conflict and keeps the draft", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return new Response(null, { status: 412 })
      if (url === "/api/v1/provider-logins/pools/p1") return json(POOL)
      return json({})
    })
    renderPage({ tab: "order" })
    fireEvent.click(await screen.findByRole("button", { name: "Move Claude Team · Jana up" }))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/This group changed since you opened it/)
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument()
    // The draft is still the moved order.
    const items = within(screen.getByRole("list", { name: "Account order" })).getAllByRole("listitem")
    expect(items[0]).toHaveTextContent("Claude Team · Jana")
  })

  it("delete uses ConfirmDialog and names the revision", async () => {
    const props = renderPage({ tab: "settings" })
    fireEvent.click(await screen.findByRole("button", { name: /Delete…/ }))
    const dialog = await screen.findByRole("alertdialog")
    expect(within(dialog).getByText(/stay in the Vault/)).toBeInTheDocument()
    expect(h.apiFetch).not.toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ method: "DELETE" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete group" }))
    await waitFor(() => expect(props.onDeleted).toHaveBeenCalled())
    const [, init] = h.apiFetch.mock.calls.find(([, i]) => i?.method === "DELETE")!
    expect(new Headers(init.headers).get("If-Match")).toBe('"4"')
  })

  it("a group that is gone is not found, with a way back", async () => {
    h.apiFetch.mockResolvedValue(new Response(null, { status: 404 }))
    const props = renderPage()
    fireEvent.click(await screen.findByRole("button", { name: "Back to AI providers" }))
    expect(props.onBack).toHaveBeenCalled()
  })

  it("a reader who cannot manage groups gets no move buttons", async () => {
    renderPage({ tab: "order", canManage: false })
    // canManage false also stops the request: the group is OWNER/ADMIN only.
    await waitFor(() => expect(screen.queryByRole("button", { name: /Move/ })).not.toBeInTheDocument())
  })
})
