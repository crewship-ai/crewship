import { describe, expect, it } from "vitest"
import { DESIGN_SYSTEM_FILE, DESIGN_SYSTEM_MAX_BYTES, colorIn, designSystemSchemaVersion, hasDesignSystemFile, parseDesignSystem } from "../manifest"
import { contrastRatio, contrastRating } from "../contrast"

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
    expect(parsed!.schemaVersion).toBe(1)
    expect(parsed!.themes).toEqual([])
    expect(parsed!.colors).toEqual([{ name: "primary", value: "#1d4ed8", values: {}, aliases: {}, usage: null }, { name: "surface", value: "#ffffff", values: {}, aliases: {}, usage: null }])
    expect(parsed!.typography).toEqual([
      { name: "body", fontFamily: "Inter, sans-serif", size: 16, weight: 400, lineHeight: 1.5, group: null },
      { name: "heading", fontFamily: null, size: 32, weight: 700, lineHeight: null, group: null },
    ])
    expect(parsed!.spacing).toEqual([{ name: "sm", value: 8 }, { name: "md", value: 16 }])
    expect(parsed!.radii.map(radius => radius.value)).toEqual([4, 999])
    expect(parsed!.components).toEqual([{ name: "Button", description: "Primary action" }])
  })

  it("accepts a minimal profile with empty groups and no components", () => {
    const parsed = parseDesignSystem(file({ schema_version: 1, colors: {}, typography: {}, spacing: {}, radii: {} }))
    expect(parsed).toEqual({ schemaVersion: 1, name: null, themes: [], colors: [], typography: [], spacing: [], radii: [], components: [] })
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
    ["a newer schema version", { ...valid, schema_version: 3 }],
    ["schema 1 with themes", { ...valid, themes: [{ id: "light", name: "Light" }] }],
    ["schema 1 with a themed colour", { ...valid, colors: { primary: { value: { light: "#000000" } } } }],
    ["schema 1 with an alias", { ...valid, colors: { primary: "#000000", ink: "{primary}" } }],
    ["schema 1 with a type group", { ...valid, typography: { body: { size: 16, group: "Text" } } }],
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

const v2 = {
  schema_version: 2,
  name: "Harbor",
  themes: [{ id: "light", name: "Light" }, { id: "dark", name: "Dark" }],
  colors: {
    surface: { value: { light: "#F4F1EA", dark: "#0B1726" }, usage: "Page background." },
    brand: "#E8833A",
    panel: { value: { light: "#FFFDF8" } },
    text: { value: { light: "{ink}", dark: "{surface}" }, usage: "Body text." },
    ink: "#0F1F33",
  },
  typography: { body: { size: 15, weight: 400, group: "Text" }, code: { size: 13 } },
  spacing: { md: 16 },
  radii: { sm: 4 },
}

describe("parseDesignSystem schema 2 (themes)", () => {
  it("keeps every theme value, the usage note and the type group", () => {
    const parsed = parseDesignSystem(file(v2))!
    expect(parsed.schemaVersion).toBe(2)
    expect(parsed.themes).toEqual([{ id: "light", name: "Light" }, { id: "dark", name: "Dark" }])
    expect(parsed.colors).toEqual([
      { name: "surface", value: "#f4f1ea", values: { light: "#f4f1ea", dark: "#0b1726" }, aliases: {}, usage: "Page background." },
      { name: "brand", value: "#e8833a", values: { light: "#e8833a", dark: "#e8833a" }, aliases: {}, usage: null },
      // A theme the value leaves out takes the first theme's.
      { name: "panel", value: "#fffdf8", values: { light: "#fffdf8", dark: "#fffdf8" }, aliases: {}, usage: null },
      // Aliases resolve per theme and stay named.
      { name: "text", value: "#0f1f33", values: { light: "#0f1f33", dark: "#0b1726" }, aliases: { light: "ink", dark: "surface" }, usage: "Body text." },
      { name: "ink", value: "#0f1f33", values: { light: "#0f1f33", dark: "#0f1f33" }, aliases: {}, usage: null },
    ])
    expect(parsed.typography.map(style => style.group)).toEqual(["Text", null])
    expect(colorIn(parsed.colors[0], "dark")).toBe("#0b1726")
    expect(colorIn(parsed.colors[0], "light")).toBe("#f4f1ea")
    // Unknown theme or schema 1: the default (first) value.
    expect(colorIn(parsed.colors[0], "sepia")).toBe("#f4f1ea")
    expect(colorIn(parsed.colors[0], null)).toBe("#f4f1ea")
  })

  it.each<[string, unknown]>([
    ["no themes", { ...v2, themes: [] }],
    ["a missing themes field", (({ themes: _, ...rest }) => rest)(v2)],
    ["duplicate theme ids", { ...v2, themes: [{ id: "light", name: "A" }, { id: "light", name: "B" }] }],
    ["too many themes", { ...v2, themes: Array.from({ length: 9 }, (_, i) => ({ id: `t${i}`, name: `T${i}` })) }],
    ["a theme with an unknown field", { ...v2, themes: [{ id: "light", name: "Light", css: "x" }] }],
    ["a theme id with CSS syntax", { ...v2, themes: [{ id: "light;x", name: "Light" }] }],
    ["a colour without the first theme", { ...v2, colors: { surface: { value: { dark: "#ffffff" } } } }],
    ["a colour with an undeclared theme", { ...v2, colors: { surface: { value: { light: "#ffffff", sepia: "#111111" } } } }],
    ["a colour with a CSS value in one theme", { ...v2, colors: { surface: { value: { light: "#ffffff", dark: "var(--x)" } } } }],
    ["a colour with an unknown field", { ...v2, colors: { surface: { value: { light: "#ffffff" }, css: "x" } } }],
    ["an alias to a missing token", { ...v2, colors: { text: "{nothing}" } }],
    ["an alias cycle", { ...v2, colors: { a: "{b}", b: { value: { light: "#000000", dark: "{a}" } } } }],
    ["a self alias", { ...v2, colors: { a: "{a}" } }],
    ["an alias chain of 17", { ...v2, colors: Object.fromEntries([...Array.from({ length: 17 }, (_, i) => [`c${i}`, `{c${i + 1}}`]), ["c17", "#000000"]]) }],
    ["an alias with CSS syntax", { ...v2, colors: { a: "#000000", b: "{a};x" } }],
    ["a control character in usage", { ...v2, colors: { surface: { value: { light: "#ffffff" }, usage: "a\u0000b" } } }],
    ["an oversized usage", { ...v2, colors: { surface: { value: { light: "#ffffff" }, usage: "x".repeat(281) } } }],
    ["an empty group", { ...v2, typography: { body: { size: 15, group: " " } } }],
  ])("rejects %s", (_, value) => {
    expect(parseDesignSystem(file(value))).toBeNull()
  })

  it("accepts an alias chain of 16", () => {
    const colors = Object.fromEntries([...Array.from({ length: 16 }, (_, i) => [`c${i}`, `{c${i + 1}}`]), ["c16", "#000000"]])
    expect(parseDesignSystem(file({ ...v2, colors }))!.colors[0].values).toEqual({ light: "#000000", dark: "#000000" })
  })

  it("leaves schema 1 output unchanged by the schema 2 reader", () => {
    expect(parseDesignSystem(file(valid))!.colors.map(color => color.value)).toEqual(["#1d4ed8", "#ffffff"])
  })
})

describe("designSystemSchemaVersion", () => {
  it("names the declared version so a newer file is reported, not called broken", () => {
    expect(designSystemSchemaVersion(file(valid))).toBe(1)
    expect(designSystemSchemaVersion(file({ ...valid, schema_version: 7 }))).toBe(7)
    expect(designSystemSchemaVersion(file("{"))).toBeNull()
    expect(designSystemSchemaVersion(file({ schema_version: "2" }))).toBeNull()
    expect(designSystemSchemaVersion([])).toBeNull()
  })
})

describe("contrast", () => {
  it("computes WCAG 2 ratios", () => {
    expect(contrastRatio("#000000", "#ffffff")).toBeCloseTo(21, 5)
    expect(contrastRatio("#ffffff", "#ffffff")).toBeCloseTo(1, 5)
    expect(contrastRatio("#777777", "#ffffff")).toBeCloseTo(4.48, 2)
    expect(contrastRatio("#0f1f33", "#f4f1ea")).toBeCloseTo(contrastRatio("#f4f1ea", "#0f1f33"), 10)
  })
  it.each<[number, string]>([[21, "AAA"], [7, "AAA"], [4.5, "AA"], [3, "AA large"], [2.99, "Low"]])("rates %s as %s", (ratio, rating) => {
    expect(contrastRating(ratio)).toBe(rating)
  })
})
