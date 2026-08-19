import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// A folder in the crew scope has to open onto something.
//
// The crew listing's top level is mostly directories — one per agent slug —
// so "click a folder, nothing happens, forever" is not an edge case here, it
// is the common case. The scope shipped with `loadingDirs={new Set()}` and a
// toggle that only wrote to `expanded`: nothing ever fetched a directory's
// children, and `buildTopLevelTree` marks every directory `childrenLoaded:
// false` with no children. The chevron turned and zero rows appeared.
//
// Same rule the tree already applies to threads: a disclosure that opens onto
// nothing is a promise the UI cannot keep. So expanding fetches, and a fetch
// that fails says so rather than leaving an open folder that looks empty.
// =============================================================================

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn() },
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { CrewFilesScope } from "../files/crew-files-scope"

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

function fail(status: number) {
  return Promise.resolve({ ok: false, status, json: () => Promise.resolve({}) })
}

function entry(path: string, isDir: boolean) {
  return {
    path,
    name: path.split("/").pop()!,
    size: isDir ? 0 : 12,
    is_dir: isDir,
    mod_time: "2026-08-01T10:00:00Z",
  }
}

function url(args: unknown[]): string {
  return String(args[0])
}

function urls(): string[] {
  return apiFetch.mock.calls.map((c) => url(c))
}

/** Agent → crew id, then the crew's top level: one agent folder, one file. */
function topLevel(dirFetch: (subdir: string) => unknown) {
  apiFetch.mockImplementation((...args: unknown[]) => {
    const u = url(args)
    if (u.includes("/api/v1/agents/agent-1?")) return ok({ id: "agent-1", crew_id: "crew-1" })
    const sub = u.match(/subdir=([^&]*)/)
    if (sub) return dirFetch(decodeURIComponent(sub[1]))
    if (u.includes("/api/v1/crews/crew-1/files")) {
      return ok([entry("crew-1/casey", true), entry("crew-1/handbook.md", false)])
    }
    return ok([])
  })
}

function scope() {
  return render(
    <CrewFilesScope
      agentId="agent-1"
      workspaceId="ws-1"
      selectedFile={null}
      onFileClick={vi.fn()}
    />,
  )
}

beforeEach(() => {
  apiFetch.mockReset()
  toastError.mockReset()
})
afterEach(() => cleanup())

describe("CrewFilesScope — a folder opens onto its children", () => {
  it("fetches a directory's children when it is expanded, and renders them", async () => {
    topLevel(() => ok([entry("crew-1/casey/notes.md", false)]))
    scope()

    fireEvent.click(await screen.findByRole("button", { name: /casey/i }))

    // The listing route takes `subdir`, relative to the crew root — the same
    // parameter the agent scope's watcher uses, not `path`.
    await waitFor(() =>
      expect(urls().some((u) => u.includes("subdir=casey"))).toBe(true),
    )
    const child = urls().find((u) => u.includes("subdir="))!
    expect(child).toContain("/api/v1/crews/crew-1/files?")
    expect(child).toContain("workspace_id=ws-1")

    expect(await screen.findByText("notes.md")).toBeInTheDocument()
  })

  it("asks once per directory, not once per render", async () => {
    topLevel(() => ok([entry("crew-1/casey/notes.md", false)]))
    scope()

    fireEvent.click(await screen.findByRole("button", { name: /casey/i }))
    await screen.findByText("notes.md")

    expect(urls().filter((u) => u.includes("subdir=casey"))).toHaveLength(1)
  })

  it("does not leave a folder sitting open and apparently empty when the fetch fails", async () => {
    topLevel(() => fail(502))
    scope()

    fireEvent.click(await screen.findByRole("button", { name: /casey/i }))

    await waitFor(() => expect(toastError).toHaveBeenCalled())
    // An open folder with no rows under it is the empty-crew lie again, one
    // level down: it must close rather than claim the folder is empty.
    await waitFor(() =>
      expect(urls().filter((u) => u.includes("subdir=casey"))).toHaveLength(1),
    )

    // And clicking again re-asks rather than sitting on the failure.
    fireEvent.click(screen.getByRole("button", { name: /casey/i }))
    await waitFor(() =>
      expect(urls().filter((u) => u.includes("subdir=casey"))).toHaveLength(2),
    )
  })

  it("hands the click on a crew file its crew id, so the reader knows which tree it is", async () => {
    const onFileClick = vi.fn()
    topLevel(() => ok([]))
    render(
      <CrewFilesScope
        agentId="agent-1"
        workspaceId="ws-1"
        selectedFile={null}
        onFileClick={onFileClick}
      />,
    )

    fireEvent.click(await screen.findByRole("button", { name: /handbook\.md/i }))

    expect(onFileClick).toHaveBeenCalledWith(
      expect.objectContaining({ path: "crew-1/handbook.md" }),
      "crew-1",
    )
  })
})
