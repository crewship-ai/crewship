// The mirrored claude.ai design system (cmd/crewship/seeddata/showcase/mirror/ds216) opens as a
// Design library with its README brand book, every token and the Cover preview.
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { parseDesignSystem, type DesignSystem } from "@/components/features/artifacts/design-system/manifest"
import { ArtifactFrame, NO_FRAME_CAPABILITIES } from "../../../frame"
import { DesignLibraryProjectView, libraryExtras, previewDocument, previewHeight, readmeBody, swatchContrast, unreadable } from "../design-library"

const manifest = JSON.parse(readFileSync(join(process.cwd(), "cmd/crewship/seeddata/showcase/mirror/ds216/manifest.json"), "utf8")) as
  { entrypoint: string; files: { path: string; media_type: string; content: string }[] }
const original = JSON.parse(manifest.files.find(file => file.path === "tokens.json")!.content) as
  { color: { themes: { id: string; name: string }[]; tokens: { name: string; value: { light: string; dark: string }; usage: string }[] }; type: { groups: { name: string; styles: { name: string }[] }[] } }

const state = vi.hoisted(() => ({ snapshot: null as ArtifactProjectSnapshot | null }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({
  useArtifactProjectSnapshot: () => ({ data: state.snapshot, error: null, refetch: vi.fn() }),
  useArtifactProjectMutations: () => ({ restore: { mutate: vi.fn(), isPending: false }, share: { mutate: vi.fn(), isPending: false }, comment: { mutate: vi.fn(), isPending: false } }),
}))

function mount() {
  state.snapshot = {
    page_id: "page", can_write: true, latest_version: 1, shared_version: 0, origin: null, comments: [], versions: [],
    manifest: { version: 1, entrypoint: manifest.entrypoint, files: manifest.files },
  } as ArtifactProjectSnapshot
  return render(<ArtifactFrame name="Template Crewship Design System" kind={{ kind: "design", mode: "library" }} caps={NO_FRAME_CAPABILITIES} onHome={vi.fn()} layout="desktop">
    <DesignLibraryProjectView onFrame={vi.fn()} parts={{
      data: state.snapshot!, previewKind: "design-system", canWrite: true, canComment: true, maySend: false,
      version: { latest: 1, shared: 0, shown: 1, viewingOld: false, pick: vi.fn() }, screens: [], screen: "", labelOf: path => path, setScreen: vi.fn(),
      select: { on: false, available: false, toggle: vi.fn(), hint: null }, preview: null, pinCard: null, canvasRef: { current: null },
      banners: null, history: null, queue: null, sendBar: null, waiting: 0, share: null, editSourceHref: null,
    }} />
  </ArtifactFrame>)
}

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

describe("Design library: mirrored Template Crewship Design System", () => {
  it("admits the mirrored design-system.json", () => {
    const system = parseDesignSystem(manifest.files)
    expect(system).not.toBeNull()
    expect(system!.name).toBe("Template Crewship Design System")
    expect(system!.schemaVersion).toBe(2)
    expect(system!.themes).toEqual(original.color.themes)
    expect([system!.colors.length, system!.typography.length, system!.spacing.length, system!.radii.length, system!.components.length]).toEqual([12, 7, 4, 3, 1])
    // Nothing is lost against the original: every token, both theme values and the usage note.
    for (const token of original.color.tokens) {
      const color = system!.colors.find(item => item.name === token.name)!
      expect(color.values).toEqual({ light: token.value.light.toLowerCase(), dark: token.value.dark.toLowerCase() })
      expect(color.usage).toBe(token.usage)
    }
    expect(system!.colors.some(color => color.name.endsWith("-dark"))).toBe(false)
    for (const group of original.type.groups) for (const style of group.styles) expect(system!.typography.find(item => item.name === style.name)!.group).toBe(group.name)
  })

  it("shows each original colour once with its usage, contrast and the value of the chosen theme", () => {
    mount()
    expect(screen.getByText("12 colors in 2 themes · 7 text styles · 4 spacing steps · 3 radii · 1 component")).toBeInTheDocument()
    const colors = screen.getByRole("region", { name: "Colors" })
    expect(colors.querySelectorAll("[data-token^='color.']")).toHaveLength(12)
    for (const token of original.color.tokens) {
      const card = colors.querySelector(`[data-token="color.${token.name}"]`)!
      expect(card.querySelector("code")).toHaveTextContent(token.value.light.toLowerCase())
      expect(card.querySelector("[data-usage]")).toHaveTextContent(token.usage)
      expect(card.querySelector("[data-theme-values]")).toHaveTextContent(`Světlé ${token.value.light.toLowerCase()} · Tmavé ${token.value.dark.toLowerCase()}`)
      expect(card.querySelector("[data-contrast]")!.textContent).toMatch(/^\d+\.\d:1 (AAA|AA|AA large|Low)$/)
    }
    // ink on the light surface: near black, so white text would fail and black text is chosen.
    expect(colors.querySelector('[data-token="color.surface"] [data-contrast]')).toHaveTextContent(/AAA$/)
    fireEvent.click(screen.getByRole("radio", { name: "Theme: Tmavé" }))
    expect(screen.getByRole("radio", { name: "Theme: Tmavé" })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByRole("radio", { name: "Theme: Světlé" })).toHaveAttribute("aria-checked", "false")
    expect(screen.getByRole("radio", { name: "Theme: Světlé" })).toHaveAttribute("tabindex", "-1")
    fireEvent.keyDown(screen.getByRole("radio", { name: "Theme: Tmavé" }), { key: "ArrowRight" })
    expect(screen.getByRole("radio", { name: "Theme: Světlé" })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByRole("radio", { name: "Theme: Světlé" })).toHaveFocus()
    fireEvent.keyDown(screen.getByRole("radio", { name: "Theme: Světlé" }), { key: "ArrowLeft" })
    for (const token of original.color.tokens) {
      expect(colors.querySelector(`[data-token="color.${token.name}"]`)).toHaveAttribute("data-theme-value", token.value.dark.toLowerCase())
    }
    const typography = screen.getByRole("region", { name: "Typography" })
    for (const group of original.type.groups) {
      const shown = within(typography).getByRole("group", { name: group.name })
      for (const style of group.styles) expect(shown.querySelector(`[data-token="typography.${style.name}"]`)).not.toBeNull()
    }
    expect(typography.querySelector("[data-font-note]")).toHaveTextContent("This library carries no font files.")
    expect(screen.getByRole("region", { name: "Spacing" }).querySelectorAll("[data-token]")).toHaveLength(4)
    expect([...screen.getByRole("region", { name: "Corner radius" }).querySelectorAll("[data-token]")].map(item => item.getAttribute("data-token"))).toEqual(["radius.radius-sm", "radius.radius-md", "radius.radius-lg"])
  })

  it("lists every mirrored file in Files, including the original index and tokens.json", () => {
    mount()
    const section = screen.getByRole("region", { name: "Files" })
    expect([...section.querySelectorAll("[data-file]")].map(row => row.getAttribute("data-file"))).toEqual(manifest.files.map(file => file.path))
    fireEvent.click(within(section).getByRole("button", { name: "View source/design-system.json" }))
    expect(within(section).getByRole("region", { name: "source/design-system.json" }).querySelector("pre")!.textContent).toBe(manifest.files.find(file => file.path === "source/design-system.json")!.content)
  })

  it("draws the Cover preview in the chosen theme", () => {
    mount()
    const doc = () => (screen.getByTitle("Cover preview") as HTMLIFrameElement).getAttribute("srcdoc")!
    expect(doc()).toContain("--surface:#f4f1ea")
    fireEvent.click(screen.getByRole("radio", { name: "Theme: Tmavé" }))
    expect(doc()).toContain("--surface:#0b1726")
    expect(doc()).toContain("--signal:#f0954f")
    expect(doc()).not.toContain("--surface:#f4f1ea")
    expect(doc()).not.toContain("-dark:")
  })

  it("renders the README brand book verbatim as markdown", () => {
    mount()
    const readme = screen.getByRole("article", { name: "README" })
    for (const heading of ["Obsah a tón", "Vizuální základy", "Ikonografie", "Co zatím chybí"]) {
      expect(within(readme).getByRole("heading", { name: heading })).toBeInTheDocument()
    }
    // The overview names the system once; the README's own title is not repeated under it.
    expect(within(readme).queryByRole("heading", { name: "Template Crewship Design System" })).toBeNull()
    expect(screen.getAllByRole("heading", { name: "Template Crewship Design System" })).toHaveLength(1)
    expect(readme).toHaveTextContent("Žádné emoji, žádné vykřičníky.")
    expect(readme.querySelector("script, iframe")).toBeNull()
  })

  it("previews the Cover in a script-free frame with the colour tokens as CSS variables", () => {
    mount()
    const frame = screen.getByTitle("Cover preview") as HTMLIFrameElement
    expect(frame.getAttribute("sandbox")).toBe("")
    expect(frame.style.height).toBe("320px")
    expect(frame.style.width).toBe("960px")
    const doc = frame.getAttribute("srcdoc")!
    expect(doc).toContain("--surface:#f4f1ea")
    expect(doc).toContain("--signal:#e8833a")
    expect(doc).toContain("default-src 'none'")
    expect(doc).toContain("Noční moře, jedna signální oranžová, klidná technika.")
  })
})

describe("Library extras", () => {
  const system: DesignSystem = { schemaVersion: 1, name: "S", themes: [], colors: [{ name: "ink", value: "#000000", values: {}, aliases: {}, usage: null }], typography: [], spacing: [], radii: [], components: [{ name: "Button", description: null }, { name: "Card", description: null }] }
  it("finds the README and only previews of declared components", () => {
    const extras = libraryExtras(system, [
      { path: "README.md", content: "# S" }, { path: "components/Button/preview.html", content: "<p>b</p>" },
      { path: "components/Other/preview.html", content: "<p>o</p>" }, { path: "components/Card/preview.html", content: "  " },
    ])
    expect(extras).toEqual({ readme: "# S", previews: { Button: "<p>b</p>" }, fonts: [] })
    expect(libraryExtras(system, [])).toEqual({ readme: null, previews: {}, fonts: [] })
    expect(libraryExtras(system, [{ path: "fonts/Plex.woff2", content: "" }, { path: "fonts/readme.txt", content: "" }]).fonts).toEqual(["fonts/Plex.woff2"])
  })
  it("drops only a leading README title that repeats the system's name", () => {
    expect(readmeBody("# S\n\nBody", "S")).toBe("Body")
    expect(readmeBody("# Other\n\nBody", "S")).toBe("# Other\n\nBody")
    expect(readmeBody("Intro\n# S", "S")).toBe("Intro\n# S")
    expect(readmeBody("# S", null)).toBe("# S")
  })
  it("bounds the preview height hint", () => {
    expect(previewHeight("<!-- @dsCard height=320 -->")).toBe(320)
    expect(previewHeight("<!-- @dsCard height=9999 -->")).toBe(1200)
    expect(previewHeight("<!-- @dsCard height=1 -->")).toBe(80)
    expect(previewHeight("<p>none</p>")).toBe(240)
  })
  it("puts the CSP before the system's HTML", () => {
    expect(previewDocument(system, "<p>x</p>")).toMatch(/^<meta http-equiv="Content-Security-Policy"[^>]+><style>:root\{--ink:#000000\}<\/style><p>x<\/p>$/)
  })
})

describe("Library notices", () => {
  it("names a newer format instead of calling the file broken", () => {
    expect(unreadable(3)).toBe("This Design System uses format version 3, which this version of Crewship cannot show. The file is unchanged.")
    expect(unreadable(2)).toBe("This Design System file is invalid or uses an unsupported format.")
    expect(unreadable(null)).toBe("This Design System file is invalid or uses an unsupported format.")
  })
  it("picks the more legible text colour for a swatch", () => {
    expect(swatchContrast("#ffffff")).toEqual({ text: "#000000", ratio: 21, rating: "AAA" })
    expect(swatchContrast("#0f1f33").text).toBe("#ffffff")
    // Black on #777777 is 4.69: shown truncated to one decimal, never rounded up.
    expect(swatchContrast("#777777")).toEqual({ text: "#000000", ratio: 4.6, rating: "AA" })
  })
})
