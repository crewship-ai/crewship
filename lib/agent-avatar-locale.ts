/**
 * Locale-aware avatar palettes for the onboarding preview.
 *
 * DiceBear micah ships with a *fixed* enum of base (skin) and hair
 * colours — passing arbitrary hex values silently falls back to the
 * full default pool. The two things that matter:
 *
 *   baseColor (skin):  ["f9c9b6", "ac6651", "77311d"]
 *                       light          medium      dark
 *
 *   hairColor:         ["000000", "77311d", "ac6651", "f4d150",
 *                        "ffeba4", "f9c9b6", "ffffff", ...]
 *                       (only the "natural" ones we actually want)
 *
 * Per the user's feedback ("dal jsem Czech a stále tam mám indický
 * avatar"), we use a SINGLE base colour per locale rather than a
 * mix — Czech avatars now read consistently as Czech because every
 * seed lands on `f9c9b6`. Per-agent variation still comes through
 * hair colour, hair style, expressions, glasses, and accessories,
 * so the four-agent crew preview never feels monolithic.
 *
 * The locale → look mapping is deliberately approximate. Each
 * language sits in one of four broad buckets based on the dominant
 * skin tone you'd see at a real workplace in that region; we don't
 * pretend to capture every demographic shade. Anything we don't
 * list falls through to the default DiceBear pool (mixed).
 *
 * Deployed agents render via the slug-only getAgentAvatarUrl so
 * they stay identical regardless of locale — this bias only ever
 * shows in the onboarding preview pane.
 */

import { createAvatar } from "@dicebear/core"
import * as micah from "@dicebear/micah"

interface LocalePalette {
  /** baseColor: micah uses this key (NOT skinColor) for the face/body
   *  tone. Valid values per the bundled schema: f9c9b6 | ac6651 | 77311d. */
  baseColor?: string[]
  /** hairColor: valid "natural" subset of the micah enum.
   *  000000 (black) | 77311d (dark brown) | ac6651 (brown) |
   *  f4d150 (blonde) | ffeba4 (light blonde) | f9c9b6 (pale).
   *  Cartoon colours (purple, blue, cyan…) intentionally omitted. */
  hairColor?: string[]
}

// ---- Reusable bucket palettes ----------------------------------------
//
// Four broad looks. Languages share buckets; that's intentional —
// the goal isn't ethnographic precision, it's that a Czech user
// doesn't see a brown-skinned team labelled "Czech".

const PALETTE_LIGHT_EUROPEAN: LocalePalette = {
  // Light skin only — keeps Czech / German / Polish / Nordic avatars
  // consistently pale-toned. Hair mix covers blonde, brown, and very
  // light blonde so every avatar still looks like a different person.
  baseColor: ["f9c9b6"],
  hairColor: ["f4d150", "ffeba4", "77311d", "ac6651"],
}

const PALETTE_MEDITERRANEAN: LocalePalette = {
  // Medium skin only — Italy, Spain, Greece, southern France,
  // Romania, Croatia. Hair leans darker (black + dark brown) which
  // matches the regional read.
  baseColor: ["ac6651"],
  hairColor: ["000000", "77311d", "ac6651"],
}

const PALETTE_MENA_SOUTH_ASIA: LocalePalette = {
  // Medium-dark skin — MENA + South Asia. Black/dark-brown hair pool.
  // This is the bucket we LET Indian-looking avatars live in, so the
  // Czech complaint stays fixed: those faces appear when a user picks
  // Hindi / Arabic / Persian / Bengali / etc., not when they pick
  // Czech.
  baseColor: ["77311d"],
  hairColor: ["000000", "77311d"],
}

const PALETTE_EAST_ASIAN: LocalePalette = {
  // Micah doesn't have an East-Asian-specific skin enum. Light base
  // with black hair is the closest stylistic match to what users
  // expect for Japanese / Korean / Chinese pickers.
  baseColor: ["f9c9b6"],
  hairColor: ["000000", "77311d"],
}

const PALETTE_SUB_SAHARAN: LocalePalette = {
  // Dark skin. Hair pool is black + dark brown.
  baseColor: ["77311d"],
  hairColor: ["000000", "77311d"],
}

/**
 * Keys are the English `name` field from lib/languages.ts (e.g.
 * "Czech", not "Čeština") so what we store in
 * workspaces.preferred_language is the same string the picker
 * displays and what we look up here.
 */
