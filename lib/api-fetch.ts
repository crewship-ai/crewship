/**
 * Centralized fetch wrapper for /api/* requests.
 *
 * The legacy app called bare `fetch("/api/v1/...")` from a dozen-plus
 * hooks; each ate 401s differently — some silently set loading=false,
 * one set authError=true with no propagation, most just left the panel
 * empty. With short-lived (15min) access tokens, that means the
 * "spinning forever after backend hiccup" UX bug.
 *
 * apiFetch fixes both layers in one place:
 *   1. On 401 it calls /api/auth/token/refresh exactly once and retries.
 *      Concurrent 401s share a single refresh promise (`refreshInflight`)
 *      so a page-load that fans out 5 requests doesn't burn 5 refresh
 *      rotations.
 *   2. If refresh also returns 401 (refresh token expired or session
 *      revoked), it dispatches an `auth:session-expired` event on
 *      window AND broadcasts the same event over a BroadcastChannel
 *      so every other tab can hard-redirect at once. The AuthProvider
 *      listens for this event and does the redirect.
 *
 * Bodies that are streams or one-shot Request objects can't be safely
 * replayed — those go through a single fetch with no retry, mirroring
 * fetch-with-retry.ts.
 */

import { bodyIsReplayable, inputIsReplayable } from "./fetch-with-retry"

const REFRESH_PATH = "/api/auth/token/refresh"
const EVENT_SESSION_EXPIRED = "auth:session-expired"
const CHANNEL_NAME = "crewship.auth"

/** ApiFetchInit deliberately omits `credentials` from the public type:
 *  the wrapper's whole point is that auth cookies always travel with
 *  the request, and a caller-supplied `credentials: "omit"` would
 *  silently disable the refresh-on-401 cycle and the path-scoped
 *  refresh cookie. The TypeScript Omit makes that misuse a compile
 *  error rather than a silent runtime bug. */
export interface ApiFetchInit extends Omit<RequestInit, "credentials"> {
  /** Skip the 401 → refresh path. Used internally by tryRefresh to
   *  avoid infinite recursion if the refresh endpoint itself 401s. */
  skipRefresh?: boolean
}

/** Centralised fetch with refresh-on-401-once + session-expired event. */
export async function apiFetch(input: RequestInfo | URL, init?: ApiFetchInit): Promise<Response> {
  // credentials goes AFTER the spread so callers can't override —
  // {credentials: "include", ...init} would let init.credentials win
  // and silently disable cookie auth.
  const initWithCreds: RequestInit = { ...init, credentials: "include" }
  // Strip our internal-only flag before handing to fetch — `RequestInit`
  // would tolerate the extra field at runtime but it's cleaner not to
  // ship it across the wire.
  if (init?.skipRefresh !== undefined) {
    delete (initWithCreds as ApiFetchInit).skipRefresh
  }

  const res = await fetch(input, initWithCreds)
  if (res.status !== 401 || init?.skipRefresh) return res

  // Reading reason is best-effort. Server emits {"error": "<reason>"}
  // alongside the WWW-Authenticate header. session_revoked / session_invalid
  // are terminal: refresh will not save us, redirect immediately.
  const reason = await peekReason(res)
  if (reason === "session_revoked" || reason === "session_invalid") {
    emitSessionExpired(reason)
    return res
  }

  // session_expired (or unknown): try refresh first. tryRefresh
  // distinguishes three outcomes — see RefreshResult — so we only
  // bounce to /login when the refresh endpoint itself returns 401
  // (the rightful "your session is gone" signal). A 5xx or network
  // failure means we can't tell yet; surface the original 401 to
  // the caller and let the page recover when the backend comes back,
  // rather than evicting valid sessions on every blip.
  const refreshResult = await tryRefresh()
  if (refreshResult === "auth_failed") {
    emitSessionExpired(reason ?? "session_expired")
    return res
  }
  if (refreshResult === "retryable_failed") {
    // The original request hit a 401 with a refreshable reason, then
    // the refresh endpoint itself failed transiently (5xx, network
    // throw, abort). The user's session is presumably still valid;
    // we just couldn't rotate.
    //
    // Returning the original 401 here would lie to the caller —
    // hooks like use-auth and consumers that branch on 401/403 →
    // "logged out" would tear down auth-dependent flows even
    // though the cookies are still good. Synthesize a 503 instead
    // so the same loading-state / retry-button UI that any other
    // backend hiccup would trigger fires here too. The
    // X-Crewship-Refresh-Failed header is the machine-readable
    // signal; the JSON body is for humans tailing logs.
    return new Response(
      JSON.stringify({
        error: "refresh_unavailable",
        detail: "Token refresh endpoint unavailable; original request returned 401 but the session is likely still valid. Retry shortly.",
      }),
      {
        status: 503,
        statusText: "Service Unavailable",
        headers: {
          "Content-Type": "application/json",
          "X-Crewship-Refresh-Failed": "1",
          "Retry-After": "5",
        },
      },
    )
  }

  const replayable = bodyIsReplayable(initWithCreds.body) && inputIsReplayable(input)
  if (!replayable) {
    // Refresh worked, but we can't safely re-send this request.
    // Hand the original 401 back; the caller's next request will
    // use the new cookies and succeed.
    return res
  }

  // Refresh wrote new cookies; the retry inherits them via the same
  // `credentials: include` and lands on a fresh access token.
  return fetch(input, initWithCreds)
}

