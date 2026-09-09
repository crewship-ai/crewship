import { ACCENT } from "@/lib/concept-accents"

const TAG_ACCENTS = [ACCENT.blue, ACCENT.purple, ACCENT.teal, ACCENT.gold, ACCENT.sky]

/** Stable decorative colour across credential detail, create and edit. */
export function credentialTagClassName(tag: string): string {
  let hash = 0
  for (const character of tag.trim().toLowerCase()) {
    hash = (Math.imul(hash, 31) + character.charCodeAt(0)) >>> 0
  }
  const accent = TAG_ACCENTS[hash % TAG_ACCENTS.length]
  return `${accent.chip} ${accent.fg}`
}
