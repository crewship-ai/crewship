// The mirrored claude.ai design system (cmd/crewship/seeddata/showcase/mirror/ds216) opens as a
// Design library with its README brand book, every token and the Cover preview.
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { parseDesignSystem } from "@/components/features/artifacts/design-system/manifest"
import { ArtifactFrame, NO_FRAME_CAPABILITIES } from "../../../frame"
import { DesignLibraryProjectView, libraryExtras, previewDocument, previewHeight, readmeBody } from "../design-library"

const manifest = JSON.parse(readFileSync(join(process.cwd(), "cmd/crewship/seeddata/showcase/mirror/ds216/manifest.json"), "utf8")) as
  { entrypoint: string; files: { path: string; media_type: string; content: string }[] }
const original = JSON.parse(manifest.files.find(file => file.path === "tokens.json")!.content) as
  { color: { tokens: { name: string; value: { light: string; dark: string } }[] }; type: { groups: { styles: { name: string }[] }[] } }

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
    expect([system!.colors.length, system!.typography.length, system!.spacing.length, system!.radii.length, system!.components.length]).toEqual([24, 7, 4, 3, 1])
  })

  it("shows every original colour in both themes, every text style, spacing step and radius", () => {
    mount()
    expect(screen.getByText("24 colors · 7 text styles · 4 spacing steps · 3 radii · 1 component")).toBeInTheDocument()
    const colors = screen.getByRole("region", { name: "Colors" })
    for (const token of original.color.tokens) {
      expect(within(colors).getByText(token.name, { selector: "b" })).toBeInTheDocument()
      expect(within(colors).getByText(`${token.name}-dark`, { selector: "b" })).toBeInTheDocument()
      expect(colors.querySelector(`[data-token="color.${token.name}"] code`)).toHaveTextContent(token.value.light.toLowerCase())
      expect(colors.querySelector(`[data-token="color.${token.name}-dark"] code`)).toHaveTextContent(token.value.dark.toLowerCase())
    }
    const typography = screen.getByRole("region", { name: "Typography" })
    for (const style of original.type.groups.flatMap(group => group.styles)) expect(typography.querySelector(`[data-token="typography.${style.name}"]`)).not.toBeNull()
    expect(screen.getByRole("region", { name: "Spacing" }).querySelectorAll("[data-token]")).toHaveLength(4)
    expect([...screen.getByRole("region", { name: "Corner radius" }).querySelectorAll("[data-token]")].map(item => item.getAttribute("data-token"))).toEqual(["radius.radius-sm", "radius.radius-md", "radius.radius-lg"])
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
  const system = { name: "S", colors: [{ name: "ink", value: "#000000" }], typography: [], spacing: [], radii: [], components: [{ name: "Button", description: null }, { name: "Card", description: null }] }
  it("finds the README and only previews of declared components", () => {
    const extras = libraryExtras(system, [
      { path: "README.md", content: "# S" }, { path: "components/Button/preview.html", content: "<p>b</p>" },
      { path: "components/Other/preview.html", content: "<p>o</p>" }, { path: "components/Card/preview.html", content: "  " },
    ])
    expect(extras).toEqual({ readme: "# S", previews: { Button: "<p>b</p>" } })
    expect(libraryExtras(system, [])).toEqual({ readme: null, previews: {} })
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
