import { describe, expect, it } from "vitest"
import { DESIGN_SYSTEM_FILE, DESIGN_SYSTEM_MAX_BYTES, hasDesignSystemFile, parseDesignSystem } from "../manifest"

const valid = {
  schema_version: 1,
  name: "Harbor",
  colors: { primary: "#1D4ED8", surface: "#ffffff" },
  typography: { body: { font_family: "Inter, sans-serif", size: 16, weight: 400, line_height: 1.5 }, heading: { size: 32, weight: 700 } },
  spacing: { sm: 8, md: 16 },
  radii: { sm: 4, full: 999 },
  components: [{ name: "Button", description: "Primary action" }],
}
const file = (value: unknown) => [{ path: DESIGN_SYSTEM_FILE, content: typeof value === "string" ? value : JSON.stringify(value) }]

describe("parseDesignSystem", () => {
  it("normalizes a complete token profile", () => {
    const parsed = parseDesignSystem(file(valid))
    expect(parsed).not.toBeNull()
    expect(parsed!.name).toBe("Harbor")
    expect(parsed!.colors).toEqual([{ name: "primary", value: "#1d4ed8" }, { name: "surface", value: "#ffffff" }])
    expect(parsed!.typography).toEqual([
      { name: "body", fontFamily: "Inter, sans-serif", size: 16, weight: 400, lineHeight: 1.5 },
      { name: "heading", fontFamily: null, size: 32, weight: 700, lineHeight: null },
    ])
    expect(parsed!.spacing).toEqual([{ name: "sm", value: 8 }, { name: "md", value: 16 }])
    expect(parsed!.radii.map(radius => radius.value)).toEqual([4, 999])
    expect(parsed!.components).toEqual([{ name: "Button", description: "Primary action" }])
  })

  it("accepts a minimal profile with empty groups and no components", () => {
    const parsed = parseDesignSystem(file({ schema_version: 1, colors: {}, typography: {}, spacing: {}, radii: {} }))
    expect(parsed).toEqual({ name: null, colors: [], typography: [], spacing: [], radii: [], components: [] })
  })

  it("is absent without exactly one design-system.json", () => {
    expect(hasDesignSystemFile([{ path: "index.html" }])).toBe(false)
    expect(hasDesignSystemFile(file(valid))).toBe(true)
    expect(parseDesignSystem([])).toBeNull()
    expect(parseDesignSystem([...file(valid), ...file(valid)])).toBeNull()
  })

  it.each<[string, unknown]>([
    ["invalid JSON", "{"],
    ["an array", [valid]],
    ["another schema version", { ...valid, schema_version: 2 }],
    ["an unknown top-level field", { ...valid, imports: ["https://example.test/x.css"] }],
    ["a missing group", { schema_version: 1, colors: {}, typography: {}, spacing: {} }],
    ["a CSS expression as color", { ...valid, colors: { primary: "var(--x)" } }],
    ["a url as color", { ...valid, colors: { primary: "url(https://example.test)" } }],
    ["a short hex color", { ...valid, colors: { primary: "#fff" } }],
    ["an uppercase token name", { ...valid, spacing: { Large: 8 } }],
    ["a reserved token name", { ...valid, radii: { constructor: 4 } }],
    ["a fractional spacing", { ...valid, spacing: { sm: 1.5 } }],
    ["a negative radius", { ...valid, radii: { sm: -1 } }],
    ["an oversized spacing", { ...valid, spacing: { huge: 4097 } }],
    ["a font family with CSS syntax", { ...valid, typography: { body: { font_family: "x;background:url(a)", size: 16 } } }],
    ["a typography entry without size", { ...valid, typography: { body: { weight: 400 } } }],
    ["an unknown typography field", { ...valid, typography: { body: { size: 16, color: "#000000" } } }],
    ["a weight outside 100..900", { ...valid, typography: { body: { size: 16, weight: 950 } } }],
    ["a component with an unknown field", { ...valid, components: [{ name: "Button", html: "<b>" }] }],
    ["a component without a name", { ...valid, components: [{ description: "x" }] }],
    ["duplicate component names", { ...valid, components: [{ name: "Button" }, { name: "Button" }] }],
    ["a non-string name", { ...valid, name: 4 }],
  ])("rejects %s", (_, value) => {
    expect(parseDesignSystem(file(value))).toBeNull()
  })

  it("rejects duplicate keys that JSON.parse would silently merge", () => {
    expect(parseDesignSystem(file('{"schema_version":1,"colors":{"a":"#000000","a":"#ffffff"},"typography":{},"spacing":{},"radii":{}}'))).toBeNull()
  })

  it("rejects oversized sources and too many tokens", () => {
    expect(parseDesignSystem(file(" ".repeat(DESIGN_SYSTEM_MAX_BYTES + 1)))).toBeNull()
    const colors = Object.fromEntries(Array.from({ length: 129 }, (_, i) => [`c${i}`, "#000000"]))
    expect(parseDesignSystem(file({ ...valid, colors }))).toBeNull()
  })
})
