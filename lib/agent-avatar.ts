"use client"

import { createAvatar, type Style } from "@dicebear/core"
// Only the most common style is imported eagerly. The other DiceBear
// collections used to be static imports here, which pulled every
// collection (~30–100 KB each, multi-hundred-KB total) into the shared
// app-shell chunk even though any given avatar renders exactly one
// style. They now load on demand via dynamic import (see STYLE_LOADERS
// below).
//
// The resident style is bottts-neutral, not the default: every agent that
// existed before #3789 was pinned to it (the default moved to Voxel Bot), so
// it is still what most rosters draw.
import * as botttsNeutral from "@dicebear/bottts-neutral"

/**
 * The style an agent draws with when neither it nor its crew names one, and
 * the one new agents start on (#3789). Voxel Bot, from DiceBear 10.
 *
 * Moving this would repaint every agent that resolves to it, so the change to
 * Voxel Bot came with a migration that pinned every existing agent which
 * resolved to the old default to that old default by name
 * (internal/database/migrate_consts_pin_legacy_avatar_style.go).
 */
export const DEFAULT_AVATAR_STYLE = "voxel-bot"

/**
 * The default before #3789. Existing agents that had no style were pinned to
 * it, which is what keeps their faces.
 */
export const LEGACY_DEFAULT_AVATAR_STYLE = "bottts-neutral"

/** Below this rendered width (CSS px) an animatable avatar stays still. */
export const AVATAR_ANIMATION_MIN_PX = 32

/**
 * DiceBear 10's animation variant for the voxel styles. "slow" blinks every
 * 6 s and stretches every 9 s: enough to read as alive, not enough to pull
 * the eye off the work. "slowest" was too rare to notice in a list.
 */
const VOXEL_ANIMATION_VARIANT = "slow"

/**
 * What a resident style can draw. 9.4 collections and DiceBear 10 style
 * definitions have different APIs; this is the one the rest of the module
 * uses. `animated` is ignored by styles that cannot move.
 */
interface AvatarRenderer {
  animatable: boolean
  render(seed: string, animated: boolean): { toString(): string; toDataUri(): string }
}

function renderer9(style: Style<object>): AvatarRenderer {
  return { animatable: false, render: (seed) => createAvatar(style, { seed, size: 128 }) }
}

/**
 * DiceBear 10 for the two voxel styles only (#3789). Loaded beside 9.4 under
 * npm aliases (@dicebear/core-v10, @dicebear/styles-v10) and pinned exactly,
 * because 10.x draws a different face than 9.4 for the same seed: the other
 * styles stay on 9.4 so no existing agent changes. Core and definition load
 * together, in the voxel chunk, the first time a voxel avatar is drawn.
 */
async function loadVoxel(definition: () => Promise<{ default: unknown }>): Promise<AvatarRenderer> {
  const [core, def] = await Promise.all([import("@dicebear/core-v10"), definition()])
  const style = new core.Style(def.default as ConstructorParameters<typeof core.Style>[0])
  return {
    animatable: true,
    render: (seed, animated) =>
      new core.Avatar(style, {
        seed,
        size: 128,
        animationVariant: animated ? VOXEL_ANIMATION_VARIANT : "none",
      }),
  }
}

/** A 9.4 collection module as its renderer. */
const v9 = (load: () => Promise<object>) => () => load().then((mod) => renderer9(mod as unknown as Style<object>))

/**
 * Lazy loaders for every non-resident DiceBear style, keyed by style slug.
 * Each fires a code-split dynamic import the first time the style is
 * actually rendered (an agent configured with it scrolls into view, or the
 * avatar picker opens).
 */
