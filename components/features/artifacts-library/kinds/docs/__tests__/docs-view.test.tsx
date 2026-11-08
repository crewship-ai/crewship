// The document session is the real one (PageDocument + usePageDocument) over a mocked API and a
// protected-store seam, as in the Pages document tests; the frame is the real ArtifactFrame.
vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
vi.mock("@/lib/local-draft-seal", () => ({
  createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
  isSealedLocalDraft: () => false, LocalDraftProtectionError: class extends Error {}, LocalDraftReadRevoked: class extends Error {},
}))
import * as React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor } from "@tiptap/core"
import { encodeUpdate } from "@/hooks/use-page-document"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import type { LibraryItem } from "../../../library-items"
import { DocsBody, type DocsFrameReport } from "../docs-view"

const env = vi.hoisted(() => ({ available: true as boolean | undefined, canWrite: true, drafts: [] as import("@/lib/document-queue").DocumentDraft[], patch: "ok" as "ok" | "hang" }))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ data: env.available, isPending: env.available === undefined }) }))
vi.mock("@/lib/document-queue", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/document-queue")>(), openDocumentDrafts: async () => ({ load: async () => env.drafts, put: async () => {}, remove: async () => {}, close: () => {}, exclusive: true }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "docs-human" } } }) }))

afterEach(() => { cleanup(); request.mockReset(); env.available = true; env.canWrite = true; env.drafts = []; env.patch = "ok" })

const item: LibraryItem = {
  slug: "launch-notes", pageId: "page-docs", name: "Launch notes", kind: "word", family: "document", sharedVersion: null, native: false,
  projectBacked: false, empty: false, private: false, ownerLabel: "Research crew", folder: { slug: "lab", name: "Demo Lab" }, updatedAt: null,
} as LibraryItem

function savedDocument() {
  const saved = new Y.Doc({ gc: false })
  const fragment = saved.getXmlFragment("default")
  const block = (type: string, id: string, text: string, attrs: Record<string, number> = {}) => {
    const node = new Y.XmlElement(type); node.setAttribute("id", id)
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value as unknown as string)
    const run = new Y.XmlText(); run.insert(0, text); node.insert(0, [run]); return node
  }
  fragment.insert(0, [block("heading", "goal", "Goal", { level: 1 }), block("paragraph", "p1", "Ship the agency offer on 2 December."), block("heading", "timeline", "Timeline", { level: 2 })])
  const snapshot = encodeUpdate(Y.encodeStateAsUpdate(saved)); saved.destroy()
  return snapshot
}

function serve() {
  const snapshot = savedDocument()
  let rev = 1
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/document/patch")) {
      if (env.patch === "hang") return new Promise(() => {})
      const body = JSON.parse(String(init?.body))
      rev += 1
      return { ok: true, json: async () => ({ id: body.id, epoch: body.epoch, rev, schema: body.schema }) }
    }
    if (url.includes("/document/checkpoints")) return { ok: true, json: async () => ({ checkpoints: [] }) }
    if (url.includes("/proposals")) return { ok: true, json: async () => ({ proposals: [] }) }
    return { ok: true, json: async () => ({ page_id: "page-docs", schema: 2, rev, epoch: "go:node:1", can_write: env.canWrite, snapshot }) }
  })
}

/** The frame as artifact-detail builds it for Docs: the chip and canEdit come from the body's report. */
function Harness({ layout, onReport }: { layout: FrameLayout; onReport?: (report: DocsFrameReport | null) => void }) {
  const [report, setReport] = React.useState<DocsFrameReport | null>(null)
  const onFrame = React.useCallback((next: DocsFrameReport | null) => { setReport(next); onReport?.(next) }, [onReport])
  return <ArtifactFrame name={item.name} kind={{ kind: "docs", mode: "page" }} folder="Demo Lab" layout={layout} onHome={() => {}}
    caps={{ ...NO_FRAME_CAPABILITIES, canEdit: report?.canEdit === true, status: report?.status ?? null, agent: { ask: { chatHref: () => "/chat" } } }}
    statusActions={report?.statusActions} statusFoot={report?.statusFoot}>
    <DocsBody workspaceId="ws1" item={item} onFrame={onFrame} fallback={<p data-testid="saved-preview">saved version</p>} />
  </ArtifactFrame>
}

function open(layout: FrameLayout = "desktop", onReport?: (report: DocsFrameReport | null) => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(<QueryClientProvider client={client}><Harness layout={layout} onReport={onReport} /></QueryClientProvider>)
}

const slot = (name: string) => document.querySelector(`[data-artifact-slot="${name}"]`) as HTMLElement | null

