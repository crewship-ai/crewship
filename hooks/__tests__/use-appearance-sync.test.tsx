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
  delete document.documentElement.dataset.textSize
  delete document.documentElement.dataset.motion
})
afterEach(() => vi.unstubAllGlobals())

describe("useAppearanceSync", () => {
  it("applies the server's appearance on load, and does not echo it back", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "light", accent: "violet", density: "compact", motion: "on", textSize: "large", sidebar: "hover" } }))
    const { result } = renderSync()

    await waitFor(() => expect(result.current.theme).toBe("light"))
    expect(setThemeSpy).toHaveBeenCalledWith("light")
    expect(result.current.accent).toBe("violet")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("violet")
    expect(document.documentElement.dataset.density).toBe("compact")
    expect(document.documentElement.dataset.textSize).toBe("large")
    // Not this device's default (pinned), so applying it is visible.
    expect(store.get("crewship_sidebar_mode")).toBe("hover")
    expect(document.documentElement.dataset.motion).toBe("reduce")
    expect(store.get(DENSITY_STORAGE_KEY)).toBe("compact")
    expect(store.get(MOTION_STORAGE_KEY)).toBe("on")

    // The server already holds this; writing it back would be a wasted PUT
    // on every page load, on every device.
    await new Promise((r) => setTimeout(r, 600))
    expect(puts()).toHaveLength(0)
  })

  it("writes a local change back, debounced into one PUT", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "dark", accent: "blue", density: "comfortable", motion: "system", textSize: "default", sidebar: "hover" } }))
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
    expect(JSON.parse(init.body)).toEqual({ theme: "light", accent: "teal", density: "compact", motion: "system", textSize: "default", sidebar: "hover" })
  })

  it("seeds the server with this device's look when it has none yet", async () => {
    store.set(ACCENT_STORAGE_KEY, "graphite")
    apiFetch.mockResolvedValue(ok({ "crews.bottomPanel.height": 240 }))
    renderSync()

    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toEqual({ theme: "dark", accent: "graphite", density: "comfortable", motion: "system", textSize: "small", sidebar: "pinned" })
  })

  it("completes a partial server value from local and writes the whole thing back", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "light", accent: "nope" } }))
    const { result } = renderSync()

    await waitFor(() => expect(result.current.theme).toBe("light"))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toEqual({ theme: "light", accent: "blue", density: "comfortable", motion: "system", textSize: "small", sidebar: "pinned" })
  })

  it("completes a value saved by an older build, keeping its fields", async () => {
    // An older build wrote four fields; this one adds its local text size and
    // sidebar mode and writes the whole value back once.
    apiFetch.mockResolvedValue(ok({ appearance: { theme: "light", accent: "workspace", density: "comfortable", motion: "system" } }))
    const { result } = renderSync()

    await waitFor(() => expect(result.current.accent).toBe("workspace"))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toEqual({ theme: "light", accent: "workspace", density: "comfortable", motion: "system", textSize: "small", sidebar: "pinned" })
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

// ── Harbor 2: the style key and the outbox (#3334) ──────────────────────
import { STYLE_STORAGE_KEY } from "@/lib/theme/appearance-style"
import { OUTBOX_STORAGE_KEY } from "@/lib/theme/appearance-outbox"
import { retryAppearanceSync, useAppearanceSyncStatus } from "../use-appearance-sync"

const STYLE_PUT = "/api/v1/me/preferences/appearance.style"
const fullAppearance = { theme: "dark", accent: "blue", density: "comfortable", motion: "system", textSize: "small", sidebar: "hover" }
function stylePuts() {
  return apiFetch.mock.calls.filter((c) => c[0] === STYLE_PUT)
}

describe("useAppearanceSync — appearance.style", () => {
  it("adopts the server's style when nothing local is pending", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: fullAppearance, "appearance.style": { v: 1, style: "harbor", knobs: { radius: "round" } } }))
    renderSync()
    await waitFor(() => expect(store.get(STYLE_STORAGE_KEY)).toBe('{"v":1,"style":"harbor","knobs":{"radius":"round"}}'))
    await new Promise((r) => setTimeout(r, 450))
    expect(stylePuts()).toHaveLength(0)
  })

  it("never seeds an empty server with a default style", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: fullAppearance }))
    renderSync()
    await new Promise((r) => setTimeout(r, 500))
    expect(stylePuts()).toHaveLength(0)
  })

  it("a style change still waiting in the outbox beats the server's value", async () => {
    store.set(STYLE_STORAGE_KEY, '{"v":1,"style":"harbor","knobs":{"radius":"sharp"}}')
    store.set(OUTBOX_STORAGE_KEY, JSON.stringify([{ key: "appearance.style", body: '{"v":1,"style":"harbor","knobs":{"radius":"sharp"}}' }]))
    apiFetch.mockResolvedValue(ok({ appearance: fullAppearance, "appearance.style": { v: 1, style: "harbor", knobs: {} } }))
    renderSync()
    await waitFor(() => expect(stylePuts()).toHaveLength(1))
    expect(stylePuts()[0][1].body).toBe('{"v":1,"style":"harbor","knobs":{"radius":"sharp"}}')
    expect(store.get(STYLE_STORAGE_KEY)).toContain("sharp")
  })

  it("writes appearance before appearance.style when both change together", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: fullAppearance }))
    const { result } = renderSync()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(PREFS))
    await new Promise((r) => setTimeout(r, 50))
    act(() => {
      // A style pick: the style first, then its accent — the order on the wire is fixed anyway.
      store.set(STYLE_STORAGE_KEY, '{"v":1,"style":"harbor","knobs":{}}')
      result.current.setAccent("teal")
    })
    await waitFor(() => expect(stylePuts()).toHaveLength(1))
    const order = apiFetch.mock.calls.map((c) => c[0]).filter((u) => u === PUT || u === STYLE_PUT)
    expect(order).toEqual([PUT, STYLE_PUT])
  })

  it("a style write refused for good keeps the local style and reports Not synced; Retry sends it again", async () => {
    apiFetch.mockImplementation(async (url: string, init?: { method?: string }) => {
      if (url === PREFS) return ok({ appearance: fullAppearance })
      if (url === STYLE_PUT && init?.method === "PUT") return { ok: false, status: 400, json: async () => ({}) }
      return ok({})
    })
    const status = renderHook(() => useAppearanceSyncStatus())
    renderSync()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(PREFS))
    await new Promise((r) => setTimeout(r, 50))
    act(() => {
      store.set(STYLE_STORAGE_KEY, '{"v":1,"style":"harbor","knobs":{"radius":"round"}}')
      window.dispatchEvent(new StorageEvent("storage", { key: STYLE_STORAGE_KEY }))
    })
    await waitFor(() => expect(status.result.current).toBe("failed"))
    expect(store.get(STYLE_STORAGE_KEY)).toContain("round")
    apiFetch.mockImplementation(async () => ok({}))
    await act(async () => retryAppearanceSync())
    await waitFor(() => expect(status.result.current).toBe("synced"))
  })

  it("paints Harbor for a style this build does not know, keeping the stored value", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: fullAppearance, "appearance.style": { v: 1, style: "from-the-future", knobs: {} } }))
    renderSync()
    await waitFor(() => expect(store.get(STYLE_STORAGE_KEY)).toContain("from-the-future"))
    expect(document.documentElement.dataset.style).toBe("harbor")
  })

  it("sync off: no request for either key", async () => {
    store.set(APPEARANCE_SYNC_STORAGE_KEY, "0")
    store.set(STYLE_STORAGE_KEY, '{"v":1,"style":"harbor","knobs":{}}')
    renderSync()
    await new Promise((r) => setTimeout(r, 500))
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

describe("sync off and on again (APR-05, independent review 2026-10-17)", () => {
  it("does not issue another PUT after sync is switched off", async () => {
    apiFetch.mockImplementation((url: string) => Promise.resolve(url === PREFS ? ok({ appearance: fullAppearance }) : { ok: false, status: 503 }))
    const hook = renderSync()
    await waitFor(() => expect(hook.result.current.textSize).toBe("small"))
    vi.useFakeTimers()
    try {
      act(() => hook.result.current.setAccent("teal"))
      await act(async () => { await vi.advanceTimersByTimeAsync(450) })
      expect(puts()).toHaveLength(1)
      act(() => hook.result.current.setSyncEnabled(false))
      await act(async () => { await vi.advanceTimersByTimeAsync(5100) })
      expect(puts()).toHaveLength(1)
    } finally {
      hook.unmount()
      vi.useRealTimers()
    }
  })

  it("switched off during a request: nothing follows it; switched on: the unsent choice is sent and kept", async () => {
    let releasePut!: (r: { ok: boolean; status: number }) => void
    apiFetch.mockImplementation((url: string) => {
      if (url === PREFS) return Promise.resolve(ok({ appearance: fullAppearance }))
      if (puts().length === 1) return new Promise((resolve) => { releasePut = resolve })
      return Promise.resolve({ ok: true, status: 204 })
    })
    const hook = renderSync()
    await waitFor(() => expect(hook.result.current.textSize).toBe("small"))
    vi.useFakeTimers()
    try {
      act(() => hook.result.current.setAccent("teal"))
      await act(async () => { await vi.advanceTimersByTimeAsync(450) })
      expect(puts()).toHaveLength(1)
      // Off while the PUT is still on the wire; the server then fails it.
      act(() => hook.result.current.setSyncEnabled(false))
      await act(async () => { releasePut({ ok: false, status: 503 }); await vi.advanceTimersByTimeAsync(130_000) })
      expect(puts()).toHaveLength(1)
      // On again: the pull must not overwrite the unsent local choice, and
      // the outbox sends it.
      act(() => hook.result.current.setSyncEnabled(true))
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
      expect(hook.result.current.accent).toBe("teal")
      expect(puts()).toHaveLength(2)
      expect(JSON.parse(puts()[1][1].body).accent).toBe("teal")
    } finally {
      hook.unmount()
      vi.useRealTimers()
    }
  })
})
