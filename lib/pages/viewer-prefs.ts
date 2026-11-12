/**
 * A viewer's own view preferences for one artifact, such as a dashboard
 * filter. They live only in this browser's storage, keyed by viewer,
 * workspace and artifact: never sent to the server, never shared with other
 * people, and lost with the browser's site data. They stay in this browser
 * after sign-out (like composer drafts); the key keeps other accounts out. Every access is guarded, so
 * a private window or blocked storage simply means nothing is remembered.
 */
export const VIEWER_PREFS_MAX_BYTES = 4096
const MAX_KEYS = 32
const MAX_KEY = 64
const MAX_TEXT = 256

export function viewerPrefsKey(userId: string, workspaceId: string, pageId: string): string {
  return `crewship:artifact-viewer-prefs:v1:${[userId, workspaceId, pageId].map(encodeURIComponent).join(":")}`
}

const plain = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype

/** A flat object of at most 32 short keys with string, finite number, boolean or null values, 4 KiB as JSON. */
export function validViewerPrefs(value: unknown): Record<string, string | number | boolean | null> | null {
  if (!plain(value)) return null
  const entries = Object.entries(value)
  if (entries.length > MAX_KEYS) return null
  const result: Record<string, string | number | boolean | null> = {}
  for (const [key, item] of entries) {
    if (key.length < 1 || key.length > MAX_KEY || /[\u0000-\u001f\u007f-\u009f]/.test(key) || key === "__proto__" || key === "constructor" || key === "prototype") return null
    if (!(item === null || typeof item === "boolean" || (typeof item === "number" && Number.isFinite(item)) || (typeof item === "string" && item.length <= MAX_TEXT))) return null
    result[key] = item
  }
  if (new TextEncoder().encode(JSON.stringify(result)).length > VIEWER_PREFS_MAX_BYTES) return null
  return result
}

export interface ViewerPrefs {
  get(): Record<string, unknown> | null
  /** False when the browser keeps nothing (blocked storage, quota). */
  set(value: Record<string, unknown>): boolean
  clear(): boolean
}

export function createViewerPrefs(storage: () => Pick<Storage, "getItem" | "setItem" | "removeItem"> | null, key: string): ViewerPrefs {
  const store = () => { try { return storage() } catch { return null } }
  return {
    get() {
      try {
        const raw = store()?.getItem(key)
        return raw && raw.length <= VIEWER_PREFS_MAX_BYTES ? validViewerPrefs(JSON.parse(raw)) : null
      } catch { return null }
    },
    set(value) {
      const valid = validViewerPrefs(value)
      const target = store()
      if (!valid || !target) return false
      // An empty object is a reset: forget the entry rather than store "{}".
      try { if (Object.keys(valid).length) target.setItem(key, JSON.stringify(valid)); else target.removeItem(key); return true } catch { return false }
    },
    clear() {
      const target = store()
      if (!target) return false
      try { target.removeItem(key); return true } catch { return false }
    },
  }
}

/** The browser's localStorage, or null where it cannot be reached. */
export function browserStorage(): Storage | null {
  try { return typeof window === "undefined" ? null : window.localStorage } catch { return null }
}
