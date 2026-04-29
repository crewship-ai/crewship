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
 */
export async function fetchWithRetry(
  input: RequestInfo | URL,
  init?: RequestInit & { retries?: number; baseDelayMs?: number },
): Promise<Response> {
  const retries = init?.retries ?? 2
  const baseDelay = init?.baseDelayMs ?? 250
  let lastError: unknown

  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      const res = await fetch(input, init)
      if (res.ok) return res
      // Only retry true gateway flaps. 429 means "stop calling me" —
      // honor that, return immediately, let the caller surface the
      // error or simply leave the panel empty until next user action.
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

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
