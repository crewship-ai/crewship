// Keeps an API context of a long PRD row signed in.
//
// The access cookie lives AccessTokenTTL (15 min, internal/auth/jwt.go) and a
// login's storage state is a snapshot: an APIRequestContext built from it
// loses its access cookie after 15 minutes and every call answers 401
// (G3 RC-3 e3f048555, A.craft.session.hour-sheet: "read admission" 401 after
// 15.6 min while the browser, which refreshes by itself, kept working).
//
// On a 401 that says the session cookie is missing or expired, the wrapper
// refreshes the context's own session once (single flight) and repeats the
// call, as the product's apiFetch does. It never logs in again: a revoked
// session answers no_credentials once its access cookie has expired
// (RequireAuth checks the cookie before revocation), so a new login would hide
// the revocation. The refresh endpoint refuses a revoked session, and that
// refusal reaches the caller as the original 401.
//
// Refresh tokens rotate with replay detection (nextauth.go RefreshToken), so
// wrap only a context that owns its login: never one whose cookies a browser
// context also uses (the second refresher would revoke the session for both).
//
// Pure (type-only imports), so `node --test` loads it (session-refresh.test.mjs).
import type { APIRequestContext, APIResponse } from "@playwright/test"

export const REFRESH_PATH = "/api/auth/token/refresh"
// internal/api/middleware.go writeAuthError: the reasons a refresh can cure.
const RENEWABLE = /error="(no_credentials|session_expired)"/
const CALLS = new Set(["get", "post", "put", "patch", "delete", "head", "fetch"])

/** Is this a 401 for a missing or expired session cookie (not a refused admission or right)? */
export function sessionExpired(res: Pick<APIResponse, "status" | "headers">): boolean {
  return res.status() === 401 && RENEWABLE.test(res.headers()["www-authenticate"] ?? "")
}

export interface SessionRenewal {
  /** Every refresh, oldest first (evidence; never tokens). */
  log?: Array<{ at: string; how: "refresh"; status: number }>
}

/**
 * Wraps `ctx` (which must own its login) so a call answered "session expired"
 * is repeated once after the session was refreshed. Any other answer, a failed
 * refresh (revoked, expired refresh token) and calls to /api/auth/ itself reach
 * the caller unchanged.
 */
export function withSessionRenewal(ctx: APIRequestContext, origin: string, opts: SessionRenewal = {}): APIRequestContext {
  const pageOrigin = new URL(origin).origin
  const log = opts.log ?? []
  let inFlight: Promise<boolean> | null = null
  const refresh = (): Promise<boolean> =>
    (inFlight ??= ctx
      .post(REFRESH_PATH, { headers: { Origin: pageOrigin }, failOnStatusCode: false })
      .then((r) => r.status(), () => 0)
      .then((status) => {
        log.push({ at: new Date().toISOString(), how: "refresh", status })
        return status === 200
      })
      .finally(() => (inFlight = null)))
  return new Proxy(ctx, {
    get(target, prop) {
      const value = Reflect.get(target, prop, target) as unknown
      if (typeof value !== "function") return value
      if (typeof prop !== "string" || !CALLS.has(prop)) return value.bind(target)
      const call = value as (...a: unknown[]) => Promise<APIResponse>
      return async (...args: unknown[]) => {
        const res = await call.apply(target, args)
        const url = typeof args[0] === "string" ? args[0] : ""
        if (!sessionExpired(res) || url.startsWith("/api/auth/")) return res
        if (!(await refresh())) return res
        return call.apply(target, args)
      }
    },
  })
}
