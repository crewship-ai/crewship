import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"
import { useSyncExternalStore } from "react"

// next-themes, reduced to what the hook uses: a stored theme and a setter
// that re-renders readers. Hoisted so the vi.mock factory can see it.
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
const setThemeSpy = vi.fn((t: string) => themeStore.set(t))
vi.mock("next-themes", () => ({
  useTheme: () => ({
    theme: useSyncExternalStore(themeStore.subscribe, themeStore.get, themeStore.get),
    setTheme: setThemeSpy,
  }),
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { useAppearanceSync } from "../use-appearance-sync"
import { useAppearance } from "../use-appearance"
import { ACCENT_STORAGE_KEY } from "@/lib/theme/accents"
import { APPEARANCE_SYNC_STORAGE_KEY, DENSITY_STORAGE_KEY, MOTION_STORAGE_KEY } from "@/lib/theme/appearance"

const PREFS = "/api/v1/me/preferences"
const PUT = "/api/v1/me/preferences/appearance"

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body }
}

function puts() {
  return apiFetch.mock.calls.filter((c) => c[0] === PUT)
}

/** The sync hook plus a picker's view of the same store, as in the app. */
function renderSync(enabled = true) {
  return renderHook(({ on }) => {
    useAppearanceSync(on)
    return useAppearance()
  }, { initialProps: { on: enabled } })
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
  setThemeSpy.mockClear()
  apiFetch.mockReset()
  delete document.documentElement.dataset.density
  delete document.documentElement.dataset.motion
})
afterEach(() => vi.unstubAllGlobals())

describe("useAppearanceSync", () => {
  it("applies the server's appearance on load, and does not echo it back", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "light", accent: "violet", density: "compact", motion: "on" } }))
    const { result } = renderSync()

    await waitFor(() => expect(result.current.theme).toBe("light"))
    expect(setThemeSpy).toHaveBeenCalledWith("light")
    expect(result.current.accent).toBe("violet")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("violet")
    expect(document.documentElement.dataset.density).toBe("compact")
    expect(document.documentElement.dataset.motion).toBe("reduce")
    expect(store.get(DENSITY_STORAGE_KEY)).toBe("compact")
    expect(store.get(MOTION_STORAGE_KEY)).toBe("on")

    // The server already holds this; writing it back would be a wasted PUT
    // on every page load, on every device.
    await new Promise((r) => setTimeout(r, 600))
    expect(puts()).toHaveLength(0)
  })

  it("writes a local change back, debounced into one PUT", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "dark", accent: "blue", density: "comfortable", motion: "system" } }))
    const { result } = renderSync()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(PREFS))
    await new Promise((r) => setTimeout(r, 0))

    // Three changes in quick succession — the top-bar picker and the
    // settings page both go through these setters.
    act(() => result.current.setAccent("teal"))
    act(() => result.current.setDensity("compact"))
    act(() => result.current.setTheme("light"))

    await waitFor(() => expect(puts()).toHaveLength(1))
    const [, init] = puts()[0]
    expect(init.method).toBe("PUT")
    expect(JSON.parse(init.body)).toEqual({ theme: "light", accent: "teal", density: "compact", motion: "system" })
  })

  it("seeds the server with this device's look when it has none yet", async () => {
    store.set(ACCENT_STORAGE_KEY, "graphite")
    apiFetch.mockResolvedValue(ok({ "crews.bottomPanel.height": 240 }))
    renderSync()

    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toEqual({ theme: "dark", accent: "graphite", density: "comfortable", motion: "system" })
  })

  it("completes a partial server value from local and writes the whole thing back", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "light", accent: "nope" } }))
    const { result } = renderSync()

    await waitFor(() => expect(result.current.theme).toBe("light"))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toEqual({ theme: "light", accent: "blue", density: "comfortable", motion: "system" })
  })

  it("sends nothing at all with sync off on this device", async () => {
    store.set(APPEARANCE_SYNC_STORAGE_KEY, "0")
    const { result } = renderSync()

    act(() => result.current.setAccent("teal"))
    await new Promise((r) => setTimeout(r, 600))
    expect(apiFetch).not.toHaveBeenCalled()
    // Local still works.
    expect(result.current.accent).toBe("teal")
  })

  it("sends nothing for a session the caller has not cleared (restricted)", async () => {
    const { result } = renderSync(false)
    act(() => result.current.setDensity("compact"))
    await new Promise((r) => setTimeout(r, 600))
    expect(apiFetch).not.toHaveBeenCalled()
    expect(document.documentElement.dataset.density).toBe("compact")
  })

  it("adopts the server's value when sync is switched on", async () => {
    store.set(APPEARANCE_SYNC_STORAGE_KEY, "0")
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "light", accent: "indigo", density: "comfortable", motion: "system" } }))
    const { result } = renderSync()
    expect(apiFetch).not.toHaveBeenCalled()

    act(() => result.current.setSyncEnabled(true))
    await waitFor(() => expect(result.current.accent).toBe("indigo"))
    expect(result.current.theme).toBe("light")
  })

  it("survives an unreachable server, and pushes only a real change afterwards", async () => {
    apiFetch.mockRejectedValueOnce(new Error("offline"))
    apiFetch.mockResolvedValue(ok({}))
    const { result } = renderSync()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(PREFS))

    // Starting offline must not overwrite the server with whatever this
    // device happened to have.
    await new Promise((r) => setTimeout(r, 600))
    expect(puts()).toHaveLength(0)
    expect(result.current.theme).toBe("dark")

    act(() => result.current.setMotion("off"))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body).motion).toBe("off")
  })

  it("treats a refused read (403, 500) like an unreachable server", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 403, json: async () => ({ error: "nope" }) })
    const { result } = renderSync()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(PREFS))
    await new Promise((r) => setTimeout(r, 600))
    expect(puts()).toHaveLength(0)
    expect(result.current.theme).toBe("dark")
  })
})
