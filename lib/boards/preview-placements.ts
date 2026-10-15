import type { BoardPlacement } from '@/hooks/use-page-board'
/** Keep the last committed preview stable during gestures/drafts, while
 * immediately applying current source redaction. This grants no source access:
 * each exact HTML revision is independently fetched through its authorized API. */
export function previewPlacements(previous: readonly BoardPlacement[], current: readonly BoardPlacement[], frozen: boolean): BoardPlacement[] {
  if (!frozen) return [...current]
  return current.map(placement => {
    const old = previous.find(item => item.id === placement.id)
    if (!placement.source || !old?.source || old.source.page_slug !== placement.source.page_slug) {
      const { source: _source, ...geometry } = placement
      return geometry
    }
    return { ...placement, source: old.source }
  })
}
