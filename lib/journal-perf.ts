import type { JournalEntry } from "@/lib/types/journal"
import { groupOf, severityOf, type EntryGroup } from "@/lib/journal-style"

/**
 * Journal entry annotated with a parsed timestamp (ms since epoch).
 * Keeps `new Date(e.ts).getTime()` out of hot paths — it gets called
 * 4–5× per entry per render across the filter chain otherwise.
 */
export type AnnotatedEntry = JournalEntry & { _tsMs: number }

/**
 * Mutate-in-place attach `_tsMs` once per entry. Safe because the
 * entries returned from `useJournalList` are owned by the page state
 * — nothing else holds a reference that cares about the property
 * being absent.
 */
export function annotateEntries(entries: JournalEntry[]): AnnotatedEntry[] {
  for (const e of entries as AnnotatedEntry[]) {
    if (typeof e._tsMs !== "number") {
      const t = new Date(e.ts).getTime()
      e._tsMs = Number.isFinite(t) ? t : 0
    }
  }
  return entries as AnnotatedEntry[]
}

export interface FilterStage {
  severity: "all" | "info" | "notice" | "warn" | "error"
  matcher: ((e: JournalEntry) => boolean) | null
  muted: Set<EntryGroup>
  bucket: { fromMs: number; toMs: number } | null
}

export interface FilterOutcome {
  /** Severity counts on the search-only-filtered set (before severity filter). */
  sevCounts: { all: number; info: number; notice: number; warn: number; error: number }
  /** Group counts on the severity+search-filtered set (before muting). */
  groupCounts: Record<EntryGroup, number>
  /** Entries that survive search + severity + muting. Drives histogram. */
  filtered: AnnotatedEntry[]
  /** Entries also surviving the bucket narrowing. Drives list + stats rail. */
  bucketed: AnnotatedEntry[]
}

const GROUP_KEYS: EntryGroup[] = [
  "exec", "network", "file", "container", "run", "keeper", "peer",
  "assignment", "approval", "mission", "cost", "skill", "memory", "system", "other",
]

/**
 * Single-pass filter — replaces the 4-stage useMemo chain with one
 * iteration that fills every downstream slice. ~4× fewer array
 * allocations and ~4× less GC pressure for chatty crews.
 */
export function filterEntries(
  entries: AnnotatedEntry[],
  stage: FilterStage,
): FilterOutcome {
  const out: FilterOutcome = {
    sevCounts: { all: 0, info: 0, notice: 0, warn: 0, error: 0 },
    groupCounts: Object.fromEntries(GROUP_KEYS.map((g) => [g, 0])) as Record<EntryGroup, number>,
    filtered: [],
    bucketed: [],
  }

  for (const e of entries) {
    const passesMatcher = !stage.matcher || stage.matcher(e)
    if (!passesMatcher) continue

    // Severity counts use search-filtered set so the segmented control
    // shows realistic numbers ("info 982 / notice 203 / …").
    const sev = severityOf(e.severity)
    out.sevCounts.all++
    out.sevCounts[sev]++

    if (stage.severity !== "all" && sev !== stage.severity) continue

    const grp = groupOf(e.entry_type)
    out.groupCounts[grp]++

    if (stage.muted.has(grp)) continue
    out.filtered.push(e)

    if (stage.bucket && (e._tsMs < stage.bucket.fromMs || e._tsMs >= stage.bucket.toMs)) continue
    out.bucketed.push(e)
  }

  return out
}
