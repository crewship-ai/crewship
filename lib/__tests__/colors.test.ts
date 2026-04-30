import { describe, it, expect } from "vitest"
import {
  STATUS_COLORS,
  ISSUE_ICON_COLORS,
  PRIORITY_COLORS,
  CREW_COLORS,
  CREW_COLOR_DEFAULT,
  resolveCrewColor,
  CREW_BG_CLASSES,
  CREW_BG_DEFAULT,
  getCrewBgClass,
  EDGE_COLOR_PALETTE,
} from "@/lib/colors"

const HEX_RE = /^#[0-9a-fA-F]{3,8}$/

describe("STATUS_COLORS", () => {
  it("every value is a valid hex color", () => {
    for (const [name, hex] of Object.entries(STATUS_COLORS)) {
      expect(HEX_RE.test(hex), `${name}=${hex}`).toBe(true)
    }
  })
  it("includes the canonical task-status keys", () => {
    expect(STATUS_COLORS.COMPLETED).toBeTruthy()
    expect(STATUS_COLORS.IN_PROGRESS).toBeTruthy()
    expect(STATUS_COLORS.FAILED).toBeTruthy()
    expect(STATUS_COLORS.BLOCKED).toBeTruthy()
  })
})

describe("ISSUE_ICON_COLORS", () => {
  it("every value is hex", () => {
    for (const [name, hex] of Object.entries(ISSUE_ICON_COLORS)) {
      expect(HEX_RE.test(hex), `${name}=${hex}`).toBe(true)
    }
  })
  it("DONE === COMPLETED (linear-style alias)", () => {
    expect(ISSUE_ICON_COLORS.DONE).toBe(ISSUE_ICON_COLORS.COMPLETED)
  })
})

describe("PRIORITY_COLORS", () => {
  it("urgent + high share the same colour (visual urgency tier)", () => {
    expect(PRIORITY_COLORS.urgent).toBe(PRIORITY_COLORS.high)
  })
  it("medium and low are distinct", () => {
    expect(PRIORITY_COLORS.medium).not.toBe(PRIORITY_COLORS.low)
  })
})

describe("CREW_COLORS palette", () => {
  it("contains the 8-color canonical palette CLAUDE.md documents", () => {
    expect(Object.keys(CREW_COLORS).sort()).toEqual([
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

  it("every palette entry maps to a hex value", () => {
    for (const [k, v] of Object.entries(CREW_COLORS)) {
      expect(HEX_RE.test(v), `${k}=${v}`).toBe(true)
    }
  })
})

describe("resolveCrewColor", () => {
  it("returns the hex for a known palette ID", () => {
    expect(resolveCrewColor("blue")).toBe(CREW_COLORS.blue)
    expect(resolveCrewColor("rose")).toBe(CREW_COLORS.rose)
  })
  it("returns CREW_COLOR_DEFAULT for null / undefined / unknown / empty string", () => {
    expect(resolveCrewColor(null)).toBe(CREW_COLOR_DEFAULT)
    expect(resolveCrewColor(undefined)).toBe(CREW_COLOR_DEFAULT)
    expect(resolveCrewColor("")).toBe(CREW_COLOR_DEFAULT)
    expect(resolveCrewColor("not-a-real-color")).toBe(CREW_COLOR_DEFAULT)
  })
  it("CLAUDE.md anti-pattern guard: HEX values are NOT accepted (only palette IDs)", () => {
    // CLAUDE.md says "Crew colors: palette ID, NOT hex". Passing a hex
    // string should fall through to default rather than echo it.
    expect(resolveCrewColor("#3b82f6")).toBe(CREW_COLOR_DEFAULT)
  })
})

describe("getCrewBgClass", () => {
  it("maps every palette ID to a Tailwind bg class", () => {
    for (const k of Object.keys(CREW_COLORS)) {
      expect(getCrewBgClass(k)).toBe(CREW_BG_CLASSES[k])
      expect(getCrewBgClass(k)).toMatch(/^bg-/)
    }
  })
  it("returns CREW_BG_DEFAULT for invalid input", () => {
    expect(getCrewBgClass(null)).toBe(CREW_BG_DEFAULT)
    expect(getCrewBgClass(undefined)).toBe(CREW_BG_DEFAULT)
    expect(getCrewBgClass("")).toBe(CREW_BG_DEFAULT)
    expect(getCrewBgClass("invalid")).toBe(CREW_BG_DEFAULT)
  })
})

describe("CREW_BG_CLASSES vs CREW_COLORS sync", () => {
  // A drift here is the real bug — palette IDs must exist in both maps.
  it("every CREW_COLORS key has a matching CREW_BG_CLASSES entry", () => {
    for (const k of Object.keys(CREW_COLORS)) {
      expect(CREW_BG_CLASSES[k], `bg class missing for palette ID ${k}`).toBeTruthy()
    }
  })
  it("every CREW_BG_CLASSES key has a matching CREW_COLORS entry", () => {
    for (const k of Object.keys(CREW_BG_CLASSES)) {
      expect(CREW_COLORS[k], `hex missing for bg class ID ${k}`).toBeTruthy()
    }
  })
})

describe("EDGE_COLOR_PALETTE", () => {
  it("non-empty array of valid hex colours", () => {
    expect(EDGE_COLOR_PALETTE.length).toBeGreaterThan(0)
    for (const c of EDGE_COLOR_PALETTE) {
      expect(HEX_RE.test(c), c).toBe(true)
    }
  })
})
