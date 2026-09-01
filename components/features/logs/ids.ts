/**
 * Display helpers for the opaque ids the journal stores. Entries carry
 * stable ids and never display strings (see docs/guides/crew-journal.mdx),
 * so every surface that shows one needs the same fallback when the lookup
 * table cannot name it.
 *
 * Distinct from `shortId` in lib/activity-stream.ts, which renders `#abcde`
 * — a reference marker inside a sentence. This one keeps the head of the id
 * as well, because it stands in for a name in a column of names, where the
 * leading characters are what a reader recognises.
 */

/** Shorten an opaque id for display when no lookup name is available. */
export function shortenId(id: string): string {
  if (id.length <= 12) return id
  return `${id.slice(0, 6)}…${id.slice(-4)}`
}
