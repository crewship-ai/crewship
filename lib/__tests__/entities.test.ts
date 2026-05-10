import { describe, it, expect } from "vitest"
import {
  CREW_ICONS,
  getCrewIconDef,
  GRADIENT_PALETTES,
  getGradientPalette,
  getCrewDotColor,
  CREW_ICON_CATEGORIES,
  searchCrewIcons,
} from "@/lib/entities"

describe("CREW_ICONS registry", () => {
  it("contains canonical icons documented in CLAUDE.md (code, rocket, clipboard...)", () => {
    const names = new Set(CREW_ICONS.map((i) => i.name))
    expect(names.has("code")).toBe(true)
    expect(names.has("rocket")).toBe(true)
    expect(names.has("clipboard")).toBe(true)
    expect(names.has("shield")).toBe(true)
    expect(names.has("brain")).toBe(true)
  })

  it("every entry has a name, icon component, and label", () => {
    for (const def of CREW_ICONS) {
      expect(def.name).toBeTruthy()
      expect(def.label).toBeTruthy()
      expect(typeof def.icon).toBe("object") // lucide forwardRef components
    }
  })

  it("names are unique (no duplicates)", () => {
    const names = CREW_ICONS.map((i) => i.name)
    const set = new Set(names)
    expect(set.size).toBe(names.length)
  })

  it("registry is non-trivially large (>100 icons)", () => {
    // The registry is sized for crew customisation at scale; if it
    // shrinks below ~100 a regression deleted entries.
    expect(CREW_ICONS.length).toBeGreaterThan(100)
  })
})

describe("getCrewIconDef", () => {
  it("returns the matching def for a known name", () => {
    expect(getCrewIconDef("code").name).toBe("code")
    expect(getCrewIconDef("rocket").name).toBe("rocket")
  })

  it("falls back to the first registered icon for unknown names", () => {
    const def = getCrewIconDef("totally-not-a-real-icon")
    expect(def.name).toBe(CREW_ICONS[0].name)
  })

  it("never returns null/undefined (safe in render)", () => {
    expect(getCrewIconDef("")).toBeTruthy()
  })
})

describe("GRADIENT_PALETTES", () => {
  it("matches the 8 documented palette IDs", () => {
    expect(GRADIENT_PALETTES.map((p) => p.id).sort()).toEqual([
      "amber",
      "blue",
      "cyan",
      "emerald",
      "fuchsia",
      "lime",
      "rose",
      "violet",
    ])
  })

  it("every palette has from / to / text / dot fields", () => {
    for (const p of GRADIENT_PALETTES) {
      expect(p.from).toMatch(/^from-/)
      expect(p.to).toMatch(/^to-/)
      expect(p.text).toMatch(/^text-/)
      expect(p.dot).toMatch(/^#/)
    }
  })
})

describe("getGradientPalette", () => {
  it("returns the matching palette for a known ID", () => {
    expect(getGradientPalette("blue").id).toBe("blue")
    expect(getGradientPalette("rose").id).toBe("rose")
  })

  it("falls back to the first palette for null/undefined/unknown", () => {
    const fallback = GRADIENT_PALETTES[0].id
    expect(getGradientPalette(null).id).toBe(fallback)
    expect(getGradientPalette(undefined).id).toBe(fallback)
    expect(getGradientPalette("").id).toBe(fallback)
    expect(getGradientPalette("not-real").id).toBe(fallback)
  })
})

describe("getCrewDotColor", () => {
  it("returns the palette dot color for a known ID", () => {
    expect(getCrewDotColor("blue")).toMatch(/^#[0-9a-f]{3,8}$/i)
    // Specifically the documented 'blue' dot from GRADIENT_PALETTES
    expect(getCrewDotColor("blue")).toBe(
      GRADIENT_PALETTES.find((p) => p.id === "blue")?.dot,
    )
  })

  it("falls back to default when input is empty/null/undefined", () => {
    expect(getCrewDotColor(null)).toMatch(/^#/)
    expect(getCrewDotColor(undefined)).toMatch(/^#/)
    expect(getCrewDotColor("")).toMatch(/^#/)
  })

  it("echoes a hex string passed in directly", () => {
    expect(getCrewDotColor("#abcdef")).toBe("#abcdef")
  })

  it("prepends # to a non-palette plain hex (defensive)", () => {
    expect(getCrewDotColor("ff00ff")).toBe("#ff00ff")
  })
})

describe("CREW_ICON_CATEGORIES", () => {
  it("contains the documented categories", () => {
    expect(CREW_ICON_CATEGORIES).toContain("engineering")
    expect(CREW_ICON_CATEGORIES).toContain("security")
    expect(CREW_ICON_CATEGORIES).toContain("design")
    expect(CREW_ICON_CATEGORIES).toContain("marketing")
  })
})

describe("searchCrewIcons", () => {
  it("empty/whitespace query returns all icons", () => {
    expect(searchCrewIcons("")).toHaveLength(CREW_ICONS.length)
    expect(searchCrewIcons("   ")).toHaveLength(CREW_ICONS.length)
  })

  it("exact category name returns that category's icons", () => {
    const got = searchCrewIcons("security")
    expect(got).toContain("shield")
    expect(got).toContain("lock")
    expect(got.length).toBeLessThan(CREW_ICONS.length)
  })

  it("matches by icon name substring", () => {
    const got = searchCrewIcons("rocket")
    expect(got).toContain("rocket")
  })

  it("matches by label substring (case-insensitive)", () => {
    const got = searchCrewIcons("ROCKET")
    expect(got).toContain("rocket")
  })

  it("partial category name (e.g. 'sec') matches the category", () => {
    const got = searchCrewIcons("sec")
    // 'sec' should resolve through category matching to security icons,
    // not silently fall back to 'all icons'. Asserting BOTH narrowing
    // (length < full registry) AND specific membership catches the
    // regression where category lookup quietly degrades.
    expect(got).toContain("shield")
    expect(got).toContain("lock")
    expect(got.length).toBeLessThan(CREW_ICONS.length)
  })

  it("query that matches nothing falls back to all icons", () => {
    const got = searchCrewIcons("zzz_no_such_icon_anywhere")
    // The fallback returns ALL_CREW_ICON_NAMES per the function contract.
    expect(got).toHaveLength(CREW_ICONS.length)
  })

  it("category icons returned are filtered against the registry (no orphans)", () => {
    const got = searchCrewIcons("engineering")
    const validNames = new Set(CREW_ICONS.map((i) => i.name))
    for (const name of got) {
      expect(validNames.has(name), `category icon ${name} not in registry`).toBe(true)
    }
  })
})
