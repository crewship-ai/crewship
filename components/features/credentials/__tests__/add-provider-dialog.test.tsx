// Add AI provider — its own large dialog (Harbor 2): Provider → Connect →
// Pays for. The providers are the provider-login API's catalog, never the
// decorative brand list; Sign in again opens at Connect with the account's
// name locked; the last step writes bindings and can join an account group.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { AddProviderDialog } from "../add-provider-dialog"
import { LOGIN_PROVIDERS } from "@/lib/credentials/login-providers"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/hooks/use-abilities", async () => {
  const { defineAbilitiesFor } = await import("@/lib/permissions/abilities")
  return { useAbilities: () => ({ abilities: defineAbilitiesFor("OWNER" as never), role: "OWNER", capabilities: [], hasCapability: () => false, loading: false }) }
})

function ok(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as unknown as Response
}

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async () => ok([]))
})

function renderDialog(over: Partial<React.ComponentProps<typeof AddProviderDialog>> = {}) {
  const props = { workspaceId: "ws1", open: true, onOpenChange: vi.fn(), onSuccess: vi.fn(), ...over }
  render(<AddProviderDialog {...props} />)
  return props
}

describe("AddProviderDialog", () => {
  it("providers come from backend catalog", () => {
    renderDialog()
    const dialog = screen.getByRole("dialog", { name: /Add AI provider/ })
    const grid = within(dialog).getByRole("group", { name: "Choose AI provider" })
    const tiles = within(grid).getAllByRole("button")
    expect(tiles).toHaveLength(LOGIN_PROVIDERS.length)
    for (const p of LOGIN_PROVIDERS) expect(within(grid).getByText(p.label)).toBeInTheDocument()
    // A brand the provider-login API does not accept is not offered.
    expect(within(grid).queryByText(/Mistral/i)).not.toBeInTheDocument()
    // Subscriptions are marked.
    expect(within(grid).getAllByText("Subscription")).toHaveLength(LOGIN_PROVIDERS.filter((p) => p.subscription).length)
    // The steps say what the dialog does.
    const steps = within(dialog).getByRole("navigation", { name: "Add credential steps" })
    expect(steps).toHaveTextContent("Provider")
    expect(steps).toHaveTextContent("Connect")
    expect(steps).toHaveTextContent("Pays for")
  })

  it("re-login starts at Connect with name locked", () => {
    renderDialog({ initial: { credentialId: "seat1", provider: "ANTHROPIC", loginMode: "subscription", signIn: "paste", name: "Claude Max · Pavel" } })
    const dialog = screen.getByRole("dialog", { name: /Sign in again/ })
    expect(within(dialog).queryByRole("group", { name: "Choose AI provider" })).not.toBeInTheDocument()
    const name = within(dialog).getByLabelText("Name") as HTMLInputElement
    expect(name.value).toBe("Claude Max · Pavel")
    expect(name).toBeDisabled()
    expect(within(dialog).getByRole("button", { name: "Change" })).toBeDisabled()
    expect(within(dialog).getByRole("button", { name: /Update login/ })).toBeInTheDocument()
  })

  it("the last step offers only fitting agents and joins the chosen account group", async () => {
    let created = false
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.startsWith("/api/v1/agents?")) return ok([{ id: "ag1", name: "Scout", cli_adapter: "CLAUDE_CODE" }, { id: "ag2", name: "Fixer", cli_adapter: "CODEX_CLI" }])
      if (url === "/api/v1/provider-logins/pools") return ok({ items: [{ id: "p1", name: "Claude seats", provider: "ANTHROPIC", mode: "api_key" }, { id: "p2", name: "Codex seats", provider: "OPENAI", mode: "api_key" }], next_cursor: null })
      if (url === "/api/v1/provider-logins/pools/p1" && !init?.method) return ok({ id: "p1", name: "Claude seats", revision: 3, allow_cross_owner: false, members: [{ credential_id: "old", priority: 0 }] })
      if (url.startsWith("/api/v1/credentials?") && init?.method === "POST") { created = true; return ok({ id: "seat-new" }, 201) }
      if (url.startsWith("/api/v1/credentials/bindings?")) return ok({ bindings: [] })
      return ok({})
    })
    const props = renderDialog({ initial: { provider: "ANTHROPIC", loginMode: "api_key", signIn: "paste", step: "values", name: "Anthropic production" } })
    // Connect: the key. Fixture text, not a key.
    fireEvent.change(screen.getByLabelText(/API key/i, { selector: "textarea, input" }), { target: { value: "fixture-" + "x".repeat(32) } })
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    // Pays for.
    fireEvent.click(await screen.findByRole("button", { name: "Choose now" }))
    expect(await screen.findByText("Scout")).toBeInTheDocument()
    expect(screen.queryByText("Fixer")).not.toBeInTheDocument()
    fireEvent.change(await screen.findByLabelText("Account group"), { target: { value: "p1" } })
    expect(within(screen.getByLabelText("Account group")).queryByText("Codex seats")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("checkbox", { name: "Scout" }))
    fireEvent.click(screen.getByRole("button", { name: /Save & assign/ }))
    await waitFor(() => expect(props.onSuccess).toHaveBeenCalledWith("seat-new"))
    expect(created).toBe(true)
    const put = h.apiFetch.mock.calls.find(([u, i]) => u === "/api/v1/provider-logins/pools/p1" && i?.method === "PUT")!
    expect(new Headers(put[1].headers).get("If-Match")).toBe('"3"')
    expect(JSON.parse(put[1].body).members).toEqual([{ credential_id: "old", priority: 0 }, { credential_id: "seat-new", priority: 1 }])
    const binding = h.apiFetch.mock.calls.find(([u, i]) => String(u).startsWith("/api/v1/credentials/bindings?") && i?.method === "POST")!
    expect(JSON.parse(binding[1].body)).toMatchObject({ credential_id: "seat-new", scope: "AGENT", agent_id: "ag1" })
  })
})
