/**
 * The server calls of the embedded workbook editor host, in one place.
 *
 * The bridge (`./bridge`) talks to the server only through `WorkbookTransport`; this module adds
 * what the host component needs around it: the runtime flag, opening and closing the frame
 * session, the editor frame URL and a cheap read of the head revision. Everything is bound to
 * one workspace and one Page and goes through `apiFetch` unless a test passes its own fetch.
 */

import { apiFetch } from "@/lib/api-fetch"
import { createWorkbookHttpTransport, parseWorkbookSession, type WorkbookSession, type WorkbookTransport } from "./bridge"

/** Workspace feature flag that switches a workbook Page from the read-only preview to the editor. */
export const WORKBOOK_EDITOR_FLAG = "gridcraft_editor"

type Fetch = (url: string, init?: RequestInit) => Promise<Response>

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0

export type OpenSessionResult =
  | { ok: true; session: WorkbookSession }
  /** `status`: the HTTP status, or 0 for a network failure or an unreadable session. */
  | { ok: false; status: number }

export interface WorkbookHead { rev: number; generation: number }

/** `error` of the `503` every live-editing endpoint answers while the operator switch or the workspace flag is off. */
export const EDITOR_DISABLED_ERROR = "gridcraft_editor_disabled"
/** `error` of the `401` for an expired or closed session, or one opened for an older generation. */
export const SESSION_EXPIRED_ERROR = "session_expired"

/**
 * A server answer that ends the editor session for good, as opposed to one worth retrying:
 * - `disabled`: `503 gridcraft_editor_disabled`; the editor was turned off while open (M2B §5.7).
 *   Any other `503` is the workbook runtime being unavailable, which is transient.
 * - `expired`: `401 session_expired`; this session is over, a new one may be opened.
 * - `access`: any other `401`, `403` or `404`; the person lost access (or never had it).
 */
export type EditorRefusal = "disabled" | "expired" | "access"

export function editorRefusal(response: { status: number; body: unknown }): EditorRefusal | null {
  const { status, body } = response
  const error = object(body) && typeof body.error === "string" ? body.error : ""
  if (status === 503) return error === EDITOR_DISABLED_ERROR ? "disabled" : null
  if (status === 401 && error === SESSION_EXPIRED_ERROR) return "expired"
  if (status === 401 || status === 403 || status === 404) return "access"
  return null
}

/**
 * What a recheck of the open session found: `live` (same bundle), `revoked` (the server names
 * another bundle, or none: the frame runs code that is no longer qualified), a refusal, or
 * `unknown` when the server could not be asked (network, an unreadable answer, a transient error).
 */
export type SessionCheck = "live" | "revoked" | "unknown" | EditorRefusal

export interface WorkbookEditorClient {
  /** The `gridcraft_editor` flag for this workspace; false on any failure. */
  editorEnabled(): Promise<boolean>
  /** `POST …/workbook/sessions`. */
  openSession(): Promise<OpenSessionResult>
  /** The bridge transport for one open session. */
  transport(session: WorkbookSession): WorkbookTransport
  /** Head revision and generation from `GET …/workbook/commits` (one entry), or the status that refused it. */
  head(since: number): Promise<{ ok: true; head: WorkbookHead } | { ok: false; status: number; refusal?: EditorRefusal }>
  /**
   * Renews the open session (`POST …/workbook/sessions` with its `session_id`, the heartbeat).
   * The server rechecks access, the operator switch and the workspace flag, and names the
   * qualified bundle again, so this is how the host learns the editor was turned off or its
   * bundle revoked while the frame was open.
   */
  checkSession(session: WorkbookSession): Promise<SessionCheck>
}


/** Effective value of one flag in `GET /api/v1/feature-flags`: the workspace override wins. */
export function flagEnabled(body: unknown, key: string): boolean {
  if (!Array.isArray(body)) return false
  const flag = body.find(item => object(item) && item.key === key)
  if (!object(flag)) return false
  if (typeof flag.override_enabled === "boolean") return flag.override_enabled
  return flag.enabled === true
}

