import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"

import {
  APPEARANCE_BOOT_SCRIPT,
  APPEARANCE_SYNC_STORAGE_KEY,
  DENSITY_STORAGE_KEY,
  MOTION_STORAGE_KEY,
  motionConfigValue,
  parseAppearance,
  readDensity,
  readMotion,
  readSyncEnabled,
  setDensity,
  setMotion,
  setSyncEnabled,
  subscribeAppearance,
} from "../appearance"

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  delete document.documentElement.dataset.density
  delete document.documentElement.dataset.motion
})
afterEach(() => vi.unstubAllGlobals())

describe("parseAppearance", () => {
  it("keeps every valid field", () => {
    expect(parseAppearance({ theme: "light", accent: "teal", density: "compact", motion: "on" })).toEqual({
      theme: "light",
      accent: "teal",
      density: "compact",
      motion: "on",
    })
  })

  it("accepts a base and the workspace accent", () => {
    expect(parseAppearance({ accent: "workspace", base: "warm" })).toEqual({ accent: "workspace", base: "warm" })
    expect(parseAppearance({ accent: "#ff0000", base: "sepia" })).toEqual({})
  })

  // Written by another build of the UI: one field it does not know must not
  // cost the user the rest of their look.
  it("drops only the fields it does not recognise", () => {
    expect(parseAppearance({ theme: "sepia", accent: "violet", density: 3 })).toEqual({ accent: "violet" })
  })

  it.each([null, undefined, "dark", 42, ["dark"]])("returns nothing for %j", (v) => {
    expect(parseAppearance(v)).toEqual({})
  })
})

describe("density and motion", () => {
  it("default to comfortable, system and sync on", () => {
    expect(readDensity()).toBe("comfortable")
    expect(readMotion()).toBe("system")
    expect(readSyncEnabled()).toBe(true)
  })

  it("paint <html> and remember the choice", () => {
    setDensity("compact")
    setMotion("on")
    expect(document.documentElement.dataset.density).toBe("compact")
    expect(document.documentElement.dataset.motion).toBe("reduce")
    expect(store.get(DENSITY_STORAGE_KEY)).toBe("compact")
    expect(store.get(MOTION_STORAGE_KEY)).toBe("on")

    // Off is an explicit "full" (it lifts the OS reduce rule); the defaults
    // are the absence of the attribute.
    setMotion("off")
    expect(document.documentElement.dataset.motion).toBe("full")
    setMotion("system")
    setDensity("comfortable")
    expect(document.documentElement.hasAttribute("data-motion")).toBe(false)
    expect(document.documentElement.hasAttribute("data-density")).toBe(false)
  })

  it("stores the sync switch per device", () => {
    setSyncEnabled(false)
    expect(store.get(APPEARANCE_SYNC_STORAGE_KEY)).toBe("0")
    expect(readSyncEnabled()).toBe(false)
  })

  it("tells every subscriber about a same-tab change", () => {
    const a = vi.fn()
    const b = vi.fn()
    const offA = subscribeAppearance(a)
    const offB = subscribeAppearance(b)
    setDensity("compact")
    expect(a).toHaveBeenCalled()
    expect(b).toHaveBeenCalled()
    offA()
    offB()
  })

  it("maps the motion choice onto MotionConfig", () => {
    expect(motionConfigValue("system")).toBe("user")
    expect(motionConfigValue("on")).toBe("always")
    expect(motionConfigValue("off")).toBe("never")
  })
})

describe("APPEARANCE_BOOT_SCRIPT", () => {
  it("paints the stored choices before first paint", () => {
    store.set(DENSITY_STORAGE_KEY, "compact")
    store.set(MOTION_STORAGE_KEY, "on")
    new Function(APPEARANCE_BOOT_SCRIPT)()
    expect(document.documentElement.dataset.density).toBe("compact")
    expect(document.documentElement.dataset.motion).toBe("reduce")
  })

  it("writes nothing for an unknown stored value", () => {
    store.set(DENSITY_STORAGE_KEY, "x\" onload=\"alert(1)")
    store.set(MOTION_STORAGE_KEY, "sideways")
    new Function(APPEARANCE_BOOT_SCRIPT)()
    expect(document.documentElement.hasAttribute("data-density")).toBe(false)
    expect(document.documentElement.hasAttribute("data-motion")).toBe(false)
  })

  it("stays inert inside an inline <script>", () => {
    expect(APPEARANCE_BOOT_SCRIPT).not.toMatch(/<\/|<!--|\u2028|\u2029/)
  })
})
