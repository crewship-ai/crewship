/**
 * fetch wrapper with exponential-backoff retry for transient HTTP errors
 * (429 rate limit, 502/503/504 gateway flaps).
 *
 * The original use case: the /api/v1/crews/{id} detail endpoint
 * occasionally returns 429 when the user navigates fast between crews
 * (each crew triggers a list + detail call back-to-back). Hard-failing
 * the canvas with "Could not load crew (429)" is worse than waiting
 * 250ms and retrying.
 *
 * Retries only on 429/502/503/504. Other HTTP errors (4xx user-fault)
 * are returned immediately — no point retrying a 404 or 401.
 */
export async function fetchWithRetry(
  input: RequestInfo | URL,
  init?: RequestInit & { retries?: number; baseDelayMs?: number },
): Promise<Response> {
  const retries = init?.retries ?? 3
  const baseDelay = init?.baseDelayMs ?? 250
  let lastError: unknown

  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      const res = await fetch(input, init)
      if (res.ok) return res
      // Retry only on transient server-side hiccups.
      if (![429, 502, 503, 504].includes(res.status) || attempt === retries) {
        return res
      }
      // Honor Retry-After if the server set it (seconds, integer-ish).
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
  // Unreachable in practice, but TypeScript needs a return path.
  throw lastError instanceof Error ? lastError : new Error("fetchWithRetry exhausted")
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
