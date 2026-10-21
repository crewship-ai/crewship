import type { AccentChoice } from "@/lib/theme/accents"

/**
 * Theme packs (#3790): a pack is one file in lib/theme-packs/ and one line in
 * lib/theme-packs/index.ts. It appears in Appearance → Style under its
 * category, can be a workspace's default look, and renders through the same
 * `data-style` mechanism as the built-in styles (lib/theme/appearance-style.ts).
 *
 * The pack's CSS is generated from this data (css.ts → app/styles/packs.generated.css),
 * so a pack is data only: no stylesheet to edit by hand.
 */

export type PackScheme = "light" | "dark"

/** Gallery sections a pack can sit in. Gaming is the first; add more here. */
export const PACK_CATEGORIES = ["Gaming"] as const
export type PackCategory = (typeof PACK_CATEGORIES)[number]

/** One self-hosted font. Files live under public/fonts/ and are fetched by the
 *  browser only when something on the page uses the family, i.e. only while a
 *  pack that names it is active. */
export interface PackFont {
  family: string
  /** Stack appended after the family; keep the app's own face in it so a missing file never breaks a page. */
  fallback: string
  faces: { weight: string; latin: string; latinExt?: string }[]
}

/** Motion flavour: how the pack's decoration moves, if at all. Reduce motion always wins. */
export interface PackMotion {
  flavour: "still" | "neon"
  /** The decorative glow may pulse (never the only signal of a state). */
  glowPulse: boolean
  /** Overview cards sit at a slight angle, in degrees; 0 for none. Cards in lists never tilt. */
  postcardTilt: number
}

export interface ThemePack {
  /** `[a-z0-9-]{1,32}`, unique across the built-in styles and every pack. */
  id: string
  name: string
  category: PackCategory
  blurb: string
  /** A pack lists only the schemes it has a legible palette for. A user in a
   *  scheme the pack lacks sees Harbor (the app never paints half a pack). */
  schemes: PackScheme[]
  /** Suggested accent when the pack is picked. Packs cannot set brand tokens. */
  accent: AccentChoice
  /** Rail, card, accent and ground, for the tile before the live preview. */
  swatch: [string, string, string, string]
  /** Custom properties per scheme, without the leading `--`. Hex colours or gradients. */
  tokens: Partial<Record<PackScheme, Record<string, string>>>
  /** Corner scale (--radius-k). */
  radiusK: number
  /** Page titles and big numbers. Omit to keep the app font everywhere. */
  displayFont?: PackFont
  /** UI text. Omit to keep the app font. */
  textFont?: PackFont
  /** Avatar style slug used for new agents while the pack is the look. Unknown slugs are ignored. */
  avatarStyle?: string
  motion: PackMotion
}

/** Token names a pack may never set: the Crewship brand blue and its sail. */
export const RESERVED_TOKEN = /^(brand|logo|sail)(-|$)/