const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]"])
const BUNDLE_PATH = /^\/b\/[0-9a-f]{64}\/index\.html$/

/**
 * The `src` of the editor frame, or null when the server's `editor_url` is not one the host may
 * frame: HTTPS (plain HTTP only on loopback outside production builds), no credentials, a pinned bundle path, and never the
 * host's own origin (an editor in the Studio origin could lift its own sandbox). Whatever query
 * the server sent is replaced: the frame gets the bridge mode and the host origin, nothing else.
 */
export function editorFrameSrc(
  editorUrl: string | undefined,
  hostOrigin: string,
  /** Plain HTTP on loopback is for local development and tests; a production build never allows it. */
  { allowLoopbackHttp = process.env.NODE_ENV !== "production" }: { allowLoopbackHttp?: boolean } = {},
): string | null {
  if (!editorUrl) return null
  let url: URL
  try { url = new URL(editorUrl) } catch { return null }
  if (url.username || url.password) return null
  if (url.protocol !== "https:" && !(allowLoopbackHttp && url.protocol === "http:" && LOOPBACK.has(url.hostname))) return null
  if (url.origin === hostOrigin || !BUNDLE_PATH.test(url.pathname)) return null
  url.search = ""
  url.hash = ""
  url.searchParams.set("host", "authority")
  url.searchParams.set("parent_origin", hostOrigin)
  url.searchParams.set("v", "1")
  return url.toString()
}

export function createWorkbookEditorClient(options: { workspaceId: string; pageId: string; fetch?: Fetch }): WorkbookEditorClient {
  const send: Fetch = options.fetch ?? ((url, init) => apiFetch(url, init))
  const workspace = new URLSearchParams({ workspace_id: options.workspaceId })
  const base = `/api/v1/pages/${encodeURIComponent("@" + options.pageId)}/workbook`
  const read = async (response: Response) => response.json().catch(() => null)
  const postSession = (body: object) => send(`${base}/sessions?${workspace}`, { method: "POST", cache: "no-store", headers: { "content-type": "application/json" }, body: JSON.stringify(body) })

  return {
    async editorEnabled() {
      try {
        const response = await send(`/api/v1/feature-flags?${workspace}`, { cache: "no-store" })
        return response.ok && flagEnabled(await read(response), WORKBOOK_EDITOR_FLAG)
      } catch {
        return false
      }
    },
    async openSession() {
      let response: Response
      try {
        response = await postSession({ protocol: 1 })
      } catch {
        return { ok: false, status: 0 }
      }
      if (!response.ok) return { ok: false, status: response.status }
      const session = parseWorkbookSession(await read(response))
      return session ? { ok: true, session } : { ok: false, status: 0 }
    },
    transport(session) {
      return createWorkbookHttpTransport({ workspaceId: options.workspaceId, pageId: options.pageId, sessionId: session.session_id, fetch: send })
    },
    async head(since) {
      let response: Response
      try {
        response = await send(`${base}/commits?${new URLSearchParams({ workspace_id: options.workspaceId, since: String(since), limit: "1" })}`, { cache: "no-store" })
      } catch {
        return { ok: false, status: 0 }
      }
      const body = await read(response)
      if (!response.ok) {
        const refusal = editorRefusal({ status: response.status, body })
        return refusal ? { ok: false, status: response.status, refusal } : { ok: false, status: response.status }
      }
      if (!object(body) || !count(body.head_rev) || !count(body.generation)) return { ok: false, status: 0 }
      return { ok: true, head: { rev: body.head_rev, generation: body.generation } }
    },
    async checkSession(session) {
      let response: Response
      try {
        response = await postSession({ session_id: session.session_id, protocol: 1 })
      } catch {
        return "unknown"
      }
      const body = await read(response)
      if (!response.ok) return editorRefusal({ status: response.status, body }) ?? "unknown"
      if (!object(body) || body.session_id !== session.session_id || typeof body.bundle_digest !== "string") return "unknown"
      return body.bundle_digest === session.bundle_digest ? "live" : "revoked"
    },
  }
}
