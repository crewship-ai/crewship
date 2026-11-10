// A design system opens as a read-only Design library in the ArtifactFrame (Harbor 2 v87 `ds`).
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import type { ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import { DesignLibraryGate, DesignLibraryProjectView, isDesignLibrary } from "../design-library"

const state = vi.hoisted(() => ({ snapshot: null as ArtifactProjectSnapshot | null }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({
  useArtifactProjectSnapshot: () => ({ data: state.snapshot, error: null, isLoading: false, refetch: vi.fn() }),
  useArtifactProjectMutations: () => ({ restore: { mutate: vi.fn(), isPending: false }, share: { mutate: vi.fn(), isPending: false }, comment: { mutate: vi.fn(), isPending: false } }),
}))

const TOKENS = JSON.stringify({ schema_version: 1, name: "Harbor", colors: { accent: "#2f6feb", ink: "#0e1726" }, typography: { body: { size: 16, weight: 400 } }, spacing: { "space-2": 8 }, radii: { "radius-lg": 12 }, components: [{ name: "Button" }] })
function snapshot(over: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot {
  return {
    page_id: "page", can_write: false, latest_version: 2, shared_version: 2, origin: null, comments: [], versions: [],
    manifest: { version: 2, entrypoint: "index.html", files: [{ path: "design-system.json", media_type: "application/json", content: TOKENS }] }, ...over,
  }
}
// The viewer's parts as the project viewer hands them to its host; its rules are the viewer's own tests.
const viewer = vi.hoisted(() => ({ parts: null as ArtifactViewerParts | null }))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-viewer-panel", () => ({
  ArtifactViewerPanel: ({ render }: { render?: (parts: ArtifactViewerParts) => React.ReactNode }) => render?.(viewer.parts!) ?? <p>legacy viewer chrome</p>,
}))
vi.mock("@/components/features/chat/artifact-context/projects/artifact-viewer-context", () => ({ ArtifactViewerProvider: ({ children }: { children: React.ReactNode }) => children, useArtifactViewer: () => null }))

function parts(over: Partial<ArtifactViewerParts> = {}): ArtifactViewerParts {
  const data = state.snapshot!
  return {
    data, previewKind: "design-system", canWrite: data.can_write === true, canComment: true, maySend: false,
    version: { latest: data.latest_version, shared: data.shared_version, shown: data.manifest.version, viewingOld: false, pick: vi.fn() },
    screens: ["index.html"], screen: "index.html", labelOf: path => path, setScreen: vi.fn(),
    select: { on: false, available: false, toggle: vi.fn(), hint: null },
    preview: <p>legacy design-system preview</p>, pinCard: null, canvasRef: { current: null },
    banners: null, history: <div role="region" aria-label="Version history">versions</div>, queue: <div role="region" aria-label="Comment queue">queue</div>,
    sendBar: null, waiting: 0, share: null, editSourceHref: null, ...over,
  }
}
const report = vi.fn()
function mount(layout: FrameLayout = "desktop", over: Partial<ArtifactViewerParts> = {}) {
  return render(<ArtifactFrame name="Harbor design system" kind={{ kind: "design", mode: "library" }} caps={NO_FRAME_CAPABILITIES} onHome={vi.fn()} layout={layout}>
    <DesignLibraryProjectView parts={parts(over)} onFrame={report} />
  </ArtifactFrame>)
}
const slot = (name: string) => document.querySelector(`[data-artifact-slot="${name}"]`) as HTMLElement | null

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "true"); state.snapshot = snapshot(); report.mockReset() })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

