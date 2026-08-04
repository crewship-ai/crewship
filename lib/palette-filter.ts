// Ranking for the ⌘K palette.
//
// cmdk ships a SUBSEQUENCE matcher: every letter of the query has to appear in
// order, but not next to each other. That is forgiving enough to be useless
// once the palette carries nine groups — "gmail" matched "Rewrite the
// HarborliGht reADMe so a newcomer can folLow it" and ranked it above the
// actual Gmail integration, because the issue list is fetched first.
//
// The palette is not a fuzzy finder over unknown text; it is a jump-to for
// things you can already name. So: substring or nothing, and rank by WHERE the
// hit lands — a name that starts with what you typed beats one that merely
// contains it.

/** Ranks the sections apart. Nothing here is a percentage; only order matters. */
const SCORE = {
  /** The value begins with the query — "classify" → "classify-ticket". */
  prefix: 1,
  /** A word inside the value begins with it — "classify" → "Eval: classify …". */
  wordStart: 0.7,
  /** It appears mid-word — "classify" → "reclassifying". */
  substring: 0.4,
  /** It matched an alias rather than the row's own text. */
  keyword: 0.3,
  /** Nothing typed yet: everything is equally worth showing. */
  empty: 1,
} as const

function scoreIn(haystack: string, needle: string): number {
  const at = haystack.indexOf(needle)
  if (at < 0) return 0
  if (at === 0) return SCORE.prefix
  // A word start is any hit preceded by something that is not a letter or a
  // digit — space, hyphen, slash, colon, dot. Slugs and titles both hit this.
  return /[^a-z0-9]/.test(haystack[at - 1]) ? SCORE.wordStart : SCORE.substring
}

/**
 * cmdk's `filter` contract: return 0 to hide the row, higher to rank it up.
 *
 * `value` is what the row registered (the palette prefixes it with the entity
 * kind — "routine Morning briefing" — so typing "routine" lists them all).
 */
export function paletteFilter(value: string, search: string, keywords?: string[]): number {
  const q = search.trim().toLowerCase()
  if (!q) return SCORE.empty

  const direct = scoreIn(value.toLowerCase(), q)
  if (direct > 0) return direct

  // Aliases are a fallback, never a competitor: a row whose own name matches
  // must outrank one that only matched a keyword.
  for (const k of keywords ?? []) {
    if (k.toLowerCase().includes(q)) return SCORE.keyword
  }
  return 0
}
