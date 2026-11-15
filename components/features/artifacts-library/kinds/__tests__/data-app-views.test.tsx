import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../frame"
import { AppProjectView } from "../app/app-view"
import { DashboardProjectView } from "../dashboard/dashboard-view"
import type { KindReport } from "../project-parts"

const read = vi.hoisted(() => ({ value: { pending: false, error: false as unknown, limitExceeded: false, collections: undefined as unknown, manifest: undefined as unknown } }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-dashboard-data", () => ({ useArtifactDashboardData: () => read.value }))
const runs = vi.hoisted(() => ({ value: null as unknown }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-dashboard-runs", () => ({ useArtifactDashboardRuns: () => runs.value }))

afterEach(cleanup)
beforeEach(() => { read.value = { pending: false, error: false, limitExceeded: false, collections: undefined, manifest: undefined }; runs.value = null })

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
    read.value = { pending: false, error: false, limitExceeded: false, collections, manifest: undefined }
    const reports: (KindReport | null)[] = []
    inFrame(<DashboardProjectView workspaceId="w" parts={parts({ screens: ["index.html"], screen: "index.html" })} onReport={report => reports.push(report)} />)
    expect(await screen.findByRole("button", { name: /^Data freshness: Updated \d\d:\d\d · HubSpot$/ })).toBeInTheDocument()
    await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "live" }))
    fireEvent.click(screen.getByRole("button", { name: "Sources" }))
    expect(screen.getAllByText("deals").length).toBeGreaterThan(0)
    expect(screen.queryByRole("button", { name: /Refresh/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /Export/ })).toBeNull()
  })

  const files = (datasets?: string) => [
    { path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["deals"]}' },
    ...(datasets ? [{ path: "datasets.json", media_type: "application/json", content: datasets }] : []),
  ]
  const declared = { schema_version: 1, collections: ["deals"] }

  it("shows the stated title, description, mode and max age with the lineage, and offers per-value citations for bound pages", async () => {
    read.value = { pending: false, error: false, limitExceeded: false, collections, manifest: declared }
    const data = snapshot({ manifest: { version: 4, entrypoint: "index.html", files: files('{"schema_version":1,"datasets":{"deals":{"title":"Open deals","description":"Pipeline from the CRM.","mode":"planned","max_age_seconds":7200}}}') } })
    inFrame(<DashboardProjectView workspaceId="w" parts={parts({ data, previewKind: "dashboard" })} />)
    fireEvent.click(await screen.findByRole("button", { name: "Sources" }))
    const row = document.querySelector('[data-dataset="deals"]') as HTMLElement
    expect(row).toHaveTextContent("Open deals")
    expect(row).toHaveTextContent("Pipeline from the CRM.")
    expect(row).toHaveTextContent("Planned refresh · declared· Max age 2 h")
    expect(row).toHaveTextContent(/Last write \d\d:\d\d · HubSpot/)
    expect(row).toHaveTextContent("Collection deals · declared by v4")
    expect(screen.getByText(/Each bound value carries its source; hover or focus a number/)).toBeInTheDocument()
  })

  it("does not promise hover citations on an interactive dashboard and says when dataset details are invalid", async () => {
    read.value = { pending: false, error: false, limitExceeded: false, collections, manifest: declared }
    const data = snapshot({ interactive: { runtime_url: "https://apps.example.test/run", development_same_origin: false, collections: ["deals"] }, manifest: { version: 4, entrypoint: "index.html", files: files('{"schema_version":1,"datasets":{"sales":{"mode":"fixed"}}}') } })
    inFrame(<DashboardProjectView workspaceId="w" parts={parts({ data, previewKind: "interactive" })} />)
    fireEvent.click(await screen.findByRole("button", { name: "Sources" }))
    expect(screen.queryByText(/hover/)).toBeNull()
    expect(screen.getByText(/values carry no per-number citation/)).toBeInTheDocument()
    expect(screen.getByText(/shows declared datasets and their stored values/)).toBeInTheDocument()
    expect(screen.getAllByRole("alert").some(node => /datasets\.json are invalid/.test(node.textContent ?? ""))).toBe(true)
  })

  it("turns Stale while the dashboard stays open and the data does not change", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true, now: new Date("2026-11-08T10:00:00Z") })
    try {
      read.value = { pending: false, error: false, limitExceeded: false, manifest: declared,
        collections: [{ ...collections[0], documents: [{ ...collections[0].documents[0], updated_at: "2026-11-08T09:59:00Z" }] }] }
      const data = snapshot({ manifest: { version: 4, entrypoint: "index.html", files: files('{"schema_version":1,"datasets":{"deals":{"mode":"live","max_age_seconds":120}}}') } })
      const reports: (KindReport | null)[] = []
      inFrame(<DashboardProjectView workspaceId="w" parts={parts({ data, previewKind: "dashboard" })} onReport={report => reports.push(report)} />)
      await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "live" }))
      await vi.advanceTimersByTimeAsync(90_000)
      await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "stale" }))
    } finally { vi.useRealTimers() }
  })

  it("shows a failed producer run with its reason and last success, and keeps the values", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true, now: new Date(2026, 10, 8, 12, 0, 0) })
    try {
      read.value = { pending: false, error: false, limitExceeded: false, collections, manifest: declared }
      runs.value = [{ collection: "deals", state: "failed", startedAt: new Date(Date.now() - 60_000), finishedAt: new Date(), lastAttemptAt: new Date(Date.now() - 60_000), lastSuccessAt: new Date(Date.now() - 3_600_000), reason: "CRM returned 502", startedBy: "Agent", partial: false, stalledAfterMs: 1_800_000 }]
      const data = snapshot({ manifest: { version: 4, entrypoint: "index.html", files: files('{"schema_version":1,"datasets":{"deals":{"title":"Open deals","mode":"planned","max_age_seconds":7200}}}') } })
      const reports: (KindReport | null)[] = []
      inFrame(<DashboardProjectView workspaceId="w" parts={parts({ data, previewKind: "dashboard" })} onReport={report => reports.push(report)} />)
      await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "stale", label: "Refresh failed", detail: expect.stringContaining("CRM returned 502") }))
      fireEvent.click(await screen.findByRole("button", { name: "Sources" }))
      const row = document.querySelector('[data-dataset="deals"]') as HTMLElement
      expect(row).toHaveTextContent("1 value")
      expect(row.querySelector('[data-run-state="failed"]')).toHaveTextContent(/Refresh failed \d\d:\d\d: CRM returned 502 · Last success \d\d:\d\d/)
    } finally { vi.useRealTimers() }
  })

  it("reports Refreshing while a producer run is in progress", async () => {
    read.value = { pending: false, error: false, limitExceeded: false, collections, manifest: declared }
    runs.value = [{ collection: "deals", state: "running", startedAt: new Date(), finishedAt: null, lastAttemptAt: new Date(), lastSuccessAt: null, reason: null, startedBy: "Agent", partial: false, stalledAfterMs: 1_800_000 }]
    const reports: (KindReport | null)[] = []
    inFrame(<DashboardProjectView workspaceId="w" parts={parts({ data: snapshot({ manifest: { version: 4, entrypoint: "index.html", files: files() } }), previewKind: "dashboard" })} onReport={report => reports.push(report)} />)
    await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "refreshing" }))
    fireEvent.click(await screen.findByRole("button", { name: "Sources" }))
    expect(document.querySelector('[data-run-state="running"]')).toHaveTextContent(/Refreshing since \d\d:\d\d · No successful refresh yet/)
  })

  it("reports Sample data from datasets.json", async () => {
    read.value = { pending: false, error: false, limitExceeded: false, collections, manifest: declared }
    const data = snapshot({ manifest: { version: 4, entrypoint: "index.html", files: files('{"schema_version":1,"datasets":{"deals":{"mode":"fixed","sample":true}}}') } })
    const reports: (KindReport | null)[] = []
    inFrame(<DashboardProjectView workspaceId="w" parts={parts({ data, previewKind: "dashboard" })} onReport={report => reports.push(report)} />)
    await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "sample" }))
  })

  it("reports an error and no Sources tab when the data cannot be read", async () => {
    read.value = { pending: false, error: true, limitExceeded: false, collections: undefined, manifest: undefined }
    const reports: (KindReport | null)[] = []
    inFrame(<DashboardProjectView workspaceId="w" parts={parts()} onReport={report => reports.push(report)} />)
    expect(await screen.findByRole("button", { name: "Data freshness: Data unavailable" })).toBeInTheDocument()
    await waitFor(() => expect(reports.at(-1)?.status).toMatchObject({ key: "error" }))
    expect(screen.queryByRole("button", { name: "Sources" })).toBeNull()
  })

  it("uses the dashboard's own pages as view tabs and fills the width on a phone", async () => {
    read.value = { pending: true, error: false, limitExceeded: false, collections: undefined, manifest: undefined }
    const setScreen = vi.fn()
    const { container } = inFrame(<DashboardProjectView workspaceId="w" parts={parts({ setScreen })} />, "phone")
    fireEvent.click(await screen.findByRole("button", { name: "index.html" }))
    expect(setScreen).toHaveBeenCalledWith("index.html")
    expect(container.querySelector("[data-dashboard-stage] > div:nth-child(2)")).not.toHaveClass("rounded-lg")
  })
})
