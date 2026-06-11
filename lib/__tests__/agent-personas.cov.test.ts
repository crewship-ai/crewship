import { describe, it, expect } from "vitest"
import {
  BUILTIN_PERSONAS,
  filterPersonas,
  categoryCounts,
  type PersonaCategory,
} from "@/lib/agent-personas"

// First direct coverage for the persona search/filter helpers — the
// BUILTIN_PERSONAS data itself is exercised by lib/entities re-export
// tests, but filterPersonas/categoryCounts were untested.

describe("filterPersonas", () => {
  it("returns the full list with no search and category 'all'", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, {})
    expect(out).toHaveLength(BUILTIN_PERSONAS.length)
  })

  it("narrows by category", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, { category: "quality" })
    expect(out.length).toBeGreaterThan(0)
    expect(out.every((p) => p.category === "quality")).toBe(true)
  })

  it("matches the persona name case-insensitively", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "VIKTOR" })
    expect(out.map((p) => p.id)).toContain("b_viktor")
    expect(out.every((p) => p.name.toLowerCase().includes("viktor") || p.systemPrompt.toLowerCase().includes("viktor"))).toBe(true)
  })

  it("matches the role title", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "technical architect" })
    expect(out.map((p) => p.id)).toContain("b_tomas")
  })

  it("matches the blurb", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "grumpy but excellent" })
    expect(out.map((p) => p.id)).toEqual(["b_martin"])
  })

  it("matches the category name as a query string", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "devops" })
    expect(out.length).toBeGreaterThan(0)
    expect(out.some((p) => p.category === "devops")).toBe(true)
  })

  it("matches text that only appears in the system prompt", () => {
    // "Hypothesis:" is unique to Petra's prompt — not in her name/blurb.
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "hypothesis:" })
    expect(out.map((p) => p.id)).toEqual(["b_petra"])
  })

  it("trims surrounding whitespace from the query", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "  viktor  " })
    expect(out.map((p) => p.id)).toContain("b_viktor")
  })

  it("composes category AND search", () => {
    // "lead" matches several personas, but only one in devops.
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "sre", category: "devops" })
    expect(out.map((p) => p.id)).toEqual(["b_ondrej"])
  })

  it("returns empty for a query with no match", () => {
    const out = filterPersonas(BUILTIN_PERSONAS, { search: "zzz-no-such-persona" })
    expect(out).toEqual([])
  })
})

describe("categoryCounts", () => {
  it("counts builtin personas per category and totals under 'all'", () => {
    const counts = categoryCounts(BUILTIN_PERSONAS)
    expect(counts.all).toBe(BUILTIN_PERSONAS.length)
    // Cross-check each bucket against a manual count.
    for (const cat of ["engineering", "research", "quality", "writing", "devops", "custom"] as PersonaCategory[]) {
      expect(counts[cat]).toBe(BUILTIN_PERSONAS.filter((p) => p.category === cat).length)
    }
  })

  it("initialises every category to zero so empty chips render", () => {
    const counts = categoryCounts([])
    expect(counts).toEqual({
      all: 0,
      engineering: 0,
      research: 0,
      quality: 0,
      writing: 0,
      devops: 0,
      custom: 0,
    })
  })

  it("builtins include no 'writing' or 'custom' personas (zero buckets preserved)", () => {
    const counts = categoryCounts(BUILTIN_PERSONAS)
    expect(counts.writing).toBe(0)
    expect(counts.custom).toBe(0)
  })
})
