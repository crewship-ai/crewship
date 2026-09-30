"use client"

import * as React from "react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { apiErrorMessage } from "@/lib/api-error"

/**
 * The one reader every Backups hook goes through.
 *
 * The backend tracks are landing the /admin/instance/backups/* endpoints in
 * parallel with this UI. Until one exists the server answers 404 (or 405/501
 * for a route that is stubbed), and the section says so quietly — "Not
 * available on this server yet" — rather than inventing numbers. Fixtures
 * appear only with `?demo=1` outside a production build, so the design can be
 * reviewed before the backend lands, and in tests.
 */

export const INSTANCE_BACKUPS = "/api/v1/admin/instance/backups"

export type ResourceStatus = "loading" | "ready" | "unavailable" | "error"

export interface Resource<T> {
  status: ResourceStatus
  data: T | null
  error: string | null
  /** True when the data came from the review fixtures. */
  demo: boolean
  reload: () => void
}

/** Statuses that mean "this server does not have the endpoint yet". */
export function isUnavailableStatus(status: number): boolean {
  return status === 404 || status === 405 || status === 501
}

/** `?demo=1`, honoured only outside a production build. */
export function isDemo(search: string = typeof window === "undefined" ? "" : window.location.search): boolean {
  if (process.env.NODE_ENV === "production") return false
  return new URLSearchParams(search).get("demo") === "1"
}

export function useDemo(): boolean {
  const [demo] = React.useState(() => isDemo())
  return demo
}

export async function readError(res: Response, fallback: string): Promise<string> {
  try {
    return apiErrorMessage(await res.json(), fallback)
  } catch {
    return fallback
  }
}

/**
 * GET `url` (null skips) and keep the answer. `fixture` stands in with
 * `?demo=1`; it is called again whenever the url changes, so a fixture can
 * depend on the same inputs the url does.
 */
export function useResource<T>(url: string | null, fixture: () => T, map?: (json: unknown) => T): Resource<T> {
  const demo = useDemo()
  const [tick, setTick] = React.useState(0)
  const [state, setState] = React.useState<Omit<Resource<T>, "reload">>({ status: "loading", data: null, error: null, demo })
  const fixtureRef = React.useRef(fixture)
  fixtureRef.current = fixture
  const mapRef = React.useRef(map)
  mapRef.current = map

  React.useEffect(() => {
    if (url === null) return
    if (demo) {
      setState({ status: "ready", data: fixtureRef.current(), error: null, demo: true })
      return
    }
    const controller = new AbortController()
    setState((s) => ({ ...s, status: s.data ? s.status : "loading" }))
    void (async () => {
      try {
        const res = await apiFetch(url, { signal: controller.signal })
        if (controller.signal.aborted) return
        if (isUnavailableStatus(res.status)) {
          setState({ status: "unavailable", data: null, error: null, demo: false })
          return
        }
        if (!res.ok) {
          setState({ status: "error", data: null, error: await readError(res, `HTTP ${res.status}`), demo: false })
          return
        }
        const json = await res.json()
        if (controller.signal.aborted) return
        setState({ status: "ready", data: mapRef.current ? mapRef.current(json) : (json as T), error: null, demo: false })
      } catch (e) {
        if (controller.signal.aborted) return
        setState({ status: "error", data: null, error: e instanceof Error ? e.message : "Network error", demo: false })
      }
    })()
    return () => controller.abort()
  }, [url, demo, tick])

  const reload = React.useCallback(() => setTick((t) => t + 1), [])
  return { ...state, reload }
}

export type SendResult<T> = { ok: true; data: T } | { ok: false; unavailable: boolean; error: string }

/** One write. A 404/405/501 is reported as unavailable, not as a failure. */
export async function send<T = unknown>(url: string, method: string, body?: unknown): Promise<SendResult<T>> {
  try {
    const res = await apiFetch(url, {
      method,
      headers: body === undefined ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (isUnavailableStatus(res.status)) return { ok: false, unavailable: true, error: "Not available on this server yet" }
    if (!res.ok) return { ok: false, unavailable: false, error: await readError(res, `HTTP ${res.status}`) }
    const data = res.status === 204 ? ({} as T) : ((await res.json().catch(() => ({}))) as T)
    return { ok: true, data }
  } catch (e) {
    return { ok: false, unavailable: false, error: e instanceof Error ? e.message : "Network error" }
  }
}

/**
 * Run one write and say what happened. In demo mode nothing is sent; a toast
 * says so, so a reviewer never mistakes the fixtures for a saved change.
 * Returns the data on success, null otherwise.
 */
export async function perform<T>(demo: boolean, fn: () => Promise<SendResult<T>>, done: string, fallback: string): Promise<T | null> {
  if (demo) {
    toast.message("Demo data · nothing was sent")
    return null
  }
  const r = await fn()
  if (r.ok) {
    if (done) toast.success(done)
    return r.data
  }
  if (r.unavailable) toast.message(`${fallback}: not available on this server yet`)
  else toast.error(`${fallback}: ${r.error}`)
  return null
}

/** Unwrap `{data: [...]}` or a bare array. */
export function listOf<T>(json: unknown): T[] {
  if (Array.isArray(json)) return json as T[]
  const d = (json as { data?: unknown; items?: unknown } | null)?.data ?? (json as { items?: unknown } | null)?.items
  return Array.isArray(d) ? (d as T[]) : []
}
