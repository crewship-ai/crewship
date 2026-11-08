// A design system opens as a read-only Design library in the ArtifactFrame (Harbor 2 v87 `ds`).
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import { DesignLibraryBody, DesignLibraryGate, isDesignLibrary } from "../design-library"

const state = vi.hoisted(() => ({ snapshot: null as ArtifactProjectSnapshot | null }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({
  useArtifactProjectSnapshot: () => ({ data: state.snapshot, error: null, refetch: vi.fn() }),
  useArtifactProjectMutations: () => ({ restore: { mutate: vi.fn(), isPending: false }, share: { mutate: vi.fn(), isPending: false }, comment: { mutate: vi.fn(), isPending: false } }),
}))

const TOKENS = JSON.stringify({ schema_version: 1, name: "Harbor", colors: { accent: "#2f6feb", ink: "#0e1726" }, typography: { body: { size: 16, weight: 400 } }, spacing: { "space-2": 8 }, radii: { "radius-lg": 12 }, components: [{ name: "Button" }] })
function snapshot(over: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot {
  return {
    page_id: "page", can_write: false, latest_version: 2, shared_version: 2, origin: null, comments: [], versions: [],
    manifest: { version: 2, entrypoint: "index.html", files: [{ path: "design-system.json", media_type: "application/json", content: TOKENS }] }, ...over,
  }
}
const report = vi.fn()
function mount(layout: FrameLayout = "desktop") {
  return render(<ArtifactFrame name="Harbor design system" kind={{ kind: "design", mode: "library" }} caps={NO_FRAME_CAPABILITIES} onHome={vi.fn()} layout={layout}>
    <DesignLibraryBody workspaceId="workspace" slug="harbor" onFrame={report} />
  </ArtifactFrame>)
}
const slot = (name: string) => document.querySelector(`[data-artifact-slot="${name}"]`) as HTMLElement | null

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "true"); state.snapshot = snapshot(); report.mockReset() })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

describe("Design library", () => {
  it("shows the navigator, colours and typography from the manifest, read-only", () => {
    mount()
    expect(within(slot("panel")!).getAllByRole("button").map(button => button.textContent)).toEqual(["Overview", "Colors2", "Typography1", "Spacing1", "Corner radius1", "Components1"])
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

  it("says so when the file is invalid", () => {
    state.snapshot = snapshot({ manifest: { version: 2, entrypoint: "index.html", files: [{ path: "design-system.json", media_type: "application/json", content: "{}" }] } })
    mount()
    expect(screen.getByRole("alert")).toHaveTextContent("invalid or uses an unsupported format")
  })

  it("routes only design systems to the library and keeps other projects in their viewer", async () => {
    render(<DesignLibraryGate workspaceId="workspace" slug="harbor" onFrame={report}><p>project viewer</p></DesignLibraryGate>)
    await act(async () => {})
    expect(screen.queryByText("project viewer")).toBeNull()
    cleanup()
    state.snapshot = snapshot({ manifest: { version: 1, entrypoint: "index.html", files: [{ path: "index.html", media_type: "text/html", content: "<p>x</p>" }] } })
    render(<DesignLibraryGate workspaceId="workspace" slug="site" onFrame={report}><p>project viewer</p></DesignLibraryGate>)
    expect(screen.getByText("project viewer")).toBeInTheDocument()
    expect(isDesignLibrary(snapshot({ artifact_kind: "slides" }))).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "false")
    expect(isDesignLibrary(snapshot())).toBe(false)
  })
})
