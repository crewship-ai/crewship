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

/**
 * Keys are the English `name` field from lib/languages.ts (e.g.
 * "Czech", not "Čeština") so what gets stored in
 * workspaces.preferred_language is consistent across the onboarding
 * wizard and Settings → General. Adding more locales is a one-entry
 * change; anything we don't list falls through to the DiceBear
 * default pool, which is itself a fine globally-mixed look.
 *
 * Palette buckets are intentionally broad — each locale still draws
 * from a mix of skin and hair colours so the preview never feels
 * monolithic. The bias just nudges the distribution toward what a
 * native speaker of that language would typically expect to see in
 * a team meeting in their region.
 */
const LOCALE_PALETTES: Record<string, LocalePalette> = {
  // Central / Eastern Europe — lighter skin range, mix of brown /
  // blonde / red hair.
  Czech:      { skinColor: ["f9c9b6", "f4cdb8", "ac6651"], hairColor: ["6a4e35", "4f3922", "9e1822", "f4d150", "77311d"] },
  Slovak:     { skinColor: ["f9c9b6", "f4cdb8", "ac6651"], hairColor: ["6a4e35", "4f3922", "9e1822", "f4d150", "77311d"] },
  Polish:     { skinColor: ["f9c9b6", "f4cdb8", "ac6651"], hairColor: ["6a4e35", "4f3922", "9e1822", "f4d150"] },
  Hungarian:  { skinColor: ["f9c9b6", "f4cdb8", "ac6651"], hairColor: ["4f3922", "6a4e35", "9e1822", "1e1e1e"] },
  Slovenian:  { skinColor: ["f9c9b6", "f4cdb8", "ac6651"], hairColor: ["6a4e35", "4f3922", "f4d150"] },
  Croatian:   { skinColor: ["f4cdb8", "ac6651", "f9c9b6"], hairColor: ["4f3922", "6a4e35", "1e1e1e"] },
  Romanian:   { skinColor: ["f4cdb8", "ac6651", "77311d"], hairColor: ["4f3922", "6a4e35", "1e1e1e"] },
  Bulgarian:  { skinColor: ["f4cdb8", "ac6651", "f9c9b6"], hairColor: ["4f3922", "6a4e35", "1e1e1e"] },
  Serbian:    { skinColor: ["f4cdb8", "ac6651"], hairColor: ["4f3922", "6a4e35", "1e1e1e"] },

  // Germanic / Nordic — lighter skin, blonde-leaning hair pool.
  German:     { skinColor: ["f9c9b6", "f4cdb8", "ac6651"], hairColor: ["f4d150", "6a4e35", "4f3922", "c8a165"] },
  Dutch:      { skinColor: ["f9c9b6", "f4cdb8", "ac6651"], hairColor: ["f4d150", "6a4e35", "4f3922", "c8a165"] },
  Swedish:    { skinColor: ["f9c9b6", "f4cdb8"],            hairColor: ["f4d150", "c8a165", "6a4e35"] },
  Norwegian:  { skinColor: ["f9c9b6", "f4cdb8"],            hairColor: ["f4d150", "c8a165", "6a4e35"] },
  Danish:     { skinColor: ["f9c9b6", "f4cdb8"],            hairColor: ["f4d150", "c8a165", "6a4e35"] },
  Finnish:    { skinColor: ["f9c9b6", "f4cdb8"],            hairColor: ["f4d150", "c8a165", "6a4e35"] },

  // Baltic
  Estonian:   { skinColor: ["f9c9b6", "f4cdb8"], hairColor: ["f4d150", "6a4e35", "c8a165"] },
  Latvian:    { skinColor: ["f9c9b6", "f4cdb8"], hairColor: ["f4d150", "6a4e35", "c8a165"] },
  Lithuanian: { skinColor: ["f9c9b6", "f4cdb8"], hairColor: ["f4d150", "6a4e35", "c8a165"] },

  // Romance / Mediterranean — warmer skin, darker hair pool.
  French:     { skinColor: ["f4cdb8", "ac6651", "77311d", "f9c9b6"], hairColor: ["4f3922", "6a4e35", "1e1e1e", "9e1822"] },
  Italian:    { skinColor: ["ac6651", "f4cdb8", "77311d"],            hairColor: ["1e1e1e", "4f3922", "6a4e35"] },
  Spanish:    { skinColor: ["ac6651", "f4cdb8", "77311d", "5e3826"],  hairColor: ["1e1e1e", "4f3922", "6a4e35"] },
  Portuguese: { skinColor: ["ac6651", "f4cdb8", "77311d", "5e3826"],  hairColor: ["1e1e1e", "4f3922", "6a4e35"] },
  "Portuguese (Brazil)": { skinColor: ["ac6651", "77311d", "5e3826", "f4cdb8"], hairColor: ["1e1e1e", "4f3922", "6a4e35"] },
  Catalan:    { skinColor: ["ac6651", "f4cdb8", "77311d"], hairColor: ["1e1e1e", "4f3922", "6a4e35"] },
  Greek:      { skinColor: ["ac6651", "f4cdb8", "77311d"], hairColor: ["1e1e1e", "4f3922", "6a4e35"] },

  // Slavic east
  Russian:    { skinColor: ["f4cdb8", "f9c9b6", "ac6651"], hairColor: ["6a4e35", "4f3922", "f4d150", "9e1822"] },
  Ukrainian:  { skinColor: ["f4cdb8", "f9c9b6", "ac6651"], hairColor: ["6a4e35", "4f3922", "f4d150"] },

  // Middle East / North Africa
  Arabic:     { skinColor: ["ac6651", "77311d", "5e3826"], hairColor: ["1e1e1e", "4f3922"] },
  Hebrew:     { skinColor: ["ac6651", "f4cdb8", "77311d"], hairColor: ["1e1e1e", "4f3922", "6a4e35"] },
  Persian:    { skinColor: ["ac6651", "77311d", "5e3826"], hairColor: ["1e1e1e", "4f3922"] },
  Turkish:    { skinColor: ["ac6651", "77311d", "f4cdb8"], hairColor: ["1e1e1e", "4f3922", "6a4e35"] },

  // South Asia
  Hindi:      { skinColor: ["77311d", "5e3826", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },
  Bengali:    { skinColor: ["77311d", "5e3826", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },
  Tamil:      { skinColor: ["77311d", "5e3826"],            hairColor: ["1e1e1e", "4f3922"] },
  Urdu:       { skinColor: ["77311d", "5e3826", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },

  // East Asia
  Japanese:   { skinColor: ["f4cdb8", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },
  Korean:     { skinColor: ["f4cdb8", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },
  Chinese:    { skinColor: ["f4cdb8", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },
  "Chinese (Traditional)": { skinColor: ["f4cdb8", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },

  // Southeast Asia
  Vietnamese: { skinColor: ["f4cdb8", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },
  Thai:       { skinColor: ["f4cdb8", "ac6651"], hairColor: ["1e1e1e", "4f3922"] },
  Indonesian: { skinColor: ["ac6651", "77311d", "f4cdb8"], hairColor: ["1e1e1e", "4f3922"] },
  Malay:      { skinColor: ["ac6651", "77311d", "f4cdb8"], hairColor: ["1e1e1e", "4f3922"] },

  // Sub-Saharan Africa
  Swahili:    { skinColor: ["77311d", "5e3826", "3a1a0d"], hairColor: ["1e1e1e"] },
  Afrikaans:  { skinColor: ["f4cdb8", "ac6651", "77311d", "5e3826"], hairColor: ["1e1e1e", "4f3922", "f4d150"] },

  // English — explicit empty palette so the default DiceBear pool
  // (full global mix) kicks in. Present here so a hit on "English"
  // is observable; languages we don't list fall through with the
  // same empty-options result via the lookup fallback.
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
