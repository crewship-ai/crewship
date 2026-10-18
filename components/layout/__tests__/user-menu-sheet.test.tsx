import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

// CHR-01 (#3482), chrome-states G12: under 768 px the user menu is a bottom
// sheet (v67 phone `pop-user`), not a popover. At 1024 px (tablet) it stays
// the popover. Both hold the same rows, in the same order, per role.

const env = vi.hoisted(() => ({
  mobile: true,
  mode: "trusted" as "trusted" | "restricted",
  role: "OWNER",
}))

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => env.mobile }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => env.mode }))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: "u1", name: "Demo User", email: "demo@crewship.ai" } }, signOut: vi.fn().mockResolvedValue(undefined) }),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: env.role, workspace: { name: "Acme Company" } }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: () => Promise.resolve({ ok: false, json: () => Promise.resolve(null) }),
}))

// An in-memory store: the spec here is the switch row, not the storage.
vi.mock("@/lib/notification-sounds", () => {
  const store = new Map<string, { enabled: boolean }>()
  return {
    SOUND_PREFERENCES_EVENT: "sound-preferences",
    readSoundPreferences: (scope: string) => store.get(scope) ?? { enabled: false },
    saveSoundPreferences: (scope: string, v: { enabled: boolean }) => {
      store.set(scope, v)
      window.dispatchEvent(new CustomEvent("sound-preferences"))
      return v
    },
    unlockNotificationAudio: () => Promise.resolve(true),
  }
})

import { UserMenu } from "../user-menu"

beforeEach(() => {
  cleanup()
  env.mobile = true
  env.mode = "trusted"
  env.role = "OWNER"
  for (const m of ["hasPointerCapture", "releasePointerCapture", "scrollIntoView"] as const) {
    if (!Element.prototype[m]) (Element.prototype as unknown as Record<string, unknown>)[m] = () => false
  }
})

function openMenu(placement?: "row" | "toolbar") {
  render(<UserMenu placement={placement} />)
  const trigger = screen.getByRole("button", { name: /user menu/i })
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
  fireEvent.click(trigger)
}

const ROWS_TRUSTED = [
  "Your profile: Demo User",
  "Profile & account",
  "Appearance",
  "Notification sounds",
  "Notification preferences",
  "Sessions & CLI tokens",
  "Connect CLI…",
  "Documentation",
  "What's new",
  "Report an issue",
  "GitHub",
  "Log out",
]

function rowNames(root: HTMLElement): string[] {
  return Array.from(root.querySelectorAll<HTMLElement>("[data-menu-row]")).map((el) =>
    (el.getAttribute("aria-label") ?? el.textContent ?? "").replace(/\d+ active$/, "").trim(),
  )
}

describe("the user menu on a phone (< 768 px)", () => {
  it("opens as a bottom sheet dialog, not a popover menu", () => {
    openMenu("toolbar")
    expect(screen.queryByRole("menu")).toBeNull()
    const sheet = screen.getByRole("dialog", { name: /user menu/i })
    expect(sheet).toHaveAttribute("data-slot", "sheet-content")
    expect(sheet.className).toMatch(/bottom-0/)
    expect(sheet.className).toMatch(/rounded-t/)
  })

  it("lists the same rows as the popover, with Help & resources opened out", () => {
    openMenu("toolbar")
    const sheet = screen.getByRole("dialog", { name: /user menu/i })
    expect(rowNames(sheet)).toEqual(ROWS_TRUSTED)
    expect(within(sheet).getByRole("radiogroup", { name: "Theme" })).toBeInTheDocument()
  })

  it("makes every row a 44 px target", () => {
    openMenu("toolbar")
    for (const row of screen.getByRole("dialog", { name: /user menu/i }).querySelectorAll<HTMLElement>("[data-menu-row]")) {
      expect(row.className).toMatch(/min-h-\[44px\]/)
    }
  })

  it("opens from the user row of the More sheet too", () => {
    openMenu("row")
    expect(screen.getByRole("dialog", { name: /user menu/i })).toBeInTheDocument()
    expect(screen.queryByRole("menu")).toBeNull()
  })

  it("closes when a row is picked", () => {
    openMenu("toolbar")
    fireEvent.click(screen.getByRole("link", { name: /^appearance$/i }))
    expect(screen.queryByRole("dialog", { name: /user menu/i })).toBeNull()
  })

  it("keeps the sheet open while sounds are toggled, and says so with aria-checked", () => {
    openMenu("toolbar")
    const sounds = screen.getByRole("switch", { name: /notification sounds/i })
    const before = sounds.getAttribute("aria-checked")
    fireEvent.click(sounds)
    expect(screen.getByRole("dialog", { name: /user menu/i })).toBeInTheDocument()
    expect(screen.getByRole("switch", { name: /notification sounds/i }).getAttribute("aria-checked")).not.toBe(before)
  })

  it("leaves out Notification preferences for a restricted session", () => {
    env.mode = "restricted"
    openMenu("toolbar")
    expect(rowNames(screen.getByRole("dialog", { name: /user menu/i }))).not.toContain("Notification preferences")
  })
})

describe("the user menu on a tablet (768 px and up)", () => {
  it("stays the popover menu", () => {
    env.mobile = false
    openMenu("toolbar")
    expect(screen.getByRole("menu")).toBeInTheDocument()
    expect(screen.queryByRole("dialog", { name: /user menu/i })).toBeNull()
  })
})
