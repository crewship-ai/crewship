import { createAvatar, type Style } from "@dicebear/core"
import * as botttsNeutral from "@dicebear/bottts-neutral"
import * as adventurer from "@dicebear/adventurer"
import * as funEmoji from "@dicebear/fun-emoji"
import * as pixelArt from "@dicebear/pixel-art"
import * as micah from "@dicebear/micah"
import * as notionists from "@dicebear/notionists"
import * as thumbs from "@dicebear/thumbs"
import * as lorelei from "@dicebear/lorelei"
import * as bigSmile from "@dicebear/big-smile"
import * as avataaars from "@dicebear/avataaars"

/** Map of available DiceBear avatar styles, keyed by style slug. */
export const AVATAR_STYLES: Record<string, { label: string; style: Style<object> }> = {
  "bottts-neutral": { label: "Robots", style: botttsNeutral as unknown as Style<object> },
  adventurer: { label: "Adventurer", style: adventurer as unknown as Style<object> },
  "fun-emoji": { label: "Fun Emoji", style: funEmoji as unknown as Style<object> },
  "pixel-art": { label: "Pixel Art", style: pixelArt as unknown as Style<object> },
  micah: { label: "Micah", style: micah as unknown as Style<object> },
  notionists: { label: "Notionists", style: notionists as unknown as Style<object> },
  thumbs: { label: "Thumbs", style: thumbs as unknown as Style<object> },
  lorelei: { label: "Lorelei", style: lorelei as unknown as Style<object> },
  "big-smile": { label: "Big Smile", style: bigSmile as unknown as Style<object> },
  avataaars: { label: "Avataaars", style: avataaars as unknown as Style<object> },
}

/** Default avatar style used when an agent has no explicit style set. */
export const DEFAULT_AVATAR_STYLE = "bottts-neutral"

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
 * Generate a DiceBear avatar data URI for an agent. Results are cached
 * in a bounded LRU keyed by (style, seed); see AVATAR_CACHE_MAX_ENTRIES.
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
  const entry = AVATAR_STYLES[resolvedStyle]
  const uri = createAvatar(entry.style, { seed, size: 128 }).toDataUri()
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