/** RefreshResult separates the three outcomes that need different
 *  handling. The previous boolean conflated "your refresh is dead"
 *  with "the backend is currently unreachable", which made every
 *  network blip look like a session expiry to the calling code.
 *
 *    ok                — refresh rotated; request can be retried.
 *    auth_failed       — server said 401: refresh token itself is
 *                        invalid/expired/revoked. Caller emits the
 *                        session-expired event.
 *    retryable_failed  — server unreachable, 5xx, or fetch threw.
 *                        Caller surfaces the original 401 without
 *                        bouncing the user to /login.
 */
export type RefreshResult = "ok" | "auth_failed" | "retryable_failed"

let refreshInflight: Promise<RefreshResult> | null = null

/** tryRefresh dedupes concurrent refresh attempts. The first call wins
 *  the network round-trip; followers await its result without firing
 *  their own POST. After settle the promise is cleared so the next
 *  401 (e.g. 15 minutes later) starts fresh.
 *
 *  Cleanup happens synchronously inside the finally block. The earlier
 *  queueMicrotask version was racy under fake-timer test environments:
 *  the microtask could fire AFTER the next test had already started
 *  awaiting a fresh tryRefresh, leaving it observing a stale promise.
 *  Synchronous cleanup is fine — racers awaiting `refreshInflight`
 *  resolved their await before the finally block runs. */
/** REFRESH_TIMEOUT_MS bounds the shared refresh round-trip. Without it,
 *  every concurrent 401 awaits the same `refreshInflight` promise — if
 *  /api/auth/token/refresh hangs (proxy buffering, half-open TCP, …),
 *  the entire app stalls behind that single hung request. AbortController
 *  + setTimeout downgrades a stuck refresh into a retryable failure
 *  (which surfaces as the synthesized 503 in apiFetch), letting callers
 *  see a transport error instead of an indefinite spinner. */
const REFRESH_TIMEOUT_MS = 10_000

export async function tryRefresh(): Promise<RefreshResult> {
  if (refreshInflight) return refreshInflight
  const promise = (async (): Promise<RefreshResult> => {
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), REFRESH_TIMEOUT_MS)
    try {
      const r = await fetch(REFRESH_PATH, {
        method: "POST",
        credentials: "include",
        headers: { Accept: "application/json" },
        signal: controller.signal,
      })
      if (r.ok) return "ok"
      // Only a 401 from the refresh endpoint is "your session is gone".
      // 5xx / 502 / 503 / 504 / 429 / anything else means the server
      // is temporarily unhappy; the user's cookies are still valid in
      // principle, just not actionable until the backend recovers.
      if (r.status === 401) return "auth_failed"
      return "retryable_failed"
    } catch {
      // Network rejection / abort (timeout) / DNS / etc. — transient.
      // Includes the abort our own setTimeout fires when the backend
      // hangs past REFRESH_TIMEOUT_MS.
      return "retryable_failed"
    } finally {
      clearTimeout(timer)
    }
  })()
  refreshInflight = promise
  promise.finally(() => {
    if (refreshInflight === promise) refreshInflight = null
  })
  return promise
}

/** _resetRefreshInflightForTesting is exported only for vitest; production
 *  code must never call it. Clears the in-flight cache between test
 *  cases so a previous test's resolution doesn't pollute the next. */
export function _resetRefreshInflightForTesting(): void {
  refreshInflight = null
}

let authChannel: BroadcastChannel | null = null
function getAuthChannel(): BroadcastChannel | null {
  if (typeof window === "undefined" || typeof BroadcastChannel === "undefined") {
    return null
  }
  if (!authChannel) {
    try {
      authChannel = new BroadcastChannel(CHANNEL_NAME)
    } catch {
      authChannel = null
    }
  }
  return authChannel
}

/** emitSessionExpired fires the local custom event AND posts to the
 *  cross-tab BroadcastChannel. Idempotent across rapid-fire callers
 *  via the in-flight guard inside the AuthProvider listener — this
 *  function itself is fire-and-forget. */
function emitSessionExpired(reason: string): void {
  if (typeof window === "undefined") return
  window.dispatchEvent(new CustomEvent(EVENT_SESSION_EXPIRED, { detail: { reason } }))
  getAuthChannel()?.postMessage({ type: "session-expired", reason })
}

/** broadcastSignOut is what AuthProvider.signOut() calls so other tabs
 *  also drop their UI state. Different event from session-expired — a
 *  user-initiated logout shouldn't show the "your session expired"
 *  toast in the other tab, just a clean redirect. */
export function broadcastSignOut(): void {
  getAuthChannel()?.postMessage({ type: "signout" })
}

/** broadcastSessionExpired is the public mirror of emitSessionExpired —
 *  exported so other auth-failure detectors (use-websocket close 4401,
 *  in-stream session_revoked frames) reach the same propagation path
 *  as HTTP-detected expiry: local CustomEvent + cross-tab BroadcastChannel.
 *
 *  Without this, a WS-only failure would only redirect the tab whose
 *  socket died; sibling tabs would keep showing stale UI until they
 *  themselves tried an HTTP request. */
export function broadcastSessionExpired(reason: string): void {
  emitSessionExpired(reason)
}

export const AUTH_EVENT = EVENT_SESSION_EXPIRED
export const AUTH_CHANNEL = CHANNEL_NAME

/** peekReason reads the JSON error body without consuming the original
 *  Response (we may need to return it). It clones first so the caller
 *  can still call .json()/.text() on the original. */
async function peekReason(res: Response): Promise<string | null> {
  try {
    const clone = res.clone()
    const data = await clone.json()
    if (typeof data?.error === "string") return data.error
    return null
  } catch {
    return null
  }
}
