import DOMPurify from "dompurify"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { NewArtifactDialog } from "../new-artifact-dialog"
import { PageArtifactView } from "../page-artifact"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
import type { ArtifactAgentRequest } from "@/hooks/use-artifact-requests"
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(window.location.search) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-pages", () => ({ pagesKeys: { all: (workspace: string) => ["pages", workspace] } }))
let head = 1
let copyable = false
let writable = true
let commentable = false
let fail = 0
let readFail = 0
let source = "<h1>Saved report</h1><script>alert(1)</script>"
let requests: ArtifactAgentRequest[] = []
let sources: Record<number, string> = {}
const calls: { path: string; body: Record<string, unknown> }[] = []
function mount(onDirtyChange?: (dirty: boolean) => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><PageArtifactView workspaceId="ws1" slug="report" onDirtyChange={onDirtyChange} /></QueryClientProvider>)
}
const supported = DOMPurify.isSupported
beforeEach(() => {
  window.history.replaceState(null, "", "/pages/report")
  DOMPurify.isSupported = false
  vi.mocked(useRealtimeEventSafe).mockClear()
  head = 1; copyable = false; writable = true; commentable = false; fail = 0; readFail = 0; source = "<h1>Saved report</h1><script>alert(1)</script>"; calls.length = 0
  requests = []; sources = {}
  vi.mocked(apiFetch).mockImplementation(async (path, options) => {
    if (options?.method === "POST") {
      const body = JSON.parse(String(options.body)); calls.push({ path: String(path), body })
      if (fail) return new Response(JSON.stringify({ error: fail === 409 ? "artifact revision changed; reload before saving" : "denied" }), { status: fail })
      if (String(path).includes("/copies")) return new Response(JSON.stringify({ page_slug: "private-copy", revision: 1 }), { status: 201 })
      head++; if (typeof body.html === "string") source = body.html
      return new Response(JSON.stringify({ head_rev: head, revision: head }))
    }
    if (String(path).includes("/requests")) return new Response(JSON.stringify({ requests, eligible_chats: [], can_dispatch: false }))
    if (String(path).includes("/threads")) return new Response(JSON.stringify({ can_comment: true, can_resolve: true, threads: [{ id: "thread1", revision: 1, status: "open", version: 1, anchor: { schema_version: 1, type: "document" }, comments: [{ id: "comment1", body: "Discuss this revision", author_user_id: "user1", author_name: "Alice", created_at: "2026-10-13T00:00:00Z" }] }] }))
    if (readFail) return new Response(JSON.stringify({ error: "Access denied" }), { status: readFail })
    const selected = new URL(String(path), "https://studio.test").searchParams.get("revision")
    return new Response(JSON.stringify({ head_rev: head, revision: selected ? Number(selected) : head, html: sources[Number(selected ?? head)] ?? source, can_copy: copyable, can_write: writable, can_comment: commentable, revisions: Array.from({ length: head }, (_, i) => ({ revision: i + 1, created_at: "2026-10-13T00:00:00Z", author_user_id: "user1" })) }))
  })
})
afterEach(() => { cleanup(); DOMPurify.isSupported = supported })
describe("human page artifact", () => {
  it("opens and closes point comments with shortcuts without intercepting text or modified keys", async () => {
    commentable = true
    mount()
    await screen.findByTitle("Artifact static preview")
    fireEvent.keyDown(window, { key: "c", ctrlKey: true })
    expect(screen.queryByRole("button", { name: "Choose comment point in preview" })).toBeNull()
    fireEvent.keyDown(window, { key: "c", isComposing: true })
    expect(screen.queryByRole("button", { name: "Choose comment point in preview" })).toBeNull()
    fireEvent.keyDown(window, { key: "c" })
    expect(screen.getByRole("button", { name: "Choose comment point in preview" })).toBeTruthy()
    fireEvent.keyDown(window, { key: "Escape" })
    expect(screen.queryByRole("button", { name: "Choose comment point in preview" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Source" }))
    fireEvent.keyDown(window, { key: "c" })
    expect(screen.queryByRole("button", { name: "Choose comment point in preview" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Static preview" }))
    const text = document.createElement("textarea")
    document.body.append(text)
    fireEvent.keyDown(text, { key: "c" })
    text.remove()
    expect(screen.queryByRole("button", { name: "Choose comment point in preview" })).toBeNull()
  })

  it("does not enable point comments for a reader through a shortcut", async () => {
    mount()
    await screen.findByTitle("Artifact static preview")
    fireEvent.keyDown(window, { key: "c" })
    expect(screen.queryByRole("button", { name: "Choose comment point in preview" })).toBeNull()
  })

  it("copies the selected published revision with exact uncertain retry and clears private copy UI on access loss", async () => {
    copyable = true; head = 2; window.history.replaceState(null, "", "/pages/report?revision=1")
    const dirty = vi.fn(); mount(dirty)
    fireEvent.click(await screen.findByRole("button", { name: "Copy" }))
    fireEvent.change(screen.getByLabelText("Copy name"), { target: { value: "Private copy" } })
    fireEvent.change(screen.getByLabelText("Copy page address"), { target: { value: "private-copy" } })
    fail = 500
    fireEvent.click(screen.getByRole("button", { name: "Create private copy" }))
    await screen.findByRole("button", { name: "Retry same copy" })
    expect(screen.getByLabelText("Copy name")).toBeDisabled()
    expect(calls[0].body.source_revision).toBe(1)
    expect(dirty).toHaveBeenLastCalledWith(true)
    fail = 0
    fireEvent.click(screen.getByRole("button", { name: "Retry same copy" }))
    await screen.findByRole("link", { name: "Open copy" })
    expect(calls[1].body).toEqual(calls[0].body)
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(false))
    readFail = 403
    const callback = vi.mocked(useRealtimeEventSafe).mock.calls.find(call => call[0] === "page.updated")?.[1]
    await act(async () => { (callback as (() => void))() })
    await waitFor(() => expect(screen.queryByRole("link", { name: "Open copy" })).toBeNull())
    expect(screen.queryByTitle("Artifact static preview")).toBeNull()
  })

  it("opens a revision URL and keeps the selected immutable revision", async () => {
    window.history.replaceState(null, "", "/pages/report?revision=1")
    head = 2; mount()
    await screen.findByTitle("Artifact static preview")
    expect(screen.getByText(/Revision 1.*latest 2/)).toBeTruthy()
  })

  it("compares two immutable revisions with sandboxed previews", async () => {
    head = 2; mount()
    fireEvent.click(await screen.findByRole("button", { name: "Compare" }))
    const earlier = await screen.findByTitle("Compare revision 1")
    const later = await screen.findByTitle("Compare revision 2")
    expect(earlier).toHaveAttribute("sandbox", "")
    expect(later).toHaveAttribute("sandbox", "")
    expect(earlier.getAttribute("srcdoc")).not.toContain("<script>")
    readFail = 403
    const callback = vi.mocked(useRealtimeEventSafe).mock.calls.find(call => call[0] === "page.updated")?.[1]
    await act(async () => { (callback as (() => void))() })
    await waitFor(() => expect(screen.queryByTitle("Compare revision 1")).toBeNull())
    expect(screen.queryByTitle("Compare revision 2")).toBeNull()
  })

  it.each(["request card", "thread result"])("compares the exact saved request pair from a %s even after newer revisions and manual selection", async entry => {
    head = 5
    sources = { 1: "<h1>Original request source</h1>", 2: "<h1>First agent result</h1>", 3: "<h1>Second agent result</h1>", 4: "<h1>Unrelated later edit</h1>", 5: "<h1>Latest unrelated edit</h1>" }
    const request: ArtifactAgentRequest = { id: "first-result", chat_id: "chat", agent_id: "agent", thread_id: "thread1", instruction: "Clarify", base_rev: 1, result_revision: 2, result_kind: "revision", status: "succeeded", created_at: "2026-10-13T12:00:00Z", updated_at: "2026-10-13T12:00:00Z" }
    requests = [request, { ...request, id: "second-result", base_rev: 2, result_revision: 3, result_kind: undefined }]
    mount()
    await screen.findByRole("button", { name: "Compare revisions 1 and 2" })
    if (entry === "thread result") {
      fireEvent.click(screen.getByRole("button", { name: "Comments" }))
      await screen.findByLabelText("Thread agent requests")
    }
    const result = () => within(entry === "thread result" ? screen.getByLabelText("Thread agent requests") : screen.getByRole("region", { name: "Agent requests" }))
    fireEvent.click(screen.getByRole("button", { name: "Compare" }))
    expect(await screen.findByLabelText("Earlier revision")).toHaveValue("4")
    expect(screen.getByLabelText("Later revision")).toHaveValue("5")
    fireEvent.click(result().getByRole("button", { name: "Compare revisions 1 and 2" }))
    expect(screen.getByLabelText("Earlier revision")).toHaveValue("1")
    expect(screen.getByLabelText("Later revision")).toHaveValue("2")
    await waitFor(() => expect(screen.getByTitle("Compare revision 1").getAttribute("srcdoc")).toContain("Original request source"))
    await waitFor(() => expect(screen.getByTitle("Compare revision 2").getAttribute("srcdoc")).toContain("First agent result"))
    fireEvent.change(screen.getByLabelText("Earlier revision"), { target: { value: "4" } })
    fireEvent.change(screen.getByLabelText("Later revision"), { target: { value: "5" } })
    fireEvent.click(result().getByRole("button", { name: "Compare revisions 1 and 2" }))
    expect(screen.getByLabelText("Earlier revision")).toHaveValue("1")
    expect(screen.getByLabelText("Later revision")).toHaveValue("2")
    fireEvent.click(result().getByRole("button", { name: "Compare revisions 2 and 3" }))
    expect(screen.getByLabelText("Earlier revision")).toHaveValue("2")
    expect(screen.getByLabelText("Later revision")).toHaveValue("3")
    await waitFor(() => expect(screen.getByTitle("Compare revision 3").getAttribute("srcdoc")).toContain("Second agent result"))
    expect(calls).toHaveLength(0)
  })

  it("offers no request comparison for replies, incomplete work or unavailable results", async () => {
    head = 3
    const request: ArtifactAgentRequest = { id: "reply", chat_id: "chat", agent_id: "agent", thread_id: "thread1", instruction: "Analyze", base_rev: 1, status: "succeeded", result_kind: "reply", result_revision: 2, reply: { body: "Reply only", run_id: "run", created_at: "2026-10-13T12:00:00Z" }, created_at: "2026-10-13T12:00:00Z", updated_at: "2026-10-13T12:00:00Z" }
    requests = [request, { ...request, id: "running", status: "running", result_kind: "revision" }, { ...request, id: "missing", result_kind: "revision", result_revision: undefined }]
    mount()
    await screen.findByText("Reply received")
    fireEvent.click(screen.getByRole("button", { name: "Comments" }))
    await screen.findByLabelText("Thread agent requests")
    expect(screen.queryByRole("button", { name: /Compare revisions/ })).toBeNull()
    expect(calls).toHaveLength(0)
  })

  it("reuses a publication identity after an uncertain server failure", async () => {
    mount(); await screen.findByTitle("Artifact static preview")
    fireEvent.click(screen.getByRole("button", { name: "Source" }))
    fireEvent.click(screen.getByRole("button", { name: "Edit as new revision" }))
    fireEvent.change(screen.getByLabelText("HTML for the next revision"), { target: { value: "<h1>Retry safely</h1>" } })
    fail = 503
    fireEvent.click(screen.getByRole("button", { name: "Save revision" }))
    await waitFor(() => expect(calls).toHaveLength(1))
    await waitFor(() => expect(screen.getByRole("button", { name: "Save revision" })).not.toBeDisabled())
    fail = 0
    fireEvent.click(screen.getByRole("button", { name: "Save revision" }))
    await waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[1].body.mutation_id).toBe(calls[0].body.mutation_id)
    await screen.findByTitle("Artifact static preview")
  })

  it("creates an empty human-owned page through the existing API", async () => {
    const onCreated = vi.fn()
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(<QueryClientProvider client={client}><NewArtifactDialog workspaceId="ws1" onClose={vi.fn()} onCreated={onCreated} /></QueryClientProvider>)
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Quarterly report" } })
    fireEvent.click(screen.getByRole("button", { name: "Create artifact page" }))
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith("quarterly-report"))
    expect(calls[0]).toMatchObject({ path: "/api/v1/pages?workspace_id=ws1", body: { name: "Quarterly report", slug: "quarterly-report", panels: [], allow_empty: true } })
    expect(calls[0].body).not.toHaveProperty("owner")
  })
  it("saves the first artifact on a new empty page", async () => {
    head = 0; source = ""; mount()
    fireEvent.click(await screen.findByRole("button", { name: "Paste HTML" }))
    fireEvent.change(screen.getByLabelText("HTML for the next revision"), { target: { value: "<h1>First report</h1>" } })
    fireEvent.click(screen.getByRole("button", { name: "Save revision" }))
    await screen.findByTitle("Artifact static preview")
    expect(calls[0].body).toMatchObject({ html: "<h1>First report</h1>", base_rev: 0 })
  })

  it("renders only sanitized static content and keeps original source escaped", async () => {
    mount()
    const frame = await screen.findByTitle("Artifact static preview")
    expect(frame.getAttribute("sandbox")).toBe("")
    expect(frame.getAttribute("srcdoc")).not.toContain("<script>")
    fireEvent.click(screen.getByRole("button", { name: "Source" }))
    expect(screen.getByText(source)).toBeTruthy()
    expect(document.querySelector("script")).toBeNull()
  })
  it("uploads HTML as a new revision with the observed head and mutation identity", async () => {
    mount(); await screen.findByTitle("Artifact static preview")
    const file = new File(["<h1>New report</h1>"], "report.html", { type: "text/html" })
    Object.defineProperty(file, "text", { value: async () => "<h1>New report</h1>" })
    fireEvent.change(screen.getByLabelText("Upload HTML"), { target: { files: [file] } })
    await screen.findByLabelText("HTML for the next revision")
    fireEvent.click(screen.getByRole("button", { name: "Save revision" }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].path).toContain("/artifact/revisions?workspace_id=ws1")
    expect(calls[0].body).toMatchObject({ html: "<h1>New report</h1>", base_rev: 1 })
    expect(calls[0].body.mutation_id).toEqual(expect.any(String))
    await waitFor(() => expect(screen.getByTitle("Artifact static preview").getAttribute("srcdoc")).toContain("New report"))
  })
  it("preserves unsaved source after a conflict and refreshes only when requested", async () => {
    mount(); await screen.findByTitle("Artifact static preview")
    fireEvent.click(screen.getByRole("button", { name: "Source" }))
    fireEvent.click(screen.getByRole("button", { name: "Edit as new revision" }))
    fireEvent.change(screen.getByLabelText("HTML for the next revision"), { target: { value: "<h1>My unsaved work</h1>" } })
    fail = 409; head = 2
    fireEvent.click(screen.getByRole("button", { name: "Save revision" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("A newer revision was saved")
    expect(screen.getByLabelText("HTML for the next revision")).toHaveValue("<h1>My unsaved work</h1>")
    expect(calls[0].body.base_rev).toBe(1)
    fireEvent.click(screen.getByRole("button", { name: "Review latest revision" }))
    await screen.findByText("Revision 2 · latest")
    expect(screen.getByLabelText("HTML for the next revision")).toHaveValue("<h1>My unsaved work</h1>")
  })
  it("offers revision history and restores through compare-and-swap", async () => {
    head = 2; mount(); await screen.findByTitle("Artifact static preview")
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    fireEvent.click(screen.getByRole("button", { name: "Restore as new revision" }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].path).toContain("/artifact/restore?")
    expect(calls[0].body).toMatchObject({ revision: 1, base_rev: 2 })
    await screen.findByText("Revision 3 · latest")
  })
  it("hides existing content after an authorization revalidation fails", async () => {
    mount(); await screen.findByTitle("Artifact static preview")
    readFail = 403
    const callback = vi.mocked(useRealtimeEventSafe).mock.calls.find(call => call[0] === "page.updated")?.[1]
    await act(async () => { (callback as (() => void))() })
    expect(await screen.findByRole("alert")).toHaveTextContent("Access denied")
    expect(screen.queryByTitle("Artifact static preview")).toBeNull()
  })
  it("keeps comment reply drafts mounted when the artifact source temporarily fails to refresh", async () => {
    const dirty = vi.fn(); mount(dirty); await screen.findByTitle("Artifact static preview")
    fireEvent.click(screen.getByRole("button", { name: "Comments" }))
    fireEvent.change(await screen.findByLabelText("Reply to thread on revision 1"), { target: { value: "Private reply remains here." } })
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(true))
    readFail = 500
    const callback = vi.mocked(useRealtimeEventSafe).mock.calls.find(call => call[0] === "page.updated")?.[1]
    await act(async () => { (callback as (() => void))() })
    expect(await screen.findByRole("alert")).toHaveTextContent("drafts are preserved")
    expect(screen.getByLabelText("Reply to thread on revision 1")).toHaveValue("Private reply remains here.")
    expect(dirty).toHaveBeenLastCalledWith(true)
    readFail = 0
    fireEvent.click(screen.getByRole("button", { name: "Retry artifact" }))
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
    expect(screen.getByLabelText("Reply to thread on revision 1")).toHaveValue("Private reply remains here.")
  })
  it("registers and clears unsaved source with the existing Pages navigation guard", async () => {
    const dirty = vi.fn(); mount(dirty); await screen.findByTitle("Artifact static preview")
    fireEvent.click(screen.getByRole("button", { name: "Source" }))
    fireEvent.click(screen.getByRole("button", { name: "Edit as new revision" }))
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(true))
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(false))
  })
  it("omits write controls for readers", async () => {
    writable = false; mount(); await screen.findByTitle("Artifact static preview")
    expect(screen.queryByLabelText("Upload HTML")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Source" }))
    expect(screen.queryByRole("button", { name: "Edit as new revision" })).toBeNull()
  })
})
