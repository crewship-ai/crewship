"use client"

import {
  createContext,
  useContext,
  useState,
  useEffect,
  useLayoutEffect,
  useCallback,
  useRef,
  type ReactNode,
} from "react"
import { z } from "zod"
import { AUTH_EVENT, AUTH_CHANNEL, broadcastSignOut } from "@/lib/api-fetch"
import { serverFetch } from "@/lib/server-base"
import { purgeOtherE2Recovery } from "@/lib/chat-e2-recovery"
import { isKnownAppPath } from "@/lib/app-routes"
import { SIGNED_OUT_URL, rememberSignedOut, signedOutKind } from "@/lib/signed-out"
import { suspendOtherDocumentDrafts, captureDocumentDrafts, removeCapturedDocumentDrafts } from "@/lib/document-queue"
import { suspendOtherArtifactDrafts, finishArtifactDraftIdentity, captureArtifactDrafts, removeCapturedArtifactDrafts, captureLegacyLocalDrafts, removeCapturedLegacyLocalDrafts } from "@/lib/artifact-drafts"

import { beginLocalDraftIdentity, finishLocalDraftIdentity } from "@/lib/local-draft-seal"
import { askConfirm, confirmHostMounted } from "@/components/ui/confirm-host"

const DRAFT_DISCARD_WARNING = "Signing out can permanently delete local recovery copies of your unsaved artifact and document work. Download or copy any draft you need first. Continue? New changes arriving during sign-out are kept."
/** Sign-out's draft warning goes through the shell's ConfirmHost; a root without one (a unit test) falls back to the browser prompt. */
function confirmDraftDiscard(): Promise<boolean> {
  if (!confirmHostMounted()) return Promise.resolve(window.confirm(DRAFT_DISCARD_WARNING))
  return askConfirm({
    title: "Sign out and delete local recovery copies?",
    description: "Signing out can permanently delete local recovery copies of your unsaved artifact and document work. Download or copy any draft you need first. New changes arriving during sign-out are kept.",
    confirmLabel: "Sign out",
    cancelLabel: "Keep my drafts",
    destructive: true,
  })
}

async function preserveDraftsForIdentity(human: string | null) {
  const generation = beginLocalDraftIdentity(human)
  await Promise.all([suspendOtherDocumentDrafts(human), suspendOtherArtifactDrafts(human)])
  finishArtifactDraftIdentity(human)
  finishLocalDraftIdentity(human, generation)
  return generation
}