describe("Design library", () => {
  it("shows the navigator, colours and typography from the manifest, read-only", () => {
    mount()
    expect(within(slot("panel")!).getAllByRole("button").map(button => button.textContent)).toEqual(["Overview", "Colors2", "Typography1", "Spacing1", "Corner radius1", "Components1", "Files1"])
    expect(screen.getByRole("heading", { name: "Harbor" })).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "Colors" })).getByText("#2f6feb")).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "Typography" })).getByText("body")).toBeInTheDocument()
    expect(report).toHaveBeenLastCalledWith(expect.objectContaining({ canEdit: false, status: expect.objectContaining({ key: "readonly" }), kind: { kind: "design", mode: "library", badge: "Library" } }))
  })

  it("hides publishing, Used by, library updates and pinning, which have no backend", () => {
    mount()
    for (const text of [/Publish/, /Used by/, /pinned/, /update/i]) expect(screen.queryByText(text)).toBeNull()
    expect(within(slot("output")!).getAllByRole("button").map(button => button.getAttribute("aria-label"))).toEqual(["Download design-system.json"])
    expect(screen.queryByRole("radio", { name: "Canvas" })).toBeNull()
  })

  it("filters with the library search", () => {
    mount()
    fireEvent.change(within(slot("view")!).getByRole("searchbox", { name: "Search the library" }), { target: { value: "ink" } })
    const colors = screen.getByRole("region", { name: "Colors" })
    expect(within(colors).queryByText("accent")).toBeNull()
    expect(within(colors).getByText("ink")).toBeInTheDocument()
  })

  it("puts the sections in the view strip on a phone instead of a bottom sheet", () => {
    mount("phone")
    expect(slot("panel")).toBeNull()
    expect(within(slot("view")!).getByRole("button", { name: "Colors" })).toBeInTheDocument()
  })

  it("is the library itself: no second title row, viewer tabs or comment column inside the frame", () => {
    mount()
    expect(screen.queryByText("legacy design-system preview")).toBeNull()
    expect(screen.queryByRole("tab")).toBeNull()
    expect(screen.queryByText("Comments for agent")).toBeNull()
    expect(screen.queryByRole("button", { name: "Edit source" })).toBeNull()
  })

  it("keeps version, share, edit source, history and comments reachable through the frame's slots and drawer", () => {
    mount("desktop", { editSourceHref: "/artifacts/page?edit=1", share: <button type="button">Share v2</button> })
    expect(within(slot("view")!).getByRole("button", { name: "Version: v2 · Latest" })).toBeInTheDocument()
    expect(within(slot("view")!).getByRole("button", { name: "Share v2" })).toBeInTheDocument()
    expect(within(slot("output")!).getByRole("link", { name: "Edit source" })).toHaveAttribute("href", "/artifacts/page?edit=1")
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(screen.getByRole("region", { name: "Version history" })).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Comments" }))
    expect(screen.getByRole("region", { name: "Comment queue" })).toBeVisible()
  })

  it("says so when the file is invalid", () => {
    state.snapshot = snapshot({ manifest: { version: 2, entrypoint: "index.html", files: [{ path: "design-system.json", media_type: "application/json", content: "{}" }] } })
    mount()
    expect(screen.queryByText("legacy design-system preview")).toBeNull()
    expect(screen.getByRole("alert")).toHaveTextContent("invalid or uses an unsupported format")
  })

  it("routes only design systems to the library and keeps other projects in their viewer", async () => {
    viewer.parts = parts()
    render(<DesignLibraryGate workspaceId="workspace" target={{ slug: "harbor", tab: "preview" }} onBack={vi.fn()} onFrame={report}><p>project viewer</p></DesignLibraryGate>)
    await act(async () => {})
    expect(screen.queryByText("project viewer")).toBeNull()
    expect(screen.queryByText("legacy viewer chrome")).toBeNull()
    expect(screen.getByRole("region", { name: "Colors" })).toBeInTheDocument()
    cleanup()
    state.snapshot = snapshot({ manifest: { version: 1, entrypoint: "index.html", files: [{ path: "index.html", media_type: "text/html", content: "<p>x</p>" }] } })
    render(<DesignLibraryGate workspaceId="workspace" target={{ slug: "site", tab: "preview" }} onBack={vi.fn()} onFrame={report}><p>project viewer</p></DesignLibraryGate>)
    expect(screen.getByText("project viewer")).toBeInTheDocument()
    expect(isDesignLibrary(snapshot({ artifact_kind: "slides" }))).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "false")
    expect(isDesignLibrary(snapshot())).toBe(false)
  })

  it("lists every file of the system read-only and shows text as text, never rendered", () => {
    const files = [
      { path: "design-system.json", media_type: "application/json", content: TOKENS },
      { path: "README.md", media_type: "text/markdown", content: "# Harbor" },
      { path: "components/Button/preview.html", media_type: "text/html", content: "<script>window.bad = 1</script><img src=x onerror=alert(1)>" },
    ]
    state.snapshot = snapshot({ manifest: { version: 2, entrypoint: "index.html", files } })
    mount()
    expect(within(slot("panel")!).getByRole("button", { name: /Files/ })).toHaveTextContent("Files3")
    const section = screen.getByRole("region", { name: "Files" })
    // The supported sections are stated, so a section the Library has no place for is not read as lost data.
    expect(section).toHaveTextContent("The Library shows colors, typography, spacing, corner radius and components from design-system.json")
    const rows = within(section).getAllByRole("row").slice(1)
    expect(rows.map(row => within(row).getAllByRole("cell").map(cell => cell.textContent))).toEqual([
      ["design-system.json", "application/json", `${new TextEncoder().encode(TOKENS).byteLength} B`, expect.any(String)],
      ["README.md", "text/markdown", "8 B", expect.any(String)],
      ["components/Button/preview.html", "text/html", "59 B", expect.any(String)],
    ])
    fireEvent.click(within(section).getByRole("button", { name: "View components/Button/preview.html" }))
    const viewer = within(section).getByRole("region", { name: "components/Button/preview.html" })
    expect(viewer.querySelector("pre")).toHaveTextContent("<script>window.bad = 1</script>")
    expect(viewer.querySelector("script, img, iframe")).toBeNull()
    expect((window as unknown as { bad?: number }).bad).toBeUndefined()
    expect(within(section).getByRole("button", { name: "View components/Button/preview.html" })).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(within(section).getByRole("button", { name: "View components/Button/preview.html" }))
    expect(within(section).queryByRole("region", { name: "components/Button/preview.html" })).toBeNull()
  })

  it("downloads a file as opaque bytes under its own name", () => {
    const created: Blob[] = []
    const createObjectURL = vi.fn((blob: Blob) => { created.push(blob); return "blob:x" })
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }))
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    mount()
    const section = screen.getByRole("region", { name: "Files" })
    fireEvent.click(within(section).getByRole("button", { name: "Download design-system.json" }))
    expect(created[0].type).toBe("application/octet-stream")
    expect(click).toHaveBeenCalledTimes(1)
    expect((click.mock.contexts[0] as HTMLAnchorElement).download).toBe("design-system.json")
    click.mockRestore()
  })

  it("filters files with the library search", () => {
    state.snapshot = snapshot({ manifest: { version: 2, entrypoint: "index.html", files: [
      { path: "design-system.json", media_type: "application/json", content: TOKENS }, { path: "tokens.json", media_type: "application/json", content: "{}" },
    ] } })
    mount()
    fireEvent.change(within(slot("view")!).getByRole("searchbox", { name: "Search the library" }), { target: { value: "tokens" } })
    expect(within(screen.getByRole("region", { name: "Files" })).getAllByRole("row").slice(1).map(row => row.firstChild!.textContent)).toEqual(["tokens.json"])
  })

  it("truncates a long file in the viewer and says so", () => {
    state.snapshot = snapshot({ manifest: { version: 2, entrypoint: "index.html", files: [
      { path: "design-system.json", media_type: "application/json", content: TOKENS }, { path: "big.txt", media_type: "text/plain", content: "a".repeat(70 * 1024) },
    ] } })
    mount()
    const section = screen.getByRole("region", { name: "Files" })
    fireEvent.click(within(section).getByRole("button", { name: "View big.txt" }))
    const viewer = within(section).getByRole("region", { name: "big.txt" })
    expect(viewer.querySelector("pre")!.textContent).toHaveLength(64 * 1024)
    expect(viewer).toHaveTextContent("Showing the first 64 KiB. Download the file for all of it.")
  })
  it("bounds multibyte file previews by UTF-8 bytes without splitting a character", () => {
    const content = "a" + "🖌".repeat(20 * 1024)
    state.snapshot = snapshot({ manifest: { version: 2, entrypoint: "index.html", files: [
      { path: "design-system.json", media_type: "application/json", content: TOKENS }, { path: "unicode.txt", media_type: "text/plain", content },
    ] } })
    mount()
    const section = screen.getByRole("region", { name: "Files" })
    fireEvent.click(within(section).getByRole("button", { name: "View unicode.txt" }))
    const viewer = within(section).getByRole("region", { name: "unicode.txt" })
    const text = viewer.querySelector("pre")!.textContent!
    expect(new TextEncoder().encode(text).byteLength).toBeLessThanOrEqual(64 * 1024)
    expect(text).toBe("a" + "🖌".repeat(16383))
    expect(text).not.toContain("�")
    expect(viewer).toHaveTextContent("Showing the first 64 KiB. Download the file for all of it.")
  })

})
