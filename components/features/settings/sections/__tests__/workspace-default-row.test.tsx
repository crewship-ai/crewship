import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

const setTheme = vi.fn()
vi.mock("next-themes", () => ({ useTheme: () => ({ theme: "dark", resolvedTheme: "dark", setTheme }) }))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const ws = vi.hoisted(() => ({
  role: "ADMIN" as string,
  appearance_default: undefined as Record<string, unknown> | undefined,
  refresh: vi.fn(async () => {}),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "w1", role: ws.role, workspace: { id: "w1", name: "Acme", appearance_default: ws.appearance_default } }),
  refreshWorkspaceSettings: () => ws.refresh(),
}))

import { WorkspaceDefaultRow } from "../workspace-default-row"

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  })
  apiFetch.mockReset()
  apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({}) })
  ws.role = "ADMIN"
  ws.appearance_default = undefined
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("WorkspaceDefaultRow", () => {
  it("saves the shared default only on its button, never on its own", async () => {
    render(<WorkspaceDefaultRow />)
    // Mounting must not touch the shared value.
    await new Promise((r) => setTimeout(r, 50))
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Use my look" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
  })

  it("lets an admin set the current look as the workspace default", async () => {
    render(<WorkspaceDefaultRow />)
    fireEvent.click(screen.getByRole("button", { name: "Use my look" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const [url, init] = apiFetch.mock.calls[0]
    expect(url).toContain("/api/v1/workspaces/w1")
    expect(init.method).toBe("PATCH")
    expect(JSON.parse(init.body)).toEqual({ appearance_default: { style: "harbor", knobs: {}, accent: "blue", theme: "dark" } })
    await waitFor(() => expect(ws.refresh).toHaveBeenCalled())
  })

  it("resets the default with {}", async () => {
    ws.appearance_default = { accent: "teal" }
    render(<WorkspaceDefaultRow />)
    fireEvent.click(screen.getByRole("button", { name: "Clear" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(JSON.parse(apiFetch.mock.calls[0][1].body)).toEqual({ appearance_default: {} })
  })

  it("shows a member the default read-only", () => {
    ws.role = "MEMBER"
    ws.appearance_default = { style: "slate", accent: "teal", theme: "dark" }
    render(<WorkspaceDefaultRow />)
    expect(screen.getByTestId("workspace-default-look")).toHaveTextContent("Slate · Teal · Dark")
    expect(screen.queryByRole("button", { name: "Use my look" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull()
  })

  it("renders nothing for a member when the workspace has no default", () => {
    ws.role = "MEMBER"
    const { container } = render(<WorkspaceDefaultRow />)
    expect(container.textContent).toBe("")
  })

  it("keeps Clear off while the workspace has no default, and says so", () => {
    render(<WorkspaceDefaultRow />)
    expect((screen.getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText(/None set, so they get Harbor/)).toBeTruthy()
  })

  it.each(["OWNER", "ADMIN"])("%s sees both buttons", (role) => {
    ws.role = role
    render(<WorkspaceDefaultRow />)
    expect(screen.getByRole("button", { name: "Use my look" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Clear" })).toBeTruthy()
  })

  it.each(["MEMBER", "VIEWER"])("%s never gets the buttons, whether or not a default is set", (role) => {
    ws.role = role
    ws.appearance_default = { accent: "teal" }
    render(<WorkspaceDefaultRow />)
    expect(screen.queryByRole("button")).toBeNull()
  })
})
