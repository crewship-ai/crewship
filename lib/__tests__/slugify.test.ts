import { describe, it, expect } from "vitest"
import { slugify } from "@/lib/utils/slugify"

describe("slugify", () => {
  it("converts 'Hello World' to 'hello-world'", () => {
    expect(slugify("Hello World")).toBe("hello-world")
  })

  it("trims and converts leading/trailing spaces", () => {
    expect(slugify("  Leading spaces  ")).toBe("leading-spaces")
  })

  it("converts uppercase to lowercase", () => {
    expect(slugify("UPPERCASE")).toBe("uppercase")
  })

  it("removes special characters", () => {
    expect(slugify("special!@#chars")).toBe("specialchars")
  })

  it("collapses multiple hyphens into one", () => {
    expect(slugify("multiple---hyphens")).toBe("multiple-hyphens")
  })

  it("returns empty string for empty input", () => {
    expect(slugify("")).toBe("")
  })

  it("keeps already-slugified strings unchanged", () => {
    expect(slugify("already-slugified")).toBe("already-slugified")
  })

  it("converts underscores (removed as non-alphanumeric)", () => {
    // The implementation removes [^a-z0-9-], so underscores are stripped
    expect(slugify("Under_scores")).toBe("underscores")
  })

  it("handles mixed whitespace", () => {
    expect(slugify("hello   world")).toBe("hello-world")
  })

  it("trims hyphens from edges", () => {
    expect(slugify("-edge-hyphens-")).toBe("edge-hyphens")
  })

  it("handles numbers in input", () => {
    expect(slugify("Agent 007")).toBe("agent-007")
  })
})
