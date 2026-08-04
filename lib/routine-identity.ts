// A routine's icon and colour, derived from its slug.
//
// Routines have no stored icon or colour — the API has no field for
// either. Offering a picker without somewhere to put the answer would
// take the user's choice and drop it, which is the same class of lie as
// a save button that saves nothing.
//
// Deriving gets the real benefit today: a workspace ends up with dozens
// of routines that look identical in a list, and this makes each one
// recognisable. The same routine always looks the same, which is what
// makes it a useful handle rather than decoration.
//
// This lives in lib, not beside a component, because two surfaces use
// it — the explorer row and the detail header — and the same routine
// rendering two different icons would be worse than rendering none.
//
// Swap both functions for the stored value the day the field exists.
// Nothing else has to change.

import { GRADIENT_PALETTES, searchCrewIcons } from "./crew-icons"

const ICON_POOL: string[] = (() => {
  // Read out of the kit's own registry rather than retyped, so a rename
  // there cannot leave this pointing at an icon that no longer resolves.
  // The categories are the ones whose glyphs read as "a thing that runs"
  // rather than as a person or a mood.
  const out: string[] = []
  for (const category of ["automation", "tech", "business", "data", "communication"]) {
    out.push(...searchCrewIcons(category))
  }
  // Every category could be renamed out from under us; falling back to
  // the whole registry keeps this working rather than returning an empty
  // pool and a blank icon.
  const pool = out.length > 0 ? out : searchCrewIcons("")
  return Array.from(new Set(pool))
})()

const COLOR_POOL: string[] = GRADIENT_PALETTES.map((p) => p.id)

/**
 * FNV-1a over the slug.
 *
 * Any stable hash would do; this one is chosen because it spreads short
 * similar strings — `eval-extract-emails` and `eval-extract-numbers` —
 * across different buckets, which is exactly the case a routine list is
 * full of. A cheaper sum would give those two the same icon.
 */
function hashSlug(slug: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < slug.length; i++) {
    h ^= slug.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h >>> 0
}

/** Crew-icon name for a routine. Always resolvable by the icon kit. */
export function routineIcon(slug: string): string {
  if (ICON_POOL.length === 0) return "workflow"
  return ICON_POOL[hashSlug(slug) % ICON_POOL.length]
}

/** Gradient-palette id for a routine. Always one the kit defines. */
export function routineColor(slug: string): string {
  if (COLOR_POOL.length === 0) return "blue"
  // A different multiplier from the icon lookup, so two routines sharing
  // an icon are unlikely to also share a colour — the pair is what makes
  // a row identifiable, not either half alone.
  return COLOR_POOL[(hashSlug(slug) >>> 3) % COLOR_POOL.length]
}