describe("Docs in the ArtifactFrame", () => {
  it("shows the saved version, never an editor, when the documents service does not admit the session", () => {
    env.available = false
    open()
    expect(screen.getByTestId("saved-preview")).toBeTruthy()
    expect(screen.queryByLabelText("Document editor")).toBeNull()
    expect(screen.queryByRole("button", { name: /^Status:/ })).toBeNull()
  })

  it("lays the page out: reading column with title and meta, Page mode only, outline, counts, tools and History", async () => {
    serve()
    open()
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    expect(screen.getByRole("heading", { level: 1, name: "Launch notes" })).toBeTruthy()
    expect(screen.getByText("Demo Lab · owned by Research crew")).toBeTruthy()
    // Page is the one mode; Canvas has no backend and is not offered.
    const modes = within(slot("view")!).getByRole("tablist", { name: "Mode" })
    expect(within(modes).getAllByRole("tab").map(tab => tab.textContent)).toEqual(["Page"])
    expect(screen.queryByText("Canvas")).toBeNull()
    // The outline is open on a desktop and lists the headings.
    const outline = within(slot("panel")!).getByRole("navigation", { name: "Headings" })
    expect(within(outline).getAllByRole("button").map(button => button.textContent)).toEqual(["Goal", "Timeline"])
    expect(slot("viewport")!.textContent).toBe("9 words · 1 min read")
    // Formatting only while editable; no Insert menu, no embeds, no Comments, no Export.
    await waitFor(() => expect(slot("tools")).not.toBeNull())
    expect(within(slot("tools")!).getByRole("button", { name: "Heading 1" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: /Insert|Embed/ })).toBeNull()
    expect(screen.queryByRole("button", { name: "Comments" })).toBeNull()
    expect(slot("output")).toBeNull()
    expect(await screen.findByRole("button", { name: "Status: Live" })).toBeTruthy()
    // History is a drawer tab with the restore rule spelled out.
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    const drawer = screen.getByRole("complementary", { name: "History" })
    expect(within(drawer).getByText("Restore creates a new version with this content. Later changes stay in History.")).toBeTruthy()
    expect(await within(drawer).findByText("No checkpoints yet.")).toBeTruthy()
  })

  it("stays on Saving… while the receipt has not arrived", async () => {
    serve()
    env.patch = "hang"
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    act(() => { editor.editor.commands.insertContent(" More") })
    expect(await screen.findByRole("button", { name: "Status: Saving…" })).toBeTruthy()
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 1200)) })
    expect(screen.getByRole("button", { name: "Status: Saving…" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Status: Saved" })).toBeNull()
  })

  it("says Saved once the server confirmed this tab's write", async () => {
    serve()
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    act(() => { editor.editor.commands.insertContent(" More") })
    expect(await screen.findByRole("button", { name: "Status: Saved" }, { timeout: 4000 })).toBeTruthy()
    expect(request.mock.calls.some(([url]) => String(url).includes("/document/patch"))).toBe(true)
  })

  it("is Read-only without write access: no tools, a read-only editor", async () => {
    serve()
    env.canWrite = false
    const reports: (DocsFrameReport | null)[] = []
    open("desktop", report => reports.push(report))
    expect(await screen.findByRole("button", { name: "Status: Read-only" })).toBeTruthy()
    expect(slot("tools")).toBeNull()
    expect((screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor.isEditable).toBe(false)
    expect(reports.at(-1)?.canEdit).toBe(false)
  })

  it("on a phone: one column, the outline closed until asked for, then a bottom sheet; no counts", async () => {
    serve()
    open("phone")
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    expect(document.querySelector("[data-docs-page]")!.className).not.toMatch(/pl-\[256px\]/)
    expect(slot("panel")).toBeNull()
    fireEvent.click(within(slot("view")!).getByRole("button", { name: "Show outline" }))
    expect(slot("panel")!.className).toMatch(/bottom-0/)
    expect(slot("viewport")!.className).toMatch(/\bhidden\b/)
    // Picking a heading closes the sheet so the page is not covered.
    fireEvent.click(within(slot("panel")!).getByRole("button", { name: "Timeline" }))
    expect(slot("panel")).toBeNull()
  })

  it("turns a recovered draft into the one banner, with download and discard, and a Recovered draft chip", async () => {
    serve()
    const saved = new Y.Doc({ gc: false }); saved.getXmlFragment("default").insert(0, [new Y.XmlElement("paragraph")])
    env.drafts = [{ id: "draft-1", update: encodeUpdate(Y.encodeStateAsUpdate(saved)), attempted: false, schema: 2, epoch: "go:node:1", snapshot: encodeUpdate(Y.encodeStateAsUpdate(saved)) }]
    saved.destroy()
    open()
    expect(await screen.findByRole("button", { name: "Status: Recovered draft" }, { timeout: 4000 })).toBeTruthy()
    const banner = document.querySelector("[data-docs-banner]") as HTMLElement
    expect(within(banner).getByRole("button", { name: "Download unsaved draft" })).toBeTruthy()
    expect(within(banner).getByRole("button", { name: "Discard recovered drafts and reopen" })).toBeTruthy()
    expect(document.querySelectorAll("[data-docs-banner]")).toHaveLength(1)
    // While a draft waits, the page is not editable and the chip offers the download too.
    expect(slot("tools")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Status: Recovered draft" }))
    expect(await screen.findByRole("button", { name: "Download my changes" })).toBeTruthy()
  })
})
