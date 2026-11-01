import { apiFetch } from "@/lib/api-fetch"
import type {
  ArtifactComment, ArtifactCommentReply, ArtifactCommentStatus, ArtifactErrorCode, ArtifactFile, ArtifactManifest,
  ArtifactProjectSnapshot, ArtifactQuota, ArtifactVersionInfo, CreateArtifactCommentBody,
} from "./types"

/**
 * Client for the artifact project routes (pages/{slug}/artifact and its restore, share and comments).
 * Unlike the chat context client, callers need the reason a write failed (a
 * 409 shows a conflict banner, a 413 a quota notice), so failures throw a
 * typed ArtifactRequestError instead of collapsing into one status.
 */
export class ArtifactRequestError extends Error {
  status: number
  code: ArtifactErrorCode
  constructor(status: number, code: ArtifactErrorCode, message: string) {
    super(message)
    this.name = "ArtifactRequestError"
    this.status = status
    this.code = code
  }
}

const CODES: ArtifactErrorCode[] = ["artifact_version_conflict", "artifact_anchor_mismatch", "artifact_history_quota", "pages_limit", "forbidden", "not_found", "invalid", "unavailable"]
const GENERIC: Record<ArtifactErrorCode, string> = {
  artifact_version_conflict: "A newer version exists. Reload to see it.",
  artifact_anchor_mismatch: "The selected element changed. Pick it again.",
  artifact_history_quota: "Version history is full.",
  pages_limit: "This workspace has reached its page limit. Nothing was created.",
  forbidden: "You do not have access to this artifact.",
  not_found: "This artifact is not available.",
  invalid: "The request was not accepted.",
  unavailable: "The artifact service is unavailable. Try again.",
}

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const str = (value: unknown): value is string => typeof value === "string"
const int = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0

function codeFor(status: number, body: unknown): ArtifactErrorCode {
  if (object(body) && CODES.includes(body.code as ArtifactErrorCode)) return body.code as ArtifactErrorCode
  if (status === 403 || status === 401) return "forbidden"
  if (status === 404) return "not_found"
  if (status === 400 || status === 422) return "invalid"
  return "unavailable"
}

const pagePath = (slug: string, rest: string, params: Record<string, string>) =>
  `/api/v1/pages/${encodeURIComponent(slug)}/artifact${rest}?${new URLSearchParams(params)}`

/** Shared by the Craft lease client: same error envelope, same typed failure. */
export async function artifactRequest<T>(url: string, parse: (body: unknown) => T | null, init: { method?: "GET" | "POST"; body?: unknown; signal?: AbortSignal; headers?: Record<string, string> } = {}): Promise<T> {
  let response: Response
  try {
    response = await apiFetch(url, init.method === "POST"
      ? { method: "POST", headers: { ...init.headers, "content-type": "application/json" }, body: JSON.stringify(init.body), signal: init.signal }
      : { method: "GET", cache: "no-store", ...(init.headers ? { headers: init.headers } : {}), signal: init.signal })
  } catch (error) {
    if ((error as { name?: string })?.name === "AbortError") throw error
    throw new ArtifactRequestError(0, "unavailable", GENERIC.unavailable)
  }
  const body: unknown = await response.json().catch(() => undefined)
  if (!response.ok) {
    const code = codeFor(response.status, body)
    // Server messages are short, user-facing strings; anything else falls back to our copy.
    const message = object(body) && str(body.error) && body.error.trim() && body.error.length <= 300 ? body.error : GENERIC[code]
    throw new ArtifactRequestError(response.status, code, message)
  }
  const data = parse(body)
  if (data === null) throw new ArtifactRequestError(response.status, "invalid", "The server sent an unexpected answer.")
  return data
}

/* ---- snapshot parsing ---- */

const STATUSES: ArtifactCommentStatus[] = ["pending", "working", "answered", "needs_clarification"]

function parseFile(raw: unknown): ArtifactFile | null {
  return object(raw) && str(raw.path) && raw.path && str(raw.content) && str(raw.media_type) ? { path: raw.path, content: raw.content, media_type: raw.media_type } : null
}

const entry = (raw: Record<string, unknown>) => str(raw.entrypoint) ? raw.entrypoint : str(raw.entry_point) ? raw.entry_point : undefined
const restoredFrom = (raw: Record<string, unknown>) => int(raw.restored_from) || raw.restored_from === null ? { restored_from: raw.restored_from as number | null } : {}

function parseManifest(raw: unknown): ArtifactManifest | null {
  if (!object(raw) || !int(raw.version) || !Array.isArray(raw.files)) return null
  const entrypoint = entry(raw)
  if (!entrypoint) return null
  const files = raw.files.map(parseFile)
  if (files.some(file => file === null)) return null
  return {
    version: raw.version, entrypoint, files: files as ArtifactFile[],
    ...(str(raw.created_at) ? { created_at: raw.created_at } : {}),
    ...(str(raw.message) ? { message: raw.message } : {}),
    ...restoredFrom(raw),
  }
}

function parseVersion(raw: unknown): ArtifactVersionInfo | null {
  if (!object(raw) || !int(raw.version)) return null
  const entryPoint = entry(raw)
  return {
    version: raw.version,
    ...(entryPoint ? { entry_point: entryPoint } : {}),
    ...(str(raw.message) ? { message: raw.message } : {}),
    ...(str(raw.actor) ? { actor: raw.actor } : {}),
    ...(str(raw.created_at) ? { created_at: raw.created_at } : {}),
    ...restoredFrom(raw),
  }
}

