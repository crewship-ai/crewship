import type { LibraryItem } from "./library-items"

/**
 * Needs attention: the fixed rule (Harbor 2 v87, issue #5132).
 *
 * An item needs its person's attention only for a REAL signal that this
 * browser has without a read per item:
 *
 * 1. `draft`  — this browser kept a recovered local draft of it (a Craft edit
 *               that never reached the server: a lost lock or a failed save).
 * 2. `failed` — the catalog reports panels whose last refresh FAILED.
 * 3. `stale`  — the catalog reports panels past their freshness window, and
 *               the caller owns the item (someone else's stale board is theirs to fix).
 *
 * Not a signal: never shared, never produced, empty, archived, test-made.
 * A held lock or a waiting proposal needs a lease or proposal read per item;
 * Home does not make those reads, so the opened artifact's status chip shows
 * them instead. Order: draft, failed, stale, then newest first; at most
 * ATTENTION_LIMIT items, so a fresh seed never fills Home with warnings.
 */
export type AttentionReason = "draft" | "failed" | "stale"

export interface AttentionSignals {
  /** Slugs with a recovered local draft in this browser. */
  drafts?: ReadonlySet<string>
}

export interface AttentionEntry {
  item: LibraryItem
  reason: AttentionReason
  title: string
  detail: string
}

export const ATTENTION_LIMIT = 6

const RANK: Record<AttentionReason, number> = { draft: 0, failed: 1, stale: 2 }

/** The strongest reason this item needs attention, or null. */
export function attentionReason(item: LibraryItem, signals: AttentionSignals = {}): AttentionReason | null {
  if (item.archived || item.test) return null
  if (signals.drafts?.has(item.slug)) return "draft"
  if ((item.failedPanels ?? 0) > 0) return "failed"
  if ((item.stalePanels ?? 0) > 0 && item.ownedByMe) return "stale"
  return null
}

export const needsAttention = (item: LibraryItem, signals: AttentionSignals = {}) => attentionReason(item, signals) !== null

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`

function describe(item: LibraryItem, reason: AttentionReason): Pick<AttentionEntry, "title" | "detail"> {
  if (reason === "draft") return { title: "Your recovered draft", detail: `${item.name} · kept in this browser, not saved yet` }
  if (reason === "failed") return { title: "Refresh failed", detail: `${item.name} · ${plural(item.failedPanels ?? 0, "panel")} kept the last good data` }
  return { title: "Data is stale", detail: `${item.name} · ${plural(item.stalePanels ?? 0, "panel")} past their refresh window` }
}

export function attentionItems(items: readonly LibraryItem[], signals: AttentionSignals = {}, limit = ATTENTION_LIMIT): AttentionEntry[] {
  return items
    .map(item => ({ item, reason: attentionReason(item, signals) }))
    .filter((entry): entry is { item: LibraryItem; reason: AttentionReason } => entry.reason !== null)
    .sort((a, b) => RANK[a.reason] - RANK[b.reason] || (b.item.updatedAt?.getTime() ?? 0) - (a.item.updatedAt?.getTime() ?? 0))
    .slice(0, limit)
    .map(entry => ({ ...entry, ...describe(entry.item, entry.reason) }))
}
