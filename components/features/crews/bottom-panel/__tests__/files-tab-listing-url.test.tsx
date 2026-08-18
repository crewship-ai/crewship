import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, waitFor } from "@testing-library/react"

// =============================================================================
// The Files tab talks to two different routes, and they read two different
// query parameters. Mixing them up is how the artifact pane shipped a
// destructive bug last week — a LISTING response read as file CONTENT — so the
// distinction is pinned here rather than left to reviewer memory.
//
//   · LISTING  — GET /agents/{id}/files reads `subdir` (and `recursive`).
//                It has never read `path`; internal/api/proxy_files.go
//                AgentFiles forwards only those two to the sidecar.
//   · CONTENT  — GET /agents/{id}/files/download and PUT .../files/save read
//                `path`, and 400 without it.
//
// The top-level listing was sending `&path=/`. Dead weight rather than a bug —
// the server drops it — but it is the same confusion, written down in the code,
// one copy-paste away from being read as "this listing is scoped to a path".
// =============================================================================

vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: <T,>(_key: string, def: T) => [def, vi.fn(), { ready: true }] as const,
}))

// The editor is a dynamic() import of CodeMirror — nothing this file asserts.
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: () => null,
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

import { FilesTab } from "../files-tab"

const agentContext = {
  kind: "agent" as const,
  agentId: "agent-1",
  agentSlug: "filip",
  agentName: "Filip",
  crewId: "crew-1",
  crewSlug: "devops",
}

const crewContext = { kind: "crew" as const, crewId: "crew-1", crewSlug: "devops" }

function jsonOk(body: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response)
}

/** Every URL the component has requested, in order. */
function urls(): string[] {
  return apiFetch.mock.calls.map((c) => String(c[0]))
}

describe("FilesTab — the listing route and the content route take different params", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockImplementation(() => jsonOk([]))
  })

  it("asks for the agent's top-level listing without a `path` the route never reads", async () => {
    render(<FilesTab workspaceId="ws-1" context={agentContext} />)

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const listing = urls().find((u) => u.includes("/agents/agent-1/files"))!
    expect(listing).toBeDefined()
    expect(listing).toContain("workspace_id=ws-1")
    // The assertion that was red: `&path=/` rode along on the listing.
    expect(listing).not.toContain("path=")
  })

  it("asks for the crew's top-level listing the same way", async () => {
    render(<FilesTab workspaceId="ws-1" context={crewContext} />)

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const listing = urls().find((u) => u.includes("/crews/crew-1/files"))!
    expect(listing).toBeDefined()
    expect(listing).not.toContain("path=")
  })

  it("never puts `path` on a listing request, whichever listing it is", async () => {
    // A folder expansion is the other listing call, and it is already correct —
    // `subdir`, not `path`. Pinned so the two stay on the same parameter.
    apiFetch.mockImplementation((url: string) => {
      if (String(url).includes("subdir=")) return jsonOk([])
      return jsonOk([{ name: "workspace", is_dir: true, path: "crew-1/filip/workspace" }])
    })
    const { container } = render(<FilesTab workspaceId="ws-1" context={agentContext} />)

    await waitFor(() => expect(container.querySelector("ul")).not.toBeNull())
    const folder = await waitFor(() => {
      const b = Array.from(container.querySelectorAll("button")).find((el) =>
        el.textContent?.includes("workspace"),
      )
      expect(b).toBeDefined()
      return b!
    })
    folder.click()

    await waitFor(() => expect(urls().some((u) => u.includes("subdir="))).toBe(true))

    for (const u of urls()) {
      // /files/download and /files/save legitimately carry `path` — they are
      // the CONTENT routes. Every plain /files call is a listing.
      const isListing = /\/files\?/.test(u)
      if (isListing) {
        expect(u, `listing carried a path param: ${u}`).not.toContain("path=")
      }
    }
  })
})
