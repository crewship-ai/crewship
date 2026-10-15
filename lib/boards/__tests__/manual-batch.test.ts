import { expect, it } from 'vitest'
import { manualBoardBatch } from '../manual-batch'
import type { PageBoard } from '../../../hooks/use-page-board'
const base: PageBoard = { format: 'board/v1', seq: 8, can_write: true, placements: [], records: [{ element_id: 'a', element_rev: 5, deleted: false, element: { id: 'a', x: 10 } }, { element_id: 'b', element_rev: 3, deleted: false, element: { id: 'b', x: 20 } }] }
it('saves changed elements and deletion with the loaded server revisions', () => {
  expect(manualBoardBatch(base, [{ id: 'a', x: 30, version: 999999 }])).toEqual({ base_seq: 8, upserts: [{ element: { id: 'a', x: 30, version: 999999 }, expected_rev: 5 }], deletes: [{ id: 'b', expected_rev: 3 }] })
})
it('ignores purely editor metadata changes and drops never-saved deleted elements', () => {
  expect(manualBoardBatch(base, [{ id: 'a', x: 10, version: 99 }, { id: 'b', x: 20 }, { id: 'never', isDeleted: true }])).toEqual({ base_seq: 8, upserts: [], deletes: [] })
})
