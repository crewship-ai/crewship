/**
 * The server calls of the embedded workbook editor host, in one place.
 *
 * The bridge (`./bridge`) talks to the server only through `WorkbookTransport`; this module adds
 * what the host component needs around it: the runtime flag, opening and closing the frame
 * session, the editor frame URL and a cheap read of the head revision. Everything is bound to
 * one workspace and one Page and goes through `createEditorFetch` unless a test passes its own fetch.
 */

import { apiFetch, tryRefresh, type ApiFetchInit, type RefreshResult } from "@/lib/api-fetch"
import { getAuthMode } from "@/lib/server-base"
import { createWorkbookHttpTransport, isBundleDigest, parseWorkbookSession, type WorkbookSession, type WorkbookTransport } from "./bridge"

/** Workspace feature flag that switches a workbook Page from the read-only preview to the editor. */
export const WORKBOOK_EDITOR_FLAG = "gridcraft_editor"

type Fetch = (url: string, init?: RequestInit) => Promise<Response>

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0

export type OpenSessionResult =
  | { ok: true; session: WorkbookSession }
  /**
   * `status`: the HTTP status, or 0 for a network failure or an unreadable session (also one with
   * no bundle pin, which the server sends until an editor origin is configured). `retryAfterMs`
   * comes with `429` (the per-person session cap): when a session is likely to be free again,
   * from the server's Retry-After, at most `WORKBOOK_SESSION_REOPEN_MAX_MS`.
   */
  | { ok: false; status: number; refusal?: EditorRefusal; retryAfterMs?: number }

export interface WorkbookHead { rev: number; generation: number }

/** `error` of the `503` every live-editing endpoint answers while the operator switch or the workspace flag is off. */
export const EDITOR_DISABLED_ERROR = "gridcraft_editor_disabled"
/**
 * `error` of the `401` that servers before `editor_session_expired` answered for an expired or
 * closed editor session; still read as an expired editor session.
 */
export const SESSION_EXPIRED_ERROR = "session_expired"
/**
 * `error` the server answers for an editor session that is over (expired, closed, opened for an
 * older generation or with another credential of the same person), whatever the status. It is not
 * `session_expired`, which the whole app reads as "the sign-in expired".
 */
export const EDITOR_SESSION_EXPIRED_ERROR = "editor_session_expired"
/** Realtime event on `page:{pageId}` naming a confirmed workbook revision (no cell content). */
export const WORKBOOK_COMMITTED_EVENT = "workbook.committed"
/**
 * Further attempts to open a session after `429` (the per-person session cap), each after
 * Retry-After, while that wait is at most `MAX_RETRY_AFTER_MS`. A longer wait is returned at once.
 */
export const WORKBOOK_SESSION_OPEN_RETRIES = 2
/** Longest Retry-After reported for a `429` on open: the server's idle expiry of a session. */
export const WORKBOOK_SESSION_REOPEN_MAX_MS = 15 * 60_000
const DEFAULT_RETRY_AFTER_MS = 1000
const MAX_RETRY_AFTER_MS = 10_000
/** `error` of the `403` for an edit whose recalculated effects could not be confirmed. */
export const EFFECT_OUT_OF_MANDATE_ERROR = "effect_out_of_mandate"

/**
 * A server answer that ends the editor session for good, as opposed to one worth retrying:
 * - `disabled`: `503 gridcraft_editor_disabled`; the editor was turned off while open (M2B §5.7).
 *   Any other `503` is the workbook runtime being unavailable, which is transient.
 * - `expired`: `editor_session_expired` (any status), or `401 session_expired` as servers before
 *   that code answer it; this session is over, a new one may be opened.
 * - `access`: any other `401`, `403` or `404`; the person lost access (or never had it).
 *
 * `403 effect_out_of_mandate` is none of these: it refuses one edit whose recalculation could not
 * be confirmed, and access is intact.
 */
export type EditorRefusal = "disabled" | "expired" | "access"

