/**
 * fetch wrapper with exponential-backoff retry for transient *gateway*
 * hiccups (502/503/504). 429 is intentionally NOT retried — when the
 * server is rate-limiting, retrying just adds requests to the same
 * leaky bucket and amplifies the back-pressure (the original symptom:
 * a tab full of canvases each retrying 4× kept the per-IP limiter
 * pinned and pushed every other panel into "loading…" forever).
 *
 * Use this only on calls where a transient flap matters (the
 * crew/agent detail loaders). Plain `fetch` is fine for everything
 * else.
 *
 * A request body that's a ReadableStream is consumed by the first
 * dispatch, so a retry with the same body would either error or send
 * an empty payload. Such requests are intentionally NOT retried —
 * callers that need retry semantics for streaming uploads must buffer
 * the payload themselves (e.g. await response.arrayBuffer()).
 */
export async function fetchWithRetry(
  input: RequestInfo | URL,
  init?: RequestInit & { retries?: number; baseDelayMs?: number },
): Promise<Response> {
  const requestedRetries = normalizeRetries(init?.retries, 2)
  // ReadableStream bodies can't be re-read after the first fetch consumes
  // them. Silently retrying would either throw "body stream already read"
  // or send an empty body to the second attempt — both worse than
  // surfacing the original failure to the caller. Check both the explicit
  // init.body AND any body attached to a Request passed as `input`, since
  // either path can deliver a stream.
  const retries =
    bodyIsReplayable(init?.body) && inputIsReplayable(input)
      ? requestedRetries
      : 0
  const baseDelay = init?.baseDelayMs ?? 250
  let lastError: unknown

  // For non-replayable bodies (ReadableStream / Request with body) we
  // skip apiFetch entirely. apiFetch owns the 401-refresh-and-retry
  // path; even though it has its own replayability gate, the round trip
  // through tryRefresh changes the failure semantics — caller gets a
  // 401 after a refresh succeeded, which looks like "session invalid"
  // when in fact the session is fine and the only problem is that the
  // body was already consumed. Plain fetch surfaces the original 401
  // unchanged so the caller can handle the consumed-stream case
  // explicitly. CodeRabbit flagged this on PR #233.
  const replayable = bodyIsReplayable(init?.body) && inputIsReplayable(input)
  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      let res: Response
      if (replayable) {
        // Lazy import keeps this module free of a `lib/api-fetch` cycle
        // (api-fetch reuses bodyIsReplayable/inputIsReplayable below).
        const { apiFetch } = await import("./api-fetch")
        res = await apiFetch(input, init)
      } else {
        res = await fetch(input, init)
      }
      if (res.ok) return res
      // Only retry true gateway flaps. 429 means "stop calling me" —
      // honor that, return immediately, let the caller surface the
      // error or simply leave the panel empty until next user action.
      // 401s are owned by apiFetch (refresh + redirect) — don't loop.
      if (![502, 503, 504].includes(res.status) || attempt === retries) {
        return res
      }
      const retryAfter = res.headers.get("Retry-After")
      const headerDelay = retryAfter ? Number(retryAfter) * 1000 : 0
      const backoff = headerDelay > 0
        ? Math.min(headerDelay, 8000)
        : baseDelay * Math.pow(2, attempt) + Math.random() * 100
      await sleep(backoff)
    } catch (err) {
      lastError = err
      if ((err as { name?: string })?.name === "AbortError") throw err
      if (attempt === retries) throw err
      await sleep(baseDelay * Math.pow(2, attempt) + Math.random() * 100)
    }
  }
  throw lastError instanceof Error ? lastError : new Error("fetchWithRetry exhausted")
}

// bodyIsReplayable returns false for body shapes that get consumed on
// first dispatch (currently just ReadableStream). Strings, Blob,
// ArrayBuffer, Uint8Array, FormData, and URLSearchParams can all be
// fetched repeatedly because the runtime re-reads from their backing
// memory on each call.
export function bodyIsReplayable(body: BodyInit | null | undefined): boolean {
  if (body == null) return true
  if (typeof ReadableStream !== "undefined" && body instanceof ReadableStream) {
    return false
  }
  return true
}

// inputIsReplayable mirrors bodyIsReplayable but for the `input` argument:
// when callers pass `new Request(url, { body: stream })` the stream lives on
// the Request object and isn't visible via init.body. Treat any Request
// carrying a non-null body as not safely replayable.
export function inputIsReplayable(input: RequestInfo | URL): boolean {
  if (typeof Request !== "undefined" && input instanceof Request) {
    return input.body == null
  }
  return true
}

// normalizeRetries clamps the caller-supplied retry count to a non-negative
// integer. Without this, NaN/negative values from a typo or runtime coercion
// would skip the loop entirely (because `attempt <= retries` is false for
// NaN/negative on first iteration) and the function would throw the
// exhaustion error before the initial fetch was ever attempted.
function normalizeRetries(value: number | undefined, fallback: number): number {
  if (value == null || !Number.isFinite(value)) return fallback
  return Math.max(0, Math.trunc(value))
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