const LOCALE_PALETTES: Record<string, LocalePalette> = {
  // Central / Eastern Europe — all light bucket.
  Czech:      PALETTE_LIGHT_EUROPEAN,
  Slovak:     PALETTE_LIGHT_EUROPEAN,
  Polish:     PALETTE_LIGHT_EUROPEAN,
  Hungarian:  PALETTE_LIGHT_EUROPEAN,
  Slovenian:  PALETTE_LIGHT_EUROPEAN,
  Romanian:   PALETTE_MEDITERRANEAN,
  Bulgarian:  PALETTE_MEDITERRANEAN,
  Serbian:    PALETTE_MEDITERRANEAN,
  Croatian:   PALETTE_MEDITERRANEAN,

  // Germanic / Nordic / Baltic — light.
  German:     PALETTE_LIGHT_EUROPEAN,
  Dutch:      PALETTE_LIGHT_EUROPEAN,
  Swedish:    PALETTE_LIGHT_EUROPEAN,
  Norwegian:  PALETTE_LIGHT_EUROPEAN,
  Danish:     PALETTE_LIGHT_EUROPEAN,
  Finnish:    PALETTE_LIGHT_EUROPEAN,
  Estonian:   PALETTE_LIGHT_EUROPEAN,
  Latvian:    PALETTE_LIGHT_EUROPEAN,
  Lithuanian: PALETTE_LIGHT_EUROPEAN,

  // Romance / Mediterranean.
  French:     PALETTE_MEDITERRANEAN,
  Italian:    PALETTE_MEDITERRANEAN,
  Spanish:    PALETTE_MEDITERRANEAN,
  Portuguese: PALETTE_MEDITERRANEAN,
  "Portuguese (Brazil)": PALETTE_MEDITERRANEAN,
  Catalan:    PALETTE_MEDITERRANEAN,
  Greek:      PALETTE_MEDITERRANEAN,

  // Slavic east — light bucket (lighter end of the pool).
  Russian:    PALETTE_LIGHT_EUROPEAN,
  Ukrainian:  PALETTE_LIGHT_EUROPEAN,

  // MENA + Iranian plateau.
  Arabic:     PALETTE_MENA_SOUTH_ASIA,
  Hebrew:     PALETTE_MEDITERRANEAN,
  Persian:    PALETTE_MENA_SOUTH_ASIA,
  Turkish:    PALETTE_MEDITERRANEAN,

  // South Asia.
  Hindi:      PALETTE_MENA_SOUTH_ASIA,
  Bengali:    PALETTE_MENA_SOUTH_ASIA,
  Tamil:      PALETTE_MENA_SOUTH_ASIA,
  Urdu:       PALETTE_MENA_SOUTH_ASIA,

  // East Asia.
  Japanese:   PALETTE_EAST_ASIAN,
  Korean:     PALETTE_EAST_ASIAN,
  Chinese:    PALETTE_EAST_ASIAN,
  "Chinese (Traditional)": PALETTE_EAST_ASIAN,

  // Southeast Asia — sit between East Asian and MENA buckets.
  Vietnamese: PALETTE_EAST_ASIAN,
  Thai:       PALETTE_EAST_ASIAN,
  Indonesian: PALETTE_MENA_SOUTH_ASIA,
  Malay:      PALETTE_MENA_SOUTH_ASIA,

  // Sub-Saharan Africa.
  Swahili:    PALETTE_SUB_SAHARAN,
  Afrikaans:  PALETTE_MEDITERRANEAN, // mixed-ancestry demographic — middle bucket

  // English — explicit empty so DiceBear's default mixed pool kicks
  // in (the "global" look). Present in the map so a hit on "English"
  // is observable rather than silent fall-through.
  English:    {},
}

const _cache = new Map<string, string>()

/**
 * DiceBear data URI for an agent slug, biased to the given language's
 * palette bucket. Same (slug, language) tuple → same face. The cache
 * keys on `lang:seed` so flipping the picker doesn't return a stale
 * face from a different palette.
 */
export function getLocalizedAgentAvatar(seed: string, language: string): string {
  const palette = LOCALE_PALETTES[language] ?? {}
  const key = `${language}:${seed}`
  const cached = _cache.get(key)
  if (cached) return cached

  const opts: Record<string, unknown> = { seed, size: 128 }
  if (palette.baseColor) opts.baseColor = palette.baseColor
  if (palette.hairColor) opts.hairColor = palette.hairColor

  const uri = createAvatar(micah as never, opts as never).toDataUri()
  _cache.set(key, uri)
  return uri
}

/**
 * Bucket inference — used by getDiverseLocale to pick a contrasting
 * locale. Hand-mapped to the four reusable palettes so the result is
 * stable when we add more language entries.
 */
function bucketOf(language: string): "light" | "mediterranean" | "mena" | "east_asian" | "sub_saharan" | "mixed" {
  const palette = LOCALE_PALETTES[language]
  if (!palette || !palette.baseColor || palette.baseColor.length === 0) return "mixed"
  const base = palette.baseColor[0]
  const hair = palette.hairColor?.[0] ?? ""
  if (base === "f9c9b6" && hair === "000000") return "east_asian"
  if (base === "f9c9b6") return "light"
  if (base === "ac6651") return "mediterranean"
  if (base === "77311d" && hair === "000000") return "sub_saharan"
  if (base === "77311d") return "mena"
  return "mixed"
}

/**
 * Returns a locale that visually CONTRASTS with the given primary
 * locale, so a crew preview can mix in one "different" agent and
 * avoid looking monolithic. The mapping is deliberately asymmetric:
 *
 *   light European  → East Asian   (Japanese)
 *   Mediterranean   → East Asian   (Korean)
 *   MENA/South Asia → light Euro   (Czech)
 *   East Asian      → Mediterranean (Italian)
 *   sub-Saharan     → light Euro   (Czech)
 *
 * English (mixed bucket) keeps itself — the default pool is already
 * varied so no extra rotation is needed.
 */
export function getDiverseLocale(primary: string): string {
  switch (bucketOf(primary)) {
    case "light":         return "Japanese"
    case "mediterranean": return "Korean"
    case "mena":          return "Czech"
    case "east_asian":    return "Italian"
    case "sub_saharan":   return "Czech"
    case "mixed":
    default:              return primary
  }
}
