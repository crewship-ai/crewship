import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../frame"
import { AppProjectView } from "../app/app-view"
import { DashboardProjectView } from "../dashboard/dashboard-view"
import type { KindReport } from "../project-parts"

const read = vi.hoisted(() => ({ value: { pending: false, error: false as unknown, limitExceeded: false, collections: undefined as unknown } }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-dashboard-data", () => ({ useArtifactDashboardData: () => read.value }))

afterEach(cleanup)
beforeEach(() => { read.value = { pending: false, error: false, limitExceeded: false, collections: undefined } })

const snapshot = (over: Partial<ArtifactProjectSnapshot> = {}): ArtifactProjectSnapshot => ({
  page_id: "page-1", name: "Pricing site", can_write: true, latest_version: 4, shared_version: 3,
  manifest: { version: 4, entrypoint: "index.html", files: [] }, versions: [{ version: 4 }, { version: 3 }], comments: [], ...over,
})

function parts(over: Partial<ArtifactViewerParts> = {}): ArtifactViewerParts {
  return {
    data: snapshot(), previewKind: "static", canWrite: true, canComment: true, maySend: false,
    version: { latest: 4, shared: 3, shown: 4, viewingOld: false, pick: vi.fn() },
    screens: ["index.html", "pricing.html"], screen: "pricing.html", labelOf: path => path, setScreen: vi.fn(),
    select: { on: false, available: true, toggle: vi.fn(), hint: null },
    preview: <iframe title="Preview of pricing.html" />, pinCard: null, canvasRef: { current: null },
    banners: null, history: <div role="region" aria-label="Version history">versions</div>, queue: <div role="region" aria-label="Comment queue">queue</div>,
    sendBar: null, waiting: 0, share: null, editSourceHref: null, ...over,
  }
}

function inFrame(body: React.ReactNode, layout: FrameLayout = "desktop", canEdit = false) {
  return render(<ArtifactFrame name="Pricing site" kind={{ kind: "app" }} caps={{ ...NO_FRAME_CAPABILITIES, canEdit, agent: { ask: { chatHref: () => "/chat" } } }} onHome={vi.fn()} layout={layout}>{body}</ArtifactFrame>)
}

describe("App view", () => {
  it("puts Preview, version and page in view with Static under it, and no Source, Compare or Export", async () => {
    inFrame(<AppProjectView parts={parts()} />)
    expect(await screen.findByRole("button", { name: "Preview" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: "Version: v4 · Latest" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Page: pricing.html" })).toBeInTheDocument()
    expect(screen.getByRole("group", { name: "Capabilities" })).toHaveTextContent("Static · no capabilities")
    for (const name of ["Source", "Compare", "Export"]) expect(screen.queryByRole("button", { name })).toBeNull()
    expect(screen.getByRole("button", { name: "Reload app" })).toBeInTheDocument()
    expect(screen.getByTitle("Preview of pricing.html")).toBeInTheDocument()
  })

  it("shows the declared capabilities of an interactive app and gates a missing runtime origin", async () => {
    const data = snapshot({ interactive: { runtime_url: "", development_same_origin: false, collections: ["quotes"] } })
    const reports: (KindReport | null)[] = []
    inFrame(<AppProjectView parts={parts({ data, previewKind: "interactive" })} onReport={report => reports.push(report)} />)
    const caps = await screen.findByRole("group", { name: "Capabilities" })
    expect(caps).toHaveTextContent("User")
    expect(caps).toHaveTextContent("Data")
    expect(screen.getByRole("alert")).toHaveTextContent("This app can't run here yet")
    expect(screen.queryByTitle("Preview of pricing.html")).toBeNull()
    await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "error", label: "Can't run here" }))
  })

  it("says Runs isolated only of an app that runs, never beside the can't-run gate", async () => {
    const gated = snapshot({ interactive: { runtime_url: "", development_same_origin: false, collections: ["quotes"] } })
    inFrame(<AppProjectView parts={parts({ data: gated, previewKind: "interactive" })} />)
    await screen.findByRole("alert")
    expect(screen.queryByText("Runs isolated")).toBeNull()
    cleanup()
    const running = snapshot({ interactive: { runtime_url: "https://apps.example.test/run", development_same_origin: false, collections: ["quotes"] } })
    inFrame(<AppProjectView parts={parts({ data: running, previewKind: "interactive" })} />)
    expect(await screen.findByText("Runs isolated")).toBeInTheDocument()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("applies device width and zoom to the preview frame on a wide stage", async () => {
    const { container } = inFrame(<AppProjectView parts={parts()} />)
    fireEvent.click(await screen.findByRole("button", { name: "Phone · 390" }))
    const frame = container.querySelector<HTMLElement>("[data-app-frame]")!
    expect(frame.style.width).toBe("390px")
    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }))
    expect(frame.style.zoom).toBe("1.25")
    fireEvent.click(screen.getByRole("button", { name: "Fit width" }))
    expect(frame.style.zoom).toBe("")
  })

  it("fills the width on a phone, with no viewport controls", async () => {
    const { container } = inFrame(<AppProjectView parts={parts()} />, "phone")
    await screen.findByRole("group", { name: "Capabilities" })
    expect(container.querySelector<HTMLElement>("[data-app-frame]")!.getAttribute("style") ?? "").toBe("")
    expect(document.querySelector('[data-artifact-slot="viewport"]')).toHaveClass("hidden")
  })

  it("moves the queue and history into the drawer and registers Propose only with the agent path", async () => {
    const { unmount } = inFrame(<AppProjectView parts={parts()} />, "desktop", true)
    expect(await screen.findByRole("button", { name: "Comments" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "History" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    expect(screen.queryByRole("radiogroup", { name: "Agent mode" })).toBeNull()
    unmount()

    inFrame(<AppProjectView parts={parts({ maySend: true, sendBar: <button type="button">Send 1 to agent</button>, waiting: 1 })} />, "desktop", true)
    fireEvent.click(await screen.findByRole("button", { name: "Agent" }))
    const propose = screen.getByRole("radio", { name: "Propose" })
    expect(propose).not.toBeDisabled()
    fireEvent.click(propose)
    expect(screen.getAllByRole("button", { name: "Send 1 to agent" }).length).toBeGreaterThan(0)
  })

  it("hides Comment when this person cannot comment", async () => {
    inFrame(<AppProjectView parts={parts({ canWrite: false, canComment: false, select: { on: false, available: false, toggle: vi.fn(), hint: null } })} />)
    await screen.findByRole("group", { name: "Capabilities" })
    expect(screen.queryByRole("button", { name: "Add a comment" })).toBeNull()
  })
})

describe("Dashboard view", () => {
  const collections = [{ collection: "deals", code_version: 4, documents: [{ id: "d1", version: 1, value: {}, updated_at: new Date().toISOString(), source_label: "HubSpot", source_kind: "agent" }] }]

  it("reports Live with freshness from the data read and lists sources in the drawer", async () => {
    read.value = { pending: false, error: false, limitExceeded: false, collections }
    const reports: (KindReport | null)[] = []
    inFrame(<DashboardProjectView workspaceId="w" parts={parts({ screens: ["index.html"], screen: "index.html" })} onReport={report => reports.push(report)} />)
    expect(await screen.findByRole("button", { name: /^Data freshness: Updated \d\d:\d\d · HubSpot$/ })).toBeInTheDocument()
    await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "live" }))
    fireEvent.click(screen.getByRole("button", { name: "Sources" }))
    expect(screen.getAllByText("deals").length).toBeGreaterThan(0)
    expect(screen.queryByRole("button", { name: /Refresh/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /Export/ })).toBeNull()
  })

  it("reports an error and no Sources tab when the data cannot be read", async () => {
    read.value = { pending: false, error: true, limitExceeded: false, collections: undefined }
    const reports: (KindReport | null)[] = []
    inFrame(<DashboardProjectView workspaceId="w" parts={parts()} onReport={report => reports.push(report)} />)
    expect(await screen.findByRole("button", { name: "Data freshness: Data unavailable" })).toBeInTheDocument()
    await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "error" }))
    expect(screen.queryByRole("button", { name: "Sources" })).toBeNull()
  })

  it("uses the dashboard's own pages as view tabs and fills the width on a phone", async () => {
    read.value = { pending: true, error: false, limitExceeded: false, collections: undefined }
    const setScreen = vi.fn()
    const { container } = inFrame(<DashboardProjectView workspaceId="w" parts={parts({ setScreen })} />, "phone")
    fireEvent.click(await screen.findByRole("button", { name: "index.html" }))
    expect(setScreen).toHaveBeenCalledWith("index.html")
    expect(container.querySelector("[data-dashboard-stage] > div:nth-child(2)")).not.toHaveClass("rounded-lg")
  })
})
