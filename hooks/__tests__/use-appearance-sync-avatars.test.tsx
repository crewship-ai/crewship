import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"
import { useSyncExternalStore } from "react"

// #3789 — "Animated avatars" syncs like the other appearance choices. The
// default (on) is not written into the synced value, so the values every
// existing device already stored stay complete and are never echoed back.

const themeStore = vi.hoisted(() => {
  let theme = "dark"
  const listeners = new Set<() => void>()
  return {
    get: () => theme,
    set: (t: string) => {
      theme = t
      listeners.forEach((l) => l())
    },
    reset: (t: string) => {
      theme = t
    },
    subscribe: (l: () => void) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
  }
})
vi.mock("next-themes", () => ({
  useTheme: () => ({
    theme: useSyncExternalStore(themeStore.subscribe, themeStore.get, themeStore.get),
    setTheme: (t: string) => themeStore.set(t),
  }),
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { useAppearanceSync } from "../use-appearance-sync"
import { useAppearance } from "../use-appearance"
import { AVATAR_ANIMATION_STORAGE_KEY } from "@/lib/theme/appearance"

const PUT = "/api/v1/me/preferences/appearance"
const full = { theme: "dark", accent: "blue", density: "comfortable", motion: "system", textSize: "small", sidebar: "pinned" }

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body }
}
function puts() {
  return apiFetch.mock.calls.filter((c) => c[0] === PUT)
}
function renderSync() {
  return renderHook(() => {
    useAppearanceSync(true)
    return useAppearance()
  })
}

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  themeStore.reset("dark")
  apiFetch.mockReset()
})
afterEach(() => vi.unstubAllGlobals())

describe("useAppearanceSync — Animated avatars", () => {
  it("adopts the server's choice", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { ...full, avatarAnimation: "off" } }))
    const { result } = renderSync()
    await waitFor(() => expect(result.current.avatarAnimation).toBe("off"))
    expect(store.get(AVATAR_ANIMATION_STORAGE_KEY)).toBe("off")
    await new Promise((r) => setTimeout(r, 600))
    expect(puts()).toHaveLength(0)
  })

  it("does not echo a complete value saved before the switch existed", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: full }))
    const { result } = renderSync()
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 600))
    expect(puts()).toHaveLength(0)
    expect(result.current.avatarAnimation).toBe("on")
  })

  it("writes the switch back when it is turned off, and drops it again when turned on", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: full }))
    const { result } = renderSync()
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 0))

    act(() => result.current.setAvatarAnimation("off"))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toEqual({ ...full, avatarAnimation: "off" })

    act(() => result.current.setAvatarAnimation("on"))
    await waitFor(() => expect(puts()).toHaveLength(2))
    expect(JSON.parse(puts()[1][1].body)).toEqual(full)
  })
})
