import { describe, it, expect, beforeEach, vi } from "vitest"
import { inheritedLook, parseWorkspaceAppearanceDefault, toWorkspaceDefaultBody, workspaceDefaultKey } from "../appearance-inherit"
import { clearExplicit, isExplicit, markExplicit, readExplicit, readInherited } from "../appearance-explicit"

describe("parseWorkspaceAppearanceDefault", () => {
  it.each([
    ["not an object", "x", {}],
    ["array", [], {}],
    ["empty", {}, {}],
    ["full", { style: "slate", knobs: { radius: "sharp" }, accent: "teal", theme: "dark" }, { style: "slate", knobs: { radius: "sharp" }, accent: "teal", theme: "dark" }],
    ["unknown accent and theme drop only themselves", { style: "slate", accent: "nope", theme: "sepia" }, { style: "slate" }],
    ["bad style id", { style: "Not Valid", accent: "teal" }, { accent: "teal" }],
    ["knobs only", { knobs: { radius: "round" } }, { knobs: { radius: "round" } }],
  ])("%s", (_n, input, want) => {
    expect(parseWorkspaceAppearanceDefault(input)).toEqual(want)
  })
})

describe("inheritedLook", () => {
  it("falls back to the built-in look", () => {
    expect(inheritedLook({})).toEqual({ theme: "system", accent: "blue", style: null })
  })
  it("takes the default's values, knobs on Harbor when no style", () => {
    expect(inheritedLook({ knobs: { radius: "round" }, accent: "teal", theme: "dark" })).toEqual({
      theme: "dark",
      accent: "teal",
      style: { v: 1, style: "harbor", knobs: { radius: "round" } },
    })
  })
  it("builds the PATCH body from the current look", () => {
    expect(toWorkspaceDefaultBody({ theme: "light", accent: "teal", style: null })).toEqual({ style: "harbor", knobs: {}, accent: "teal", theme: "light" })
    expect(workspaceDefaultKey({ accent: "teal" })).not.toBe(workspaceDefaultKey({ accent: "blue" }))
  })
})

describe("explicit set", () => {
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    })
  })
  it("marks, clears, and remembers what was inherited", () => {
    expect(readExplicit().size).toBe(0)
    markExplicit("accent")
    markExplicit("density")
    expect(isExplicit("accent")).toBe(true)
    clearExplicit(["accent", "theme"])
    expect(isExplicit("accent")).toBe(false)
    expect(isExplicit("density")).toBe(true)
    expect([...readInherited()].sort()).toEqual(["accent", "theme"])
    markExplicit("accent")
    expect([...readInherited()]).toEqual(["theme"])
  })
})
