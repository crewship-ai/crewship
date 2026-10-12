// An issue's own timeline, read directly by issue (#2983).
//
// Activity used to derive an issue's history from the loaded page of chains
// and each chain's five capped issue refs, so older history and the sixth issue
// simply vanished. The timeline is now three streams the backend already has —
// the event log (mission_activity by seq), the comments, and the runs — each
// paged on its own cursor and merged newest first.
//
// Merging pages from three cursors has one rule that matters: an item may be
// shown only when no stream that still has older pages could hold something
// newer than it. Otherwise a 10-01 event would sit above 10-02 comments that
// simply were not loaded yet, and the timeline would lie about order.

export interface TimelineItem {
  kind: "event" | "comment" | "run"
  /** Unique across streams. */
  key: string
  /** ISO instant the item happened. */
  at: string
  title: string
  detail?: string
  actor?: string
  /** Event log action, or run status. */
  action?: string
  /** For a run: the id to open it by. */
  runId?: string
}

export interface TimelineStream {
  items: TimelineItem[]
  /** No older page exists. */
  exhausted: boolean
  /** The first page has arrived. */
  loaded: boolean
}

export interface MergedTimeline {
  items: TimelineItem[]
  /** Some stream has an older page to fetch. */
  canLoadOlder: boolean
  /** A stream's first page has not arrived yet. */
  loading: boolean
}

export function mergeTimeline(streams: TimelineStream[]): MergedTimeline {
  const loading = streams.some((s) => !s.loaded)
  if (loading) return { items: [], canLoadOlder: false, loading: true }

  // The watermark: the newest "oldest loaded item" among streams that still
  // have older pages. Nothing below it is safe to show yet. A stream with
  // more pages but no items loaded would block everything, which is right:
  // its first page says nothing about where its items fall.
  let watermark: string | null = null
  for (const s of streams) {
    if (s.exhausted) continue
    const oldest = s.items.reduce<string | null>((min, i) => (min == null || i.at < min ? i.at : min), null)
    if (oldest == null) return { items: [], canLoadOlder: true, loading: false }
    if (watermark == null || oldest > watermark) watermark = oldest
  }

  const seen = new Set<string>()
  const items: TimelineItem[] = []
  for (const s of streams) {
    for (const i of s.items) {
      // The comment stream is the record of comments; the event log's
      // "commented" rows would list each one twice.
      if (i.kind === "event" && i.action === "commented") continue
      if (seen.has(i.key)) continue
      if (watermark != null && i.at < watermark) continue
      seen.add(i.key)
      items.push(i)
    }
  }
  items.sort((a, b) => (a.at === b.at ? a.key.localeCompare(b.key) : a.at < b.at ? 1 : -1))
  return { items, canLoadOlder: streams.some((s) => !s.exhausted), loading: false }
}
