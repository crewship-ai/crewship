"use client"

import { useCallback, useRef } from "react"
import { useMutation, useQueryClient, type QueryKey } from "@tanstack/react-query"

import { apiFetch, type ApiFetchInit } from "@/lib/api-fetch"
import { readApiError, readApiErrorDetail } from "@/lib/api-error"

/**
 * The one shared executor behind every write button in the app (PRD
 * docs/prd/pages.md §8b.5).
 *
 * `apiFetch` resolves on 4xx/5xx — it only rejects on transport failure.
 * Issue #1563 was four mutations that toasted success for a write the
 * server had refused. The fix
 * (`components/features/orchestration/task-actions.ts:18-73`) established
 * four rules by hand, once, for one call site:
 *
 *   1. check `res.ok` BEFORE any success toast / cache update
 *   2. say the server's own words (`readApiError`), falling back to a
 *      `(HTTP <status>)` string when the body carries none
 *   3. never destroy state a retry needs on a refusal
 *   4. `catch` covers transport failure only — status handling happens
 *      first, so a 500 and a network error are never conflated
 *
 * There was no lint rule and no shared helper enforcing those — every new
 * button re-implements `useState(busy)` + try/catch/finally, and any one
 * of them can reintroduce the bug. `useApiMutation` is that helper: it
 * makes the four rules the only path, not a convention someone has to
 * remember.
 *
 * Built on TanStack Query's `useMutation` — the documented canonical data
 * layer (CONTRIBUTING.md:156-182, `hooks/use-inbox.ts:180-252`) — rather
 * than a bespoke state machine, so callers already fluent in
 * `isPending` / `data` / `error` from the query hooks get the same shape
 * here, and cache invalidation goes through the one queryClient everything
 * else uses.
 *
 * On top of the four rules this also gives every caller, for free, the
 * three server-supported behaviours nothing in the app used yet:
 *
 *   - an `Idempotency-Key` header (Stripe pattern; server side is
 *     `internal/pipeline/idempotency.go`, 24h TTL), minted once per
 *     logical click via `mutate()`/`mutateAsync()` and REUSED across
 *     `retry()`/`retryAsync()` of that same click. A new `mutate()` call
 *     always mints a fresh key — only an explicit retry of the last click
 *     reuses one.
 *   - 429 + `Retry-After` surfaced as a first-class "already running"
 *     outcome (`data.kind === "already-running"`), not a generic error —
 *     the same "graceful resolution, not a failure" treatment
 *     `isAlreadyDecidedError` gives a 409/410 in `lib/api/waitpoints.ts`.
 *     Nothing invalidates on it, because nothing changed.
 *   - 202 Accepted treated as a real, distinct outcome
 *     (`data.kind === "accepted"`): a success that is NOT a completion.
 *     The parsed body is handed back so the caller can pull out whatever
 *     pending/run id the endpoint returns and go watch it.
 *
 * What this hook CANNOT enforce, and still relies on discipline at the
 * call site:
 *
 *   - Toasting itself. It deliberately does not import `sonner` — a
 *     hook that always toasts can't be used for a mutation the caller
 *     wants to report some other way (inline banner, optimistic row,
 *     silent background write). `onOk` / `onAccepted` / `onAlreadyRunning`
 *     / `onError` hand back exactly the pieces rule 2 promises (the
 *     server's message, the status, the retry-after) — but a call site
 *     that ignores `onError` and toasts `onOk` unconditionally can still
 *     reintroduce the bug at ITS layer. `task-actions.ts`-style call
 *     sites remain the worked example.
 *   - `invalidateKeys` is a fixed list, not a function of the outcome
 *     (mirroring `hooks/use-inbox.ts`'s in-place `setQueryData` being
 *     the exception, not the rule). A caller needing outcome-dependent
 *     invalidation reaches for `onOk`/`onAccepted` and calls
 *     `useQueryClient()` itself; this hook still won't invalidate
 *     anything on a refusal or a 429, since that decision is made
 *     before either callback runs.
 *   - Whether the caller's `request()` builds the RIGHT request (method,
 *     body, URL). The hook only owns what happens to the Response.
 *   - Migrating existing call sites — out of scope for this change.
 */

/** Thrown for a genuine refusal: any non-2xx status other than 429, which
 *  is handled as `already-running` (see below) rather than thrown. Never
 *  thrown for a transport failure — `apiFetch`/`fetch` rejecting (network
 *  error, abort, DNS) propagates as whatever `fetch` throws, so
 *  `error instanceof ApiMutationError` is exactly rule 4's "catch covers
 *  transport failure only" made checkable. */
