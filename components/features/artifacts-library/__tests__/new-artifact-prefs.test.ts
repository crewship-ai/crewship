import { describe, expect, it } from "vitest"
import { effectivePins, lastKind, matchesKind, movePin, PIN_MAX, pushRecent, togglePin } from "../new-artifact-prefs"

describe("New artifact pins", () => {
  it("defaults to the first five kinds this person can make, in the library order", () => {
    expect(effectivePins(null, ["docs", "dashboard", "slides", "design", "app", "word", "sheet"])).toEqual(["docs", "dashboard", "slides", "design", "app"])
    expect(effectivePins(null, ["app", "word"])).toEqual(["app", "word"])
  })

  it("keeps a stored list as it is, dropping only names that are not kinds", () => {
    expect(effectivePins(["sheet", "word"], ["word", "sheet", "app"])).toEqual(["sheet", "word"])
    expect(effectivePins(["sheet", "nope", "sheet"] as never, ["sheet"])).toEqual(["sheet"])
    expect(effectivePins([], ["word"])).toEqual([])
  })

  it("pins up to five, refuses a sixth and anything this person cannot make", () => {
    expect(togglePin(["word"], "sheet", ["word", "sheet"])).toEqual({ pins: ["word", "sheet"], result: "pinned" })
    expect(togglePin(["word", "sheet"], "word", ["word", "sheet"])).toEqual({ pins: ["sheet"], result: "unpinned" })
    const full = ["docs", "dashboard", "slides", "design", "app"] as const
    expect(full).toHaveLength(PIN_MAX)
    expect(togglePin(full, "word", ["docs", "dashboard", "slides", "design", "app", "word"])).toEqual({ pins: full, result: "full" })
    expect(togglePin(["word"], "sheet", ["word"])).toEqual({ pins: ["word"], result: "unavailable" })
    // Unpinning always works, even a kind that is no longer available here.
    expect(togglePin(["word", "sheet"], "sheet", ["word"])).toEqual({ pins: ["word"], result: "unpinned" })
  })

  it("moves a pin up or down and leaves the ends alone", () => {
    expect(movePin(["word", "sheet", "app"], "sheet", -1)).toEqual(["sheet", "word", "app"])
    expect(movePin(["word", "sheet", "app"], "sheet", 1)).toEqual(["word", "app", "sheet"])
    expect(movePin(["word", "sheet"], "word", -1)).toEqual(["word", "sheet"])
    expect(movePin(["word", "sheet"], "sheet", 1)).toEqual(["word", "sheet"])
  })
})

describe("New artifact recent and last", () => {
  it("puts the newest first, once, and keeps four", () => {
    expect(pushRecent(["word", "app", "sheet", "docs"], "sheet")).toEqual(["sheet", "word", "app", "docs"])
    expect(pushRecent(["word", "app", "sheet", "docs"], "slides")).toEqual(["slides", "word", "app", "sheet"])
    expect(pushRecent(null, "app")).toEqual(["app"])
  })

  it("the main button makes the last kind still available, else the first available", () => {
    expect(lastKind(["sheet", "word"], ["word", "sheet"])).toBe("sheet")
    expect(lastKind(["sheet", "word"], ["word"])).toBe("word")
    expect(lastKind([], ["app", "word"])).toBe("app")
    expect(lastKind(["sheet"], [])).toBeUndefined()
  })
})

describe("New artifact search", () => {
  it("finds a kind by name, editor, file type or description", () => {
    expect(matchesKind("sheet", "sheet")).toBe(true)
    expect(matchesKind("sheet", "grid")).toBe(true)
    expect(matchesKind("sheet", ".xlsx")).toBe(true)
    expect(matchesKind("word", "docx")).toBe(true)
    expect(matchesKind("dashboard", "routines")).toBe(true)
    expect(matchesKind("word", "grid")).toBe(false)
    expect(matchesKind("app", "")).toBe(true)
  })
})
