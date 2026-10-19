import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"
import { useSyncExternalStore } from "react"

// Workspace default look (WSD-02/03/04): explicit vs inherited values.

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
import { APPEARANCE_SYNC_STORAGE_KEY } from "@/lib/theme/appearance"
import { STYLE_STORAGE_KEY } from "@/lib/theme/appearance-style"

const PUT = "/api/v1/me/preferences/appearance"
const STYLE_PUT = "/api/v1/me/preferences/appearance.style"

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body })
const puts = (url = PUT) => apiFetch.mock.calls.filter((c) => c[0] === url)
const settle = (ms = 600) => new Promise((r) => setTimeout(r, ms))

const DEFAULT_A = { style: "slate", knobs: { radius: "sharp" }, accent: "violet", theme: "light" }
const DEFAULT_B = { style: "ink", accent: "teal", theme: "dark" }

function renderWith(initial: unknown) {
  return renderHook(({ d }) => {
    useAppearanceSync(true, d)
    return useAppearance()
  }, { initialProps: { d: initial } })
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
  apiFetch.mockResolvedValue(ok({}))
})
afterEach(() => vi.unstubAllGlobals())

describe("workspace default look", () => {
  it("is inherited by a user who chose nothing, and never written to the server", async () => {
    const { result } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.accent).toBe("violet"))
    expect(result.current.theme).toBe("light")
    expect(result.current.style).toEqual({ v: 1, style: "slate", knobs: { radius: "sharp" } })
    expect(result.current.explicit.size).toBe(0)
    await settle()
    expect(puts()).toHaveLength(0)
    expect(puts(STYLE_PUT)).toHaveLength(0)
  })

  it("does nothing while the workspace (and so its default) is not known yet", async () => {
    store.set(ACCENT_STORAGE_KEY, "graphite")
    const { result } = renderWith(undefined)
    await settle(100)
    expect(result.current.accent).toBe("graphite")
    expect(setThemeSpy).not.toHaveBeenCalled()
  })

  it("leaves a device's own old look alone when the workspace has no default", async () => {
    store.set(ACCENT_STORAGE_KEY, "graphite")
    const { result } = renderWith({})
    await settle(100)
    expect(result.current.accent).toBe("graphite")
    expect(result.current.theme).toBe("dark")
  })

  it("an explicit value beats the default; the rest is still inherited", async () => {
    const { result } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.accent).toBe("violet"))
    act(() => result.current.setAccent("teal"))
    await settle(100)
    expect(result.current.accent).toBe("teal")
    expect(result.current.theme).toBe("light")
    expect([...result.current.explicit]).toEqual(["accent"])
    // Only the explicit value reaches the server.
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(JSON.parse(puts()[0][1].body)).toEqual({ accent: "teal" })
    expect(puts(STYLE_PUT)).toHaveLength(0)
  })

  it("a changed workspace default reaches everyone who inherits, not who chose", async () => {
    const { result, rerender } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.accent).toBe("violet"))
    act(() => result.current.setAccent("pink"))

    rerender({ d: DEFAULT_B })
    await waitFor(() => expect(result.current.theme).toBe("dark"))
    expect(result.current.accent).toBe("pink")
    expect(result.current.style).toEqual({ v: 1, style: "ink", knobs: {} })
  })

  it("switching to a workspace without a default returns inherited values to the built-in look", async () => {
    const { result, rerender } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.accent).toBe("violet"))

    rerender({ d: {} })
    await waitFor(() => expect(result.current.accent).toBe("blue"))
    expect(result.current.theme).toBe("system")
    expect(result.current.style).toBeNull()
    expect(store.has(STYLE_STORAGE_KEY)).toBe(false)
  })

  it("Use workspace default clears the explicit values and applies the default", async () => {
    const { result } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.accent).toBe("violet"))
    act(() => {
      result.current.setAccent("teal")
      result.current.setTheme("dark")
      result.current.pickStyle("mono")
      result.current.setDensity("compact")
    })
    await waitFor(() => expect(puts()).toHaveLength(1))

    act(() => result.current.resetToWorkspaceDefault())
    await waitFor(() => expect(result.current.accent).toBe("violet"))
    expect(result.current.theme).toBe("light")
    expect(result.current.style?.style).toBe("slate")
    // Density is not part of the workspace default: still the user's.
    expect([...result.current.explicit]).toEqual(["density"])
    // The server forgets the cleared values too.
    await waitFor(() => expect(puts()).toHaveLength(2))
    expect(JSON.parse(puts()[1][1].body)).toEqual({ density: "compact" })
    expect(JSON.parse(puts(STYLE_PUT).at(-1)![1].body)).toEqual({})
  })

  it("Use workspace default with no default set returns to the built-in look", async () => {
    const { result } = renderWith({})
    act(() => result.current.setAccent("teal"))
    await waitFor(() => expect(result.current.explicit.has("accent")).toBe(true))
    act(() => result.current.resetToWorkspaceDefault())
    await waitFor(() => expect(result.current.accent).toBe("blue"))
  })

  it("server values are the user's own: they are kept over the default", async () => {
    apiFetch.mockResolvedValue(ok({ appearance: { accent: "indigo" } }))
    const { result } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.accent).toBe("indigo"))
    expect(result.current.theme).toBe("light")
    expect([...result.current.explicit]).toEqual(["accent"])
    await settle()
    expect(puts()).toHaveLength(0)
  })

  it("sync off: local explicit values apply, the rest is inherited, no request", async () => {
    store.set(APPEARANCE_SYNC_STORAGE_KEY, "0")
    const { result } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.theme).toBe("light"))
    act(() => result.current.setAccent("teal"))
    await settle()
    expect(result.current.accent).toBe("teal")
    expect(result.current.theme).toBe("light")
    expect(result.current.style?.style).toBe("slate")
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("does not fire the local pickers' explicit marks when the default is applied", async () => {
    const { result } = renderWith(DEFAULT_A)
    await waitFor(() => expect(result.current.accent).toBe("violet"))
    expect(result.current.explicit.size).toBe(0)
    expect(store.get("crewship-appearance-explicit")).toBeUndefined()
  })
})
