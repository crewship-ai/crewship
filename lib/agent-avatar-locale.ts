/**
 * Locale-aware avatar palettes for the onboarding preview.
 *
 * The default DiceBear micah style randomises across a wide pool of
 * skin and hair tones — which reads as "globally mixed", a sensible
 * default for English-speaking audiences. For other locales the
 * preview feels less personal: users picking a regional language
 * tend to see avatars from a pool that doesn't reflect anyone they
 * know.
 *
 * This module biases (not exclusively maps) the colour pool per
 * language so the preview avatars feel locally plausible without
 * being stereotypical. The deterministic seed (agent slug) still
 * picks which entry in the pool an agent gets — same agent in the
 * same crew template always produces the same face within a given
 * locale.
 *
 * Important caveats:
 *   1. Language ≠ ethnicity. We bias toward demographic frequency,
 *      we don't lock to it. Every locale's pool still contains a mix
 *      so the preview avoids the "everyone here looks the same" trap.
 *   2. Only the onboarding preview uses this. Deployed agents render
 *      via the existing slug-only getAgentAvatarUrl so they stay
 *      identical across renders and across users.
 *   3. Locales we don't list fall through to the default pool
 *      (which is what DiceBear gives without any options).
 */

import { createAvatar } from "@dicebear/core"
import * as micah from "@dicebear/micah"

/**
 * Each entry lists the skin / hair colour pools we want the random
 * pick to draw from for that locale. The default DiceBear pool is
 * roughly the union of all these — leaving the field unset (English
 * branch) keeps the full mix.
 */
interface LocalePalette {
  skinColor?: string[]
  hairColor?: string[]
}

const LOCALE_PALETTES: Record<string, LocalePalette> = {
  // Central Europe — lighter skin range biased, mix of brown / blonde
  // / red hair. Czech, Slovak, Polish demographics are similar enough
  // that one palette covers all three without being uniform.
  "Čeština": {
    skinColor: ["f9c9b6", "f4cdb8", "ac6651"],
    hairColor: ["6a4e35", "4f3922", "9e1822", "f4d150", "77311d"],
  },
  "Slovenčina": {
    skinColor: ["f9c9b6", "f4cdb8", "ac6651"],
    hairColor: ["6a4e35", "4f3922", "9e1822", "f4d150", "77311d"],
  },
  "Polski": {
    skinColor: ["f9c9b6", "f4cdb8", "ac6651"],
    hairColor: ["6a4e35", "4f3922", "9e1822", "f4d150"],
  },
  // Germanic — lighter palette, more blonde / light-brown hair.
  Deutsch: {
    skinColor: ["f9c9b6", "f4cdb8", "ac6651"],
    hairColor: ["f4d150", "6a4e35", "4f3922", "c8a165"],
  },
  Nederlands: {
    skinColor: ["f9c9b6", "f4cdb8", "ac6651"],
    hairColor: ["f4d150", "6a4e35", "4f3922", "c8a165"],
  },
  // Mediterranean — warmer skin, darker hair pool.
  Français: {
    skinColor: ["f4cdb8", "ac6651", "77311d", "f9c9b6"],
    hairColor: ["4f3922", "6a4e35", "1e1e1e", "9e1822"],
  },
  Italiano: {
    skinColor: ["ac6651", "f4cdb8", "77311d"],
    hairColor: ["1e1e1e", "4f3922", "6a4e35"],
  },
  Español: {
    skinColor: ["ac6651", "f4cdb8", "77311d", "5e3826"],
    hairColor: ["1e1e1e", "4f3922", "6a4e35"],
  },
  Português: {
    skinColor: ["ac6651", "f4cdb8", "77311d", "5e3826"],
    hairColor: ["1e1e1e", "4f3922", "6a4e35"],
  },
  // English — explicitly empty so the default DiceBear pool kicks in
  // (full mix). Present in the map so a lookup miss falls through
  // deliberately to "unknown locale" rather than silently using
  // English defaults.
  English: {},
}

const _cache = new Map<string, string>()

/**
 * Get a DiceBear micah avatar data URI for the given agent slug,
 * biased toward the colour pool of the given language. Same slug +
 * same language → same face (results are cached). Unknown languages
 * fall through to the full default pool (same as English).
 */
export function getLocalizedAgentAvatar(seed: string, language: string): string {
  const palette = LOCALE_PALETTES[language] ?? {}
  // Cache key includes language so switching the picker in onboarding
  // doesn't return a stale face on the second render.
  const key = `${language}:${seed}`
  const cached = _cache.get(key)
  if (cached) return cached

  const opts: Record<string, unknown> = { seed, size: 128 }
  if (palette.skinColor) opts.skinColor = palette.skinColor
  if (palette.hairColor) opts.hairColor = palette.hairColor

  // micah's TS shape exposes specific keys; cast through unknown so a
  // future option (e.g. accessory bias per locale) doesn't fight the
  // type system.
  const uri = createAvatar(micah as never, opts as never).toDataUri()
  _cache.set(key, uri)
  return uri
}
