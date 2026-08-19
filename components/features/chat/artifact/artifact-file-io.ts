"use client"

import { apiFetch } from "@/lib/api-fetch"

/**
 * The artifact pane's only door to agent files.
 *
 * It exists because the pane used to open files through the *listing* route:
 *
 *     GET /api/v1/agents/{id}/files?workspace_id=…&path=<file path>
 *
 * `AgentFiles` (internal/api/proxy_files.go) builds its IPC call from
 * `agent_slug`, `recursive` and `subdir` — it never reads `path`. So the pane
 * received a JSON directory listing, rendered it as the file's text, and its
 * Save button would write that JSON over the real file. File bytes come from
 * a different route entirely: `GET /api/v1/agents/{id}/files/download?path=…`.
 *
 * Pointing at the right URL is only half of it. The failure mode was silent —
 * a 200 with a plausible-looking body — so this module also refuses anything
 * that is not demonstrably a file body, on two independent grounds:
 *
 *   1. the response must have come back from the download route, and
 *   2. it must not carry a JSON media type.
 *
 * (2) is safe for `.json` artifacts: `AgentFileDownload` hard-codes
 * `Content-Type: application/octet-stream` for every file it streams, while
 * every listing and every error envelope this API produces is JSON. A refusal
 * is loud — the editor stays empty rather than filling with a body nobody
 * asked for.
 */

const DOWNLOAD_ROUTE = "/files/download"

/** Thrown when a 200 response is not the file that was requested. */
export class ArtifactContentRefused extends Error {
  constructor(reason: string) {
    super(reason)
    this.name = "ArtifactContentRefused"
  }
}

export interface ArtifactFile {
  /** The path the bytes were read from. Callers bind bytes to path with it. */
  path: string
  text: string
}

export function artifactDownloadUrl(
  agentId: string,
  workspaceId: string,
  path: string,
): string {
  return `/api/v1/agents/${encodeURIComponent(agentId)}/files/download?workspace_id=${encodeURIComponent(workspaceId)}&path=${encodeURIComponent(path)}`
}

export function artifactSaveUrl(
  agentId: string,
  workspaceId: string,
  path: string,
): string {
  return `/api/v1/agents/${encodeURIComponent(agentId)}/files/save?workspace_id=${encodeURIComponent(workspaceId)}&path=${encodeURIComponent(path)}`
}

/** True for application/json, text/json and any `+json` structured suffix. */
function isJsonMediaType(contentType: string): boolean {
  const essence = contentType.split(";")[0]!.trim().toLowerCase()
  return (
    essence === "application/json" ||
    essence === "text/json" ||
    essence.endsWith("+json")
  )
}

function pathnameOf(url: string): string | null {
  try {
    // Relative URLs are the norm here; the base is only for parsing.
    return new URL(url, "http://artifact.invalid").pathname
  } catch {
    return null
  }
}

/**
 * assertFileResponse is the guard. It runs before the body is read as text so
 * a non-file body never becomes editable content.
 */
function assertFileResponse(res: Response, requestedUrl: string): void {
  // `Response.url` is empty for some synthesized/mocked responses; the URL we
  // asked for is then the best available evidence. When it IS populated it is
  // the stronger signal, because it reflects redirects.
  const answered = typeof res.url === "string" && res.url !== "" ? res.url : requestedUrl
  const pathname = pathnameOf(answered)
  if (pathname === null || !pathname.endsWith(DOWNLOAD_ROUTE)) {
    throw new ArtifactContentRefused(
      `refusing artifact body: answered by ${pathname ?? answered}, not the file download route`,
    )
  }
  const contentType = res.headers?.get?.("content-type") ?? null
  if (contentType && isJsonMediaType(contentType)) {
    throw new ArtifactContentRefused(
      `refusing artifact body: ${contentType} is not a file stream (the file listing route answers with JSON)`,
    )
  }
}

export async function readArtifactFile(opts: {
  agentId: string
  workspaceId: string
  path: string
  signal?: AbortSignal
}): Promise<ArtifactFile> {
  const url = artifactDownloadUrl(opts.agentId, opts.workspaceId, opts.path)
  const res = await apiFetch(url, { signal: opts.signal })
  if (!res.ok) throw new Error(`Failed to load: HTTP ${res.status}`)
  assertFileResponse(res, url)
  return { path: opts.path, text: await res.text() }
}

export async function saveArtifactFile(opts: {
  agentId: string
  workspaceId: string
  path: string
  text: string
}): Promise<void> {
  const res = await apiFetch(
    artifactSaveUrl(opts.agentId, opts.workspaceId, opts.path),
    {
      method: "PUT",
      headers: { "Content-Type": "text/plain" },
      body: opts.text,
    },
  )
  if (!res.ok) throw new Error(`Save failed: HTTP ${res.status}`)
}
