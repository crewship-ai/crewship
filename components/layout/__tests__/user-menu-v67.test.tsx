import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

// CHR-01 (#3482), chrome-states G12: the user menu as the v67 `pop-user`.

const menu = vi.hoisted(() => ({
  mode: "trusted" as "trusted" | "restricted",
  version: { current: "dev", commit: "a41d725abc", newer: true, latest: "0.9.0", url: "https://example.test/releases/0.9.0" } as Record<string, unknown>,
}))

vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => menu.mode }))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: "u1", name: "Demo User", email: "demo@crewship.ai" } }, signOut: vi.fn().mockResolvedValue(undefined) }),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: "OWNER", workspace: { name: "Acme Company" } }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (path: string) =>
    Promise.resolve(
      path === "/api/v1/system/version"
        ? { ok: true, json: () => Promise.resolve(menu.version) }
        : path === "/api/v1/auth/sessions"
          ? { ok: true, json: () => Promise.resolve([{}, {}, {}]) }
          : { ok: false, json: () => Promise.resolve(null) },
    ),
}))

import { UserMenu } from "../user-menu"

beforeEach(() => {
  cleanup()
  menu.mode = "trusted"
  menu.version = { current: "dev", commit: "a41d725abc", newer: true, latest: "0.9.0", url: "https://example.test/releases/0.9.0" }
  for (const m of ["hasPointerCapture", "releasePointerCapture", "scrollIntoView"] as const) {
    if (!Element.prototype[m]) (Element.prototype as unknown as Record<string, unknown>)[m] = () => false
  }
})

function open(placement?: "row" | "sidebar") {
  render(<UserMenu placement={placement} />)
  fireEvent.pointerDown(screen.getByRole("button", { name: /user menu/i }), { button: 0, ctrlKey: false })
}

describe("the user menu (v67 pop-user)", () => {
  it("lists the rows in the order of the design", async () => {
    open()
    await screen.findByTestId("user-menu-build")
    const names = screen.getAllByRole("menuitem").map((el) => (el.textContent ?? "").replace(/\d+ active$/, "").trim())
    expect(names).toEqual([
      "Demo Userdemo@crewship.ai",
      "Profile & account",
      "Appearance",
      "Notification sounds",
      "Notification preferences",
      "Sessions & CLI tokens",
      "Connect CLI…",
      "Help & resources",
      "Log out",
    ])
  })

  it("has an Appearance row that opens Settings > Appearance", () => {
    open()
    expect(screen.getByRole("menuitem", { name: /^appearance$/i })).toHaveAttribute("href", "/settings?tab=appearance")
  })

  it("keeps the Theme segment in the menu", () => {
    open()
    expect(screen.getByRole("radiogroup", { name: "Theme" })).toBeInTheDocument()
  })

  it("draws Log out neutral: signing out is not a destructive action", () => {
    open()
    expect(screen.getByRole("menuitem", { name: /log out/i }).className).not.toMatch(/destructive/)
  })

  it("ends in a footer with the build and, when one exists, Update available", async () => {
    open()
    const footer = await screen.findByTestId("user-menu-footer")
    expect(within(footer).getByTestId("user-menu-build")).toHaveTextContent("dev · a41d725")
    const update = within(footer).getByRole("link", { name: "Update available" })
    expect(update).toHaveAttribute("href", "https://example.test/releases/0.9.0")
  })

  it("leaves out Update available when the build is current", async () => {
    menu.version = { current: "dev", commit: "a41d725abc", newer: false, latest: null, url: null }
    open()
    const footer = await screen.findByTestId("user-menu-footer")
    expect(within(footer).queryByRole("link", { name: "Update available" })).toBeNull()
  })

  it("hides Notification preferences and the build footer from a restricted session", () => {
    menu.mode = "restricted"
    open()
    expect(screen.queryByRole("menuitem", { name: /notification preferences/i })).toBeNull()
    expect(screen.queryByTestId("user-menu-footer")).toBeNull()
  })
})

describe("the user row in the phone More sheet", () => {
  it("is a row reading name, role and a chevron, and opens the same menu", () => {
    render(<UserMenu placement="row" />)
    const trigger = screen.getByRole("button", { name: /user menu/i })
    expect(trigger).toHaveTextContent("Demo User · Owner")
    fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
    expect(screen.getByRole("menuitem", { name: /log out/i })).toBeInTheDocument()
  })
})
