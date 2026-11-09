import { describe, expect, it } from "vitest"
import { VIEWER_PREFS_MAX_BYTES, createViewerPrefs, validViewerPrefs, viewerPrefsKey } from "../viewer-prefs"

class MemoryStorage {
  data = new Map<string, string>()
  getItem(key: string) { return this.data.get(key) ?? null }
  setItem(key: string, value: string) { this.data.set(key, value) }
  removeItem(key: string) { this.data.delete(key) }
}
const throwing = { getItem() { throw new Error("blocked") }, setItem() { throw new Error("blocked") }, removeItem() { throw new Error("blocked") } }

describe("viewer prefs (browser storage only)", () => {
  it("keys by viewer, workspace and artifact, so nothing is shared between people or dashboards", () => {
    expect(viewerPrefsKey("u1", "w1", "p1")).toBe("crewship:artifact-viewer-prefs:v1:u1:w1:p1")
    const storage = new MemoryStorage()
    const ada = createViewerPrefs(() => storage, viewerPrefsKey("ada", "w1", "p1"))
    const bob = createViewerPrefs(() => storage, viewerPrefsKey("bob", "w1", "p1"))
    const other = createViewerPrefs(() => storage, viewerPrefsKey("ada", "w1", "p2"))
    expect(ada.set({ crew: "Frontend" })).toBe(true)
    expect(ada.get()).toEqual({ crew: "Frontend" })
    expect(bob.get()).toBeNull()
    expect(other.get()).toBeNull()
    expect(ada.clear()).toBe(true)
    expect(ada.get()).toBeNull()
  })

  it("never throws when storage is missing or blocked", () => {
    for (const storage of [() => null, () => throwing as unknown as Storage, () => { throw new Error("SecurityError") }]) {
      const prefs = createViewerPrefs(storage, "k")
      expect(prefs.get()).toBeNull()
      expect(prefs.set({ crew: "x" })).toBe(false)
      expect(prefs.clear()).toBe(false)
    }
  })

  it("ignores a stored value that is not valid instead of handing it to the page", () => {
    const storage = new MemoryStorage()
    const prefs = createViewerPrefs(() => storage, "k")
    for (const raw of ["{", "[]", '{"a":{"b":1}}', '{"a":"x"}trailing']) {
      storage.setItem("k", raw)
      expect(prefs.get()).toBeNull()
    }
  })

  it.each<[string, unknown]>([
    ["an array", ["x"]],
    ["a nested object", { a: { b: 1 } }],
    ["a function-like prototype key", JSON.parse('{"__proto__":"x"}')],
    ["a non-finite number", { a: Infinity }],
    ["a long string", { a: "x".repeat(257) }],
    ["a long key", { ["k".repeat(65)]: 1 }],
    ["a control character in a key", { "a\u0000": 1 }],
    ["too many keys", Object.fromEntries(Array.from({ length: 33 }, (_, i) => [`k${i}`, i]))],
    ["an oversized value", Object.fromEntries(Array.from({ length: 32 }, (_, i) => [`k${i}`, "x".repeat(200)]))],
  ])("refuses %s", (_, value) => {
    expect(validViewerPrefs(value)).toBeNull()
  })

  it("admits strings, finite numbers, booleans and null within the size bound", () => {
    expect(validViewerPrefs({ crew: "Frontend", weeks: 6, open: true, owner: null })).toEqual({ crew: "Frontend", weeks: 6, open: true, owner: null })
    expect(validViewerPrefs({})).toEqual({})
    expect(VIEWER_PREFS_MAX_BYTES).toBe(4096)
  })
})
