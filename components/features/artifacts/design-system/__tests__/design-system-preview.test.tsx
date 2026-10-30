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
})
