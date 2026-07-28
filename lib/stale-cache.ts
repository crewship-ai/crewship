/**
 * A tiny module-level cache with stale-while-revalidate.
 *
 * The Composio surface fetches four things — inventory, the toolkit catalog,
 * settings and every agent's bindings — and it fetched all four on mount. Since
 * the surface unmounts whenever you leave its tab, every visit paid the full
 * round trip again and showed skeletons for it, even when nothing had changed
 * since ten seconds ago. The catalog call in particular reaches Composio's API
 * and is the slowest of the four.
 *
 * So: reads return whatever is cached immediately, and refresh behind. A stale
 * value is a far better first paint than a skeleton, because the alternative
 * on a repeat visit is showing nothing while re-fetching data the user was
 * looking at moments ago.
 *
 * Deliberately NOT a request-dedupe library. It is ~60 lines because the only
 * behaviours needed are: serve stale, refresh once, and invalidate on write.
 */

interface Entry<T> {
  /**
   * Absent while the FIRST fetch for this key is still running. The entry
   * exists anyway, because it is what holds `inflight` — without it a cold
   * cache lets every concurrent caller fire its own request, which is exactly
   * the case where sharing matters most (a tab mounting several sections at
   * once).
   */
  value?: T
  /** epoch ms of the last successful write; 0 while there has never been one. */
  at: number
  /** In-flight fetch, so N callers in one tick share one request. */
  inflight?: Promise<T>
}

const store = new Map<string, Entry<unknown>>()

/** How long a value is served without a background refresh. */
export const DEFAULT_TTL_MS = 30_000

export interface StaleCacheResult<T> {
  /** Cached value, if any. Present even while a refresh is running. */
  value: T | undefined
  /** Resolves with the fresh value; rejects if the fetch failed. */
  fresh: Promise<T>
  /** true = `value` was served from cache rather than fetched now. */
  fromCache: boolean
}

/**
 * Read `key`, serving a cached value straight away when there is one.
 *
 * `fresh` always resolves — even on a cache hit inside the TTL, where it
 * resolves with the cached value rather than firing a request. Callers can
 * therefore always await it without having to reason about which case they are
 * in.
 */
export function readThrough<T>(
  key: string,
  fetcher: () => Promise<T>,
  ttlMs: number = DEFAULT_TTL_MS,
): StaleCacheResult<T> {
  const hit = store.get(key) as Entry<T> | undefined
  const cached = hit && hit.value !== undefined ? hit : undefined
  const age = cached ? Date.now() - cached.at : Infinity

  if (cached && age < ttlMs) {
    return { value: cached.value, fresh: Promise.resolve(cached.value as T), fromCache: true }
  }

  // One fetch per key, however many callers ask before it settles.
  if (hit?.inflight) {
    return { value: cached?.value, fresh: hit.inflight, fromCache: cached !== undefined }
  }

  const inflight = fetcher().then(
    (value) => {
      store.set(key, { value, at: Date.now() })
      return value
    },
    (err) => {
      // A failed refresh must not poison the cache: keep serving the last good
      // value, and let the next read try again. A failed FIRST fetch leaves
      // nothing behind, so the retry is a clean miss.
      if (cached) store.set(key, { value: cached.value, at: cached.at })
      else store.delete(key)
      throw err
    },
  )

  store.set(key, { value: cached?.value, at: cached?.at ?? 0, inflight })

  return { value: cached?.value, fresh: inflight, fromCache: cached !== undefined }
}

/**
 * Drop cached entries so the next read refetches.
 *
 * Call after a write. Serving a stale list right after the user connected an
 * account would show their own change missing, which reads as a failure.
 */
export function invalidate(prefix: string): void {
  for (const key of [...store.keys()]) {
    if (key.startsWith(prefix)) store.delete(key)
  }
}

/** Test seam — drops everything. */
export function clearStaleCache(): void {
  store.clear()
}
