import { createAvatar, type Style } from "@dicebear/core"
// Only the DEFAULT style is imported eagerly. The other nine DiceBear
// collections used to be static imports here, which pulled every
// collection (~30–100 KB each, multi-hundred-KB total) into the shared
// app-shell chunk even though any given avatar renders exactly one
// style — and the vast majority of agents use the default. They now
// load on demand via dynamic import (see STYLE_LOADERS below).
import * as botttsNeutral from "@dicebear/bottts-neutral"

/** Default avatar style used when an agent has no explicit style set. */
export const DEFAULT_AVATAR_STYLE = "bottts-neutral"

/**
 * Lazy loaders for every non-default DiceBear collection, keyed by style
 * slug. Each fires a code-split dynamic import the first time the style
 * is actually rendered (an agent configured with it scrolls into view,
 * or the avatar picker opens).
 */
const STYLE_LOADERS: Record<string, () => Promise<{ default?: unknown } & object>> = {
  adventurer: () => import("@dicebear/adventurer"),
  "fun-emoji": () => import("@dicebear/fun-emoji"),
  "pixel-art": () => import("@dicebear/pixel-art"),
  micah: () => import("@dicebear/micah"),
  notionists: () => import("@dicebear/notionists"),
  thumbs: () => import("@dicebear/thumbs"),
  lorelei: () => import("@dicebear/lorelei"),
  "big-smile": () => import("@dicebear/big-smile"),
  avataaars: () => import("@dicebear/avataaars"),
}

/**
 * Map of available DiceBear avatar styles, keyed by style slug. Labels
 * only — the style implementations live behind STYLE_LOADERS so pickers
 * can enumerate keys/labels without forcing every collection to load.
 */
export const AVATAR_STYLES: Record<string, { label: string }> = {
  "bottts-neutral": { label: "Robots" },
  adventurer: { label: "Adventurer" },
  "fun-emoji": { label: "Fun Emoji" },
  "pixel-art": { label: "Pixel Art" },
  micah: { label: "Micah" },
  notionists: { label: "Notionists" },
  thumbs: { label: "Thumbs" },
  lorelei: { label: "Lorelei" },
  "big-smile": { label: "Big Smile" },
  avataaars: { label: "Avataaars" },
}

// Styles whose implementation is resident. The default is loaded from
// the start; lazies join as their imports resolve.
const _loadedStyles = new Map<string, Style<object>>([
  [DEFAULT_AVATAR_STYLE, botttsNeutral as unknown as Style<object>],
])
const _pendingStyles = new Set<string>()

// ─── change notification ──────────────────────────────────────────────
//
// getAgentAvatarUrl stays synchronous: while a style implementation is
// in flight it returns a deterministic placeholder, and when the import
// resolves the version below bumps so subscribed components re-render
// and pick up the real avatar. Consumed via useAvatarStylesVersion
// (useSyncExternalStore) in components that render non-default styles.

let _stylesVersion = 0
const _listeners = new Set<() => void>()

/** Subscribe to style-load events. Returns an unsubscribe function. */
export function subscribeAvatarStyles(listener: () => void): () => void {
  _listeners.add(listener)
  return () => _listeners.delete(listener)
}

/** Monotonic counter bumped whenever a lazy style finishes loading. */
export function avatarStylesVersion(): number {
  return _stylesVersion
}

function notifyStylesChanged(): void {
  _stylesVersion++
  for (const l of _listeners) l()
}

/**
 * Kick off (or join) the dynamic import for a style. Resolves when the
 * style is resident. Safe to call for already-loaded or unknown styles.
 * Pickers can call this for all keys on open to warm the grid.
 */
export function preloadAvatarStyle(styleName: string): Promise<void> {
  if (_loadedStyles.has(styleName)) return Promise.resolve()
  const loader = STYLE_LOADERS[styleName]
  if (!loader) return Promise.resolve()
  _pendingStyles.add(styleName)
  return loader()
    .then((mod) => {
      _loadedStyles.set(styleName, mod as unknown as Style<object>)
      _pendingStyles.delete(styleName)
      // Drop any placeholder URIs cached under this style so the next
      // render regenerates the real avatar.
      for (const key of _avatarCache.keys()) {
        if (key.startsWith(styleName + ":")) _avatarCache.delete(key)
      }
      notifyStylesChanged()
    })
    .catch(() => {
      // Import failed (offline chunk fetch, etc.) — leave the
      // placeholder in place; a later call may retry.
      _pendingStyles.delete(styleName)
    })
}

/**
 * Hard cap on the in-memory avatar cache. Each entry is a DiceBear data
 * URI — typically 4–8 KB of base64-encoded SVG — so 500 entries works
 * out to ~2–4 MB. Plenty for normal browsing (the picker tops out at
 * ~50 visible cards plus history); the cap exists to bound the worst
 * case where a user scrolls a long agent list with diverse style mixes
 * and the previous unbounded Map would have crept toward double-digit
 * megabytes per tab.
 */
const AVATAR_CACHE_MAX_ENTRIES = 500

// Map iteration order in JS is guaranteed to be insertion order
// (ECMAScript 2015 §23.1), so we lean on delete+set to bump a hit to
// the freshest slot and rely on the first-inserted key as the LRU
// victim. No allocation per hit beyond Map's internal bookkeeping.
const _avatarCache = new Map<string, string>()

