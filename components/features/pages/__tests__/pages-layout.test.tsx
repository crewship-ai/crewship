/**
 * The /pages shell — PRD §9b.1 (three zones) and §9b.2 (the header line).
 *
 * The header line is the Routines/Credentials idiom: icon + name + `·` + a
 * dense count summary — `38 routines · 0 runs`, `12 secrets · 2 waiting on a
 * tool`, `12 pages · 3 stale`. The second clause is the one worth testing:
 * claiming "all fresh" over an index that reported no freshness at all is the
 * silent-old-numbers failure §4 exists to prevent, so it is dropped rather
 * than guessed.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

const identity = vi.hoisted(() => ({ id: "pages-test-human" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: identity.id } }, status: "authenticated" }) }))

const access = vi.hoisted(() => ({role: "MANAGER" as string | null, capabilities: [] as string[], loading: false, error: false}))
vi.mock("@/hooks/use-workspace", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/use-workspace")>(),
  useWorkspace: () => ({...access, workspaceId: "ws-1", workspaces: [{id:"ws-1",currentUserRole:access.role}], workspace:{id:"ws-1"}}),
}))
const push = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

// The four editor sections are five other files' worth of behaviour and are
// tested there. This suite is about the shell around them: which door opens,
// what the address says, and that the rail never blinks.
vi.mock("@/components/features/pages/editor/section-content", () => ({
  EditorContentSection: () => <div data-testid="section-content">Content</div>,
}))
vi.mock("@/components/features/pages/editor/section-data-actions", () => ({
  EditorDataActionsSection: () => <div data-testid="section-data">Data & actions</div>,
}))
vi.mock("@/components/features/pages/editor/section-access", () => ({
  EditorAccessSection: () => <div data-testid="section-access">Access</div>,
}))
vi.mock("@/components/features/pages/editor/section-history", () => ({
  EditorHistorySection: () => <div data-testid="section-history">History</div>,
}))
// A stand-in application host: reports "an application is on screen" for a
// Page that has one, and follows the header's switch between the frame and
// the panels — which is exactly the contract the real view keeps.
vi.mock("@/components/features/pages/page-application", () => ({
  PageApplicationView: ({
    page,
    panels,
    fallback,
    onAvailableChange,
  }: {
    page: { has_application?: boolean } | null
    panels?: boolean
    fallback: React.ReactNode
    onAvailableChange?: (available: boolean) => void
  }) => {
    const available = page?.has_application === true
    React.useEffect(() => {
      onAvailableChange?.(available)
      return () => onAvailableChange?.(false)
    }, [available, onAvailableChange])
    if (!available || panels) return <>{fallback}</>
    return <div data-testid="application">Running application</div>
  },
}))

vi.mock("@/components/features/pages/page-document",()=>({PageDocument:({pageId,workspaceId}:{pageId:string;workspaceId:string})=><div data-testid="native-document">Native document {workspaceId}:{pageId}</div>}))
const nativeHost = vi.hoisted(() => ({ unavailable: undefined as undefined | (() => void) }))
vi.mock("@/components/features/pages/wordcraft-editor-host", () => ({ WordCraftEditorHost: ({ onDirtyChange, onUnavailable, unavailable }: { unavailable?: boolean; onDirtyChange: (dirty: boolean) => void; onUnavailable?: () => void }) => {
  nativeHost.unavailable = onUnavailable
  return <div data-testid="native-wordcraft" data-blocked={String(Boolean(unavailable))}><button onClick={() => onDirtyChange(true)}>Make draft</button></div>
} }))
vi.mock("@/components/features/pages/wordcraft-command-editor-host", () => ({ WordCraftCommandEditorHost: ({ onDirtyChange, unavailable }: { unavailable?: boolean; onDirtyChange: (dirty: boolean) => void }) => <div data-testid="native-commands" data-blocked={String(Boolean(unavailable))}><button onClick={() => onDirtyChange(true)}>Make command pending</button></div> }))
vi.mock("@/components/features/pages/native-document-access-dialog", () => ({ NativeDocumentAccessDialog: ({ slug, onClose }: { slug: string; onClose: () => void }) => <div role="dialog">Access {slug}<button onClick={onClose}>Close access</button></div> }))
// The leaf's independent ACL/query behavior has its own tests. Here observe
// whether the layout mounts CURRENT metadata beside a retained draft producer.
vi.mock("@/components/features/chat/right-panel-tabs/chat-document-access", () => ({ NativeDocumentProvenance: ({ pageId, viewerId }: { pageId: string; viewerId: string }) => <div data-testid="native-provenance" data-page={pageId} data-viewer={viewerId} /> }))

vi.mock("@/components/features/pages/artifact-project", () => ({ ArtifactProject: ({workspaceId, slug, initialVersion}: {workspaceId: string; slug: string; initialVersion?: number}) => <div data-testid="artifact-reader" data-workspace={workspaceId} data-page={slug} data-pin={initialVersion}>Versioned artifact reader</div> }))

import { PagesLayout } from "@/components/features/pages/pages-layout"
import { PAGE_STATE_ORDER } from "@/components/features/pages/page-state"
import { PANEL_STATES } from "@/components/features/pages/panels/types"
import { pagesKeys, type WirePage } from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

const FLEET: WirePage = {
  id: "cpage1",
  slug: "fleet-201",
  name: "Flotila .201",
  owner: "crew/lookout",
  panels: [
    { id: "a", schema: "status.v1", title: "Jede to?", span: 8, state: "stale", data: { items: [] } },
  ],
}
const CLOSE: WirePage = {
  id: "cpage2",
  slug: "nightly-close",
  name: "Nightly close",
  owner: "crew/finance",
  panels: [{ id: "a", schema: "metric.v1", span: 4, state: "fresh", data: { value: 1 } }],
}

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) } as unknown as Response
}

function newQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
}

function renderLayout(list: WirePage[], slug?: string, documentStatus: object = {enabled:false}) {
  const qc = newQueryClient()
  const mockFetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/api/v1/documents/status")) return okJSON(documentStatus)
    // The folders route has its own prefix (`/page-folders`, because
    // `/pages/folders/{slug}` collides with `/pages/{slug}/grants` in the Go
    // mux); this layout has no folders, so the rail groups by owner.
    if (url.includes("/api/v1/page-folders")) return okJSON({ folders: [] })
    if (url.includes("/api/v1/pages/")) {
      const wanted = decodeURIComponent(url.split("/api/v1/pages/")[1].split("?")[0])
      return okJSON(list.find((p) => p.slug === wanted) ?? null)
    }
    return okJSON(list)
  })
  vi.stubGlobal("fetch", mockFetch)
  const view = render(
    <QueryClientProvider client={qc}>
      <PagesLayout workspaceId="ws-1" slug={slug} now={NOW} />
    </QueryClientProvider>,
  )
  return { ...view, qc, mockFetch }
}

describe("PagesLayout", () => {
  beforeEach(() => {
    cleanup()
    identity.id = "pages-test-human"
    Object.assign(access, {role:"MANAGER",capabilities:[],loading:false,error:false})
    push.mockReset()
    // The editor's mode and section live in the address now, so a test that
    // left `?mode=edit` there would hand it to the next one. (resets the
    // address between tests.)
    window.history.replaceState(null, "", "/pages")
  })
  afterEach(() => { vi.unstubAllGlobals(); vi.unstubAllEnvs() })

  it.each(["project", "dashboard", "slides"] as const)("uses current selected %s metadata inside the guarded shell", async kind => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    renderLayout([{...FLEET, artifact_kind: kind, has_application: true}], FLEET.slug!)
    expect(await screen.findByTestId("artifact-reader")).toHaveAttribute("data-page", FLEET.slug)
    expect(screen.queryByTestId("application")).toBeNull()
    expect(screen.queryByRole("button", {name: "Edit"})).toBeNull()
    expect(screen.queryByRole("button", {name: "Share"})).toBeNull()
    expect(screen.queryByText("Jede to?")).toBeNull()
  })

  it("preserves legacy application routing with the artifact flag off", async () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "false")
    renderLayout([{...FLEET, artifact_kind: "dashboard", has_application: true}], FLEET.slug!)
    await screen.findByTestId("application")
    expect(screen.queryByTestId("artifact-reader")).toBeNull()
  })

  it("does not mount either content reader before initial selected metadata is authorized", () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    const qc = newQueryClient()
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})))
    render(<QueryClientProvider client={qc}><PagesLayout workspaceId="ws-1" slug={FLEET.slug!} now={NOW} /></QueryClientProvider>)
    expect(screen.queryByTestId("artifact-reader")).toBeNull()
    expect(screen.queryByTestId("application")).toBeNull()
    expect(screen.queryByText("Jede to?")).toBeNull()
  })

  it.each([undefined, 2, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])("applies only valid selected code pin %s", async pin => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    const qc = newQueryClient()
    qc.setQueryData(pagesKeys.detail("ws-1", FLEET.slug!, identity.id), {...FLEET, artifact_kind: "dashboard"})
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})))
    render(<QueryClientProvider client={qc}><PagesLayout workspaceId="ws-1" slug={FLEET.slug!} initialArtifactVersion={pin} now={NOW} /></QueryClientProvider>)
    expect((await screen.findByTestId("artifact-reader")).getAttribute("data-pin")).toBe(pin === 2 ? "2" : null)
  })

  it.each(["keep", "discard"] as const)("keeps the real dirty producer until %s answers navigation to an artifact", async choice => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    window.history.replaceState(null, "", `/pages/${FLEET.slug}`)
    const view = renderLayout([{...FLEET, has_collaboration: true}, {...CLOSE, artifact_kind: "dashboard"}], FLEET.slug!)
    const original = await screen.findByTestId("native-wordcraft")
    fireEvent.click(screen.getByRole("button", {name: "Make draft"}))
    window.history.replaceState(null, "", `/pages/${CLOSE.slug}`)
    view.rerender(<QueryClientProvider client={view.qc}><PagesLayout workspaceId="ws-1" slug={CLOSE.slug!} initialArtifactVersion={7} now={NOW} /></QueryClientProvider>)
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("Leave with unsent edits?")
    expect(screen.getByTestId("native-wordcraft")).toBe(original)
    expect(screen.queryByTestId("artifact-reader")).toBeNull()
    fireEvent.click(screen.getByRole("button", {name: choice === "keep" ? "Keep editing" : "Leave anyway"}))
    if (choice === "keep") {
      expect(screen.getByTestId("native-wordcraft")).toBe(original)
      expect(screen.queryByTestId("artifact-reader")).toBeNull()
      expect(window.location.pathname).toBe(`/pages/${FLEET.slug}`)
    } else {
      expect(await screen.findByTestId("artifact-reader")).toHaveAttribute("data-page", CLOSE.slug)
      expect(screen.getByTestId("artifact-reader")).toHaveAttribute("data-pin", "7")
      expect(original.isConnected).toBe(false)
    }
  })

  it.each([403, 404, 500])("does not render stale artifact data or panels after metadata refresh %s", async status => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    const view = renderLayout([{...FLEET, artifact_kind: "dashboard", has_application: true}], FLEET.slug!)
    await screen.findByTestId("artifact-reader")
    view.mockFetch.mockImplementation(async () => new Response(JSON.stringify({error:"unavailable"}), {status}))
    await act(async () => { await view.qc.refetchQueries({queryKey: pagesKeys.detail("ws-1", FLEET.slug!, identity.id)}) })
    await waitFor(() => expect(screen.queryByTestId("artifact-reader")).toBeNull())
    expect(screen.queryByTestId("application")).toBeNull()
    expect(screen.queryByText("Jede to?")).toBeNull()
  })

  it("opens native sharing without replacing a dirty WordCraft editor and closes it when the host stops", async () => {
    renderLayout([{ ...FLEET, has_collaboration: true }], FLEET.slug!)
    const editor = await screen.findByTestId("native-wordcraft")
    fireEvent.click(screen.getByRole("button", { name: "Make draft" }))
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(screen.getByRole("dialog")).toHaveTextContent(`Access ${FLEET.slug}`)
    expect(screen.getByTestId("native-wordcraft")).toBe(editor)
    expect(screen.queryByTestId("section-content")).not.toBeInTheDocument()
    act(() => nativeHost.unavailable?.())
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(screen.getByTestId("native-wordcraft")).toBe(editor)
  })

  it("selects C1 by immutable format and keeps that original host blocked when availability is withdrawn", async () => {
    const view = renderLayout([{ ...FLEET, has_collaboration: true, collaboration_format: "wordcraft-c1-v2" }], FLEET.slug!, {enabled:false,wordcraft_commands:true})
    const editor = await screen.findByTestId("native-commands")
    expect(screen.queryByTestId("native-wordcraft")).toBeNull()
    fireEvent.click(screen.getByRole("button",{name:"Make command pending"}))
    act(() => { view.qc.setQueryData(["documents-status","ws-1"],{enabled:false,wordcraftCommands:false}) })
    await waitFor(() => expect(editor).toHaveAttribute("data-blocked","true"))
    expect(screen.getByTestId("native-commands")).toBe(editor)
    expect(screen.queryByTestId("native-wordcraft")).toBeNull()
  })

  it.each(["unknown","wordcraft-c1-v2"])("does not open an alternate engine for unavailable format %s",async format=>{
    renderLayout([{ ...FLEET, has_collaboration:true,collaboration_format:format }], FLEET.slug!, {enabled:false,wordcraft_commands:false})
    await screen.findByText("This document format is unavailable. No alternate editor has been opened.")
    expect(screen.queryByTestId("native-wordcraft")).toBeNull()
    expect(screen.queryByTestId("native-commands")).toBeNull()
  })

  it("does not fall back to a pilot when declared format contradicts storage classification",async()=>{
    renderLayout([{...FLEET,has_collaboration:false,has_mooring:true,collaboration_format:"unknown"}],FLEET.slug!)
    await screen.findByText("This document format is unavailable. No alternate editor has been opened.")
    expect(screen.queryByTestId("native-wordcraft")).toBeNull()
  })

  it("does not replace a retained C1 producer when a metadata refresh changes its format",async()=>{
    const page = {...FLEET,has_collaboration:true,collaboration_format:"wordcraft-c1-v2"}
    const view=renderLayout([page],FLEET.slug!,{enabled:false,wordcraft_commands:true})
    const editor=await screen.findByTestId("native-commands")
    act(()=>view.qc.setQueryData(pagesKeys.detail("ws-1",FLEET.slug!,identity.id),{...page,collaboration_format:"mooring-causal-v1"}))
    await waitFor(()=>expect(editor).toHaveAttribute("data-blocked","true"))
    expect(screen.getByTestId("native-commands")).toBe(editor)
    expect(screen.queryByTestId("native-wordcraft")).toBeNull()
  })

  it("replaces the old native host even when the new human Page cache is already warm", async () => {
    const page = { ...FLEET, has_collaboration: true }
    const view = renderLayout([page], FLEET.slug!)
    const original = await screen.findByTestId("native-wordcraft")
    view.qc.setQueryData(pagesKeys.detail("ws-1", FLEET.slug!, "second-human"), page)
    identity.id = "second-human"
    view.rerender(<QueryClientProvider client={view.qc}><PagesLayout workspaceId="ws-1" slug={FLEET.slug!} now={NOW} /></QueryClientProvider>)
    await waitFor(() => expect(screen.getByTestId("native-wordcraft")).not.toBe(original))
    expect(original.isConnected).toBe(false)
    expect(screen.getByTestId("native-provenance")).toHaveAttribute("data-viewer", "second-human")
  })

  it.each([403, 404])("retains and blocks the dirty original native host when its actual Page refetch returns %s", async status => {
    const view = renderLayout([{ ...FLEET, has_collaboration: true }], FLEET.slug!)
    const original = await screen.findByTestId("native-wordcraft")
    expect(screen.getByTestId("native-provenance")).toHaveAttribute("data-page", FLEET.id)
    fireEvent.click(screen.getByRole("button", { name: "Make draft" }))
    view.mockFetch.mockImplementation(async input => String(input).includes(`/api/v1/pages/${FLEET.slug}`)
      ? new Response(JSON.stringify({ error: "Current Page denied." }), { status, headers: { "Content-Type": "application/json" } })
      : okJSON([]))
    await act(async () => { await view.qc.refetchQueries({ queryKey: pagesKeys.detail("ws-1", FLEET.slug!, identity.id) }) })
    await waitFor(() => {
      expect(screen.getByTestId("native-wordcraft")).toBe(original)
      expect(original).toHaveAttribute("data-blocked", "true")
      expect(screen.queryByTestId("native-provenance")).toBeNull()
    })
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("keeps readable Pages while hiding creation and import for VIEWER without page.create", async () => {
    access.role = "VIEWER"
    renderLayout([FLEET])
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
    expect(screen.queryByRole("button", {name:/^New page$/})).toBeNull()
    expect(screen.queryByRole("button", {name:/^Import$/})).toBeNull()
    expect(screen.queryByRole("button",{name:"New folder"})).toBeNull()
  })

  it("offers no creation shortcut in an empty VIEWER catalog", () => {
    access.role="VIEWER"
    renderLayout([])
    expect(screen.queryByRole("button", {name:/^New page$/})).toBeNull()
    expect(screen.queryByText(/Or author one as YAML/)).toBeNull()
  })

  it.each(["OWNER", "ADMIN", "MANAGER"])("preserves %s creation and import", (role) => {
    access.role=role
    renderLayout([])
    expect(screen.getAllByRole("button", {name:/^New page$/}).length).toBeGreaterThan(0)
    expect(screen.getAllByRole("button", {name:/^Import$/}).length).toBeGreaterThan(0)
  })
  it("preserves an explicit VIEWER page.create grant", () => {
    access.role="VIEWER";access.capabilities=["page.create"]
    renderLayout([])
    expect(screen.getAllByRole("button", {name:/^New page$/}).length).toBeGreaterThan(0)
    expect(screen.getAllByRole("button", {name:/^Import$/}).length).toBeGreaterThan(0)
    expect(screen.queryByRole("button",{name:"New folder"})).toBeNull()
  })

  it("writes the header line as '<n> pages · <n> stale'", async () => {
    renderLayout([FLEET, CLOSE])
    const header = screen.getByLabelText("Artifacts")
    await waitFor(() => expect(header.textContent).toContain("2 pages"))
    expect(header.textContent).toContain("· 1 stale")
  })

  it("says 'all fresh' rather than '0 stale' when nothing has gone quiet", async () => {
    renderLayout([CLOSE])
    const header = screen.getByLabelText("Artifacts")
    await waitFor(() => expect(header.textContent).toContain("· all fresh"))
  })

  it("claims neither when the index carried no freshness at all", async () => {
    // A count is not a verdict. "all fresh" over an index that reported
    // nothing is exactly the lie §4 exists to prevent.
    renderLayout([{ id: "p", slug: "p", name: "P", panels: 2 }])
    const header = screen.getByLabelText("Artifacts")
    await waitFor(() => expect(header.textContent).toContain("1 page"))
    const text = header.textContent ?? ""
    expect(text).toContain("1 page")
    expect(text).not.toContain("all fresh")
    expect(text).not.toContain("stale")
  })

  // Picking a page rewrites the address bar WITHOUT navigating, and the
  // difference is the whole reason this is not `router.push`.
  //
  // Routing to /pages/<slug> made Next unmount the shell and rebuild it, so the
  // rail — which has nothing to do with which page is open — blinked out and
  // came back on every click, losing its scroll position and its filters with
  // it. The URL still has to change, because /pages/[slug] is a real route that
  // a refresh, a bookmark and a shared link all arrive through; it just must
  // not be a navigation.
  it("rewrites the URL on a pick without navigating", async () => {
    const pushState = vi.spyOn(window.history, "pushState")
    try {
      renderLayout([FLEET, CLOSE])
      await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
      fireEvent.click(screen.getByText("Flotila .201"))

      expect(pushState).toHaveBeenCalledWith(null, "", "/pages/fleet-201")
      // The router is the thing that would have remounted everything.
      expect(push).not.toHaveBeenCalled()
    } finally {
      pushState.mockRestore()
    }
  })

  // The failure this guards is the one a reader reports as "the sidebar
  // flickers": if the rail is torn down and rebuilt on selection, its DOM node
  // identity changes. Holding the node across the click is the strongest
  // assertion available here that nothing above it remounted.
  it("keeps the rail mounted across a selection", async () => {
    renderLayout([FLEET, CLOSE])
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())

    const railBefore = document.querySelector('[data-slot="pages-rail"]')
    expect(railBefore).toBeTruthy()

    fireEvent.click(screen.getByText("Flotila .201"))
    await waitFor(() => {
      const railAfter = document.querySelector('[data-slot="pages-rail"]')
      expect(railAfter).toBe(railBefore)
    })
  })

  // #2515: Edit hands the content column to the editor. The rail must not be
  // torn down for that — its scroll and filters have to be there on return —
  // and it is not moved out of the way either: it is the one rail every
  // surface shares, and the editor no longer brings a second one that needed
  // the width. Same node before, during and after.
  it("keeps the rail mounted, reachable and the same node while editing", async () => {
    renderLayout([FLEET, CLOSE], "fleet-201")
    await waitFor(() => expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy())
    const rail = document.querySelector('[data-slot="pages-rail"]')
    const aside = rail?.closest("aside")
    expect(aside).toBeTruthy()
    expect(aside?.hasAttribute("inert")).toBe(false)

    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    await waitFor(() => expect(document.querySelector("[data-slot='page-editor']")).toBeTruthy())
    expect(document.querySelector('[data-slot="pages-rail"]')).toBe(rail)
    expect(aside?.hasAttribute("inert")).toBe(false)
    expect(aside?.getAttribute("aria-hidden")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: /back to page/i }))
    await waitFor(() => expect(document.querySelector("[data-slot='page-editor']")).toBeNull())
    expect(document.querySelector('[data-slot="pages-rail"]')).toBe(rail)
    expect(aside?.hasAttribute("inert")).toBe(false)
  })

  // The Application | Panels switch replaces the "Stop application / show
  // panels" bar. It sits in the page header, only while an application is
  // on screen, and Panels closes the frame in this tab.
  it("offers Application | Panels in the header only for a running application, and Panels shows the grid", async () => {
    const APP: WirePage = { ...CLOSE, slug: "ops-lab", name: "Operations Lab", has_application: true, publication_version: 4 }
    renderLayout([FLEET, APP], "fleet-201")
    await waitFor(() => expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy())
    expect(screen.queryByRole("group", { name: "Show" })).toBeNull()

    cleanup()
    renderLayout([FLEET, APP], "ops-lab")
    await waitFor(() => expect(screen.getByTestId("application")).toBeTruthy())
    // The view reports availability from an effect, one commit after the
    // frame appears, so the header's switch is awaited on its own.
    const group = await screen.findByRole("group", { name: "Show" })
    expect(screen.getByRole("button", { name: "Application" }).getAttribute("aria-pressed")).toBe("true")
    expect(group.textContent).not.toContain("Stop")

    fireEvent.click(screen.getByRole("button", { name: "Panels" }))
    await waitFor(() => expect(screen.queryByTestId("application")).toBeNull())
    expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Panels" }).getAttribute("aria-pressed")).toBe("true")

    fireEvent.click(screen.getByRole("button", { name: "Application" }))
    await waitFor(() => expect(screen.getByTestId("application")).toBeTruthy())

    // Editing takes the whole column; the switch is not a thing to do while editing.
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    await waitFor(() => expect(document.querySelector("[data-slot='page-editor']")).toBeTruthy())
    expect(screen.queryByRole("group", { name: "Show" })).toBeNull()
  })

  it("renders the overview with no slug, and the page's grid with one", async () => {
    renderLayout([FLEET, CLOSE])
    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy())
    expect(document.querySelector("[data-slot='panel-grid']")).toBeNull()

    cleanup()
    renderLayout([FLEET, CLOSE], "fleet-201")
    await waitFor(() => expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy())
    expect(screen.getByRole("button", { name: /back to pages/i })).toBeTruthy()
    // The rail stays: opening a page must not cost the search and the facets.
    expect(screen.getByLabelText("Search pages")).toBeTruthy()
  })
})

describe("the STATUS facet covers the closed vocabulary", () => {
  it("offers an option for every panel state", () => {
    // A facet silently missing an option is a filter that hides rows, and the
    // failure is invisible at runtime.
    expect([...PAGE_STATE_ORDER].sort()).toEqual([...PANEL_STATES].sort())
  })
})


it("dispatches a native descriptor to the Document editor through the existing Page URL",async()=>{
  cleanup();window.history.replaceState(null,"","/pages");renderLayout([{id:"native-id",slug:"notes",name:"Notes",has_document:true}],"notes")
  expect(await screen.findByTestId("native-document")).toHaveTextContent("ws-1:native-id")
  expect(screen.queryByTestId("application")).toBeNull()
})


it("clears cached native catalog entries when an authorized refresh denies access",async()=>{
 cleanup();window.history.replaceState(null,"","/pages")
 const {qc,mockFetch}=renderLayout([{id:"private-native",slug:"private-notes",name:"Private notes",has_document:true}])
 await screen.findByRole("button",{name:"Private notes Doc"})
 mockFetch.mockResolvedValue({ok:false,status:403,json:async()=>({error:"access revoked"})} as Response)
 await act(async()=>{await qc.invalidateQueries()})
 await waitFor(()=>expect(screen.getByText("access revoked")).toBeInTheDocument())
 expect(screen.queryByRole("button",{name:"Private notes Doc"})).toBeNull()
 expect(screen.queryByText("Private notes")).toBeNull()
})


it("keeps native Docs out of the HTML editor even through a manual edit URL",async()=>{
 cleanup();window.history.replaceState(null,"","/pages/notes?mode=edit&section=access")
 renderLayout([{id:"native-id",slug:"notes",name:"Notes",has_document:true}],"notes")
 await screen.findByTestId("native-document")
 expect(screen.queryByTestId("section-content")).toBeNull()
 expect(screen.queryByTestId("section-history")).toBeNull()
 expect(screen.queryByTestId("section-access")).toBeNull()
 expect(screen.queryByRole("button",{name:"Share"})).toBeNull()
 expect(screen.queryByRole("button",{name:"Ask about this Page"})).toBeNull()
})

it("shows unavailable for a native descriptor without inventing its stable Page identity",async()=>{
 cleanup();window.history.replaceState(null,"","/pages/notes?mode=edit")
 renderLayout([{slug:"notes",name:"Notes",has_document:true}],"notes")
 expect(await screen.findByText("The original Page identity is unavailable.")).toBeInTheDocument()
 expect(screen.queryByTestId("native-document")).toBeNull()
 expect(screen.queryByTestId("section-content")).toBeNull()
 expect(screen.queryByTestId("section-access")).toBeNull()
 expect(screen.queryByTestId("application")).toBeNull()
})

it.each([false,true])("keeps native Docs out of the legacy panel overview (mixed: %s)",async(mixed)=>{
 cleanup();window.history.replaceState(null,"","/pages")
 renderLayout([{id:"native-id",slug:"notes",name:"Notes",has_document:true},...(mixed?[FLEET]:[])])
 await screen.findByRole("button",{name:"Notes Doc"})
 if(mixed) {
   expect(screen.getByText("1 page in this workspace")).toBeInTheDocument()
   expect(screen.getByText("Flotila .201")).toBeInTheDocument()
 } else {
   expect(screen.queryByRole("heading",{name:"Overview"})).toBeNull()
   expect(screen.queryByText("no panels declared")).toBeNull()
   expect(screen.queryByText("No pages yet")).toBeNull()
 }
})

it("explains an empty filtered Docs-only list and restores the card when cleared",async()=>{
 cleanup();window.history.replaceState(null,"","/pages")
 renderLayout([{id:"native-id",slug:"notes",name:"Notes",has_document:true}])
 await screen.findByRole("button",{name:"Notes Doc"})
 fireEvent.change(screen.getByLabelText("Search pages"),{target:{value:"unmatched"}})
 expect(await screen.findByRole("status")).toHaveTextContent("No documents match these filters.")
 expect(screen.queryByRole("button",{name:"Notes Doc"})).toBeNull()
 expect(screen.queryByRole("heading",{name:"Overview"})).toBeNull()
 fireEvent.change(screen.getByLabelText("Search pages"),{target:{value:""}})
 expect(await screen.findByRole("button",{name:"Notes Doc"})).toBeInTheDocument()
 expect(screen.queryByText("No documents match these filters.")).toBeNull()
})

it("offers New Doc and WordCraft when native collaboration is available without legacy Docs",async()=>{
 cleanup();window.history.replaceState(null,"","/pages")
 Object.assign(access,{role:"OWNER",capabilities:[],loading:false,error:false})
 const view=renderLayout([],undefined,{enabled:false,wordcraft_sdk_prefix:"/mooring-sdk/generated/"+"a".repeat(64)})
 fireEvent.click(await screen.findByRole("button",{name:"New Doc"}))
 expect(await screen.findByLabelText("Format")).toHaveValue("wordcraft")
 expect(screen.queryByText("Document creation is unavailable.")).not.toBeInTheDocument()
 view.qc.clear()
})
