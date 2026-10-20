import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

// #3789 — Settings › Appearance carries the per-viewer "Animated avatars"
// switch, on by default, beside Reduce motion.

vi.mock("next-themes", () => ({ useTheme: () => ({ theme: "dark", setTheme: vi.fn() }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/components/layout/workspace-accent", () => ({ useWorkspaceAccentHex: () => null }))
vi.mock("@/hooks/use-appearance-sync", () => ({ useAppearanceSyncStatus: () => "synced", retryAppearanceSync: vi.fn() }))

import { AppearanceSection } from "../appearance-section"
import { AVATAR_ANIMATION_STORAGE_KEY } from "@/lib/theme/appearance"

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("AppearanceSection — Animated avatars", () => {
  it("is a switch, on by default, that turns avatar motion off for this viewer", () => {
    render(<AppearanceSection />)
    const sw = screen.getByRole("switch", { name: "Animated avatars" })
    expect(sw.getAttribute("aria-checked")).toBe("true")
    expect(screen.getByText(/Reduce motion On also keeps them still/)).toBeTruthy()
    fireEvent.click(sw)
    expect(store.get(AVATAR_ANIMATION_STORAGE_KEY)).toBe("off")
    expect(screen.getByRole("switch", { name: "Animated avatars" }).getAttribute("aria-checked")).toBe("false")
  })
})