const STYLE_LOADERS: Record<string, () => Promise<AvatarRenderer>> = {
  "voxel-bot": () => loadVoxel(() => import("@dicebear/styles-v10/voxel-bot.json")),
  "voxel-art": () => loadVoxel(() => import("@dicebear/styles-v10/voxel-art.json")),
  adventurer: v9(() => import("@dicebear/adventurer")),
  "adventurer-neutral": v9(() => import("@dicebear/adventurer-neutral")),
  avataaars: v9(() => import("@dicebear/avataaars")),
  "avataaars-neutral": v9(() => import("@dicebear/avataaars-neutral")),
  "big-ears": v9(() => import("@dicebear/big-ears")),
  "big-ears-neutral": v9(() => import("@dicebear/big-ears-neutral")),
  "big-smile": v9(() => import("@dicebear/big-smile")),
  bottts: v9(() => import("@dicebear/bottts")),
  croodles: v9(() => import("@dicebear/croodles")),
  "croodles-neutral": v9(() => import("@dicebear/croodles-neutral")),
  dylan: v9(() => import("@dicebear/dylan")),
  "fun-emoji": v9(() => import("@dicebear/fun-emoji")),
  lorelei: v9(() => import("@dicebear/lorelei")),
  "lorelei-neutral": v9(() => import("@dicebear/lorelei-neutral")),
  micah: v9(() => import("@dicebear/micah")),
  miniavs: v9(() => import("@dicebear/miniavs")),
  notionists: v9(() => import("@dicebear/notionists")),
  "notionists-neutral": v9(() => import("@dicebear/notionists-neutral")),
  "open-peeps": v9(() => import("@dicebear/open-peeps")),
  personas: v9(() => import("@dicebear/personas")),
  "pixel-art": v9(() => import("@dicebear/pixel-art")),
  "pixel-art-neutral": v9(() => import("@dicebear/pixel-art-neutral")),
  thumbs: v9(() => import("@dicebear/thumbs")),
  "toon-head": v9(() => import("@dicebear/toon-head")),
}

/** Slugs that have a lazy loader. The resident style has none. */
export function loadableStyleSlugs(): string[] {
  return Object.keys(STYLE_LOADERS)
}

/**
 * Map of available DiceBear avatar styles, keyed by style slug. Labels
 * only — the style implementations live behind STYLE_LOADERS so pickers
 * can enumerate keys/labels without forcing every collection to load.
 */
export interface AvatarStyleMeta {
  /** DiceBear's own name for the style. */
  label: string
  /** Shown with a "New" mark in the picker. */
  isNew?: boolean
  /** Can animate (DiceBear 10's voxel styles). */
  animated?: boolean
}

export const AVATAR_STYLES: Record<string, AvatarStyleMeta> = {
  // DiceBear 10 (#3789). First because Voxel Bot is the default.
  "voxel-bot": { label: "Voxel Bot", isNew: true, animated: true },
  "voxel-art": { label: "Voxel Art", isNew: true, animated: true },
  // Labels are DiceBear's own names, not ours. "Robots" for bottts-neutral was
  // a local invention, and it stopped matching the moment the sibling collection
  // bottts arrived — two robot styles, one of them called Robots. Anyone
  // comparing this list against dicebear.com now finds the same words.
  "bottts-neutral": { label: "Bottts Neutral" },
  bottts: { label: "Bottts" },
  adventurer: { label: "Adventurer" },
  "adventurer-neutral": { label: "Adventurer Neutral" },
  avataaars: { label: "Avataaars" },
  "avataaars-neutral": { label: "Avataaars Neutral" },
  "big-ears": { label: "Big Ears" },
  "big-ears-neutral": { label: "Big Ears Neutral" },
  "big-smile": { label: "Big Smile" },
  croodles: { label: "Croodles" },
  "croodles-neutral": { label: "Croodles Neutral" },
  dylan: { label: "Dylan" },
  "fun-emoji": { label: "Fun Emoji" },
  lorelei: { label: "Lorelei" },
  "lorelei-neutral": { label: "Lorelei Neutral" },
  micah: { label: "Micah" },
  miniavs: { label: "Miniavs" },
  notionists: { label: "Notionists" },
  "notionists-neutral": { label: "Notionists Neutral" },
  "open-peeps": { label: "Open Peeps" },
  personas: { label: "Personas" },
  "pixel-art": { label: "Pixel Art" },
  "pixel-art-neutral": { label: "Pixel Art Neutral" },
  thumbs: { label: "Thumbs" },
  "toon-head": { label: "Toon Head" },
}