export class ApiMutationError extends Error {
  readonly status: number
  /**
   * The parsed refusal body, when there was one.
   *
   * Optional and untyped on purpose: almost every caller wants the message and
   * nothing else, and the handful that need more — the page import's 422 lists
   * every reference it could not bind — know the shape of their own endpoint's
   * refusal and can narrow it themselves.
   */
  readonly body: unknown
  constructor(message: string, status: number, body?: unknown) {
    super(message)
    this.name = "ApiMutationError"
    this.status = status
    this.body = body
  }
}

/** The "already running" outcome for a 429. Not an error — resolves
 *  through the same success path as ok/accepted, just without invalidating
 *  anything, because the server didn't do anything new. */
export interface AlreadyRunningOutcome {
  kind: "already-running"
  status: 429
  /** Parsed from the `Retry-After` header (seconds). `null` when the
   *  header is absent or not a plain integer (an HTTP-date form, which
   *  no endpoint in this app emits today). */
  retryAfterSeconds: number | null
  message: string
}

/** A write the server accepted. `kind: "accepted"` (HTTP 202) is a
 *  success that is NOT a completion — `data` is whatever pending/run id
 *  shape the endpoint returns for the caller to go watch. `kind: "ok"`
 *  covers every other 2xx. */
export interface WriteOutcome<TData> {
  kind: "ok" | "accepted"
  status: number
  data: TData
}

export type ApiMutationOutcome<TData> = WriteOutcome<TData> | AlreadyRunningOutcome

export interface ApiMutationRequest {
  input: RequestInfo | URL
  init?: ApiFetchInit
}

export interface UseApiMutationOptions<TVariables, TData = unknown> {
  /** Builds the request for this attempt. Called on every attempt
   *  (`mutate()` and every `retry()`), so caller-side data is always
   *  fresh, but `idempotencyKey` is only ever a NEW value on `mutate()` —
   *  a `retry()` of the same click passes the same key back in. */
  request: (variables: TVariables, idempotencyKey: string) => ApiMutationRequest
  /** Parses the body of a response already known to be a successful
   *  write (2xx, not 429). Defaults to JSON-or-undefined, tolerating an
   *  empty body (204, or a 202 with none) instead of throwing. */
  parse?: (res: Response) => Promise<TData>
  /** Invalidated ONLY after `kind: "ok" | "accepted"` — never on a
   *  refusal, never on `already-running`. This is rule 1 and rule 3 made
   *  structural: there is no code path from a non-ok response to this
   *  list. */
  invalidateKeys?: QueryKey[]
  onOk?: (data: TData, variables: TVariables) => void
  onAccepted?: (data: TData, variables: TVariables) => void
  onAlreadyRunning?: (outcome: AlreadyRunningOutcome, variables: TVariables) => void
  /** Fires for a genuine refusal (`ApiMutationError`) or a transport
   *  failure — never for `already-running`, which is not an error. */
  onError?: (error: unknown, variables: TVariables) => void
}

export interface UseApiMutationResult<TVariables, TData> {
  /** Starts a NEW logical click: mints a fresh idempotency key and
   *  remembers `variables` for a possible `retry()`. A no-op — reusing the
   *  in-flight promise rather than issuing a second request — while a
   *  previous call from this hook instance hasn't settled yet, which is
   *  what makes double-submit impossible regardless of how fast the UI
   *  re-renders `isPending`. */
  mutate: (variables: TVariables) => void
  mutateAsync: (variables: TVariables) => Promise<ApiMutationOutcome<TData>>
  /** Re-sends the LAST `mutate()`/`retry()` call with the SAME
   *  idempotency key (the Stripe pattern). A no-op returning a resolved
   *  `undefined` if nothing has been attempted yet, and the same
   *  in-flight dedupe as `mutate()` while a call is pending. */
  retry: () => void
  retryAsync: () => Promise<ApiMutationOutcome<TData> | undefined>
  isPending: boolean
  /** The last settled non-error outcome: "ok", "accepted", or
   *  "already-running". Undefined until the first attempt settles without
   *  throwing. */
  data: ApiMutationOutcome<TData> | undefined
  /** Set only for a genuine refusal (`ApiMutationError`) or a transport
   *  failure. `undefined` otherwise — including after an "already-running"
   *  outcome, which is not an error and lives in `data`. */
  error: unknown
  isAlreadyRunning: boolean
  reset: () => void
}

interface Click<TVariables> {
  variables: TVariables
  idempotencyKey: string
}

