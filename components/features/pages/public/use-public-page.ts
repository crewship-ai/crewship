"use client"

/**
 * Fetching a public page (PRD `docs/prd/pages.md` §7.3.1, §7.3.3).
 *
 * THIS HOOK DELIBERATELY DOES NOT USE `apiFetch`.
 *
 * §7.3.1: "A public page is served from a separate URL space that shares no
 * session, no cookie and no workspace context with the app." `apiFetch` is the
 * app's authenticated client — it attaches the session, and on a terminal auth
 * state it fires the AUTH_EVENT that `hooks/use-auth.tsx` turns into a hard
 * redirect to /login. A reader with no account hitting a 401 from a
 * password-protected link would be bounced to a login screen they cannot use,
 * for a page that was never theirs to log into. So this is a bare `fetch` with
 * `credentials: "omit"`: no cookie leaves the browser, no header is attached,
 * and a 401 here means "this link wants a password" and nothing else.
 *
 * The password is POSTed and never put in the URL (§7.3.3), and it is never
 * persisted — no cookie, no localStorage, no query string. A reload asks again.
 * That is a deliberate cost: the alternative is inventing a session for a
 * surface whose whole definition is that it has none.
 */

import { useCallback, useEffect, useRef, useState } from "react"

import { apiErrorMessage } from "@/lib/api-error"
import type { PublicPage, PublicPageStatus } from "./types"

/** The API path behind /p/{token}. One place, so the client and the route agree. */
export function publicPagePath(token: string): string {
  return `/api/v1/public/pages/${encodeURIComponent(token)}`
}

export function publicPageUnlockPath(token: string): string {
  return `${publicPagePath(token)}/unlock`
}

export interface PublicPageState {
  status: PublicPageStatus
  page: PublicPage | null
  /** The server's own words on a refusal, never a sentence invented here. */
  message: string | null
  /** True while a password submission is in flight. */
  submitting: boolean
  submit: (password: string) => Promise<void>
  reload: () => void
}

/**
 * `fetch` is injectable so the tests drive real state transitions rather than
 * mocking the hook out. Defaulting to `globalThis.fetch` rather than capturing
 * `fetch` at module scope keeps happy-dom's per-test stubbing working.
 */
export type PublicFetch = (input: string, init?: RequestInit) => Promise<Response>

export function usePublicPage(token: string | null, fetchImpl?: PublicFetch): PublicPageState {
  const [status, setStatus] = useState<PublicPageStatus>("loading")
  const [page, setPage] = useState<PublicPage | null>(null)
  const [message, setMessage] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)

  // A stale response from a previous token must never overwrite the current
  // one. The counter is the same guard the rest of the app uses for this.
  const generation = useRef(0)

  const doFetch = useCallback<PublicFetch>(
    (input, init) => (fetchImpl ?? globalThis.fetch)(input, { ...init, credentials: "omit" }),
    [fetchImpl],
  )

  useEffect(() => {
    if (!token) return
    const gen = ++generation.current
    let cancelled = false

    setStatus("loading")
    setMessage(null)

    doFetch(publicPagePath(token), { headers: { Accept: "application/json" } })
      .then(async (res) => {
        const body = await res.json().catch(() => null)
        if (cancelled || gen !== generation.current) return
        if (res.ok) {
          setPage(body as PublicPage)
          setStatus("ready")
          return
        }
        if (res.status === 401) {
          // A password, not a login. There is no account to sign into.
          setStatus("password")
          setMessage(null)
          return
        }
        setPage(null)
        setStatus(res.status === 404 ? "unavailable" : "error")
        setMessage(apiErrorMessage(body, "This link could not be opened."))
      })
      .catch(() => {
        if (cancelled || gen !== generation.current) return
        setStatus("error")
        setMessage("This page could not be reached. Check your connection and try again.")
      })

    return () => {
      cancelled = true
    }
  }, [token, doFetch, reloadKey])

  const submit = useCallback(
    async (password: string) => {
      if (!token) return
      setSubmitting(true)
      setMessage(null)
      try {
        const res = await doFetch(publicPageUnlockPath(token), {
          method: "POST",
          headers: { "Content-Type": "application/json", Accept: "application/json" },
          // The password travels in the body. Never a query parameter: a URL is
          // written to every proxy log and every browser history between here
          // and the reader.
          body: JSON.stringify({ password }),
        })
        const body = await res.json().catch(() => null)
        if (res.ok) {
          setPage(body as PublicPage)
          setStatus("ready")
          return
        }
        // The server answers a wrong password and an unknown link with the
        // same sentence (§7.3.3), and this is where that matters: the UI shows
        // what it was told rather than deciding which of the two happened.
        setStatus(res.status === 429 ? "password" : "password")
        setMessage(apiErrorMessage(body, "That link and password do not match."))
      } catch {
        setMessage("This page could not be reached. Check your connection and try again.")
      } finally {
        setSubmitting(false)
      }
    },
    [token, doFetch],
  )

  const reload = useCallback(() => setReloadKey((n) => n + 1), [])

  return { status, page, message, submitting, submit, reload }
}