export function editorRefusal(response: { status: number; body: unknown }): EditorRefusal | null {
  const { status, body } = response
  const error = object(body) && typeof body.error === "string" ? body.error : ""
  if (error === EDITOR_SESSION_EXPIRED_ERROR && status >= 400) return "expired"
  if (status === 503) return error === EDITOR_DISABLED_ERROR ? "disabled" : null
  if (status === 401 && error === SESSION_EXPIRED_ERROR) return "expired"
  if (status === 403 && error === EFFECT_OUT_OF_MANDATE_ERROR) return null
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


/**
 * The fetch of every editor request. It never lets `apiFetch` run its app-wide 401 handling (token
 * refresh with replay on the web, sign-out in the desktop shell), because an editor session ending
 * is not the sign-in ending:
 * - `editor_session_expired` goes straight back to the bridge, which opens a new editor session;
 * - any other refreshable 401 in cookie mode refreshes the access token here and replays the request
 *   once (the editor may be the only thing the person is using); a refresh that could not be done is
 *   a transient 503, and one the server refused leaves the 401 to the bridge;
 * - in bearer mode, or for a revoked sign-in, the 401 is returned as it is.
 */
export function createEditorFetch(deps: {
  apiFetch: (url: string, init?: ApiFetchInit) => Promise<Response>
  tryRefresh: () => Promise<RefreshResult>
  bearer: () => boolean
}): Fetch {
  return async (url, init) => {
    const call = () => deps.apiFetch(url, { ...(init as ApiFetchInit), skipRefresh: true })
    const first = await call()
    if (first.status !== 401) return first
    const body = await first.clone().json().catch(() => null)
    const error = object(body) && typeof body.error === "string" ? body.error : ""
    if (error === EDITOR_SESSION_EXPIRED_ERROR || deps.bearer() || error === "session_revoked" || error === "session_invalid") return first
    const refreshed = await deps.tryRefresh()
    if (refreshed === "ok") return call()
    if (refreshed === "retryable_failed") return new Response(JSON.stringify({ error: "refresh_unavailable" }), { status: 503, headers: { "content-type": "application/json", "retry-after": "5" } })
    return first
  }
}

/** The revision a `workbook.committed` push names for this Page, or null. */
export function parseCommittedPush(payload: unknown, pageId: string): WorkbookHead | null {
  if (!object(payload) || payload.page_id !== pageId || !count(payload.rev) || !count(payload.generation)) return null
  return { rev: payload.rev, generation: payload.generation }
}

function retryAfterMs(response: Response): number {
  const seconds = Number(response.headers.get("retry-after"))
  return Number.isFinite(seconds) && seconds > 0 ? Math.min(seconds * 1000, WORKBOOK_SESSION_REOPEN_MAX_MS) : DEFAULT_RETRY_AFTER_MS
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
/** Where the server serves the editor on the Studio origin when no editor origin is configured. */
export const SAME_HOST_PREFIX = "/_editor"

/**
 * The `src` of the editor frame, or null when the server's `editor_url` is not one the host may
 * frame: HTTPS (plain HTTP only on loopback outside production builds), no credentials, and the
 * session's own bundle pin in the path: `/b/<bundleDigest>/index.html` on a dedicated editor
 * origin, or, on the host's own origin, only `/_editor/b/<bundleDigest>/index.html` (the zero-config
 * default), which the server sandboxes and which is the only path in Studio's `frame-src`. No other
 * path of the host's origin is ever framed. The pin is plain lowercase hex
 * SHA-256 of the bundle manifest, as the editor origin serves it and the editor's hello names it;
 * an empty pin (no editor origin configured) or any other spelling frames nothing. Whatever query
 * the server sent is replaced: the frame gets the bridge mode and the host origin, nothing else.
 */
export function editorFrameSrc(
  editorUrl: string | undefined,
  bundleDigest: string,
  hostOrigin: string,
  /** Plain HTTP on loopback is for local development and tests; a production build never allows it. */
  { allowLoopbackHttp = process.env.NODE_ENV !== "production" }: { allowLoopbackHttp?: boolean } = {},
): string | null {
  if (!editorUrl || !isBundleDigest(bundleDigest)) return null
  let url: URL
  try { url = new URL(editorUrl) } catch { return null }
  if (url.username || url.password) return null
  if (url.protocol !== "https:" && !(allowLoopbackHttp && url.protocol === "http:" && LOOPBACK.has(url.hostname))) return null
  const path = url.origin === hostOrigin ? `${SAME_HOST_PREFIX}/b/${bundleDigest}/index.html` : `/b/${bundleDigest}/index.html`
  if (url.pathname !== path) return null
  url.search = ""
  url.hash = ""
  url.searchParams.set("host", "authority")
  url.searchParams.set("parent_origin", hostOrigin)
  url.searchParams.set("v", "1")
  return url.toString()
}

export function createWorkbookEditorClient(options: { workspaceId: string; pageId: string; fetch?: Fetch; sleep?: (ms: number) => Promise<void> }): WorkbookEditorClient {
  const send: Fetch = options.fetch ?? createEditorFetch({ apiFetch: (url, init) => apiFetch(url, init), tryRefresh: () => tryRefresh(), bearer: () => getAuthMode() === "bearer" })
  const sleep = options.sleep ?? (ms => new Promise<void>(resolve => setTimeout(resolve, ms)))
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
      for (let attempt = 0; ; attempt++) {
        try {
          response = await postSession({ protocol: 1 })
        } catch {
          return { ok: false, status: 0 }
        }
        // The per-person session cap: wait as told, a bounded number of times, and only briefly;
        // a longer wait goes back to the host, which offers to reopen and schedules it.
        if (response.status !== 429 || attempt >= WORKBOOK_SESSION_OPEN_RETRIES || retryAfterMs(response) > MAX_RETRY_AFTER_MS) break
        await sleep(retryAfterMs(response))
      }
      if (response.status === 429) return { ok: false, status: 429, retryAfterMs: retryAfterMs(response) }
      if (!response.ok) {
        const refusal = editorRefusal({ status: response.status, body: await read(response) })
        return refusal ? { ok: false, status: response.status, refusal } : { ok: false, status: response.status }
      }
      const body = await read(response)
      const session = parseWorkbookSession(body)
      if (session) return { ok: true, session }
      // Opened but unusable (no bundle pin before an editor origin is configured, an unreadable
      // answer): close it, so it does not hold one of the person's sessions until it expires.
      if (object(body) && typeof body.session_id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(body.session_id)) {
        void send(`${base}/sessions/${encodeURIComponent(body.session_id)}?${workspace}`, { method: "DELETE", cache: "no-store" }).catch(() => {})
      }
      return { ok: false, status: 0 }
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
