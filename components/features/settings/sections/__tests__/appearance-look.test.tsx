import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, within } from "@testing-library/react"

let theme = "light"
const setTheme = vi.fn()
vi.mock("next-themes", () => ({ useTheme: () => ({ theme, resolvedTheme: theme === "light" ? "light" : "dark", setTheme }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const toastMock = vi.hoisted(() => Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), dismiss: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast: toastMock }))
vi.mock("@/components/layout/workspace-accent", () => ({ useWorkspaceAccentHex: () => null }))
vi.mock("@/hooks/use-appearance-sync", () => ({ useAppearanceSyncStatus: () => "synced", retryAppearanceSync: vi.fn() }))
const ws = vi.hoisted(() => ({ role: "ADMIN" as string, appearance_default: undefined as Record<string, unknown> | undefined }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "w1", role: ws.role, workspace: { id: "w1", name: "Acme", appearance_default: ws.appearance_default } }),
  refreshWorkspaceSettings: vi.fn(async () => {}),
}))

import { AppearanceSection } from "../appearance-section"
import { markExplicit, readExplicit } from "@/lib/theme/appearance-explicit"

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  toastMock.mockReset()
  theme = "light"
  ws.role = "ADMIN"
  ws.appearance_default = undefined
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const source = () => screen.getByTestId("look-source").textContent
const reset = () => screen.getByRole("button", { name: "Reset to default" }) as HTMLButtonElement

describe("Your look", () => {
  it("names the style, accent and theme in one line", () => {
    render(<AppearanceSection />)
    expect(screen.getByTestId("look-summary")).toHaveTextContent("Harbor · Blue · Light")
  })

  it("says the built-in look is in use while nothing is chosen and no default is set; reset is off", () => {
    render(<AppearanceSection />)
    expect(source()).toMatch(/built-in look/)
    expect(reset().disabled).toBe(true)
  })

  it("says the workspace default is in use while the person chose nothing", () => {
    ws.appearance_default = { style: "slate", accent: "teal", theme: "dark" }
    render(<AppearanceSection />)
    expect(source()).toMatch(/This is the workspace default/)
    expect(reset().disabled).toBe(true)
  })

  it("says the look is the person's own once they chose all of it, and part of it when only some", () => {
    ws.appearance_default = { style: "slate" }
    render(<AppearanceSection />)
    act(() => markExplicit("accent"))
    expect(source()).toMatch(/part of this yourself/)
    act(() => {
      markExplicit("theme")
      markExplicit("style")
    })
    expect(source()).toMatch(/You chose this yourself/)
  })

  it("one Reset to default forgets the person's own choices, with Undo, and nothing else is a reset", () => {
    ws.appearance_default = { accent: "teal" }
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("radio", { name: "Pink" }))
    expect(readExplicit().has("accent")).toBe(true)
    fireEvent.click(reset())
    expect(readExplicit().has("accent")).toBe(false)
    const call = toastMock.mock.calls.at(-1)!
    expect(call[0]).toBe("Back to the workspace default")
    act(() => call[1].action.onClick())
    expect(readExplicit().has("accent")).toBe(true)
    // The old resets and chips are gone.
    expect(screen.queryByRole("button", { name: "Reset style" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Use workspace default" })).toBeNull()
    expect(screen.queryByTestId("inherit-accent")).toBeNull()
    expect(screen.queryByText("Set by you")).toBeNull()
  })

  it("the toast says built-in look when there is no workspace default", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("radio", { name: "Pink" }))
    fireEvent.click(reset())
    expect(toastMock.mock.calls.at(-1)![0]).toBe("Back to the built-in look")
  })
})

// The workspace default row: admin tier only; members only read it.
describe("Devices and workspace role gate", () => {
  const card = () => screen.getByRole("region", { name: "Devices and workspace" })

  it.each(["OWNER", "ADMIN"])("%s can use their look as the default or clear it", (role) => {
    ws.role = role
    render(<AppearanceSection />)
    expect(within(card()).getByRole("button", { name: "Use my look" })).toBeTruthy()
    expect(within(card()).getByRole("button", { name: "Clear" })).toBeTruthy()
    expect(within(card()).getByRole("switch", { name: "Sync across devices" })).toBeTruthy()
  })

  it("a member sees the default as text when one is set", () => {
    ws.role = "MEMBER"
    ws.appearance_default = { style: "slate", accent: "teal", theme: "dark" }
    render(<AppearanceSection />)
    expect(within(card()).getByText("Slate · Teal · Dark")).toBeTruthy()
    expect(within(card()).queryByRole("button", { name: "Use my look" })).toBeNull()
    expect(within(card()).queryByRole("button", { name: "Clear" })).toBeNull()
  })

  it("a member sees no workspace default row when none is set, but still sync", () => {
    ws.role = "MEMBER"
    render(<AppearanceSection />)
    expect(within(card()).queryByText("Workspace default")).toBeNull()
    expect(within(card()).getByRole("switch", { name: "Sync across devices" })).toBeTruthy()
  })
})
