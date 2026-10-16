import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

import {
  APPEARANCE_BOOT_SCRIPT,
  APPEARANCE_VERSION,
  APPEARANCE_VERSION_STORAGE_KEY,
  TEXT_SIZE_STORAGE_KEY,
  migrateDeviceAppearance,
  parseAppearance,
  readTextSize,
} from "../appearance"

// Harbor 2 (APR-12): text size keeps its absolute meaning for everyone who
// already has one — "default" is 14 px body text, "large" 112.5 % — while a
// brand-new device starts at Compact (13 px, stored as "small"). A device
// that ran Crewship before but never stored a size keeps Standard.

let store: Map<string, string>
function stubStorage(initial: Record<string, string> = {}) {
  store = new Map(Object.entries(initial))
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
    key: (i: number) => [...store.keys()][i] ?? null,
    get length() {
      return store.size
    },
  })
}
beforeEach(() => {
  stubStorage()
  delete document.documentElement.dataset.textSize
})
afterEach(() => vi.unstubAllGlobals())

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")

describe("text size scale", () => {
  it("Compact is 13 px body text, Standard 14 px, Large 112.5 %", () => {
    // Body text is 0.875rem: 13 px needs a 13/14 root.
    expect(css).toMatch(/:root\[data-text-size="small"\]\s*\{\s*font-size:\s*92\.857%;/)
    expect(css).toMatch(/:root\[data-text-size="large"\]\s*\{\s*font-size:\s*112\.5%;/)
    expect(css).not.toMatch(/:root\[data-text-size="default"\]/)
  })
})

describe("migrateDeviceAppearance", () => {
  it("a brand-new device starts at Compact", () => {
    migrateDeviceAppearance()
    expect(store.get(TEXT_SIZE_STORAGE_KEY)).toBeUndefined()
    expect(readTextSize()).toBe("small")
    expect(store.get(APPEARANCE_VERSION_STORAGE_KEY)).toBe(APPEARANCE_VERSION)
  })

  it("a device that ran Crewship before keeps Standard", () => {
    stubStorage({ "crewship-accent": "teal", crewship_sidebar_mode: "hover" })
    migrateDeviceAppearance()
    expect(readTextSize()).toBe("default")
  })

  it("never changes a stored choice", () => {
    stubStorage({ "crewship-accent": "teal", [TEXT_SIZE_STORAGE_KEY]: "large" })
    migrateDeviceAppearance()
    expect(readTextSize()).toBe("large")
    stubStorage({ [TEXT_SIZE_STORAGE_KEY]: "small" })
    migrateDeviceAppearance()
    expect(readTextSize()).toBe("small")
  })

  it("runs once: a later reset to the new default is not undone", () => {
    stubStorage({ "crewship-accent": "teal" })
    migrateDeviceAppearance()
    store.delete(TEXT_SIZE_STORAGE_KEY)
    migrateDeviceAppearance()
    expect(readTextSize()).toBe("small")
  })

  it("survives storage without enumeration or that throws", () => {
    vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => undefined, removeItem: () => undefined })
    expect(() => migrateDeviceAppearance()).not.toThrow()
    vi.stubGlobal("localStorage", { getItem: () => { throw new Error("blocked") } })
    expect(() => migrateDeviceAppearance()).not.toThrow()
  })
})

describe("boot script (before first paint)", () => {
  it("paints Compact on a new device and Standard on an existing one", () => {
    new Function(APPEARANCE_BOOT_SCRIPT)()
    expect(document.documentElement.dataset.textSize).toBe("small")
    expect(store.get(APPEARANCE_VERSION_STORAGE_KEY)).toBe(APPEARANCE_VERSION)

    delete document.documentElement.dataset.textSize
    stubStorage({ "crewship-density": "compact" })
    new Function(APPEARANCE_BOOT_SCRIPT)()
    expect(document.documentElement.dataset.textSize).toBeUndefined()
    expect(store.get(TEXT_SIZE_STORAGE_KEY)).toBe("default")
  })

  it("matches migrateDeviceAppearance on the same storage", () => {
    for (const initial of [{}, { "crewship-accent": "teal" }, { [TEXT_SIZE_STORAGE_KEY]: "large", theme: "light" }]) {
      stubStorage(initial)
      migrateDeviceAppearance()
      const viaFn = new Map(store)
      stubStorage(initial)
      new Function(APPEARANCE_BOOT_SCRIPT)()
      expect(new Map(store)).toEqual(viaFn)
    }
  })
})

describe("mixed clients", () => {
  it("an older client's stored 'default' still means Standard (14 px)", () => {
    expect(parseAppearance({ textSize: "default" }).textSize).toBe("default")
    stubStorage({ [APPEARANCE_VERSION_STORAGE_KEY]: APPEARANCE_VERSION, [TEXT_SIZE_STORAGE_KEY]: "default" })
    new Function(APPEARANCE_BOOT_SCRIPT)()
    expect(document.documentElement.dataset.textSize).toBeUndefined()
  })
})
