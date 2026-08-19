import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// The right rail's Files tab, and the four things it has to be able to say.
//
// The chat tree briefly grew an "AgentFilesPane" that duplicated this tab.
// It was deleted — but it distinguished three states this tab did not, and
// those are worth more than the pane was:
//
//   · in flight        — say so, do not draw an empty box
//   · fetch failed     — say THAT, name it, and offer a retry
//   · could not ask    — "this agent is in no crew" is not "the crew shares
//                        nothing"
//
// The crew scope in particular used to swallow a failed fetch into an empty
// array (files/three-tier-files.tsx:67) and render "No shared crew files",
// which is a claim the UI had no standing to make. It is now the one thing
// this file exists to prevent.
// =============================================================================

let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))

vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: (_k: string, initial: unknown) => [initial, vi.fn()],
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const toastError = vi.fn()
const toastSuccess = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    error: (...a: unknown[]) => toastError(...a),
    success: (...a: unknown[]) => toastSuccess(...a),
  },
}))

// The editor is code-split behind next/dynamic; the stub stands in for
// CodeMirror and exposes the bytes it was handed plus a way to save them.
vi.mock("next/dynamic", () => ({
  default: () =>
    function StubFileEditor({ code, onSave }: { code: string; onSave: (next: string) => void }) {
      return (
        <div>
          <pre data-testid="editor-code">{code}</pre>
          <button type="button" onClick={() => onSave(`${code} EDITED`)}>
            stub-save
          </button>
        </div>
      )
    },
}))

import { RightPanel } from "../right-panel"

// ---------------------------------------------------------------- helpers

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

function fail(status: number) {
  return Promise.resolve({ ok: false, status, json: () => Promise.resolve({}) })
}

function file(name: string, prefix = "crew-1/shared") {
  return { path: `${prefix}/${name}`, name, size: 12, is_dir: false, mod_time: "2026-08-01T10:00:00Z" }
}

function url(args: unknown[]): string {
  return String(args[0])
}

function panel(props: Partial<React.ComponentProps<typeof RightPanel>> = {}) {
  return render(
    <RightPanel agentId="agent-1" workspaceId={workspaceId} files={[]} initialTab="files" {...props} />,
  )
}

/** The crew scope is loaded on demand — opening it is what asks. */
async function openCrewScope() {
  fireEvent.click(await screen.findByRole("button", { name: /crew/i }))
}

beforeEach(() => {
  workspaceId = "ws-1"
  apiFetch.mockReset()
  toastError.mockReset()
  toastSuccess.mockReset()
})
afterEach(() => cleanup())

// ---------------------------------------------------------------- tests

