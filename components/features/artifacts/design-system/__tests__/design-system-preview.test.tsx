import { describe, expect, it } from "vitest"
import { render, screen, within } from "@testing-library/react"
import { ArtifactDesignSystemPreview } from "../design-system-preview"

const files = [{ path: "index.html", content: "<h1>Docs</h1>" }, { path: "design-system.json", content: JSON.stringify({
  schema_version: 1, name: "Harbor",
  colors: { primary: "#1D4ED8" },
  typography: { heading: { size: 32, weight: 700, font_family: "Georgia, serif" } },
  spacing: { md: 16 }, radii: { lg: 12 },
  components: [{ name: "Button", description: "Primary action" }],
}) }]

describe("ArtifactDesignSystemPreview", () => {
  it("renders swatches, type scale, spacing, radii and components from the version's files", () => {
    const { container } = render(<ArtifactDesignSystemPreview files={files} title="Preview" />)
    expect(screen.getByRole("heading", { name: "Harbor" })).toBeTruthy()
    const swatch = container.querySelector('[data-token="color.primary"]')!
    expect(within(swatch as HTMLElement).getByText("#1d4ed8")).toBeTruthy()
    expect((swatch.firstElementChild as HTMLElement).style.backgroundColor).toMatch(/^(#1d4ed8|rgb\(29, 78, 216\))$/)
    const heading = container.querySelector('[data-token="typography.heading"]')!
    const sample = heading.lastElementChild as HTMLElement
    expect(sample.style.fontSize).toBe("32px")
    expect(sample.style.fontWeight).toBe("700")
    expect(((container.querySelector('[data-token="spacing.md"]')!.lastElementChild) as HTMLElement).style.width).toBe("16px")
    expect(((container.querySelector('[data-token="radius.lg"]')!.firstElementChild) as HTMLElement).style.borderRadius).toBe("12px")
    expect(screen.getByText("Button")).toBeTruthy()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("refuses an invalid token file instead of rendering partial values", () => {
    render(<ArtifactDesignSystemPreview files={[{ path: "design-system.json", content: '{"schema_version":1,"colors":{"x":"url(a)"},"typography":{},"spacing":{},"radii":{}}' }]} title="Preview" />)
    expect(screen.getByRole("alert").textContent).toContain("invalid")
    expect(document.querySelector("[data-token]")).toBeNull()
  })

  it("shows every theme's value, usage notes and type groups of a schema 2 file", () => {
    const { container } = render(<ArtifactDesignSystemPreview title="Preview" files={[{ path: "design-system.json", content: JSON.stringify({
      schema_version: 2, name: "Harbor", themes: [{ id: "light", name: "Light" }, { id: "dark", name: "Dark" }],
      colors: { surface: { value: { light: "#F4F1EA", dark: "#0B1726" }, usage: "Page background." }, text: { value: { light: "#000000", dark: "{surface}" } } },
      typography: { body: { size: 15, group: "Text" } }, spacing: {}, radii: {},
    }) }]} />)
    expect(screen.getByText("Themes: Light (default), Dark")).toBeTruthy()
    const surface = container.querySelector('[data-token="color.surface"]') as HTMLElement
    expect(surface.textContent).toContain("Light #f4f1ea")
    expect(surface.textContent).toContain("Dark #0b1726")
    expect(surface.textContent).toContain("Page background.")
    expect((container.querySelector('[data-token="color.text"]') as HTMLElement).textContent).toContain("Dark #0b1726 {surface}")
    expect((container.querySelector('[data-token="typography.body"]') as HTMLElement).textContent).toContain("Text")
  })

  it("names a newer format instead of calling the file invalid", () => {
    render(<ArtifactDesignSystemPreview files={[{ path: "design-system.json", content: '{"schema_version":3}' }]} title="Preview" />)
    expect(screen.getByRole("alert").textContent).toBe("This Design System uses format version 3, which this version of Crewship cannot show. The file is unchanged.")
  })
})
