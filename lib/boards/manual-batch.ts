import type { BoardBatch, BoardElement } from './revision-adapter'
import type { PageBoard } from '../../hooks/use-page-board'
const stamp = (element: BoardElement) => JSON.stringify(Object.fromEntries(Object.entries({ ...element, boundElements: element.boundElements ?? [] }).filter(([key]) => !['version', 'versionNonce', 'updated'].includes(key)).sort(([a], [b]) => a.localeCompare(b))))
/** Explicit-save diff against the originally loaded canonical revisions.
 * Never uses a refetched head to silently rebase a human's unsaved drawing. */
export function manualBoardBatch(base: PageBoard, draft: readonly BoardElement[]): BoardBatch {
  const records = new Map(base.records.map(record => [record.element_id, record]))
  const seen = new Set<string>()
  const batch: BoardBatch = { base_seq: base.seq, upserts: [], deletes: [] }
  for (const element of draft) {
    seen.add(element.id)
    const previous = records.get(element.id)
    if (element.isDeleted) {
      if (previous && !previous.deleted) batch.deletes.push({ id: element.id, expected_rev: previous.element_rev })
    } else if (!previous?.element || previous.deleted || stamp(previous.element) !== stamp(element)) {
      batch.upserts.push({ element, expected_rev: previous?.element_rev ?? 0 })
    }
  }
  for (const record of base.records) if (!record.deleted && !seen.has(record.element_id)) batch.deletes.push({ id: record.element_id, expected_rev: record.element_rev })
  return batch
}