describe("RightPanel — the Files tab is honest about the crew scope", () => {
  it("says the crew files are loading rather than drawing an empty scope", async () => {
    apiFetch.mockImplementation(() => new Promise(() => {}))
    panel()
    await openCrewScope()

    expect(await screen.findByTestId("crew-files-loading")).toBeInTheDocument()
    expect(screen.queryByText(/no shared crew files/i)).toBeNull()
  })

  it("says a failed fetch FAILED — it does not render as an empty crew", async () => {
    apiFetch.mockImplementation((...args: unknown[]) => {
      if (url(args).includes("/api/v1/agents/agent-1?")) return ok({ id: "agent-1", slug: "casey", crew_id: "crew-1" })
      return fail(502)
    })
    panel()
    await openCrewScope()

    const err = await screen.findByTestId("crew-files-error")
    // The status is the one fact that tells a 403 from a 502; without it
    // every retry after this is a guess.
    expect(err).toHaveTextContent(/502/)
    expect(screen.queryByText(/no shared crew files/i)).toBeNull()
  })

  it("retries on demand, and the retry actually re-asks", async () => {
    apiFetch.mockImplementation((...args: unknown[]) => {
      if (url(args).includes("/api/v1/agents/agent-1?")) return ok({ id: "agent-1", slug: "casey", crew_id: "crew-1" })
      return fail(502)
    })
    panel()
    await openCrewScope()
    await screen.findByTestId("crew-files-error")

    apiFetch.mockImplementation((...args: unknown[]) => {
      if (url(args).includes("/api/v1/agents/agent-1?")) return ok({ id: "agent-1", slug: "casey", crew_id: "crew-1" })
      if (url(args).includes("/api/v1/crews/crew-1/files")) return ok([file("handbook.md")])
      return ok([])
    })
    fireEvent.click(screen.getByRole("button", { name: /retry/i }))

    expect(await screen.findByText("handbook.md")).toBeInTheDocument()
    expect(screen.queryByTestId("crew-files-error")).toBeNull()
  })

  it("separates 'this agent is in no crew' from 'the crew shares nothing'", async () => {
    apiFetch.mockImplementation((...args: unknown[]) => {
      if (url(args).includes("/api/v1/agents/agent-1?")) return ok({ id: "agent-1", slug: "casey", crew_id: null })
      return ok([])
    })
    panel()
    await openCrewScope()

    expect(await screen.findByTestId("crew-files-no-crew")).toHaveTextContent(/crew/i)
    // The empty-list sentence is a different sentence, and must not appear.
    expect(screen.queryByTestId("crew-files-empty")).toBeNull()
  })

  it("says the crew is empty only when the crew really answered with nothing", async () => {
    apiFetch.mockImplementation((...args: unknown[]) => {
      if (url(args).includes("/api/v1/agents/agent-1?")) return ok({ id: "agent-1", slug: "casey", crew_id: "crew-1" })
      if (url(args).includes("/api/v1/crews/crew-1/files")) return ok([])
      return ok([])
    })
    panel()
    await openCrewScope()

    expect(await screen.findByTestId("crew-files-empty")).toBeInTheDocument()
    expect(screen.queryByTestId("crew-files-error")).toBeNull()
  })

  it("asks nothing until the crew scope is actually opened", async () => {
    apiFetch.mockImplementation(() => ok([]))
    panel()

    await screen.findByRole("button", { name: /crew/i })
    await waitFor(() =>
      expect(apiFetch.mock.calls.some((c) => url(c).includes("/api/v1/crews/"))).toBe(false),
    )
  })
})

// =============================================================================
// A file is read from a tree, and it must be written back to THAT tree.
//
// The crew scope lists `/crews/{crewId}/files`, whose keys are shaped
// `<crewId>/<name>`, and used to hand the click to the AGENT editor. The read
// was a guaranteed 403 (proxy_files.go rejects a `<crewId>/` path that is not
// under `<crewId>/<slug>/`), and the save — had a path ever slipped past that
// prefix test — would have PUT a crew file into the agent's own tree. The read
// failing loudly is the mild half; the write landing in the wrong tree is not.
// =============================================================================

/** A download/save response: the editor reads bytes with .text(). */
function okBytes(body: string) {
  return Promise.resolve({
    ok: true,
    status: 200,
    text: () => Promise.resolve(body),
    json: () => Promise.resolve({}),
  })
}

const agentFile = {
  path: "crew-1/casey/main.py",
  name: "main.py",
  size: 20,
  is_dir: false,
  mod_time: "2026-08-01T10:00:00Z",
}

/** Every URL requested, in order. */
function requested(): string[] {
  return apiFetch.mock.calls.map((c) => url(c))
}

