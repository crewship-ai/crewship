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

describe("RightPanel — the open panel says which panel it is", () => {
  it("names itself in a header when the rail hides the tab strip", async () => {
    apiFetch.mockImplementation(() => ok([]))
    panel({ hideTabs: true, initialTab: "files" })

    // Opened from the rail there is no tab strip, so without this the panel
    // is three unlabelled icons and a file tree.
    expect(await screen.findByRole("heading", { name: /^files$/i })).toBeInTheDocument()
  })
})
