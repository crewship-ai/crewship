"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import type { ZodType } from "zod"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Generic read-only data-fetch hook that consolidates the
 * `{data, loading, error, notConfigured}` + reqId race-guard + 404→
 * notConfigured + `HTTP ${status}` + zod `safeParse→fallback` +
 * `catch→"Network error"` block that was hand-copied across paymaster,
 * the crew panels, and friends.
 *
 * Built on `apiFetch` (same-origin /api/* with refresh-on-401). The
 * behaviour is intentionally identical to the bespoke copies it replaces:
 *
 *   - 404 → `notConfigured=true` (or an error, via `on404: "error"`).
 *   - non-2xx → `error = "HTTP <status>"`.
 *   - rejected fetch → `error = "Network error"`.
 *   - schema parse failure → `data = fallback` when a `fallback` is given,
 *     otherwise the previous `data` is kept untouched.
 *   - every response is stamped with a monotonic request id so a slow
 *     response from an earlier url/range can't clobber a newer one.
 *
 * Pass `url = null` (or `enabled: false`) to disable the hook; a disabled
 * hook drops `loading` to false without firing a request.
 */

export interface UseApiResourceState<T> {
  data: T | null
  loading: boolean
  error: string | null
  notConfigured: boolean
}

export interface UseApiResourceOptions<T> {
  /** Zod schema validated against the JSON body. Omit to cast the body
   *  to `T` directly (no validation), mirroring the components that did
   *  `(await res.json()) as Foo[]`. */
  schema?: ZodType<T>
  /** Value assigned to `data` when schema parse fails. When omitted, the
   *  previous `data` is preserved on a parse failure. */
  fallback?: T
  /** Default true. When false the hook fires no request and reports
   *  `loading=false`. */
  enabled?: boolean
  /** Poll interval in ms. 0 (default) disables polling. */
  pollMs?: number
  /** What a 404 means. "notConfigured" (default) flips `notConfigured`;
   *  "error" surfaces `HTTP 404` like any other non-2xx. */
  on404?: "notConfigured" | "error"
  /** When true, transient errors (non-2xx, network throw, 404) leave the
   *  existing `data` in place instead of clearing it to null. Used by the
   *  crew panels, which fail silently and keep the last good list. */
  keepDataOnError?: boolean
  /** When the hook is disabled, reset state to a clean idle snapshot
   *  (`data=null`) instead of merely dropping `loading`. */
  resetOnDisable?: boolean
  /** Bump to force a refetch without changing `url`. */
  reloadKey?: number
}

export interface UseApiResourceResult<T> extends UseApiResourceState<T> {
  /** Imperative refetch. `{ silent: true }` refetches without flipping
   *  `loading` (used for realtime-driven background refreshes that must
   *  not flash a spinner). */
  reload: (opts?: { silent?: boolean }) => Promise<void>
}

const INITIAL = { data: null, loading: true, error: null, notConfigured: false }

export function useApiResource<T>(
  url: string | null,
  opts: UseApiResourceOptions<T> = {},
): UseApiResourceResult<T> {
  const {
    enabled = true,
    pollMs = 0,
    reloadKey = 0,
  } = opts

  const [state, setState] = useState<UseApiResourceState<T>>({ ...INITIAL } as UseApiResourceState<T>)
  const reqIdRef = useRef(0)

  // Mutable policy + url held in refs so the fetch closure can stay stable
  // (empty dep list) and so changing schema/fallback/flags — which are new
  // object identities on every render — does NOT retrigger a fetch. The
  // bespoke copies keyed their effect only on url-shaping inputs, never on
  // the (module-constant) schema; refs reproduce that exactly.
  const urlRef = useRef(url)
  const enabledRef = useRef(enabled)
  const schemaRef = useRef(opts.schema)
  const fallbackRef = useRef(opts.fallback)
  const on404Ref = useRef(opts.on404 ?? "notConfigured")
  const keepDataRef = useRef(opts.keepDataOnError ?? false)
  const resetOnDisableRef = useRef(opts.resetOnDisable ?? false)
  urlRef.current = url
  enabledRef.current = enabled
  schemaRef.current = opts.schema
  fallbackRef.current = opts.fallback
  on404Ref.current = opts.on404 ?? "notConfigured"
  keepDataRef.current = opts.keepDataOnError ?? false
  resetOnDisableRef.current = opts.resetOnDisable ?? false

  const runFetch = useCallback(async ({ silent = false }: { silent?: boolean } = {}) => {
    const currentUrl = urlRef.current
    if (!enabledRef.current || currentUrl == null) {
      // Disabled: never bump reqId (we aren't firing). Either reset to a
      // clean snapshot or just drop the spinner, preserving prior data.
      if (resetOnDisableRef.current) {
        setState({ data: null, loading: false, error: null, notConfigured: false })
      } else {
        setState((s) => ({ ...s, loading: false }))
      }
      return
    }

    const reqId = ++reqIdRef.current
    if (!silent) setState((s) => ({ ...s, loading: true, error: null }))

    try {
      const res = await apiFetch(currentUrl)
      if (reqIdRef.current !== reqId) return

      if (res.status === 404) {
        if (on404Ref.current === "notConfigured") {
          setState((s) => ({
            data: keepDataRef.current ? s.data : null,
            loading: false,
            error: null,
            notConfigured: true,
          }))
        } else {
          setState((s) => ({
            data: keepDataRef.current ? s.data : null,
            loading: false,
            error: `HTTP ${res.status}`,
            notConfigured: false,
          }))
        }
        return
      }

      if (!res.ok) {
        setState((s) => ({
          data: keepDataRef.current ? s.data : null,
          loading: false,
          error: `HTTP ${res.status}`,
          notConfigured: false,
        }))
        return
      }

      const json = await res.json()
      if (reqIdRef.current !== reqId) return

      const schema = schemaRef.current
      if (schema) {
        const parsed = schema.safeParse(json)
        if (reqIdRef.current !== reqId) return
        if (!parsed.success) {
          const fb = fallbackRef.current
          setState((s) => ({
            data: fb !== undefined ? fb : s.data,
            loading: false,
            error: null,
            notConfigured: false,
          }))
          return
        }
        setState({ data: parsed.data, loading: false, error: null, notConfigured: false })
        return
      }

      setState({ data: json as T, loading: false, error: null, notConfigured: false })
    } catch {
      if (reqIdRef.current === reqId) {
        setState((s) => ({
          data: keepDataRef.current ? s.data : null,
          loading: false,
          error: "Network error",
          notConfigured: false,
        }))
      }
    }
  }, [])

  // Initial fetch + refetch whenever the url, enabled flag, or reloadKey
  // changes. runFetch is stable so this effect is keyed purely on those.
  useEffect(() => {
    void runFetch()
  }, [url, enabled, reloadKey, runFetch])

  // Optional polling. Silent so a background refresh never flashes the
  // spinner over already-rendered data.
  useEffect(() => {
    if (!pollMs || pollMs <= 0 || !enabled || url == null) return
    const id = setInterval(() => {
      void runFetch({ silent: true })
    }, pollMs)
    return () => clearInterval(id)
  }, [pollMs, enabled, url, runFetch])

  const reload = useCallback(
    (o?: { silent?: boolean }) => runFetch(o),
    [runFetch],
  )

  return { ...state, reload }
}
