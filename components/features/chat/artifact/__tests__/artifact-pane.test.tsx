import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, act } from "@testing-library/react"

// =============================================================================
// P0.4 — the pane asked the *listing* route for file contents.
//
//   GET /api/v1/agents/{id}/files?workspace_id=…&path=<file>   ← path ignored
//
// `AgentFiles` builds its IPC call from agent_slug / recursive / subdir and
// never reads `path`, so the pane got a JSON directory listing, rendered it as
// the file, and its Save button would write that JSON over the real file.
//
// These tests pin three things:
//   · the read goes to /files/download and renders the exact bytes,
//   · a listing body can never reach the editor, whatever route returned it,
//   · a save writes the edited bytes to the path that was actually read.
// =============================================================================

let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))

const toastError = vi.fn()
const toastSuccess = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    error: (...a: unknown[]) => toastError(...a),
    success: (...a: unknown[]) => toastSuccess(...a),
  },
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

// The editor is code-split behind next/dynamic; the stub stands in for
// CodeMirror and exposes exactly what the pane hands it plus a way to save.
vi.mock("next/dynamic", () => ({
  default: () =>
    function StubFileEditor({
      code,
      onSave,
    }: {
      code: string
      onSave: (next: string) => void
    }) {
      return (
        <div>
          <pre data-testid="editor-code">{code}</pre>
          <button type="button" onClick={() => onSave(`${code}EDIT`)}>
            stub-save
          </button>
        </div>
      )
    },
}))

import { ArtifactPane } from "../artifact-pane"
import { useArtifactStore } from "@/stores/artifact-store"

const LISTING = JSON.stringify([
  { name: "notes.md", path: "crew-1/scout/workspace/notes.md", is_dir: false, size: 12 },
  { name: "src", path: "crew-1/scout/workspace/src", is_dir: true, size: 0 },
])

function response(init: {
  ok?: boolean
  status?: number
  body?: string
  contentType?: string | null
  url?: string
}) {
  const headers = new Headers()
  if (init.contentType) headers.set("content-type", init.contentType)
  return Promise.resolve({
    ok: init.ok ?? true,
    status: init.status ?? 200,
    url: init.url ?? "",
    headers,
    text: async () => init.body ?? "",
    json: async () => JSON.parse(init.body ?? "null"),
  } as unknown as Response)
}

function openTab(id: string, path: string) {
  act(() => {
    useArtifactStore.getState().openFile({
      id,
      agentId: "agent-1",
      path,
      title: path.split("/").pop() ?? path,
    })
  })
}

beforeEach(() => {
  workspaceId = "ws-1"
  apiFetch.mockReset()
  toastError.mockReset()
  toastSuccess.mockReset()
  useArtifactStore.setState({ open: false, tabs: [], activeId: null })
})

afterEach(() => cleanup())

describe("ArtifactPane — reading a file", () => {
  it("requests the download route and renders the exact bytes", async () => {
    apiFetch.mockReturnValue(
      response({ body: "# notes\nsecond line\n", contentType: "application/octet-stream" }),
    )

    render(<ArtifactPane agentId="agent-1" />)
    openTab("t1", "workspace/notes.md")

    expect(await screen.findByTestId("editor-code")).toHaveTextContent("second line")
    expect(screen.getByTestId("editor-code").textContent).toBe("# notes\nsecond line\n")

    const url = String(apiFetch.mock.calls[0][0])
    expect(url).toContain("/api/v1/agents/agent-1/files/download")
    expect(url).toContain("path=workspace%2Fnotes.md")
    // The listing route is what the bug called; it must not be called at all.
    expect(url).not.toMatch(/\/files\?/)
  })

  it("refuses a directory listing and leaves the editor unpopulated", async () => {
    // 200 OK, JSON listing — precisely what the old URL returned.
    apiFetch.mockReturnValue(
      response({
        body: LISTING,
        contentType: "application/json",
        url: "http://localhost/api/v1/agents/agent-1/files/download?path=workspace%2Fnotes.md",
      }),
    )

    render(<ArtifactPane agentId="agent-1" />)
    openTab("t1", "workspace/notes.md")

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(screen.queryByTestId("editor-code")).toBeNull()
    expect(screen.queryByText(/is_dir/)).toBeNull()
    // No editor means no save button, so the listing can never be written back.
    expect(screen.queryByRole("button", { name: "stub-save" })).toBeNull()
  })

  it("refuses a body returned by any route other than download", async () => {
    apiFetch.mockReturnValue(
      response({
        body: LISTING,
        contentType: "application/octet-stream",
        url: "http://localhost/api/v1/agents/agent-1/files?workspace_id=ws-1",
      }),
    )

    render(<ArtifactPane agentId="agent-1" />)
    openTab("t1", "workspace/notes.md")

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(screen.queryByTestId("editor-code")).toBeNull()
  })
})

describe("ArtifactPane — saving", () => {
  it("writes the edited bytes to the same path it read", async () => {
    apiFetch.mockImplementation((url: string) =>
      String(url).includes("/files/download")
        ? response({ body: "original\n", contentType: "application/octet-stream" })
        : response({ body: "{}", contentType: "application/json" }),
    )

    render(<ArtifactPane agentId="agent-1" />)
    openTab("t1", "workspace/notes.md")

    await screen.findByTestId("editor-code")
    fireEvent.click(screen.getByRole("button", { name: "stub-save" }))

    await waitFor(() => expect(apiFetch.mock.calls.length).toBe(2))
    const [saveUrl, init] = apiFetch.mock.calls[1] as [string, RequestInit]
    expect(saveUrl).toContain("/api/v1/agents/agent-1/files/save")
    expect(saveUrl).toContain("path=workspace%2Fnotes.md")
    expect(init.method).toBe("PUT")
    expect(init.body).toBe("original\nEDIT")
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
  })

  it("cannot save bytes read for a different path after a tab switch", async () => {
    apiFetch.mockImplementationOnce(() =>
      response({ body: "tab one bytes\n", contentType: "application/octet-stream" }),
    )
    // Second tab's load never resolves — the editor must not stay up showing
    // (and offering to save) the first tab's bytes under the second's path.
    apiFetch.mockImplementationOnce(() => new Promise<Response>(() => {}))

    render(<ArtifactPane agentId="agent-1" />)
    openTab("t1", "workspace/one.md")
    await screen.findByTestId("editor-code")

    openTab("t2", "workspace/two.md")

    await waitFor(() => expect(screen.queryByTestId("editor-code")).toBeNull())
    expect(screen.queryByRole("button", { name: "stub-save" })).toBeNull()
    // Only the two reads happened; nothing was written.
    expect(
      apiFetch.mock.calls.filter((c) => String(c[0]).includes("/files/save")).length,
    ).toBe(0)
  })
})
