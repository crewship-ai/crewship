"use client"

import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  useRef,
  type ReactNode,
} from "react"
import { z } from "zod"
import { AUTH_EVENT, AUTH_CHANNEL, broadcastSignOut } from "@/lib/api-fetch"
import { serverFetch } from "@/lib/server-base"

const sessionSchema = z.object({
  user: z.object({
    id: z.string(),
    name: z.string().optional().default(""),
    email: z.string().optional().default(""),
    // Served by /api/auth/session from the live users row, so an upload is
    // visible on the next refresh() instead of waiting for a token rotation.
    // "" means the user has none — the caller falls back to initials.
    avatar_url: z.string().optional().default(""),
  }),
  expires: z.string(),
})

const csrfSchema = z.object({
  csrfToken: z.string(),
})

type AuthSession = z.infer<typeof sessionSchema>

type AuthStatus = "loading" | "authenticated" | "unauthenticated"

interface AuthContextValue {
  session: AuthSession | null
  status: AuthStatus
  signIn: (email: string, password: string) => Promise<{ ok: boolean; error?: string }>
  signOut: () => Promise<void>
  refresh: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

/**
 * Session-poll retry policy. A 429 (rate limited), a 5xx/408 (transient
 * backend hiccup), or a network blip on /api/auth/session is NOT a
 * "you're logged out" signal — the user's cookies may be perfectly valid.
 * refresh() retries these instead of dropping the session, so hammering
 * refresh or a momentary outage can never evict a logged-in user.
 *
 * These are module-level (not const) solely so tests can collapse the backoff
 * to near-zero; production never reassigns them.
 */
export const sessionRetryPolicy = {
  maxAttempts: 5,
  baseMs: 500,
  capMs: 8000,
}

type SessionResult =
  // Definitive answer from the backend: a valid session, or `null` meaning
  // genuinely not authenticated (empty {} body, 401/403, revoked cookie).
  | { kind: "settled"; session: AuthSession | null }
  // Transient failure (429 / 5xx / 408 / network) — retry, never log out.
  | { kind: "retry"; retryAfterMs?: number }

function parseRetryAfterMs(res: Response): number | undefined {
  try {
    const raw = res.headers?.get?.("Retry-After")
    if (!raw) return undefined
    const secs = Number(raw)
    if (!Number.isFinite(secs) || secs < 0) return undefined
    return Math.min(secs * 1000, sessionRetryPolicy.capMs)
  } catch {
    return undefined
  }
}

async function fetchSession(): Promise<SessionResult> {
  try {

    const res = await serverFetch("/api/auth/session")
    // Transient, retryable statuses must not read as a logout. 429: the
    // rate limiter fired (a burst of refreshes). 408/5xx: the backend hit
    // a transient error and deliberately left the auth cookies intact
    // (see NextAuthHandler.Session) so the next probe can recover.
    if (res.status === 429 || res.status === 408 || res.status >= 500) {
      return { kind: "retry", retryAfterMs: parseRetryAfterMs(res) }
    }
    if (!res.ok) {
      // 401/403/other 4xx — a definitive "not authenticated".
      return { kind: "settled", session: null }
    }
    const data = await res.json()
    const parsed = sessionSchema.safeParse(data)
    return { kind: "settled", session: parsed.success ? parsed.data : null }
  } catch {
    // Network error / unreadable body — treat as transient, not a logout.
    return { kind: "retry" }
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function fetchCsrfToken(): Promise<string | null> {
  try {
     
    const res = await serverFetch("/api/auth/csrf")
    if (!res.ok) return null
    const data = await res.json()
    const parsed = csrfSchema.safeParse(data)
    return parsed.success ? parsed.data.csrfToken : null
  } catch {
    return null
  }
}

interface AuthProviderProps {
  children: ReactNode
}

/** Provides auth context (session, signIn, signOut) to the component tree. */
export function AuthProvider({ children }: AuthProviderProps) {
  const [session, setSession] = useState<AuthSession | null>(null)
  const [status, setStatus] = useState<AuthStatus>("loading")

  // Monotonic generation guard: a newer refresh() (e.g. one fired by signIn)
  // supersedes any retry loop still backing off, so a stale loop can't
  // clobber fresh session state.
  const refreshGen = useRef(0)

  const refresh = useCallback(async () => {
    const gen = ++refreshGen.current

    for (let attempt = 0; ; attempt++) {
      if (gen !== refreshGen.current) return // superseded by a newer refresh
      const result = await fetchSession()

      if (result.kind === "settled") {
        if (gen !== refreshGen.current) return
        setSession(result.session)
        setStatus(result.session ? "authenticated" : "unauthenticated")
        return
      }

      // Transient failure: never downgrade the session here. Back off and
      // retry up to the policy limit.
      if (attempt >= sessionRetryPolicy.maxAttempts - 1) break
      const backoff = Math.min(
        result.retryAfterMs ?? sessionRetryPolicy.baseMs * 2 ** attempt,
        sessionRetryPolicy.capMs,
      )
      await sleep(backoff)
    }

    if (gen !== refreshGen.current) return
    // Retries exhausted on transient failures only. Preserve an existing
    // authenticated session — a real outage must never force a logout. Only a
    // cold load that never once reached the backend settles on
    // "unauthenticated", so the UI can offer login instead of spinning
    // forever. (setSession is intentionally left untouched.)
    setStatus((prev) => (prev === "authenticated" ? prev : "unauthenticated"))
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  // Hard-redirect to /login when apiFetch detects a terminal auth state
  // (refresh failed, session_revoked, etc). The BroadcastChannel echo
  // covers other tabs in the same browser. The redirect carries the
  // current path as ?redirect= so post-login can return the user to
  // where they were instead of dumping them on /.
  useEffect(() => {
    if (typeof window === "undefined") return

    let redirected = false
    const goLoginExpired = () => {
      if (redirected) return
      redirected = true
      const { pathname, search } = window.location
      const currentPath = pathname + search
      const params = new URLSearchParams({ reason: "expired" })
      // Don't append ?redirect for /login itself or unsafe absolute URLs.
      // The check uses pathname (not pathname+search) so /login?reason=expired
      // — the URL we're about to redirect TO — is recognised as the
      // login page even when it carries query params. Without this,
      // the user could end up bouncing /login → /login?redirect=/login?...
      if (pathname !== "/login" && currentPath.startsWith("/") && !currentPath.startsWith("//")) {
        params.set("redirect", currentPath)
      }
      window.location.replace(`/login?${params.toString()}`)
    }
    const goLoginSignedOutElsewhere = () => {
      if (redirected) return
      redirected = true
      window.location.replace("/login")
    }

    const expiredHandler = () => goLoginExpired()
    window.addEventListener(AUTH_EVENT, expiredHandler)

    let channel: BroadcastChannel | null = null
    if (typeof BroadcastChannel !== "undefined") {
      try {
        channel = new BroadcastChannel(AUTH_CHANNEL)
        channel.onmessage = (ev) => {
          if (ev.data?.type === "session-expired") goLoginExpired()
          else if (ev.data?.type === "signout") goLoginSignedOutElsewhere()
        }
      } catch {
        channel = null
      }
    }

    return () => {
      window.removeEventListener(AUTH_EVENT, expiredHandler)
      channel?.close()
    }
  }, [])

  const signIn = useCallback(async (email: string, password: string) => {
    const csrfToken = await fetchCsrfToken()
    if (!csrfToken) {
      return { ok: false, error: "Failed to get CSRF token" }
    }

    try {
       
      const res = await serverFetch("/api/auth/callback/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password, csrfToken, redirect: "false" }),
      })

      if (!res.ok) {
        const data = await res.json().catch(() => null)
        return { ok: false, error: data?.error ?? "Login failed" }
      }

      const data = await res.json()
      if (data.error) {
        return { ok: false, error: data.error === "CredentialsSignin" ? "Invalid email or password" : data.error }
      }

      await refresh()
      return { ok: true }
    } catch {
      return { ok: false, error: "Network error" }
    }
  }, [refresh])

  const signOut = useCallback(async () => {
    // Gate the local reset on a successful (or already-expired) server
    // response. With revocation now enforced server-side, fanning out
    // "signed out" while the refresh chain is still active server-side
    // would leave every other tab thinking they're logged out while the
    // session keeps refreshing in the background. CodeRabbit flagged
    // this on PR #233.
    let serverAcknowledged = false
    try {
       
      const res = await serverFetch("/api/auth/signout", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
      })
      // 401 means the access cookie was already expired/revoked when
      // the request landed — treat that as an effective sign-out so a
      // user with a stale tab can still log back out.
      serverAcknowledged = res.ok || res.status === 401
    } catch {
      // Network error — leave local state intact so a transient outage
      // doesn't desync this tab from the still-active server session.
      return
    }
    if (!serverAcknowledged) {
      return
    }
    setSession(null)
    setStatus("unauthenticated")
    // Tell other tabs in this browser to drop their session UI too.
    broadcastSignOut()
  }, [])

  return (
    <AuthContext value={{ session, status, signIn, signOut, refresh }}>
      {children}
    </AuthContext>
  )
}

/** Returns the full auth context (session, status, signIn, signOut, refresh). */
export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error("useAuth must be used within an AuthProvider")
  }
  return ctx
}

/** Returns session data and auth status (drop-in replacement for next-auth useSession). */
export function useSession() {
  const { session, status } = useAuth()
  return { data: session, status }
}