function parseReply(raw: unknown): ArtifactCommentReply | null {
  if (!object(raw) || !str(raw.text) || !int(raw.version)) return null
  return { text: raw.text, version: raw.version, ...(str(raw.author) ? { author: raw.author } : {}), ...(str(raw.created_at) ? { created_at: raw.created_at } : {}) }
}

export function parseArtifactComment(raw: unknown): ArtifactComment | null {
  if (!object(raw) || !str(raw.id) || !raw.id || !int(raw.version) || !str(raw.path) || !str(raw.text)) return null
  const replies = Array.isArray(raw.replies) ? raw.replies.map(parseReply).filter((reply): reply is ArtifactCommentReply => reply !== null) : undefined
  return {
    id: raw.id, version: raw.version, path: raw.path,
    selector: str(raw.selector) ? raw.selector : "", quote: str(raw.quote) ? raw.quote : "", text: raw.text,
    ...(str(raw.author) ? { author: raw.author } : {}),
    ...(str(raw.task_id) ? { task_id: raw.task_id } : {}),
    ...(typeof raw.delivery_retryable === "boolean" ? { delivery_retryable: raw.delivery_retryable } : {}),
    ...(str(raw.created_at) ? { created_at: raw.created_at } : {}),
    ...(STATUSES.includes(raw.status as ArtifactCommentStatus) ? { status: raw.status as ArtifactCommentStatus } : {}),
    ...(int(raw.answered_version) ? { answered_version: raw.answered_version } : {}),
    ...(replies ? { replies } : {}),
  }
}

function parseQuota(raw: unknown): ArtifactQuota | undefined {
  if (!object(raw) || !int(raw.used_bytes) || !int(raw.limit_bytes) || typeof raw.warning !== "boolean") return undefined
  return { used_bytes: raw.used_bytes, limit_bytes: raw.limit_bytes, warning: raw.warning, ...(str(raw.retention) ? { retention: raw.retention } : {}) }
}

/** Validates the required fields and drops any optional one with the wrong type, so the UI never renders a guess. */
export function parseArtifactProject(raw: unknown): ArtifactProjectSnapshot | null {
  if (!object(raw) || !str(raw.page_id) || !raw.page_id || typeof raw.can_write !== "boolean" || !int(raw.latest_version) || !int(raw.shared_version)) return null
  const manifest = parseManifest(raw.manifest)
  if (!manifest) return null
  const origin = raw.origin === null ? null : object(raw.origin) && str(raw.origin.chat_id) && str(raw.origin.agent_id) ? { chat_id: raw.origin.chat_id, agent_id: raw.origin.agent_id } : undefined
  const quota = parseQuota(raw.quota)
  return {
    page_id: raw.page_id,
    ...(raw.artifact_kind === "slides" ? { artifact_kind: "slides" as const } : {}),
    ...(str(raw.name) && raw.name.trim() ? { name: raw.name } : {}),
    ...(str(raw.slug) && raw.slug ? { slug: raw.slug } : {}),
    ...(origin !== undefined ? { origin } : {}),
    ...(typeof raw.enabled === "boolean" ? { enabled: raw.enabled } : {}),
    can_write: raw.can_write,
    ...Object.fromEntries(["can_share","can_restore","can_send_to_agent","can_comment"].filter(key=>typeof raw[key]==="boolean").map(key=>[key,raw[key]])),
    latest_version: raw.latest_version,
    shared_version: raw.shared_version,
    manifest,
    versions: Array.isArray(raw.versions) ? raw.versions.map(parseVersion).filter((v): v is ArtifactVersionInfo => v !== null) : [],
    ...(quota ? { quota } : {}),
    comments: Array.isArray(raw.comments) ? raw.comments.map(parseArtifactComment).filter((c): c is ArtifactComment => c !== null) : [],
  }
}

/* ---- calls ---- */

/** A missing route or any failure reads as "off": the surface stays hidden rather than erroring. */
export async function fetchArtifactAvailability(workspaceId: string, signal?: AbortSignal): Promise<boolean> {
  try {
    return await artifactRequest(`/api/v1/pages/artifacts/availability?${new URLSearchParams({ workspace_id: workspaceId })}`, body => object(body) && body.enabled === true, { signal })
  } catch (error) {
    if ((error as { name?: string })?.name === "AbortError") throw error
    return false
  }
}

export function fetchArtifactProject(workspaceId: string, slug: string, version?: number, signal?: AbortSignal): Promise<ArtifactProjectSnapshot> {
  const params: Record<string, string> = { workspace_id: workspaceId }
  if (version !== undefined) params.version = String(version)
  return artifactRequest(pagePath(slug, "", params), parseArtifactProject, { signal })
}

export function restoreArtifactVersion(workspaceId: string, slug: string, body: { base_version: number; version: number }): Promise<{ version: number; actor?: string }> {
  return artifactRequest(pagePath(slug, "/restore", { workspace_id: workspaceId }), raw => object(raw) && int(raw.version) ? { version: raw.version, ...(str(raw.actor) ? { actor: raw.actor } : {}) } : null, { method: "POST", body })
}

export function shareArtifactVersion(workspaceId: string, slug: string, body: { base_version: number; version: number }): Promise<{ shared_version: number; url?: string }> {
  return artifactRequest(pagePath(slug, "/share", { workspace_id: workspaceId }), raw => object(raw) && int(raw.shared_version) ? { shared_version: raw.shared_version, ...(str(raw.url) ? { url: raw.url } : {}) } : null, { method: "POST", body })
}

export function createArtifactComment(workspaceId: string, slug: string, body: CreateArtifactCommentBody): Promise<ArtifactComment> {
  return artifactRequest(pagePath(slug, "/comments", { workspace_id: workspaceId }), parseArtifactComment, { method: "POST", body })
}
