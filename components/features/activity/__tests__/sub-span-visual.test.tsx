import { describe, expect, it } from "vitest"
import { render } from "@testing-library/react"
import { subSpanVisual, SubSpanIcon } from "../sub-span-visual"
import type { SubSpanKind } from "@/lib/trace/types"

// The KIND_VISUAL table is typed Record<SubSpanKind, …>, so a missing entry is
// a compile error — but subSpanVisual also falls back to the generic `tool`
// visual at runtime, which is exactly what a new kind would silently render as
// if the table were not widened with it (#848 pillar 2.4).
describe("subSpanVisual", () => {
  const KINDS: SubSpanKind[] = [
    "bash",
    "db",
    "write",
    "read",
    "edit",
    "mcp_tool",
    "http",
    "tool",
    "think",
  ]

  it("gives every kind a visual, and no kind but `tool` the tool fallback", () => {
    const fallback = subSpanVisual("tool")
    for (const kind of KINDS) {
      const v = subSpanVisual(kind)
      expect(v, `no visual for kind ${kind}`).toBeTruthy()
      expect(v.label).toBe(kind === "mcp_tool" ? "mcp" : kind)
      if (kind !== "tool") {
        expect(v, `kind ${kind} fell through to the tool visual`).not.toBe(fallback)
      }
    }
  })

  it("tints a db span apart from a bash span", () => {
    expect(subSpanVisual("db").tint).not.toBe(subSpanVisual("bash").tint)
  })
})

describe("SubSpanIcon", () => {
  // A db span carries its engine in attributes.tool, so the trace shows the
  // Postgres mark rather than the GNU bash logo a "Bash"-tagged span resolves.
  it("renders the engine brand logo for a db span", () => {
    const { container } = render(<SubSpanIcon kind="db" tool="postgres" />)
    const svg = container.querySelector("svg")
    expect(svg).toBeTruthy()
    // Brand glyphs are rendered in the brand tint; generic lucide glyphs are not.
    expect(svg?.getAttribute("style")).toContain("color")
  })

  it("falls back to the generic database glyph for an engine with no logo", () => {
    const { container } = render(<SubSpanIcon kind="db" tool="clickhouse" />)
    expect(container.querySelector("svg")).toBeTruthy()
  })
})