const sessionSchema = z.object({
  user: z.object({
    id: z.string(),
    name: z.string().optional().default(""),
    email: z.string().optional().default(""),
    // Served by /api/auth/session from the live users row, so an upload is
    // visible on the next refresh() instead of waiting for a token rotation.
    // "" means the user has none — the caller falls back to initials.
    avatar_url: z.string().optional().default(""),
    // Administers the instance (the Admin console is theirs). A workspace
    // role does not decide this; the server does, per request.
    is_instance_admin: z.boolean().optional().default(false),
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
  /** `status` and `retryAfter` (seconds) are set when the server refused with
   *  a status the page words on its own, e.g. 429 from the sign-in limiter. */
  signIn: (email: string, password: string) => Promise<{ ok: boolean; error?: string; status?: number; retryAfter?: number }>
  /** Resolves false when the server did not confirm the sign-out (the
   *  session is still alive), so the caller does not say "signed out". */
  signOut: () => Promise<boolean | void>
  refresh: () => Promise<void>
  /**
   * Set while a cold load cannot reach the backend at all (#3787): the status
   * stays "loading" — nobody is signed out — and the app shows "Crewship is
   * restarting" with the time of the next try. Optional so test doubles need
   * not supply it.
   */
  unreachable?: { retryAt: number } | null
  /** Try the session again now instead of at `unreachable.retryAt`. */
  retryNow?: () => void
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
  /** After the attempts above, a cold load that never reached the backend
   *  tries once every this many ms ("Next try in N s"). */
  unreachableRetryMs: 10_000,
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
  const [draftAdmission, setDraftAdmission] = useState(0)
  // Last admitted identity transition. Only a tree a person used can hold their
  // drafts or editors, so only a change after one remounts it.
  const admitted = useRef<{ generation: number; human: string | null } | null>(null)
  const [draftRecovery, setDraftRecovery] = useState<{ retry: () => Promise<void> } | null>(null)
  const [draftRetrying, setDraftRetrying] = useState(false)
  const retryDraftTransition = useRef<() => Promise<void>>(async () => {})
  const [unreachable, setUnreachable] = useState<{ retryAt: number } | null>(null)
  // Mirrors for the redirect listener and the retry loop, which are created
  // once and would otherwise read the values of the render that made them.
  const sessionRef = useRef<AuthSession | null>(null)
  const statusRef = useRef<AuthStatus>("loading")
  const unreachableTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => { sessionRef.current = session }, [session])
  useEffect(() => { statusRef.current = status }, [status])

  // Monotonic generation guard: a newer refresh() (e.g. one fired by signIn)
  // supersedes any retry loop still backing off, so a stale loop can't
  // clobber fresh session state.
  const refreshGen = useRef(0)

  const settle = useCallback(async (next: AuthSession | null, gen: number) => {
    if (gen !== refreshGen.current) return
    clearTimeout(unreachableTimer.current)
    unreachableTimer.current = undefined
    purgeOtherE2Recovery(next?.user.id ?? null)
    let admission: number
    try { admission = await preserveDraftsForIdentity(next?.user.id ?? null) }
    catch { if (gen === refreshGen.current) setDraftRecovery({ retry: () => retryDraftTransition.current() }); return }
    if (gen !== refreshGen.current) return
    // The cold load and signed-out pages hold nothing of a person, so admitting
    // one there keeps the tree (and anything typed meanwhile) mounted.
    if (admitted.current?.human && admitted.current.generation !== admission) setDraftAdmission(admission)
    admitted.current = { generation: admission, human: next?.user.id ?? null }
    setDraftRecovery(null)
    setUnreachable(null)
    sessionRef.current = next
    statusRef.current = next ? "authenticated" : "unauthenticated"
    setSession(next)
    setStatus(next ? "authenticated" : "unauthenticated")
  }, [])

  // A cold load that has never reached the backend: one attempt per
  // unreachableRetryMs until it answers. The status stays "loading" the whole
  // time — a server that is restarting has not signed anybody out.
  const probe = useCallback(async (gen: number) => {
    clearTimeout(unreachableTimer.current)
    unreachableTimer.current = undefined
    if (gen !== refreshGen.current) return
    const result = await fetchSession()
    if (gen !== refreshGen.current) return
    if (result.kind === "settled") {
      await settle(result.session, gen)
      return
    }
    const ms = sessionRetryPolicy.unreachableRetryMs
    setUnreachable({ retryAt: Date.now() + ms })
    unreachableTimer.current = setTimeout(() => { void probe(gen) }, ms)
  }, [settle])

  const refresh = useCallback(async () => {
    const gen = ++refreshGen.current
    clearTimeout(unreachableTimer.current)
    unreachableTimer.current = undefined

    for (let attempt = 0; ; attempt++) {
      if (gen !== refreshGen.current) return // superseded by a newer refresh
      const result = await fetchSession()

      if (result.kind === "settled") {
        if (gen !== refreshGen.current) return
        await settle(result.session, gen)
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
    // authenticated session — a real outage must never force a logout. A
    // cold load that never once reached the backend used to settle on
    // "unauthenticated" and send the person to a login form that could not
    // work either (#3787); it now stays "loading", reports `unreachable` (the
    // app shows "Crewship is restarting") and keeps trying.
    if (statusRef.current === "authenticated") return
    const ms = sessionRetryPolicy.unreachableRetryMs
    setUnreachable({ retryAt: Date.now() + ms })
    unreachableTimer.current = setTimeout(() => { void probe(gen) }, ms)
  }, [settle, probe])

  useEffect(() => { retryDraftTransition.current = refresh }, [refresh])

  const retryNow = useCallback(() => {
    if (!unreachableTimer.current) return
    void probe(refreshGen.current)
  }, [probe])

  useEffect(() => {
    refresh()
  }, [refresh])

  // The network coming back is the moment to look again, not the next tick.
  useEffect(() => {
    if (!unreachable) return
    window.addEventListener("online", retryNow)
    return () => window.removeEventListener("online", retryNow)
  }, [unreachable, retryNow])

  useEffect(() => () => clearTimeout(unreachableTimer.current), [])

  // Hard-redirect to /login when apiFetch detects a terminal auth state
  // (refresh failed, session_revoked, etc). The BroadcastChannel echo
  // covers other tabs in the same browser. The redirect carries the
  // current path as ?redirect= so post-login can return the user to
  // where they were instead of dumping them on /.
  useEffect(() => {
    if (typeof window === "undefined") return

    // The generation this tab is leaving under. A handler returns while that
    // generation is still current; any newer refresh or settle bumps it and
    // re-arms both listeners, so no path can leave them stuck (#4714).
    let redirectingGen = -1
    // A pending unreachable-backend probe (timer or the online event) reuses
    // the current generation; left armed it could settle a live session under
    // the leaving handler's generation and keep both listeners disarmed.
    const cancelUnreachableProbe = () => { clearTimeout(unreachableTimer.current); unreachableTimer.current = undefined }
    const goLoginExpired = async (reason?: string) => {
      if (redirectingGen === refreshGen.current) return
      // A path the app has no page for shows the not-found state, signed in
      // or not (app/(dashboard)/layout.tsx). The static-export backend serves
      // the dashboard there, whose first requests answer 401 for a visitor
      // without a session — that is not an expired session, and the login
      // page is not where an unknown link should lead.
      if (!isKnownAppPath(window.location.pathname)) return
      const gen = ++refreshGen.current
      redirectingGen = gen
      cancelUnreachableProbe()
      purgeOtherE2Recovery(null)
      try { await preserveDraftsForIdentity(null) }
      catch { setDraftRecovery({ retry: async () => { if (redirectingGen === gen) redirectingGen = -1; await goLoginExpired(reason) } }); return }
      // Superseded by a newer refresh: this tab stays and, the generation
      // having moved, still follows a later sign-out or expiry (#4705).
      if (gen !== refreshGen.current) return
      const { pathname, search } = window.location
      const currentPath = pathname + search
      // no_credentials: the browser never had a session (or its cookies
      // were cleared) — send it to sign in without saying anything expired.
      // Otherwise the login page opens on its signed-out screen (#3787):
      // expired, or revoked elsewhere — and may say who was signed in and
      // that an unsent chat message is kept (lib/signed-out.ts).
      const kind = signedOutKind(reason)
      if (kind) rememberSignedOut(sessionRef.current?.user.email)
      const params = new URLSearchParams(kind ? { reason: kind } : {})
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
    const goLoginSignedOutElsewhere = async (human?: string) => {
      if (redirectingGen === refreshGen.current) return
      const currentHuman = sessionRef.current?.user.id
      if (human && currentHuman && human !== currentHuman) return
      const gen = ++refreshGen.current
      redirectingGen = gen
      cancelUnreachableProbe()
      purgeOtherE2Recovery(null)
      try { await preserveDraftsForIdentity(null) }
      catch { setDraftRecovery({ retry: async () => { if (redirectingGen === gen) redirectingGen = -1; await goLoginSignedOutElsewhere(human) } }); return }
      if (gen !== refreshGen.current) return
      // The sender deletes only its captured revisions with CAS. A delayed
      // message never authorizes deleting this tab's newer recovery rows.
      const latest = await fetchSession()
      if (gen !== refreshGen.current) return
      // Still signed in: the tab stays and keeps listening (#4705). The settle
      // keeps this generation, so leaving is disarmed explicitly.
      if (latest.kind === "settled" && latest.session) { redirectingGen = -1; await settle(latest.session, gen); return }
      window.location.replace(SIGNED_OUT_URL)
    }

    const expiredHandler = (e: Event) => { void goLoginExpired((e as CustomEvent<{ reason?: string }>).detail?.reason) }
    window.addEventListener(AUTH_EVENT, expiredHandler)

    let channel: BroadcastChannel | null = null
    if (typeof BroadcastChannel !== "undefined") {
      try {
        channel = new BroadcastChannel(AUTH_CHANNEL)
        channel.onmessage = (ev) => {
          if (ev.data?.type === "session-expired") void goLoginExpired(ev.data?.reason)
          else if (ev.data?.type === "signout") void goLoginSignedOutElsewhere(typeof ev.data.human === "string" ? ev.data.human : undefined)
        }
      } catch {
        channel = null
      }
    }

    return () => {
      window.removeEventListener(AUTH_EVENT, expiredHandler)
      channel?.close()
    }
  }, [settle])

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
        const error = data?.error ?? "Login failed"
        if (res.status !== 429) return { ok: false, error }
        // The sign-in limiter: the page words this one itself.
        const retryAfter = Number(res.headers?.get?.("Retry-After"))
        return { ok: false, error, status: 429, ...(retryAfter > 0 ? { retryAfter } : {}) }
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

  const recoveryElement = useRef<HTMLElement>(null)
  useLayoutEffect(() => {
    if (!draftRecovery || !recoveryElement.current) return
    const mask = recoveryElement.current, hidden = new Set<HTMLElement>()
    // Portals live outside the React wrapper. Hide those DOM siblings too,
    // keeping their models mounted while capture is retried.
    const contain = () => {
      let current: HTMLElement = mask
      while (current.parentElement) {
        const parent = current.parentElement
        for (const sibling of parent.children) {
          if (sibling !== current && sibling instanceof HTMLElement && !sibling.inert) { sibling.inert = true; hidden.add(sibling) }
        }
        if (parent === document.body) break
        current = parent
      }
    }
    contain(); mask.focus({preventScroll:true})
    const observer = new MutationObserver(contain)
    observer.observe(document.body,{childList:true,subtree:true})
    return () => { observer.disconnect(); for (const element of hidden) element.inert = false }
  }, [draftRecovery])

  const signOut = useCallback(async (): Promise<boolean> => {
    // Gate the local reset on a successful (or already-expired) server
    // response. With revocation now enforced server-side, fanning out
    // "signed out" while the refresh chain is still active server-side
    // would leave every other tab thinking they're logged out while the
    // session keeps refreshing in the background. CodeRabbit flagged
    // this on PR #233.
    const signingOutHuman = sessionRef.current?.user.id, startingGeneration = refreshGen.current
    let capturedDocuments: Awaited<ReturnType<typeof captureDocumentDrafts>> = []
    let capturedArtifacts: Awaited<ReturnType<typeof captureArtifactDrafts>> = []
    let capturedLegacy: ReturnType<typeof captureLegacyLocalDrafts> = []
    try { if (signingOutHuman) [capturedDocuments, capturedArtifacts] = await Promise.all([captureDocumentDrafts(signingOutHuman), captureArtifactDrafts(signingOutHuman)]) }
    catch { /* No verified deletion snapshot: retain those encrypted copies. */ }
    try { if (signingOutHuman) capturedLegacy = captureLegacyLocalDrafts(signingOutHuman) } catch { return false }
    const discardDrafts = capturedDocuments.length + capturedArtifacts.length + capturedLegacy.length > 0
    if (discardDrafts && !(await confirmDraftDiscard())) return false
    if (sessionRef.current?.user.id !== signingOutHuman || refreshGen.current !== startingGeneration) return false
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
      return false
    }
    if (!serverAcknowledged) {
      return false
    }
    if (sessionRef.current?.user.id !== signingOutHuman || refreshGen.current !== startingGeneration) return false
    const gen = ++refreshGen.current // An older session read must not revive a signed-out writer.
    purgeOtherE2Recovery(null)
    const finalize = async (): Promise<boolean> => {
      try {
        const admission = await preserveDraftsForIdentity(null)
        if (gen !== refreshGen.current) return false
        if (discardDrafts) {
          await Promise.all([removeCapturedDocumentDrafts(capturedDocuments), removeCapturedArtifactDrafts(capturedArtifacts)])
          removeCapturedLegacyLocalDrafts(capturedLegacy)
        }
        if (gen !== refreshGen.current) return false
        sessionRef.current = null; statusRef.current = "unauthenticated"
        admitted.current = { generation: admission, human: null }
        setSession(null); setStatus("unauthenticated"); setDraftAdmission(admission); setDraftRecovery(null)
        broadcastSignOut(signingOutHuman)
        return true
      } catch {
        setDraftRecovery({ retry: async () => { if (await finalize()) window.location.replace(SIGNED_OUT_URL) } })
        return false
      }
    }
    return finalize()
  }, [])

  return (
    <AuthContext value={{ session, status, signIn, signOut, refresh, unreachable, retryNow }}>
      <div key={draftAdmission} inert={Boolean(draftRecovery)} aria-hidden={draftRecovery ? true : undefined} style={{ display: draftRecovery ? "none" : "contents" }}>{children}</div>
      {draftRecovery && <section ref={recoveryElement} tabIndex={-1} onKeyDown={event => {
        event.stopPropagation()
        if (event.key !== "Tab") return
        const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>("button:not(:disabled),a[href]"))
        if (!controls.length) { event.preventDefault(); return }
        const index = controls.indexOf(document.activeElement as HTMLElement)
        event.preventDefault(); controls[(index + (event.shiftKey ? -1 : 1) + controls.length) % controls.length].focus()
      }} role="alertdialog" aria-modal="true" aria-labelledby="draft-recovery-title" className="fixed inset-0 z-[2147483647] flex items-center justify-center bg-background p-6"><div className="max-w-lg space-y-4"><h1 id="draft-recovery-title" className="text-xl font-semibold">Keep this tab open</h1><p>A local recovery check could not finish. Keep this tab open and retry before leaving or changing accounts. Any open editor remains in this tab.</p><p>You can sign in again in a new tab if needed.</p><div className="flex gap-4"><button type="button" disabled={draftRetrying} onClick={() => { setDraftRetrying(true); void draftRecovery.retry().catch(() => {}).finally(() => setDraftRetrying(false)) }}>{draftRetrying ? "Keeping changes…" : "Retry keeping my changes"}</button><a href="/login" target="_blank" rel="noopener noreferrer">Sign in in a new tab</a></div></div></section>}
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

/**
 * Session data when a provider is mounted, and null when one is not.
 *
 * The `useSession` below throws without a provider, which is right for
 * everything that cannot function without knowing who is signed in — a route
 * guard, a mutation, the sign-out button. It is wrong for DECORATION: the
 * transcript draws the reader's own face beside their messages, and a face
 * that throws takes the whole message down with it. Anything mounted outside
 * the dashboard tree (a test rendering one turn, an embedded surface) hit
 * exactly that.
 *
 * Same shape as `useRealtimeEventSafe` and `useRealtimeStatusSafe`, and for
 * the same reason: the tolerant variant is a deliberate, named choice at the
 * call site rather than a silent weakening of the strict one.
 */
export function useSessionSafe() {
  const ctx = useContext(AuthContext)
  return { data: ctx?.session ?? null, status: ctx?.status ?? "loading" }
}

/** Whether an AuthProvider is mounted — for chrome that shows account
 *  controls only inside the signed-in app (the sidebar's user row). */
export function useAuthAvailable(): boolean {
  return useContext(AuthContext) != null
}

/** Returns session data and auth status (drop-in replacement for next-auth useSession). */
export function useSession() {
  const { session, status } = useAuth()
  return { data: session, status }
}

/**
 * Whether the signed-in person administers the instance — the only thing that
 * shows the Admin console. null while the session is still loading, so a
 * caller can wait instead of redirecting on a guess.
 */
export function useIsInstanceAdmin(): boolean | null {
  const { data, status } = useSessionSafe()
  if (status === "loading") return null
  return data?.user.is_instance_admin ?? false
}
