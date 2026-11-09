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
import { decodeUpdate, encodeUpdate } from "@/hooks/use-page-document"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import type { LibraryItem } from "../../../library-items"
import { DocsBody, type DocsFrameReport } from "../docs-view"

const env = vi.hoisted(() => ({ proposals: undefined as boolean | undefined, available: true as boolean | undefined, canWrite: true, schema: 2, drafts: [] as import("@/lib/document-queue").DocumentDraft[], patch: "ok" as "ok" | "hang", denied: false }))
const request = vi.hoisted(() => vi.fn())
const upgrades = vi.hoisted(() => ({ outcome: "ok" as "ok" | "stale" | "missing" | "lost", bodies: [] as Record<string, unknown>[], urls: [] as string[] }))
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ data: env.available, isPending: env.available === undefined }) }))
vi.mock("@/lib/document-queue", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/document-queue")>(), openDocumentDrafts: async () => ({ load: async () => env.drafts, put: async () => {}, remove: async () => {}, close: () => {}, exclusive: true }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "docs-human" } } }) }))

afterEach(() => { cleanup(); request.mockReset(); upgrades.outcome = "ok"; upgrades.bodies = []; upgrades.urls = []; env.proposals = undefined; env.available = true; env.canWrite = true; env.schema = 2; env.drafts = []; env.patch = "ok"; env.denied = false })

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
    if (url.includes("/document/upgrade")) {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>
      upgrades.bodies.push(body)
      upgrades.urls.push(url)
      if (upgrades.outcome === "stale") return { ok: false, status: 409, json: async () => ({ error: "original Page identity, revision and supported upgrade required" }) }
      if (upgrades.outcome === "missing") return { ok: false, status: 404, json: async () => ({ error: "not found" }) }
      // The first attempt is applied but its answer is lost; the exact retry gets the original receipt.
      if (upgrades.outcome === "lost" && upgrades.bodies.length === 1) { rev += 1; env.schema = 6; throw new TypeError("network") }
      if (upgrades.outcome === "lost") return JSON.stringify(body) === JSON.stringify(upgrades.bodies[0]) ? { ok: true, json: async () => ({ id: body.id, rev, epoch: "go:node:2", schema: 6, duplicate: true }) } : { ok: false, status: 409, json: async () => ({ error: "stale" }) }
      rev += 1; env.schema = 6
      return { ok: true, json: async () => ({ id: body.id, rev, epoch: "go:node:2", schema: 6 }) }
    }
    if (url.includes("/document/checkpoints")) return { ok: true, json: async () => ({ checkpoints: [] }) }
    if (url.includes("/proposals")) return { ok: true, json: async () => ({ proposals: [] }) }
    if (env.denied) return { ok: false, status: 403, json: async () => ({ error: "document access denied" }) }
    return { ok: true, json: async () => ({ page_id: "page-docs", schema: env.schema, rev, generation: 0, epoch: "go:node:1", can_write: env.canWrite, snapshot, ...(env.proposals === undefined ? {} : { proposals: env.proposals }) }) }
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

  it("lays the page out: reading column, Page mode only, outline, counts, tools and History", async () => {
    serve()
    open()
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    // The frame names the artifact and the Info drawer its owner: the body starts with the document itself.
    const page = screen.getByRole("region", { name: "Doc page" })
    expect(within(page).queryByText("Launch notes")).toBeNull()
    expect(within(page).queryByText(/owned by/)).toBeNull()
    expect(within(page).getAllByRole("heading", { level: 1 }).map(heading => heading.textContent)).toEqual(["Goal"])
    // Below content schema 6 the document is on its Page; Canvas is only offered to enable, and nothing upgrades it by itself.
    const modes = within(slot("view")!).getByRole("tablist", { name: "Mode" })
    expect(within(modes).getAllByRole("tab").map(tab => [tab.textContent, tab.getAttribute("aria-selected")])).toEqual([["Page", "true"], ["Canvas", "false"]])
    expect(screen.queryByRole("application", { name: "Canvas" })).toBeNull()
    expect(request.mock.calls.some(([url]) => String(url).includes("/document/upgrade"))).toBe(false)
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

  it.each([[undefined], [false]] as const)("offers no Propose mode and no Proposals tab when the server does not serve proposals (%s)", async proposals => {
    env.proposals = proposals
    serve()
    open()
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    await waitFor(() => expect(screen.getByRole("button", { name: "Status: Live" })).toBeTruthy())
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    expect(screen.queryByRole("radiogroup", { name: "Agent mode" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Proposals" })).toBeNull()
    expect(request.mock.calls.some(([url]) => String(url).includes("/proposals"))).toBe(false)
  })

  // A schema with no proposals never offers Propose, whatever the flag says (#5233).
  it("offers no Propose on a schema 2 Doc even if proposals are claimed", async () => {
    env.proposals = true
    serve()
    open()
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    expect(screen.queryByRole("radiogroup", { name: "Agent mode" })).toBeNull()
  })

  it("registers Propose and the Proposals review only when the server serves proposals", async () => {
    env.proposals = true
    env.schema = 5
    serve()
    open()
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    await waitFor(() => expect(screen.getByRole("button", { name: "Status: Live" })).toBeTruthy())
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    const modes = await screen.findByRole("radiogroup", { name: "Agent mode" })
    expect(within(modes).getAllByRole("radio").map(radio => radio.textContent)).toEqual(["Ask", "Propose"])
    fireEvent.click(within(modes).getByRole("radio", { name: "Propose" }))
    fireEvent.change(screen.getByRole("textbox", { name: "What should the agent change?" }), { target: { value: "Tighten the goal" } })
    const href = new URL(screen.getByRole("link", { name: "Ask for a proposal" }).getAttribute("href")!, "https://x.test")
    expect(href.pathname).toBe("/chat")
    expect(Object.fromEntries(href.searchParams)).toEqual({ workspace_id: "ws1", propose: "page-docs.1.0.5", prompt: "Tighten the goal", draft: "1" })
    fireEvent.click(screen.getByRole("button", { name: "Proposals" }))
    expect(await within(screen.getByRole("complementary", { name: "Proposals" })).findByText("No proposals.")).toBeTruthy()
  })

  it("keeps Propose and the Proposals review on a Canvas document (schema 6)", async () => {
    env.proposals = true
    env.schema = 6
    serve()
    open()
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    await waitFor(() => expect(screen.getByRole("button", { name: "Status: Live" })).toBeTruthy())
    fireEvent.click(within(slot("view")!).getByRole("tab", { name: "Canvas" }))
    await screen.findByRole("application", { name: "Canvas" })
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    const modes = await screen.findByRole("radiogroup", { name: "Agent mode" })
    fireEvent.click(within(modes).getByRole("radio", { name: "Propose" }))
    fireEvent.change(screen.getByRole("textbox", { name: "What should the agent change?" }), { target: { value: "Tighten the goal" } })
    const href = new URL(screen.getByRole("link", { name: "Ask for a proposal" }).getAttribute("href")!, "https://x.test")
    expect(href.searchParams.get("propose")).toBe("page-docs.1.0.6")
    fireEvent.click(screen.getByRole("button", { name: "Proposals" }))
    expect(await within(screen.getByRole("complementary", { name: "Proposals" })).findByText("No proposals.")).toBeTruthy()
  })

  it("offers no Propose to a reader who cannot edit, but still the Proposals to read", async () => {
    env.proposals = true
    env.schema = 5
    env.canWrite = false
    serve()
    open()
    expect(await screen.findByText("Ship the agency offer on 2 December.")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    expect(screen.queryByRole("radiogroup", { name: "Agent mode" })).toBeNull()
    expect(screen.queryByRole("link", { name: "Ask for a proposal" })).toBeNull()
    expect(await screen.findByRole("button", { name: "Proposals" })).toBeTruthy()
  })

  it("offers Canvas at schema 6: the same document, a placement is saved as a patch, and the Page editor stays mounted", async () => {
    serve()
    env.schema = 6
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    const modes = within(slot("view")!).getByRole("tablist", { name: "Mode" })
    expect(within(modes).getAllByRole("tab").map(tab => tab.textContent)).toEqual(["Page", "Canvas"])
    fireEvent.click(within(modes).getByRole("tab", { name: "Canvas" }))
    const canvas = await screen.findByRole("application", { name: "Canvas" })
    expect(document.querySelector("[data-docs-canvas]")!.getAttribute("data-editable")).toBe("true")
    // Formatting and the outline belong to the Page; the editor is hidden, not unmounted.
    expect(slot("tools")).toBeNull()
    expect(slot("panel")).toBeNull()
    expect(editor.closest("article")!.hidden).toBe(true)
    fireEvent.click(screen.getByText("Add blocks (3)"))
    fireEvent.click(screen.getByRole("button", { name: "Ship the agency offer on 2 December." }))
    expect(canvas.querySelector('[data-placement="p1"]')).toBeTruthy()
    await waitFor(() => expect(request.mock.calls.some(([url]) => String(url).includes("/document/patch"))).toBe(true), { timeout: 4000 })
    const patch = request.mock.calls.find(([url]) => String(url).includes("/document/patch"))!
    const body = JSON.parse(String(patch[1]?.body)) as { update: string; schema: number }
    expect(body.schema).toBe(6)
    const replica = new Y.Doc({ gc: false }); Y.applyUpdate(replica, decodeUpdate(body.update))
    expect([...replica.getMap("surface:placements").keys()]).toEqual(["p1"])
    replica.destroy()
    // Back on the Page the same editor, its text unchanged by the placement.
    fireEvent.click(within(modes).getByRole("tab", { name: "Page" }))
    expect(screen.getByLabelText("Document editor")).toBe(editor)
    expect(editor.textContent).toContain("Ship the agency offer on 2 December.")
    expect(screen.queryByRole("application", { name: "Canvas" })).toBeNull()
  })

  it("offers Enable Canvas on an older document: the first switch asks, the upgrade keeps the content and opens Canvas", async () => {
    serve()
    env.schema = 5
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    const modes = within(slot("view")!).getByRole("tablist", { name: "Mode" })
    const tab = within(modes).getByRole("tab", { name: "Canvas" })
    expect(tab.getAttribute("aria-haspopup")).toBe("dialog")
    fireEvent.click(tab)
    const dialog = await screen.findByRole("alertdialog", { name: "Enable Canvas for this document?" })
    expect(dialog.textContent).toContain("The Page, its text and formatting stay exactly as they are.")
    expect(dialog.textContent).toContain("Versions and comments are kept.")
    expect(dialog.textContent).toContain("Open proposals can no longer be accepted")
    // Nothing is upgraded until the person confirms.
    expect(upgrades.bodies).toEqual([])
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    expect(upgrades.bodies).toEqual([])
    fireEvent.click(within(within(slot("view")!).getByRole("tablist", { name: "Mode" })).getByRole("tab", { name: "Canvas" }))
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Enable Canvas" }))
    expect(await screen.findByRole("application", { name: "Canvas" })).toBeTruthy()
    expect(upgrades.bodies).toHaveLength(1)
    expect(upgrades.bodies[0]).toMatchObject({ schema: 5, target_schema: 6, page_id: "page-docs", epoch: "go:node:1", ifRev: 1 })
    expect(String(upgrades.bodies[0].id)).toMatch(/^[a-zA-Z0-9_-]{1,128}$/)
    // The route is workspace-scoped like every Docs call.
    expect(new URL(upgrades.urls[0], "https://x.test").searchParams.get("workspace_id")).toBe("ws1")
    // The reopened session is the upgraded document, with the same Page text.
    fireEvent.click(within(within(slot("view")!).getByRole("tablist", { name: "Mode" })).getByRole("tab", { name: "Page" }))
    expect((await screen.findByLabelText("Document editor")).textContent).toContain("Ship the agency offer on 2 December.")
  })

  it("keeps the dialog open with a reason when the document changed meanwhile, and asks again with a fresh request", async () => {
    serve()
    env.schema = 5
    upgrades.outcome = "stale"
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    fireEvent.click(within(slot("view")!).getByRole("tab", { name: "Canvas" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Enable Canvas" }))
    expect(await within(dialog).findByRole("alert")).toHaveProperty("textContent", "The document changed while you were deciding. Nothing was changed; try again.")
    expect(screen.queryByRole("application", { name: "Canvas" })).toBeNull()
    fireEvent.click(within(dialog).getByRole("button", { name: "Enable Canvas" }))
    await waitFor(() => expect(upgrades.bodies).toHaveLength(2))
    expect(upgrades.bodies[1].id).not.toBe(upgrades.bodies[0].id)
  })

  it("retries an unknown outcome as the exact same request, so an upgrade that did happen is confirmed", async () => {
    serve()
    env.schema = 5
    upgrades.outcome = "lost"
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    fireEvent.click(within(slot("view")!).getByRole("tab", { name: "Canvas" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Enable Canvas" }))
    expect(await within(dialog).findByRole("alert")).toHaveProperty("textContent", "We could not confirm whether Canvas was enabled. Try again; it is never enabled twice.")
    fireEvent.click(within(dialog).getByRole("button", { name: "Enable Canvas" }))
    await waitFor(() => expect(upgrades.bodies).toHaveLength(2))
    expect(upgrades.bodies[1]).toEqual(upgrades.bodies[0])
    expect(await screen.findByRole("application", { name: "Canvas" })).toBeTruthy()
  })

  it("never upgrades while this tab has unsaved edits: the dialog says why and nothing is sent", async () => {
    serve()
    env.schema = 5
    env.patch = "hang"
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    act(() => { editor.editor.commands.insertContent(" Unsaved") })
    expect(await screen.findByRole("button", { name: "Status: Saving…" })).toBeTruthy()
    fireEvent.click(within(slot("view")!).getByRole("tab", { name: "Canvas" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Enable Canvas" }))
    expect(await within(dialog).findByRole("alert")).toHaveProperty("textContent", "Wait until your changes are saved, then enable Canvas.")
    expect(upgrades.bodies).toEqual([])
    expect(screen.queryByRole("application", { name: "Canvas" })).toBeNull()
  })

  it("treats a missing upgrade route as an error, never as lost access", async () => {
    serve()
    env.schema = 5
    upgrades.outcome = "missing"
    open()
    const editor = (await screen.findByLabelText("Document editor")) as HTMLElement & { editor: Editor }
    await waitFor(() => expect(editor.editor.isEditable).toBe(true))
    fireEvent.click(within(slot("view")!).getByRole("tab", { name: "Canvas" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Enable Canvas" }))
    expect(await within(dialog).findByRole("alert")).toHaveProperty("textContent", "Canvas could not be enabled. Nothing was changed.")
    expect(screen.queryByRole("button", { name: "Status: No access" })).toBeNull()
    expect(editor.editor.isEditable).toBe(true)
  })

  it("offers no Enable Canvas to a person who cannot edit an older document", async () => {
    serve()
    env.schema = 5
    env.canWrite = false
    open()
    expect(await screen.findByRole("button", { name: "Status: Read-only" })).toBeTruthy()
    expect(within(slot("view")!).queryByRole("tab", { name: "Canvas" })).toBeNull()
  })

  it("lets a read-only person open Canvas to look around but not change it", async () => {
    serve()
    env.schema = 6
    env.canWrite = false
    open()
    expect(await screen.findByRole("button", { name: "Status: Read-only" })).toBeTruthy()
    fireEvent.click(within(slot("view")!).getByRole("tab", { name: "Canvas" }))
    await screen.findByRole("application", { name: "Canvas" })
    expect(document.querySelector("[data-docs-canvas]")!.getAttribute("data-editable")).toBe("false")
    expect(screen.queryByText(/Add blocks/)).toBeNull()
    expect(screen.queryByRole("button", { name: "Add note" })).toBeNull()
    expect(screen.getByRole("button", { name: "Fit to screen" })).toBeTruthy()
    // Trying to change the canvas from the keyboard writes nothing, even after a poll cycle.
    const canvas = screen.getByRole("application", { name: "Canvas" })
    fireEvent.keyDown(canvas, { key: "ArrowRight" })
    fireEvent.keyDown(canvas, { key: "Delete" })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 1200)) })
    expect(request.mock.calls.some(([url]) => String(url).includes("/document/patch"))).toBe(false)
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

  it("keeps a No access chip when the first read is refused (#5202)", async () => {
    serve()
    env.denied = true
    open()
    expect(await screen.findByRole("button", { name: "Status: No access" })).toBeTruthy()
    expect(screen.queryByLabelText("Document editor")).toBeNull()
  })
  it("turns the chip into No access when access is removed while the document is open (#5202)", async () => {
    serve()
    open()
    expect(await screen.findByLabelText("Document editor")).toBeTruthy()
    env.denied = true
    expect(await screen.findByRole("button", { name: "Status: No access" }, { timeout: 5_000 })).toBeTruthy()
    expect(screen.queryByText("Ship the agency offer on 2 December.")).toBeNull()
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