/**
 * Deterministic background colour for an initials/glyph avatar, derived
 * from a seed string (author name, agent slug, …). Same seed always maps
 * to the same hue, so a given author keeps a stable colour across rows
 * without persisting any per-entity colour. Returns an `hsl(...)` string
 * suitable for an inline `background` style.
 *
 * Deduped from the identical `avatarColor` helper that used to live in
 * `comments-tab.tsx`; kept here next to getAgentAvatarUrl so all
 * seed→avatar mapping lives in one module.
 */
export function seedColor(seed: string): string {
  let h = 0
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) % 360
  return `hsl(${h} 55% 45%)`
}

/**
 * Deterministic stand-in rendered while a lazy style implementation is
 * still downloading: a soft seed-coloured disc. Same seed → same
 * placeholder, so lists don't flicker between renders.
 */
function placeholderAvatarUri(seed: string): string {
  const color = seedColor(seed)
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128">` +
    `<rect width="128" height="128" rx="24" fill="${color}" opacity="0.35"/>` +
    `<circle cx="64" cy="64" r="28" fill="${color}" opacity="0.6"/>` +
    `</svg>`
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`
}

/**
 * Generate a DiceBear avatar data URI for an agent. Results are cached
 * in a bounded LRU keyed by (style, seed); see AVATAR_CACHE_MAX_ENTRIES.
 *
 * Synchronous by design: the default style renders immediately; a
 * not-yet-loaded lazy style returns a deterministic placeholder, starts
 * the dynamic import, and bumps avatarStylesVersion() when the real
 * collection arrives (subscribe via useAvatarStylesVersion /
 * subscribeAvatarStyles to re-render).
 *
 * @param seed - Deterministic seed for avatar generation (typically the agent slug).
 * @param styleName - Avatar style key from AVATAR_STYLES; defaults to bottts-neutral.
 */
export function getAgentAvatarUrl(seed: string, styleName?: string | null): string {
  // Canonicalize unknown / null style names to DEFAULT_AVATAR_STYLE
  // BEFORE building the cache key. Otherwise three callers passing
  // "robots", "robot", and undefined (all of which fall back to the
  // default in the lookup below) would each spawn a separate cache
  // entry for the same generated URI, eroding the LRU's effective
  // working set on a misconfigured caller.
  const resolvedStyle =
    styleName && AVATAR_STYLES[styleName] ? styleName : DEFAULT_AVATAR_STYLE
  const key = `${resolvedStyle}:${seed}`
  const cached = _avatarCache.get(key)
  if (cached !== undefined) {
    // Bump the hit to the most-recently-used slot. Without this we'd
    // be doing FIFO not LRU, which thrashes when a small working set
    // sits adjacent to a long tail of one-off lookups.
    _avatarCache.delete(key)
    _avatarCache.set(key, cached)
    return cached
  }

  const style = _loadedStyles.get(resolvedStyle)
  let uri: string
  if (style) {
    uri = createAvatar(style, { seed, size: 128 }).toDataUri()
  } else {
    // Style implementation not resident yet: kick off the lazy import
    // and cache the placeholder under the same key — preloadAvatarStyle
    // purges the style's entries when the real collection lands.
    void preloadAvatarStyle(resolvedStyle)
    uri = placeholderAvatarUri(seed)
  }

  if (_avatarCache.size >= AVATAR_CACHE_MAX_ENTRIES) {
    // Evict the oldest entry — Map's iterator yields keys in
    // insertion order so the first .keys().next() is the LRU victim.
    const oldest = _avatarCache.keys().next().value
    if (oldest !== undefined) {
      _avatarCache.delete(oldest)
    }
  }
  _avatarCache.set(key, uri)
  return uri
}

/**
 * Raw SVG markup for an agent avatar, or null when the style's collection
 * isn't resident yet.
 *
 * Distinct from getAgentAvatarUrl in two ways that both matter to the
 * persistence path (see lib/agent-avatar-persist.ts): it returns the markup
 * rather than a data URI, and it returns **null** instead of a placeholder
 * disc when the lazy import is still in flight. Persisting a placeholder
 * would freeze the wrong picture forever, and because storing is write-once
 * server-side, there'd be no second chance to get it right.
 *
 * Not cached: this is called once per agent per session at most, whereas
 * getAgentAvatarUrl runs on every render.
 *
 * @param seed - Deterministic seed for avatar generation (typically the agent slug).
 * @param styleName - Avatar style key from AVATAR_STYLES; defaults to bottts-neutral.
 */
export function getAgentAvatarSVG(seed: string, styleName?: string | null): string | null {
  const resolvedStyle =
    styleName && AVATAR_STYLES[styleName] ? styleName : DEFAULT_AVATAR_STYLE
  const style = _loadedStyles.get(resolvedStyle)
  if (!style) {
    // Kick the import so a later attempt (next mount, next session) finds
    // the collection resident and can persist the real thing.
    void preloadAvatarStyle(resolvedStyle)
    return null
  }
  return createAvatar(style, { seed, size: 128 }).toString()
}

/**
 * Test-only escape hatch. Clears the avatar cache so a unit test can
 * observe eviction behaviour without inheriting another test's hits.
 * Not exported through the package index — only the test file imports
 * agent-avatar.ts directly.
 */
export function _resetAvatarCacheForTest(): void {
  _avatarCache.clear()
}

/** Test-only inspector for the cache size. See _resetAvatarCacheForTest. */
export function _avatarCacheSizeForTest(): number {
  return _avatarCache.size
}
