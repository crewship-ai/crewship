import { expect, it } from 'vitest'
import { previewPlacements } from './preview-placements'
import type { BoardPlacement } from '@/hooks/use-page-board'
const pin: BoardPlacement = { id: 'pin', mode: 'follow_latest', placement_rev: 2, x: 0, y: 0, width: 100, height: 100, source: { page_slug: 'report', title: 'Report', revision: 3, pinned_revision: 1 } }
it('holds the shown exact revision during edits and adopts the latest head only after release', () => {
  const latest = { ...pin, source: { ...pin.source!, revision: 4 } }
  expect(previewPlacements([pin], [latest], true)[0].source?.revision).toBe(3)
  expect(previewPlacements([pin], [latest], false)[0].source?.revision).toBe(4)
  expect(latest.source.revision).toBe(4)
})
it('applies current source redaction immediately even while frozen', () => {
  const { source: _source, ...redacted } = pin
  expect(previewPlacements([pin], [redacted], true)[0].source).toBeUndefined()
  expect(previewPlacements([pin], [{ ...pin, source: { ...pin.source!, page_slug: 'another' } }], true)[0].source).toBeUndefined()
})
