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

import { WorkspaceDefaultCard, InheritChip } from "../workspace-default-card"
import { markExplicit } from "@/lib/theme/appearance-explicit"

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

describe("WorkspaceDefaultCard", () => {
  it("lets an admin set the current look as the workspace default", async () => {
    render(<WorkspaceDefaultCard />)
    fireEvent.click(screen.getByRole("button", { name: "Use my look as default" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const [url, init] = apiFetch.mock.calls[0]
    expect(url).toContain("/api/v1/workspaces/w1")
    expect(init.method).toBe("PATCH")
    expect(JSON.parse(init.body)).toEqual({ appearance_default: { style: "harbor", knobs: {}, accent: "blue", theme: "dark" } })
    await waitFor(() => expect(ws.refresh).toHaveBeenCalled())
  })

  it("resets the default with {}", async () => {
    ws.appearance_default = { accent: "teal" }
    render(<WorkspaceDefaultCard />)
    fireEvent.click(screen.getByRole("button", { name: "Reset" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(JSON.parse(apiFetch.mock.calls[0][1].body)).toEqual({ appearance_default: {} })
  })

  it("shows a member the default read-only", () => {
    ws.role = "MEMBER"
    ws.appearance_default = { style: "slate", accent: "teal", theme: "dark" }
    render(<WorkspaceDefaultCard />)
    expect(screen.getByText("Slate · Teal · Dark")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Use my look as default" })).toBeNull()
    expect((screen.getByRole("button", { name: "Use workspace default" }) as HTMLButtonElement).disabled).toBe(true)
  })

  it("renders nothing for a member when the workspace has no default", () => {
    ws.role = "MEMBER"
    const { container } = render(<WorkspaceDefaultCard />)
    expect(container.textContent).toBe("")
  })

  it("Use workspace default clears what the user set", () => {
    ws.role = "MEMBER"
    ws.appearance_default = { accent: "teal" }
    markExplicit("accent")
    render(<WorkspaceDefaultCard />)
    const button = screen.getByRole("button", { name: "Use workspace default" }) as HTMLButtonElement
    expect(button.disabled).toBe(false)
    fireEvent.click(button)
    expect(store.get("crewship-appearance-explicit")).toBe("[]")
  })
})

describe("InheritChip", () => {
  it("says whether the value is the user's or the workspace's", () => {
    ws.appearance_default = { accent: "teal" }
    const { rerender } = render(<InheritChip field="accent" />)
    expect(screen.getByTestId("inherit-accent").textContent).toContain("Workspace default")
    markExplicit("accent")
    rerender(<InheritChip field="accent" />)
    expect(screen.getByTestId("inherit-accent").textContent).toContain("Set by you")
  })

  it("is silent when the workspace has no default", () => {
    const { container } = render(<InheritChip field="accent" />)
    expect(container.textContent).toBe("")
  })
})
