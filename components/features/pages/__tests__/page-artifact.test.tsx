import DOMPurify from "dompurify"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { NewArtifactDialog } from "../new-artifact-dialog"
import { PageArtifactView } from "../page-artifact"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-pages", () => ({ pagesKeys: { all: (workspace: string) => ["pages", workspace] } }))
let head = 1
let writable = true
let fail = 0
let readFail = 0
let source = "<h1>Saved report</h1><script>alert(1)</script>"
const calls: { path: string; body: Record<string, unknown> }[] = []
function mount(onDirtyChange?: (dirty: boolean) => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><PageArtifactView workspaceId="ws1" slug="report" onDirtyChange={onDirtyChange} /></QueryClientProvider>)
}
const supported = DOMPurify.isSupported
beforeEach(() => {
  DOMPurify.isSupported = false
  vi.mocked(useRealtimeEventSafe).mockClear()
  head = 1; writable = true; fail = 0; readFail = 0; source = "<h1>Saved report</h1><script>alert(1)</script>"; calls.length = 0
  vi.mocked(apiFetch).mockImplementation(async (path, options) => {
    if (options?.method === "POST") {
      const body = JSON.parse(String(options.body)); calls.push({ path: String(path), body })
      if (fail) return new Response(JSON.stringify({ error: fail === 409 ? "artifact revision changed; reload before saving" : "denied" }), { status: fail })
      head++; if (typeof body.html === "string") source = body.html
      return new Response(JSON.stringify({ head_rev: head, revision: head }))
    }
    if (readFail) return new Response(JSON.stringify({ error: "Access denied" }), { status: readFail })
    const selected = new URL(String(path), "https://studio.test").searchParams.get("revision")
    return new Response(JSON.stringify({ head_rev: head, revision: selected ? Number(selected) : head, html: source, can_write: writable, revisions: Array.from({ length: head }, (_, i) => ({ revision: i + 1, created_at: "2026-10-13T00:00:00Z", author_user_id: "user1" })) }))
  })
})
afterEach(() => { cleanup(); DOMPurify.isSupported = supported })
describe("human page artifact", () => {
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
    fireEvent.click(screen.getByRole("button", { name: "Source", exact: true }))
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
    fireEvent.click(screen.getByRole("button", { name: "Source", exact: true }))
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
    fireEvent.click(screen.getByRole("button", { name: "History", exact: true }))
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
  it("registers and clears unsaved source with the existing Pages navigation guard", async () => {
    const dirty = vi.fn(); mount(dirty); await screen.findByTitle("Artifact static preview")
    fireEvent.click(screen.getByRole("button", { name: "Source", exact: true }))
    fireEvent.click(screen.getByRole("button", { name: "Edit as new revision" }))
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(true))
    fireEvent.click(screen.getByRole("button", { name: "Cancel", exact: true }))
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(false))
  })
  it("omits write controls for readers", async () => {
    writable = false; mount(); await screen.findByTitle("Artifact static preview")
    expect(screen.queryByLabelText("Upload HTML")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Source", exact: true }))
    expect(screen.queryByRole("button", { name: "Edit as new revision" })).toBeNull()
  })
})
