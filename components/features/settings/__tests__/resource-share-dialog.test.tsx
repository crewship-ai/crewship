import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ResourceShareDialog } from "../resource-share-dialog"
import { resourceShareReplacement, type ResourcePolicy } from "@/lib/resource-sharing"

const h = vi.hoisted(() => ({ fetch: vi.fn(), role: "OWNER", mode: "trusted", loading: false }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, loading: h.loading }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => h.mode }))
const resource = { kind: "routine" as const, id: "routine-1", name: "Invoice intake" }
const policy: ResourcePolicy = {
  membership_id: "member-1", revision: 7, mode: "restricted",
  rights: [{ kind: "agent", id: "agent-1", operation: "run" }, { kind: "routine", id: "routine-1", operation: "run" }],
  grant_details: [{ kind: "routine", id: "routine-1", operation: "run", issuer_id: "former-admin", issuer_name: "Alice", created_at: "2026-10-14T00:00:00Z", live: false }],
}
const roster = [
  { id: "owner-member", role: "OWNER", access_mode: "trusted", user: { id: "owner", full_name: "Owner", email: "owner@example.test" } },
  { id: "member-1", role: "VIEWER", access_mode: "restricted", user: { id: "guest", full_name: "Client One", email: "guest@example.test" } },
]
function response(body: unknown, status = 200) { return { ok: status < 400, status, json: async () => body } }
function mockApi(writeStatus = 200) {
  h.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 }, writeStatus)
    return response(url.includes("/access?") ? policy : roster)
  })
}
function show(kind: "routine" | "project" = "routine") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  render(<QueryClientProvider client={client}><ResourceShareDialog workspaceId="workspace-1" resource={{ ...resource, kind }} /></QueryClientProvider>)
  return client
}
const writes = () => h.fetch.mock.calls.filter(([, init]) => init?.method === "PUT")
afterEach(() => { cleanup(); h.fetch.mockReset(); h.role = "OWNER"; h.mode = "trusted"; h.loading = false })

describe("resource Share", () => {
  it.each([ ["VIEWER", "trusted", false], ["ADMIN", "restricted", false], ["ADMIN", "loading", false], ["ADMIN", "trusted", true] ])("does not offer Share or fetch for role %s / mode %s / loading %s", (role, mode, loading) => {
    h.role = role as string; h.mode = mode as string; h.loading = loading as boolean
    show(); expect(screen.queryByRole("button", { name: "Share" })).toBeNull(); expect(h.fetch).not.toHaveBeenCalled()
  })
  it("opens from the object, shows inactive provenance and never loads trusted member policies", async () => {
    mockApi(); show(); expect(h.fetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(await screen.findByText(/Inactive — issuer no longer has authority/)).toHaveTextContent("Alice")
    expect(screen.getByRole("dialog")).toHaveAccessibleName("Share Invoice intake")
    expect(screen.getByText(/also requires access to every agent/)).toBeTruthy()
    expect(h.fetch.mock.calls.some(([url]) => url.includes("/members/owner-member/access"))).toBe(false)
    expect(screen.getByRole("button", { name: "Save access for Client One" })).toBeDisabled()
  })
  it("revokes only this routine, keeps agent grants and sends no forged provenance", async () => {
    mockApi(); const client = show()
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Run"))
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0][1].body))).toEqual({ membership_id: "member-1", revision: 7, mode: "restricted", rights: [policy.rights[0]] })
    await waitFor(() => expect(client.getQueryData<ResourcePolicy>(["member-resource-access", "workspace-1", "member-1"])?.revision).toBe(8))
  })
  it("reissues inactive rights only with explicit consent", async () => {
    mockApi(); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Reissue run as me"))
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0][1].body)).reissue).toEqual([policy.rights[1]])
  })
  it("keeps a failed edit and permits explicit retry", async () => {
    mockApi(500); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Run")); fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Your edits are kept")
    expect(screen.getByLabelText("Run")).not.toBeChecked()
    expect(screen.getByRole("button", { name: "Save access for Client One" })).toBeEnabled()
  })
  it("blocks a stale save without retry and reloads the current revision explicitly", async () => {
    mockApi(409); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Run")); fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Access changed")
    expect(writes()).toHaveLength(1); expect(screen.getByRole("button", { name: "Save access for Client One" })).toBeDisabled()
    h.fetch.mockImplementation(async (url: string) => response(url.includes("/access?") ? { ...policy, revision: 9 } : roster))
    fireEvent.click(screen.getByRole("button", { name: "Reload shares" }))
    await waitFor(() => expect(screen.getByLabelText("Run")).toBeEnabled())
    expect(screen.getByLabelText("Run")).toBeChecked()
  })
  it("shows a policy read failure instead of an empty sharing list", async () => {
    h.fetch.mockImplementation(async (url: string) => response(url.includes("/access?") ? {} : roster, url.includes("/access?") ? 404 : 200))
    show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load all shares")
    expect(screen.queryByRole("button", { name: "Save access for Client One" })).toBeNull()
  })
  it("keeps independent project list/read rights and unrelated inactive rights unchanged", async () => {
    mockApi(); show("project"); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("List in Issues"))
    expect(screen.getByLabelText("Read issues")).not.toBeChecked()
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0][1].body))).toEqual({ membership_id: "member-1", revision: 7, mode: "restricted", rights: [...policy.rights, { kind: "project", id: "routine-1", operation: "list" }] })
  })
  it("preserves unknown operations and refuses account conversion or invalid reissue", () => {
    const extended = { ...policy, rights: [...policy.rights, { kind: "routine" as const, id: "routine-1", operation: "future-operation" }] }
    expect(resourceShareReplacement(extended, resource, [], []).rights).toEqual([policy.rights[0], extended.rights[2]])
    expect(() => resourceShareReplacement({ ...policy, mode: "trusted" }, resource, [], [])).toThrow("Trusted access")
    expect(() => resourceShareReplacement(policy, resource, [], ["run"])).toThrow("Invalid resource operation")
    expect(() => resourceShareReplacement(policy, resource, ["edit"], [])).toThrow("Invalid resource operation")
  })
})
