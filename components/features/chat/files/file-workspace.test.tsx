import { cleanup, fireEvent, render, screen, waitFor, act } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { FileWorkspace } from "./file-workspace"
import { readPreviewBytes } from "./file-preview-data"
import { apiFetch } from "@/lib/api-fetch"

const role = vi.hoisted(() => ({ value: "MANAGER" }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ role: role.value }) }))
vi.mock("./file-preview-data", async (importOriginal) => ({
  ...await importOriginal<typeof import("./file-preview-data")>(),
  readPreviewBytes: vi.fn(),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({ code, onDirtyChange, onSave, extraExtensions, readOnly }: {
    code: string; onDirtyChange: (dirty: boolean) => void; onSave: (text: string) => void; extraExtensions: unknown[]; readOnly?: boolean
  }) => <div>
    <span data-testid="editor-mode">{extraExtensions.length ? "read only" : "editable"}</span>
    <textarea readOnly={readOnly} aria-label="File content" defaultValue={code} onChange={() => onDirtyChange(true)} />
    <button onClick={() => onSave("updated content")}>Editor save</button>
  </div>,
}))
vi.mock("./file-preview", () => ({ FilePreview: ({ name }: { name: string }) => <div>PDF or image: {name}</div> }))

const read = vi.mocked(readPreviewBytes)
const fetch = vi.mocked(apiFetch)
const file = { path: "crew/agent/docs/checks.js", name: "checks.js", scope: { kind: "agent" as const } }
const base = { agentId: "agent", workspaceId: "ws", onClose: vi.fn() }

beforeEach(() => {
  vi.clearAllMocks()
  role.value = "MANAGER"
  read.mockResolvedValue(new TextEncoder().encode("initial content"))
  fetch.mockResolvedValue(new Response(null, { status: 204 }))
})
afterEach(cleanup)

describe("FileWorkspace", () => {
  it("opens agent text through the scoped download route and saves only after Edit", async () => {
    const onDirtyChange = vi.fn()
    render(<FileWorkspace {...base} file={file} onDirtyChange={onDirtyChange} />)
    expect(await screen.findByDisplayValue("initial content")).toBeInTheDocument()
    expect(read).toHaveBeenCalledWith("/api/v1/agents/agent/files/download?workspace_id=ws&path=crew%2Fagent%2Fdocs%2Fchecks.js", expect.any(AbortSignal))
    expect(screen.getByTestId("editor-mode")).toHaveTextContent("read only")
    fireEvent.click(screen.getByRole("button", { name: "Editor save" }))
    expect(fetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    expect(screen.getByTestId("editor-mode")).toHaveTextContent("editable")
    fireEvent.change(screen.getByRole("textbox", { name: "File content" }), { target: { value: "updated content" } })
    expect(onDirtyChange).toHaveBeenCalledWith(true)
    fireEvent.click(screen.getByRole("button", { name: "Editor save" }))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      "/api/v1/agents/agent/files/save?workspace_id=ws&path=crew%2Fagent%2Fdocs%2Fchecks.js",
      expect.objectContaining({ method: "PUT", body: "updated content" }),
    ))
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(false))
  })

  it("shows read-only preview for a viewer and routes PDF to the safe renderer", async () => {
    role.value = "VIEWER"
    const { rerender } = render(<FileWorkspace {...base} file={file} />)
    await screen.findByDisplayValue("initial content")
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument()
    rerender(<FileWorkspace {...base} file={{ ...file, path: "crew/agent/report.pdf", name: "report.pdf" }} />)
    expect(screen.getByText("PDF or image: report.pdf")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Download file" })).toHaveAttribute("href", "/api/v1/agents/agent/files/download?workspace_id=ws&path=crew%2Fagent%2Freport.pdf")
    expect(screen.queryByRole("button", { name: "Chat alongside" })).not.toBeInTheDocument()
    expect(screen.queryByText("initial content")).not.toBeInTheDocument()
  })

  it("does not display a binary payload under a text filename", async () => {
    read.mockResolvedValue(new Uint8Array([60, 104, 0, 116, 109, 108, 62]))
    render(<FileWorkspace {...base} file={{ ...file, name: "index.html" }} />)
    expect(await screen.findByRole("alert")).toHaveTextContent("binary data")
    expect(screen.queryByRole("textbox", { name: "File content" })).not.toBeInTheDocument()
  })
})

 it("locks the editor and cancel during a pending save", async () => {
   let resolve!: (response: Response) => void
   fetch.mockReturnValue(new Promise<Response>((done) => { resolve = done }))
   render(<FileWorkspace {...base} file={file} />)
   await screen.findByDisplayValue("initial content")
   fireEvent.click(screen.getByRole("button", { name: "Edit" }))
   fireEvent.change(screen.getByRole("textbox"), { target: { value: "updated content" } })
   fireEvent.click(screen.getByRole("button", { name: "Editor save" }))
   expect(screen.getByRole("textbox")).toHaveAttribute("readonly")
   expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled()
   expect(screen.getByRole("button", { name: "Back to chat" })).toBeDisabled()
   await act(async () => resolve(new Response(null, { status: 204 })))
   expect(screen.getByRole("textbox")).toHaveValue("updated content")
 })
