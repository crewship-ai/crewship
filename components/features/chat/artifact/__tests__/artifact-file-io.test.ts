import { describe, it, expect, vi, beforeEach } from "vitest"

// =============================================================================
// The artifact pane used to read file contents from the *listing* route
// (`GET /agents/{id}/files?...&path=...`). That route ignores `path` entirely
// (internal/api/proxy_files.go — it builds its IPC call from agent_slug /
// recursive / subdir), so the pane received a JSON directory listing, put it
// in an editor, and offered to save it back over the real file.
//
// This module is the only way the pane is allowed to touch a file. Its job is
// not merely to hit the right URL — it is to make the wrong body unusable:
// a response that did not come from the download route, or that carries a
// JSON media type (which the download route never sends — it hard-codes
// application/octet-stream), is refused instead of returned.
// =============================================================================

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import {
  readArtifactFile,
  saveArtifactFile,
  artifactDownloadUrl,
  artifactSaveUrl,
  ArtifactContentRefused,
} from "../artifact-file-io"

function res(init: {
  ok?: boolean
  status?: number
  body?: string
  contentType?: string | null
  url?: string
}): Response {
  const headers = new Headers()
  if (init.contentType) headers.set("content-type", init.contentType)
  return {
    ok: init.ok ?? true,
    status: init.status ?? 200,
    url: init.url ?? "",
    headers,
    text: async () => init.body ?? "",
  } as unknown as Response
}

const args = { agentId: "a1", workspaceId: "ws-1", path: "workspace/notes.md" }

beforeEach(() => {
  apiFetch.mockReset()
})

describe("artifact URLs", () => {
  it("reads from the download route, not the listing route", () => {
    const url = artifactDownloadUrl("a1", "ws-1", "workspace/a b.md")
    expect(url).toBe(
      "/api/v1/agents/a1/files/download?workspace_id=ws-1&path=workspace%2Fa%20b.md",
    )
    // The listing route with a `path` query is the exact bug being fixed.
    expect(url).not.toMatch(/\/files\?/)
  })

  it("writes to the save route with the same encoding", () => {
    expect(artifactSaveUrl("a1", "ws-1", "workspace/a b.md")).toBe(
      "/api/v1/agents/a1/files/save?workspace_id=ws-1&path=workspace%2Fa%20b.md",
    )
  })
})

describe("readArtifactFile", () => {
  it("requests the download route and returns the exact bytes with their path", async () => {
    apiFetch.mockResolvedValueOnce(
      res({ body: "# notes\nline two\n", contentType: "application/octet-stream" }),
    )

    const file = await readArtifactFile(args)

    expect(String(apiFetch.mock.calls[0][0])).toBe(
      "/api/v1/agents/a1/files/download?workspace_id=ws-1&path=workspace%2Fnotes.md",
    )
    expect(file).toEqual({ path: "workspace/notes.md", text: "# notes\nline two\n" })
  })

  it("refuses a JSON body even when the download route answered", async () => {
    // What the bug produced: a directory listing, 200 OK, application/json.
    const listing = JSON.stringify([
      { name: "notes.md", path: "crew-1/agent/workspace/notes.md", is_dir: false, size: 12 },
    ])
    apiFetch.mockResolvedValueOnce(
      res({
        body: listing,
        contentType: "application/json",
        url: "http://localhost/api/v1/agents/a1/files/download?path=workspace%2Fnotes.md",
      }),
    )

    await expect(readArtifactFile(args)).rejects.toBeInstanceOf(ArtifactContentRefused)
  })

  it("refuses a body that came back from any route other than download", async () => {
    apiFetch.mockResolvedValueOnce(
      res({
        body: "[]",
        contentType: "application/octet-stream",
        // e.g. a redirect that landed on the listing route.
        url: "http://localhost/api/v1/agents/a1/files?workspace_id=ws-1",
      }),
    )

    await expect(readArtifactFile(args)).rejects.toBeInstanceOf(ArtifactContentRefused)
  })

  it("refuses a +json media type too", async () => {
    apiFetch.mockResolvedValueOnce(
      res({ body: "{}", contentType: "application/problem+json; charset=utf-8" }),
    )
    await expect(readArtifactFile(args)).rejects.toBeInstanceOf(ArtifactContentRefused)
  })

  it("still accepts a .json artifact that the download route streamed", async () => {
    apiFetch.mockResolvedValueOnce(
      res({ body: '{"a":1}', contentType: "application/octet-stream" }),
    )
    const file = await readArtifactFile({ ...args, path: "workspace/config.json" })
    expect(file.text).toBe('{"a":1}')
  })

  it("throws on a non-OK response without reading the body as content", async () => {
    apiFetch.mockResolvedValueOnce(res({ ok: false, status: 404, body: "not found" }))
    await expect(readArtifactFile(args)).rejects.toThrow(/404/)
  })
})

describe("saveArtifactFile", () => {
  it("PUTs the edited bytes to the save route for the given path", async () => {
    apiFetch.mockResolvedValueOnce(res({}))

    await saveArtifactFile({ ...args, text: "edited body" })

    const [url, init] = apiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(
      "/api/v1/agents/a1/files/save?workspace_id=ws-1&path=workspace%2Fnotes.md",
    )
    expect(init.method).toBe("PUT")
    expect(init.body).toBe("edited body")
  })

  it("throws when the save is rejected", async () => {
    apiFetch.mockResolvedValueOnce(res({ ok: false, status: 403 }))
    await expect(saveArtifactFile({ ...args, text: "x" })).rejects.toThrow()
  })
})
