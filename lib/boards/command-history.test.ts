import { describe, expect, it } from 'vitest'
import { planSavedUndo, savedBoardCommand } from './command-history'
import type { BoardHead, CollaborativeBatch } from './collaboration-types'
const base = (): BoardHead => ({ format: 'test', seq: 1, can_write: true, placements: [], records: [{ element_id: 'a', element_rev: 1, deleted: false, element: { id: 'a', x: 0 } }, { element_id: 'b', element_rev: 1, deleted: false, element: { id: 'b', x: 0 } }] })
const expiry = '2099-01-01T00:00:00Z'
const move: CollaborativeBatch = { base_seq: 1, upserts: [{ element: { id: 'a', x: 20 }, expected_rev: 1 }], deletes: [] }
describe('server-revision saved undo', () => {
  it('undoes only the accepted local change while retaining disjoint remote work', () => {
    const before = base(); const after = structuredClone(before); after.seq = 3; after.records[1].element_rev = 2; after.records[1].element = { id: 'b', x: 90 }
    const command = savedBoardCommand(before, move, { seq: 3, result_seq: 2, element_revs: { a: 2 }, undo_expires_at: expiry }, after)
    expect(command.applied.records[1].element_rev).toBe(1)
    expect(planSavedUndo(command, { ...after, records: [command.applied.records[0], after.records[1]] })).toMatchObject({ kind: 'ready', batch: { base_seq: 3, upserts: [{ element: { id: 'a', x: 0 }, expected_rev: 2 }], deletes: [] } })
  })
  it('refuses a mixed-coordinate selective undo after remote edits', () => {
    const command = savedBoardCommand(base(), move, { seq: 2, result_seq: 2, element_revs: { a: 2, b: 2 }, undo_expires_at: expiry }, base())
    const latest = structuredClone(command.applied); latest.seq = 3; latest.records[0].element_rev = 3; latest.records[0].element = { id: 'a', x: 80, y: 90 }
    expect(planSavedUndo(command, latest)).toEqual({ kind: 'conflict', conflicts: [{ kind: 'element', id: 'a' }] })
  })
  it('records grouped deletion as one inverse batch', () => {
    const before = base(); const forward: CollaborativeBatch = { base_seq: 1, upserts: [], deletes: before.records.map(item => ({ id: item.element_id, expected_rev: item.element_rev })) }
    const command = savedBoardCommand(before, forward, { seq: 2, result_seq: 2, element_revs: { a: 2, b: 2 }, undo_expires_at: expiry }, before)
    const undo = planSavedUndo(command, command.applied)
    expect(undo.kind).toBe('ready'); if (undo.kind === 'ready') expect(undo.batch.upserts.map(item => [item.element.id, item.expected_rev])).toEqual([['a', 2], ['b', 2]])
  })
  it('requires explicit placement deletion receipt and refuses ABA restores', () => {
    const before = base(); before.placements = [{ id: 'p', placement_rev: 4, x: 0, y: 0, width: 50, height: 50 }]
    const forward: CollaborativeBatch = { base_seq: 1, upserts: [], deletes: [], placement_deletes: [{ id: 'p', expected_rev: 4 }] }
    expect(() => savedBoardCommand(before, forward, { seq: 2, result_seq: 2, element_revs: { a: 2, b: 2 }, undo_expires_at: expiry }, before)).toThrow('placement authority')
    const command = savedBoardCommand(before, forward, { seq: 2, result_seq: 2, undo_expires_at: expiry, placement_revs: { p: 5 } }, before)
    expect(planSavedUndo(command, command.applied)).toMatchObject({ kind: 'ready', batch: { placement_restores: [{ id: 'p', expected_rev: 5 }] } })
    const latest = structuredClone(command.applied); latest.seq = 4; latest.placement_tombstones![0].placement_rev = 7
    expect(planSavedUndo(command, latest).kind).toBe('conflict')
  })
})

it('uses canonical new-element revision instead of inferring expected plus one', () => {
  const before = base(); before.seq = 10
  const command = savedBoardCommand(before, { base_seq: 10, upserts: [{ element: { id: 'new', x: 0 }, expected_rev: 0 }], deletes: [] }, { seq: 11, result_seq: 11, element_revs: { new: 11 }, undo_expires_at: expiry }, before)
  expect(command.inverse.deletes).toEqual([{ id: 'new', expected_rev: 11 }])
})
it('withholds undo when a receipt lacks actual element revisions or expiration', () => {
  expect(() => savedBoardCommand(base(), move, { seq: 2, result_seq: 2, undo_expires_at: expiry }, base())).toThrow('element authority')
  expect(() => savedBoardCommand(base(), move, { seq: 2, result_seq: 2, element_revs: { a: 2 } }, base())).toThrow('expiration')
})
it('expires at the original server deadline and refuses compacted targets before expiry', () => {
  const command = savedBoardCommand(base(), { base_seq: 1, upserts: [], deletes: [{ id: 'a', expected_rev: 1 }] }, { seq: 2, result_seq: 2, element_revs: { a: 9 }, undo_expires_at: expiry }, base())
  expect(planSavedUndo(command, command.applied, Date.parse(expiry))).toEqual({ kind: 'expired' })
  const compacted = { ...command.applied, seq: 3, records: command.applied.records.filter(item => item.element_id !== 'a') }
  expect(planSavedUndo(command, compacted, Date.parse(expiry) - 1)).toEqual({ kind: 'conflict', conflicts: [{ kind: 'element', id: 'a' }] })
})
it('undoes pin mode using its prior stored baseline and ignores dynamic head advances', () => {
  const before = base(); before.placements = [{ id: 'pin', mode: 'fixed', placement_rev: 8, x: 0, y: 0, width: 100, height: 100, source: { page_slug: 'report', title: 'Report', revision: 3, pinned_revision: 3 } }]
  const forward: CollaborativeBatch = { base_seq: 1, upserts: [], deletes: [], placement_modes: [{ id: 'pin', expected_rev: 8, mode: 'follow_latest', source_revision: 3 }] }
  const command = savedBoardCommand(before, forward, { seq: 2, result_seq: 2, placement_revs: { pin: 9 }, undo_expires_at: expiry }, before)
  const latest = structuredClone(command.applied); latest.placements[0].source = { page_slug: 'report', title: 'Report', revision: 10, pinned_revision: 3 }
  expect(planSavedUndo(command, latest)).toMatchObject({ kind: 'ready', batch: { placement_modes: [{ id: 'pin', expected_rev: 9, mode: 'fixed', source_revision: 3 }] } })
})
it('restores the prior follow baseline rather than inferring it from the last displayed head', () => {
  const before = base(); before.placements = [{ id: 'pin', mode: 'follow_latest', placement_rev: 8, x: 0, y: 0, width: 100, height: 100, source: { page_slug: 'report', title: 'Report', revision: 7, pinned_revision: 1 } }]
  const forward: CollaborativeBatch = { base_seq: 1, upserts: [], deletes: [], placement_modes: [{ id: 'pin', expected_rev: 8, mode: 'fixed', source_revision: 7 }] }
  const receipt = { seq: 2, result_seq: 2, placement_revs: { pin: 9 }, undo_expires_at: expiry }
  expect(savedBoardCommand(before, forward, receipt, before).inverse.placement_modes).toEqual([{ id: 'pin', expected_rev: 9, mode: 'follow_latest', source_revision: 1 }])
  delete before.placements[0].source!.pinned_revision
  expect(() => savedBoardCommand(before, forward, receipt, before)).toThrow('original pin mode baseline')
})