// Styles whose implementation is resident. bottts-neutral is loaded from
// the start; lazies join as their imports resolve.
const _loadedStyles = new Map<string, AvatarRenderer>([
  [LEGACY_DEFAULT_AVATAR_STYLE, renderer9(botttsNeutral as unknown as Style<object>)],
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
    .then((renderer) => {
      _loadedStyles.set(styleName, renderer)
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

/** The style a slug draws with: itself when known, else the default. */
export function resolveAvatarStyle(styleName?: string | null): string {
  return styleName && AVATAR_STYLES[styleName] ? styleName : DEFAULT_AVATAR_STYLE
}

/** Whether a style can animate at all. Unknown and null are not styles. */
export function isAnimatableAvatarStyle(styleName?: string | null): boolean {
  return Boolean(styleName && AVATAR_STYLES[styleName]?.animated)
}

/**
 * Whether one avatar should move (#3789). All of these must hold:
 *
 *   - its style can animate (null resolves to the default, which can);
 *   - it is drawn at AVATAR_ANIMATION_MIN_PX or larger — a blink at 20 px
 *     is noise in a list, not life;
 *   - the app's Reduce motion is not On, and with it on System, the OS does
 *     not ask for reduced motion;
 *   - the viewer has not turned Animated avatars off.
 *
 * The OS setting is checked here as well as inside the SVG (its keyframes sit
 * in `@media (prefers-reduced-motion: no-preference)`): the e2e run showed
 * Chromium's reduced-motion emulation does not reach the separate document
 * of an SVG loaded as an <img>, so the SVG's own guard cannot be the only one.
 */
export function shouldAnimateAvatar(input: {
  style?: string | null
  sizePx: number
  motion: string
  avatarAnimation: string
  osReducedMotion?: boolean
}): boolean {
  return (
    isAnimatableAvatarStyle(resolveAvatarStyle(input.style)) &&
    input.sizePx >= AVATAR_ANIMATION_MIN_PX &&
    input.motion !== "on" &&
    !(input.motion === "system" && input.osReducedMotion) &&
    input.avatarAnimation !== "off"
  )
}

/**
 * Generate a DiceBear avatar data URI for an agent. Results are cached
 * in a bounded LRU keyed by (style, still/animated, seed); see
 * AVATAR_CACHE_MAX_ENTRIES.
 *
 * Synchronous by design: the resident style renders immediately; a
 * not-yet-loaded lazy style returns a deterministic placeholder, starts
 * the dynamic import, and bumps avatarStylesVersion() when the real
 * collection arrives (subscribe via useAvatarStylesVersion /
 * subscribeAvatarStyles to re-render).
 *
 * @param seed - Deterministic seed for avatar generation (typically the agent slug).
 * @param styleName - Avatar style key from AVATAR_STYLES; unknown and null draw the default.
 * @param opts.animated - Draw the moving variant. Ignored by styles that cannot
 *   move; decide it with shouldAnimateAvatar.
 */
export function getAgentAvatarUrl(
  seed: string,
  styleName?: string | null,
  opts?: { animated?: boolean },
): string {
  // Canonicalize unknown / null style names to DEFAULT_AVATAR_STYLE
  // BEFORE building the cache key. Otherwise three callers passing
  // "robots", "robot", and undefined (all of which fall back to the
  // default in the lookup below) would each spawn a separate cache
  // entry for the same generated URI, eroding the LRU's effective
  // working set on a misconfigured caller.
  const resolvedStyle = resolveAvatarStyle(styleName)
  // Same idea for the flag: a style that cannot move has one render.
  const animated = Boolean(opts?.animated) && isAnimatableAvatarStyle(resolvedStyle)
  const key = `${resolvedStyle}:${animated ? "a" : "s"}:${seed}`
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
    uri = style.render(seed, animated).toDataUri()
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
 * isn't resident yet. Always the still render.
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
 * @param styleName - Avatar style key from AVATAR_STYLES; unknown and null draw the default.
 */
export function getAgentAvatarSVG(seed: string, styleName?: string | null): string | null {
  const resolvedStyle = resolveAvatarStyle(styleName)
  const style = _loadedStyles.get(resolvedStyle)
  if (!style) {
    // Kick the import so a later attempt (next mount, next session) finds
    // the collection resident and can persist the real thing.
    void preloadAvatarStyle(resolvedStyle)
    return null
  }
  return style.render(seed, false).toString()
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