/** Same fallback `hooks/use-chat.ts`'s `uuid()` uses: `crypto.randomUUID`
 *  is unavailable in non-secure (HTTP) contexts, so a plain-dev HTTP
 *  origin still gets a usable — if not cryptographically random —
 *  v4-shaped id rather than a thrown exception on every click. */
function newIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16)
  })
}

/** Tolerates an empty body (204, or a 202 with none) instead of letting
 *  `res.json()` throw a parse error over a response that was already a
 *  legitimate success. */
async function defaultParse<TData>(res: Response): Promise<TData> {
  const text = await res.text()
  if (!text) return undefined as TData
  try {
    return JSON.parse(text) as TData
  } catch {
    return undefined as TData
  }
}

export function useApiMutation<TVariables = void, TData = unknown>(
  options: UseApiMutationOptions<TVariables, TData>,
): UseApiMutationResult<TVariables, TData> {
  const qc = useQueryClient()
  // Read through a ref so callers passing a fresh options object/inline
  // callbacks on every render don't need to be memoized to get correct
  // behaviour — only the LATEST request()/parse()/callbacks are ever used,
  // same as the ref pattern `lib/api-fetch.ts`'s own module state avoids
  // needing.
  const optionsRef = useRef(options)
  optionsRef.current = options

  const lastClickRef = useRef<Click<TVariables> | null>(null)
  // The double-submit guard. `mutation.isPending` is authoritative for the
  // UI but flips one React render after the click that triggers it — late
  // enough for a fast double-click (or two buttons sharing one hook
  // instance) to slip a second request out before the first re-render
  // lands. This ref is set/cleared synchronously around the actual call.
  const inFlightRef = useRef<Promise<ApiMutationOutcome<TData>> | null>(null)
  // EVERY in-flight click, not just the most recent one.
  //
  // A single ref was the first fix and it had two holes of its own: with
  // A(x) → B(y) → A(x) the ref held B by the time the third call arrived, so x
  // went twice; and if B settled first it cleared the shared promise ref while
  // A was still in flight, after which nothing was guarded at all. A list keyed
  // by the click's own variables has neither.
  const inFlightClicksRef = useRef<Array<{ click: Click<TVariables>; p: Promise<ApiMutationOutcome<TData>> }>>([])

  const mutation = useMutation<ApiMutationOutcome<TData>, unknown, Click<TVariables>>({
    // This hook owns retrying explicitly via retry()/retryAsync(), which
    // reuses the idempotency key on purpose. TanStack's own automatic
    // retry would re-invoke mutationFn with a NEW attempt that still
    // carries the same Click — harmless for idempotency, but it would
    // retry a genuine 4xx refusal (e.g. a 403) against a server that will
    // never say yes, which is exactly the "don't destroy state, but don't
    // pretend a refusal might resolve itself" middle rule 3 implies.
    retry: false,
    mutationFn: async ({ variables, idempotencyKey }) => {
      const { input, init } = optionsRef.current.request(variables, idempotencyKey)
      const headers = new Headers(init?.headers)
      headers.set("Idempotency-Key", idempotencyKey)
      // apiFetch resolves on 4xx/5xx (that's the whole premise of this
      // hook) and only rejects on transport failure — a rejection here
      // propagates out of mutationFn untouched, so it lands in `error` as
      // something that is NOT an ApiMutationError. That is rule 4.
      const res = await apiFetch(input, { ...init, headers })

      // Rule 1, structurally: every branch below runs BEFORE anything is
      // reported as success, and only the res.ok branch can produce a
      // "kind" that onSuccess is willing to invalidate on.
      if (res.status === 429) {
        const retryAfterHeader = res.headers.get("Retry-After")
        const parsedRetryAfter = retryAfterHeader === null ? Number.NaN : Number(retryAfterHeader)
        const message = await readApiError(res, "Already running (HTTP 429)")
        const outcome: AlreadyRunningOutcome = {
          kind: "already-running",
          status: 429,
          retryAfterSeconds: Number.isFinite(parsedRetryAfter) ? parsedRetryAfter : null,
          message,
        }
        return outcome
      }
      if (!res.ok) {
        // Rule 2: the server's own words, falling back to "(HTTP
        // <status>)" — never a made-up client-side guess at why.
        const { message, body } = await readApiErrorDetail(
          res,
          `Request failed (HTTP ${res.status})`,
        )
        throw new ApiMutationError(message, res.status, body)
      }
      const parse = optionsRef.current.parse ?? defaultParse<TData>
      const data = await parse(res)
      return { kind: res.status === 202 ? "accepted" : "ok", status: res.status, data }
    },
    onSuccess: (outcome, { variables }) => {
      if (outcome.kind === "already-running") {
        // 429 changed nothing server-side: no invalidation, and this is
        // explicitly NOT routed through onError — the caller asked to
        // treat it as "already running", not as a failure.
        optionsRef.current.onAlreadyRunning?.(outcome, variables)
        return
      }
      // Only a write the server accepted (200-series or 202) ever
      // invalidates a query key.
      for (const key of optionsRef.current.invalidateKeys ?? []) {
        qc.invalidateQueries({ queryKey: key })
      }
      if (outcome.kind === "accepted") {
        optionsRef.current.onAccepted?.(outcome.data, variables)
      } else {
        optionsRef.current.onOk?.(outcome.data, variables)
      }
    },
    onError: (error, { variables }) => {
      optionsRef.current.onError?.(error, variables)
    },
  })
  const { mutateAsync: mutationMutateAsync } = mutation

  const run = useCallback(
    (click: Click<TVariables>): Promise<ApiMutationOutcome<TData>> => {
      // Dedupe the SAME click, not merely "a click while one is pending".
      //
      // This guard exists for the double-submit — one button, pressed twice
      // before `isPending` flips. Keyed on "something is in flight" it also
      // swallowed a DIFFERENT mutation from a shared hook instance: granting
      // alice and then bob issued one request, and the second caller's
      // `onOk` fired with the FIRST one's data and variables. The call site
      // reported success for a write that was never sent, and a later
      // `retry()` replayed alice.
      //
      // On a variables comparison that cannot be made, dedupe — which is the
      // behaviour this guard always had. `mutate` mints a fresh idempotency
      // key per call, so issuing on doubt means two real writes, and a
      // duplicated write is worse than a UI that has to be clicked again.
      const twin = inFlightClicksRef.current.find((c) => sameVariables(c.click, click))
      if (twin) return twin.p
      lastClickRef.current = click
      const p = mutationMutateAsync(click).finally(() => {
        inFlightClicksRef.current = inFlightClicksRef.current.filter((c) => c.p !== p)
        if (inFlightRef.current === p) inFlightRef.current = null
      })
      inFlightClicksRef.current = [...inFlightClicksRef.current, { click, p }]
      inFlightRef.current = p
      return p
    },
    [mutationMutateAsync],
  )

  const mutateAsync = useCallback(
    (variables: TVariables) => run({ variables, idempotencyKey: newIdempotencyKey() }),
    [run],
  )

  const retryAsync = useCallback((): Promise<ApiMutationOutcome<TData> | undefined> => {
    const last = lastClickRef.current
    if (!last) return Promise.resolve(undefined)
    return run(last)
  }, [run])

  const mutate = useCallback(
    (variables: TVariables) => {
      mutateAsync(variables).catch(() => {
        // Swallowed here on purpose: onError above already ran, and
        // mutate() (unlike mutateAsync()) is the fire-and-forget form —
        // matching useMutation's own mutate()/mutateAsync() split, an
        // unhandled rejection here would just be React Query's own
        // resolved error re-thrown for no listener.
      })
    },
    [mutateAsync],
  )

  const retry = useCallback(() => {
    retryAsync().catch(() => {
      // Same rationale as mutate() above.
    })
  }, [retryAsync])

  const reset = useCallback(() => {
    lastClickRef.current = null
    mutation.reset()
  }, [mutation])

  return {
    mutate,
    mutateAsync,
    retry,
    retryAsync,
    isPending: mutation.isPending,
    data: mutation.data,
    error: mutation.isError ? mutation.error : undefined,
    isAlreadyRunning: mutation.data?.kind === "already-running",
    reset,
  }
}

/** Whether two clicks carry the same inputs, for the in-flight dedupe.
 *
 * Structural rather than referential: a call site that rebuilds its variables
 * object on every render — most of them — would otherwise never dedupe, and the
 * double-submit guard would be gone.
 *
 * An unserialisable value (a cycle, a BigInt) returns TRUE: the guard's
 * historical behaviour was to collapse, and collapsing on doubt costs a click
 * while issuing on doubt costs a duplicate write. */
function sameVariables<T>(a: Click<T> | null, b: Click<T>): boolean {
  if (!a) return false
  if (a.variables === b.variables) return true
  try {
    return JSON.stringify(a.variables) === JSON.stringify(b.variables)
  } catch {
    return true
  }
}
