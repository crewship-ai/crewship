/**
 * Server-base abstraction — the seam that lets the frontend run from a
 * non-same-origin shell (Tauri v2 desktop app) against a user-chosen
 * crewshipd, while staying a byte-identical no-op for the normal web app
 * served by the Go binary.
 *
 * In the web app nothing is configured: getServerBase() returns "" and every
 * helper below degrades to today's same-origin behavior (relative /api/*
 * paths, cookie credentials, window.location-derived WS URL).
 *
 * A desktop shell configures the seam by injecting globals before the app
 * boots (Tauri: window.__CREWSHIP_SERVER_BASE__ / __CREWSHIP_TOKEN__ via an
 * init script), or persistently via localStorage "crewship.serverBase" (a
 * server-picker UI writes it). Auth then runs in BEARER mode: the shell
 * supplies a crewship_cli_ token (obtained via the /api/v1/auth/pair/* flow,
 * stored in the OS keychain — the keychain integration lives in the shell,
 * not here) and every request carries `Authorization: Bearer` with
 * credentials omitted, because cookie jars don't span custom-scheme origins.
 */

export type AuthMode = "cookie" | "bearer"

declare global {
  interface Window {
    /** Absolute base URL of the crewshipd to talk to, e.g.
     *  "https://crewship.example.com". Injected by a desktop shell. */
    __CREWSHIP_SERVER_BASE__?: string
    /** Bearer token (or a getter for one — e.g. backed by the OS keychain)
     *  the shell provisions after device pairing. Presence switches the
     *  app into bearer auth mode. */
    __CREWSHIP_TOKEN__?: string | (() => string | null)
  }
}

const STORAGE_KEY = "crewship.serverBase"

/** getServerBase returns the configured server base URL without a trailing
 *  slash, or "" for the same-origin default. Invalid values (non-http(s),
 *  unparseable) are ignored fail-safe — a corrupt localStorage entry must
 *  not brick the app into fetching a garbage origin. */
export function getServerBase(): string {
  if (typeof window === "undefined") return ""
  const injected = window.__CREWSHIP_SERVER_BASE__
  const stored = ((): string | null => {
    try {
      return window.localStorage.getItem(STORAGE_KEY)
    } catch {
      return null
    }
  })()
  const raw = (injected ?? stored ?? "").trim()
  if (raw === "") return ""
  try {
    const u = new URL(raw)
    if (u.protocol !== "http:" && u.protocol !== "https:") return ""
    // origin only — a base with a path would silently break the /api/*
    // concatenation contract.
    return u.origin
  } catch {
    return ""
  }
}

/** getServerOrigin returns the origin of the configured base, or null when
 *  running same-origin. Consumed by apiFetch's origin guard. */
export function getServerOrigin(): string | null {
  const base = getServerBase()
  return base === "" ? null : base
}

/** getBearerToken returns the shell-provisioned token, or null. Tokens are
 *  deliberately NOT read from localStorage — persistent web-readable token
 *  storage is the exact anti-pattern the pairing/keychain design avoids. */
export function getBearerToken(): string | null {
  if (typeof window === "undefined") return null
  const t = window.__CREWSHIP_TOKEN__
  if (typeof t === "function") {
    try {
      return t() || null
    } catch {
      return null
    }
  }
  return t || null
}

/** getAuthMode: bearer iff a shell provisioned a token; cookie otherwise.
 *  The web app never sets the global, so it always runs cookie mode. */
export function getAuthMode(): AuthMode {
  return getBearerToken() !== null ? "bearer" : "cookie"
}

/** withServerBase prefixes a same-origin-relative "/api/..." path with the
 *  configured base. Absolute URLs and the same-origin default pass through
 *  untouched. */
export function withServerBase(path: string): string {
  const base = getServerBase()
  if (base === "" || !path.startsWith("/") || path.startsWith("//")) return path
  return base + path
}

/** applyAuthInit layers base-aware credential/authorization handling onto a
 *  RequestInit WITHOUT changing same-origin cookie-mode behavior:
 *    - bearer mode: Authorization header + credentials "omit" (custom-scheme
 *      origins have no shared cookie jar; sending credentials would also
 *      trip stricter CORS).
 *    - cookie mode with a remote base: credentials "include" so the session
 *      cookie travels cross-origin (same-site web deployments).
 *    - same-origin cookie mode: init returned as-is — byte-identical.
 */
export function applyAuthInit(init?: RequestInit): RequestInit | undefined {
  const mode = getAuthMode()
  if (mode === "bearer") {
    const headers = new Headers(init?.headers)
    const token = getBearerToken()
    if (token && !headers.has("Authorization")) {
      headers.set("Authorization", `Bearer ${token}`)
    }
    return { ...init, headers, credentials: "omit" }
  }
  if (getServerBase() !== "") {
    return { ...init, credentials: "include" }
  }
  return init
}

/** serverFetch is the drop-in replacement for the bare `fetch("/api/...")`
 *  calls in the auth/onboarding pages: same-origin web app behavior is
 *  identical to bare fetch, while a configured base transparently routes
 *  the call (with the right credentials/Authorization) to the chosen
 *  server. Not a substitute for apiFetch — no refresh-on-401 here; these
 *  call sites are pre-auth or explicitly session-probing. */
export function serverFetch(path: string, init?: RequestInit): Promise<Response> {
  return fetch(withServerBase(path), applyAuthInit(init))
}

/** resolveWsBase returns the "ws(s)://host[:port]" prefix WS URL builders
 *  append their path to ("/ws", "/ws/terminal"). Same-origin default keeps
 *  the historical window.location derivation exactly; a configured base
 *  maps its scheme http→ws / https→wss. */
export function resolveWsBase(): string {
  if (typeof window === "undefined") return ""
  const base = getServerBase()
  if (base === "") {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
    return `${proto}//${window.location.host}`
  }
  const u = new URL(base)
  const proto = u.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${u.host}`
}
