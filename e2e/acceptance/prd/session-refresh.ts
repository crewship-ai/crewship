// Keeps an API context of a long PRD row signed in.
//
// The access cookie lives AccessTokenTTL (15 min, internal/auth/jwt.go) and a
// login's storage state is a snapshot: an APIRequestContext built from it
// loses its access cookie after 15 minutes and every call answers 401
// (G3 RC-3 e3f048555, A.craft.session.hour-sheet: "read admission" 401 after
// 15.6 min while the browser, which refreshes by itself, kept working).
//
// On a 401 that says the session cookie is missing or expired, the wrapper
// renews the session once (single flight) and repeats the call, as the
// product's apiFetch does. Refresh tokens rotate with replay detection
// (nextauth.go RefreshToken), so a context may refresh only a session it owns
// alone: a context that shares its login with a browser context first moves to
// a login of its own (`relogin`), otherwise the second refresher would revoke
// the session for both.
//
// Pure (type-only imports), so `node --test` loads it (session-refresh.test.mjs).
import type { APIRequestContext, APIResponse } from "@playwright/test"

export const REFRESH_PATH = "/api/auth/token/refresh"
// internal/api/middleware.go writeAuthError: the reasons a new session cures.
const RENEWABLE = /error="(no_credentials|session_expired)"/
const CALLS = new Set(["get", "post", "put", "patch", "delete", "head", "fetch"])

/** Is this a 401 for a missing or expired session cookie (not a refused admission or right)? */
export function sessionExpired(res: Pick<APIResponse, "status" | "headers">): boolean {
  return res.status() === 401 && RENEWABLE.test(res.headers()["www-authenticate"] ?? "")
}

export interface SessionRenewal {
  /**
   * The context shares its login with a browser context: on the first expired
   * session, replace it by a context with a login of its own (refreshed from
   * then on). Leave out for a context that owns its login.
   */
  relogin?: () => Promise<APIRequestContext>
  /** Every renewal, oldest first (evidence; never tokens). */
  log?: Array<{ at: string; how: "refresh" | "relogin"; status: number }>
}

/**
 * Wraps `ctx` so a call answered "session expired" is repeated once after the
 * session was renewed. Any other answer, and a failed renewal, reaches the
 * caller unchanged; calls to /api/auth/ itself are never repeated.
 */
export function withSessionRenewal(ctx: APIRequestContext, origin: string, opts: SessionRenewal = {}): APIRequestContext {
  const pageOrigin = new URL(origin).origin
  const log = opts.log ?? []
  const contexts = [ctx]
  let current = ctx
  let shared = Boolean(opts.relogin)
  let inFlight: Promise<boolean> | null = null
  const note = (how: "refresh" | "relogin", status: number) => log.push({ at: new Date().toISOString(), how, status })
  const renewNow = async (): Promise<boolean> => {
    if (shared) {
      const own = await opts.relogin!().catch(() => null)
      note("relogin", own ? 200 : 0)
      if (!own) return false
      contexts.push(own)
      current = own
      shared = false
      return true
    }
    const status = await current.post(REFRESH_PATH, { headers: { Origin: pageOrigin }, failOnStatusCode: false }).then((r) => r.status(), () => 0)
    note("refresh", status)
    return status === 200
  }
  // Calls that failed on the same session share one renewal.
  const renew = (failedOn: APIRequestContext): Promise<boolean> => {
    if (failedOn !== current) return Promise.resolve(true)
    return (inFlight ??= renewNow().finally(() => (inFlight = null)))
  }
  return new Proxy(ctx, {
    get(_target, prop) {
      if (prop === "dispose") return async (...args: unknown[]) => void (await Promise.all(contexts.map((c) => c.dispose(...(args as [])))))
      const value = Reflect.get(current, prop, current) as unknown
      if (typeof value !== "function") return value
      if (typeof prop !== "string" || !CALLS.has(prop)) return value.bind(current)
      return async (...args: unknown[]) => {
        const on = current
        const call = Reflect.get(on, prop, on) as (...a: unknown[]) => Promise<APIResponse>
        const res = await call.apply(on, args)
        const url = typeof args[0] === "string" ? args[0] : ""
        if (!sessionExpired(res) || url.startsWith("/api/auth/")) return res
        if (!(await renew(on))) return res
        const again = Reflect.get(current, prop, current) as (...a: unknown[]) => Promise<APIResponse>
        return again.apply(current, args)
      }
    },
  })
}
