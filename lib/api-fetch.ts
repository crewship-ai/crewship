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

export interface ApiFetchInit extends RequestInit {
  /** Skip the 401 → refresh path. Used internally by tryRefresh to
   *  avoid infinite recursion if the refresh endpoint itself 401s. */
  skipRefresh?: boolean
}

/** Centralised fetch with refresh-on-401-once + session-expired event. */
export async function apiFetch(input: RequestInfo | URL, init?: ApiFetchInit): Promise<Response> {
  const initWithCreds: RequestInit = { credentials: "include", ...init }
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

  // session_expired (or unknown): try refresh once. If the body / input
  // wasn't safely replayable we can't reissue the request, so propagate
  // the original 401 — caller will hit the same response shape it would
  // have got without the wrapper.
  const replayable = bodyIsReplayable(initWithCreds.body) && inputIsReplayable(input)
  if (!replayable) {
    emitSessionExpired(reason ?? "session_expired")
    return res
  }

  const refreshOk = await tryRefresh()
  if (!refreshOk) {
    emitSessionExpired(reason ?? "session_expired")
    return res
  }

  // Refresh wrote new cookies; the retry inherits them via the same
  // `credentials: include` and lands on a fresh access token.
  return fetch(input, initWithCreds)
}

let refreshInflight: Promise<boolean> | null = null

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
export async function tryRefresh(): Promise<boolean> {
  if (refreshInflight) return refreshInflight
  const promise = (async () => {
    try {
      const r = await fetch(REFRESH_PATH, {
        method: "POST",
        credentials: "include",
        headers: { Accept: "application/json" },
      })
      return r.ok
    } catch {
      return false
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