describe("RightPanel — a crew file is read and written in the crew's tree", () => {
  beforeEach(() => {
    apiFetch.mockImplementation((...args: unknown[]) => {
      const u = url(args)
      if (u.includes("/api/v1/agents/agent-1?")) return ok({ id: "agent-1", crew_id: "crew-1" })
      if (u.includes("/files/download")) return okBytes("shared bytes")
      if (u.includes("/files/save")) return okBytes("{}")
      if (u.includes("/api/v1/crews/crew-1/files")) return ok([file("handbook.md", "crew-1")])
      return ok([])
    })
  })

  it("opens a shared crew file through the crew route and shows its bytes", async () => {
    panel()
    await openCrewScope()

    fireEvent.click(await screen.findByRole("button", { name: /handbook\.md/i }))

    const download = await waitFor(() => {
      const d = requested().find((u) => u.includes("/files/download"))
      expect(d).toBeDefined()
      return d!
    })
    expect(download).toContain("/api/v1/crews/crew-1/files/download")
    expect(download).toContain("path=crew-1%2Fhandbook.md")
    // The agent editor is the wrong reader for this tree: it 403s on any
    // `<crewId>/` path that is not under `<crewId>/<slug>/`.
    expect(download).not.toContain("/api/v1/agents/")

    expect(await screen.findByTestId("editor-code")).toHaveTextContent("shared bytes")
    expect(toastError).not.toHaveBeenCalled()
  })

  it("saves it back to the crew tree — never into the agent's own", async () => {
    panel()
    await openCrewScope()
    fireEvent.click(await screen.findByRole("button", { name: /handbook\.md/i }))
    await screen.findByTestId("editor-code")

    fireEvent.click(screen.getByRole("button", { name: /stub-save/i }))

    const save = await waitFor(() => {
      const s = requested().find((u) => u.includes("/files/save"))
      expect(s).toBeDefined()
      return s!
    })
    expect(save).toContain("/api/v1/crews/crew-1/files/save")
    expect(save).toContain("path=crew-1%2Fhandbook.md")
    expect(save).not.toContain("/api/v1/agents/")
    // Read and write are the same tree, which is the whole assertion.
    expect(requested().every((u) => !/\/agents\/[^/]+\/files\/(save|download)/.test(u))).toBe(true)
  })

  it("still reads and writes an agent file through the agent routes", async () => {
    panel({ files: [agentFile] })

    fireEvent.click(await screen.findByRole("button", { name: /main\.py/i }))
    const download = await waitFor(() => {
      const d = requested().find((u) => u.includes("/files/download"))
      expect(d).toBeDefined()
      return d!
    })
    expect(download).toContain("/api/v1/agents/agent-1/files/download")
    expect(download).toContain("path=crew-1%2Fcasey%2Fmain.py")

    await screen.findByTestId("editor-code")
    fireEvent.click(screen.getByRole("button", { name: /stub-save/i }))
    const save = await waitFor(() => {
      const s = requested().find((u) => u.includes("/files/save"))
      expect(s).toBeDefined()
      return s!
    })
    expect(save).toContain("/api/v1/agents/agent-1/files/save")
    expect(save).not.toContain("/api/v1/crews/")
  })

  it("keeps the tree straight when a crew file is opened after an agent one", async () => {
    panel({ files: [agentFile] })

    fireEvent.click(await screen.findByRole("button", { name: /main\.py/i }))
    await screen.findByTestId("editor-code")

    await openCrewScope()
    fireEvent.click(await screen.findByRole("button", { name: /handbook\.md/i }))
    await waitFor(() =>
      expect(
        requested().some((u) => u.includes("/api/v1/crews/crew-1/files/download")),
      ).toBe(true),
    )

    fireEvent.click(screen.getByRole("button", { name: /stub-save/i }))

    const saves = await waitFor(() => {
      const s = requested().filter((u) => u.includes("/files/save"))
      expect(s.length).toBe(1)
      return s
    })
    expect(saves[0]).toContain("/api/v1/crews/crew-1/files/save")
  })
})

describe("RightPanel — the open panel says which panel it is", () => {
  it("names itself in a header when the rail hides the tab strip", async () => {
    apiFetch.mockImplementation(() => ok([]))
    panel({ hideTabs: true, initialTab: "files" })

    // Opened from the rail there is no tab strip, so without this the panel
    // is three unlabelled icons and a file tree.
    expect(await screen.findByRole("heading", { name: /^files$/i })).toBeInTheDocument()
  })
})
