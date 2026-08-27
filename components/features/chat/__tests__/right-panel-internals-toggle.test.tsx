import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// A directory expanded across a `Show internals` toggle must not go empty.
//
// The tree is rebuilt from scratch whenever the visible file list changes, and
// the toggle changes it. Rebuilding throws away every lazily-loaded child —
// but `fetchedDirsRef` is a ref, so it SURVIVED the rebuild and went on
// claiming that each of those directories had already been fetched. The
// fetch effect then skipped every still-expanded directory, and it stayed
// empty until the panel reset on an agent change.
//
// The fix is one line — clearing the ref in the same effect that rebuilds the
// tree — which is exactly the kind of line that gets removed by someone
// tidying up a "redundant" reset. Hence this file.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))

vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: (_k: string, initial: unknown) => [initial, vi.fn()],
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

vi.mock("next/dynamic", () => ({
  default: () => function StubFileEditor() { return null },
}))

import { RightPanel } from "../right-panel"
import { ChatAgentProvider } from "../chat-agent-context"

const PREFIX = "crew-1/riley/"

function entry(name: string, isDir: boolean) {
  return {
    path: PREFIX + name,
    name: name.split("/").pop() as string,
    size: isDir ? 0 : 12,
    is_dir: isDir,
    mod_time: "2026-08-01T10:00:00Z",
  }
}

// One real directory, one file inside it that only arrives from the lazy
// fetch, and one root-level plumbing file so the toggle has something to
// toggle. `AGENTS.md` at the root of the namespace is what `classifyAgentFile`
// calls plumbing.
const files = [entry("src", true), entry("AGENTS.md", false)]

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

/** Every subdir the panel asked the server to expand, in order. */
function subdirFetches(): string[] {
  return apiFetch.mock.calls
    .map((c) => String(c[0]))
    .filter((u) => u.includes("subdir="))
    .map((u) => decodeURIComponent(u.split("subdir=")[1]))
}

beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockImplementation((u: string) =>
    String(u).includes("subdir=") ? ok([entry("src/main.ts", false)]) : ok([]),
  )
})
afterEach(() => cleanup())

function panel() {
  return render(
    <ChatAgentProvider
      agent={{
        id: "agent-1",
        name: "Riley",
        slug: "riley",
        crewId: "crew-1",
        avatarSeed: "riley",
        avatarStyle: null,
        avatarUrl: null,
      }}
    >
      <RightPanel agentId="agent-1" workspaceId="ws-1" files={files} initialTab="files" />
    </ChatAgentProvider>,
  )
}

describe("RightPanel — the lazy-load cache does not outlive the tree it describes", () => {
  it("re-fetches a directory that is still expanded after Show internals is toggled", async () => {
    panel()

    fireEvent.click(await screen.findByText("src"))
    await waitFor(() => expect(subdirFetches()).toEqual(["src"]))
    // The child arrived, so the directory is genuinely loaded.
    expect(await screen.findByText("main.ts")).toBeInTheDocument()

    // The toggle rebuilds the tree from the newly-visible list, which discards
    // every loaded child. `src` is still expanded, so it has to be asked for
    // again — this is the assertion the one-line fix exists for.
    fireEvent.click(await screen.findByRole("button", { name: /internal file/i }))

    await waitFor(() => expect(subdirFetches()).toEqual(["src", "src"]))
    expect(await screen.findByText("main.ts")).toBeInTheDocument()
  })

  it("does not re-fetch a directory while the tree is unchanged", async () => {
    panel()

    fireEvent.click(await screen.findByText("src"))
    await waitFor(() => expect(subdirFetches()).toEqual(["src"]))

    // Collapse and re-expand with no rebuild in between: the children are
    // still in the tree, so nothing should go back to the server. The cache is
    // not wrong, it was only outliving the thing it described.
    fireEvent.click(screen.getByText("src"))
    fireEvent.click(screen.getByText("src"))

    await waitFor(() => expect(screen.getByText("main.ts")).toBeInTheDocument())
    expect(subdirFetches()).toEqual(["src"])
  })
})
