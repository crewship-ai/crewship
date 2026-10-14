import { describe, expect, it } from 'vitest'
import { planSavedUndo, recordSavedUndo, savedBoardCommand } from './command-history'
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


describe('receipt-backed multi-step saved undo', () => {
  const firstMove = () => savedBoardCommand(base(), move, { seq: 2, result_seq: 2, element_revs: { a: 10 }, undo_expires_at: expiry }, base())
  const ready = (command: ReturnType<typeof savedBoardCommand>, latest: BoardHead) => {
    const plan = planSavedUndo(command, latest)
    expect(plan.kind).toBe('ready')
    if (plan.kind !== 'ready') throw new Error('Expected ready undo')
    return plan.batch
  }
  it('carries actual receipt revisions through moves and deletion/restore without extending expiration', () => {
    const first = firstMove()
    const deletion = savedBoardCommand(first.applied, { base_seq: 2, upserts: [], deletes: [{ id: 'a', expected_rev: 10 }] }, { seq: 3, result_seq: 3, element_revs: { a: 12 }, undo_expires_at: expiry }, first.applied)
    const inverse = ready(deletion, deletion.applied)
    const [remaining] = recordSavedUndo([first, deletion], inverse, { seq: 4, result_seq: 4, element_revs: { a: 17 } })
    const latest = structuredClone(first.applied); latest.seq = 4; latest.records[0].element_rev = 17
    expect(ready(remaining, latest).upserts).toEqual([{ expected_rev: 17, element: { id: 'a', x: 0 } }])
    expect(remaining.expiresAt).toBe(first.expiresAt)
    expect(planSavedUndo(remaining, latest, first.expiresAt)).toEqual({ kind: 'expired' })
    expect(first.applied.records[0].element_rev).toBe(10)
  })
  it('carries pin authority through movement and undoing deletion, without source metadata', () => {
    const before = base(); before.placements = [{ id: 'p', placement_rev: 5, x: 0, y: 0, width: 100, height: 100, source: { title: 'Private title', page_slug: 'private', revision: 3 } }]
    const first = savedBoardCommand(before, { base_seq: 1, upserts: [], deletes: [], placement_updates: [{ id: 'p', expected_rev: 5, x: 10, y: 0, width: 100, height: 100 }] }, { seq: 2, result_seq: 2, placement_revs: { p: 6 }, undo_expires_at: expiry }, before)
    const deletion = savedBoardCommand(first.applied, { base_seq: 2, upserts: [], deletes: [], placement_deletes: [{ id: 'p', expected_rev: 6 }] }, { seq: 3, result_seq: 3, placement_revs: { p: 7 }, undo_expires_at: expiry }, first.applied)
    const inverse = ready(deletion, deletion.applied)
    expect(() => recordSavedUndo([first, deletion], inverse, { seq: 4, result_seq: 4 })).toThrow('undo authority')
    const [remaining] = recordSavedUndo([first, deletion], inverse, { seq: 4, result_seq: 4, placement_revs: { p: 9 } })
    const latest = structuredClone(first.applied); latest.seq = 4; latest.placements[0].placement_rev = 9
    expect(ready(remaining, latest).placement_updates).toEqual([{ id: 'p', expected_rev: 9, x: 0, y: 0, width: 100, height: 100 }])
    expect(JSON.stringify(remaining)).not.toContain('Private title')
    latest.seq = 5; latest.placements[0].placement_rev = 10
    expect(planSavedUndo(remaining, latest)).toEqual({ kind: 'conflict', conflicts: [{ kind: 'placement', id: 'p' }] })
  })
  it('preserves foreign ABA edits made before a later local save', () => {
    const first = firstMove()
    const remote = structuredClone(first.applied); remote.seq = 3; remote.records[0].element_rev = 11
    const second = savedBoardCommand(remote, { base_seq: 3, upserts: [{ element: { id: 'a', x: 30 }, expected_rev: 11 }], deletes: [] }, { seq: 4, result_seq: 4, element_revs: { a: 12 }, undo_expires_at: expiry }, remote)
    const [remaining] = recordSavedUndo([first, second], ready(second, second.applied), { seq: 5, result_seq: 5, element_revs: { a: 13 } })
    const latest = structuredClone(remote); latest.seq = 5; latest.records[0].element_rev = 13
    expect(planSavedUndo(remaining, latest)).toEqual({ kind: 'conflict', conflicts: [{ kind: 'element', id: 'a' }] })
  })
  it('rebases connected snapshots for owned inverse changes but still detects foreign graph edits', () => {
    const before = base(); before.records.forEach(item => { item.element!.groupIds = ['group'] })
    const first = savedBoardCommand(before, { ...move, upserts: [{ element: { ...before.records[0].element!, x: 20 }, expected_rev: 1 }] }, { seq: 2, result_seq: 2, element_revs: { a: 2 }, undo_expires_at: expiry }, before)
    const second = savedBoardCommand(first.applied, { base_seq: 2, upserts: [{ element: { ...first.applied.records[1].element!, x: 50 }, expected_rev: 1 }], deletes: [] }, { seq: 3, result_seq: 3, element_revs: { b: 2 }, undo_expires_at: expiry }, first.applied)
    const [remaining] = recordSavedUndo([first, second], ready(second, second.applied), { seq: 4, result_seq: 4, element_revs: { b: 3 } })
    const latest = structuredClone(first.applied); latest.seq = 4; latest.records[1].element_rev = 3
    expect(ready(remaining, latest).upserts[0].expected_rev).toBe(2)
    latest.seq = 5; latest.records[1].element_rev = 4
    expect(planSavedUndo(remaining, latest)).toEqual({ kind: 'conflict', conflicts: [{ kind: 'element', id: 'b' }] })
  })
  it('rebases retained pin mode preconditions without changing the prior source baseline', () => {
    const before = base(); before.placements = [{ id: 'p', placement_rev: 5, mode: 'fixed', x: 0, y: 0, width: 100, height: 100, source: { title: 'Private title', page_slug: 'private', revision: 3, pinned_revision: 3 } }]
    const first = savedBoardCommand(before, { base_seq: 1, upserts: [], deletes: [], placement_modes: [{ id: 'p', expected_rev: 5, mode: 'follow_latest', source_revision: 3 }] }, { seq: 2, result_seq: 2, placement_revs: { p: 6 }, undo_expires_at: expiry }, before)
    const second = savedBoardCommand(first.applied, { base_seq: 2, upserts: [], deletes: [], placement_updates: [{ id: 'p', expected_rev: 6, x: 10, y: 0, width: 100, height: 100 }] }, { seq: 3, result_seq: 3, placement_revs: { p: 7 }, undo_expires_at: expiry }, first.applied)
    const [remaining] = recordSavedUndo([first, second], ready(second, second.applied), { seq: 4, result_seq: 4, placement_revs: { p: 8 } })
    const latest = structuredClone(first.applied); latest.seq = 4; latest.placements[0].placement_rev = 8
    expect(ready(remaining, latest).placement_modes).toEqual([{ id: 'p', expected_rev: 8, mode: 'fixed', source_revision: 3 }])
  })
  it('rejects missing, stale or wrong-sequence undo proof and altered inverse payloads', () => {
    const first = firstMove()
    const second = savedBoardCommand(first.applied, { base_seq: 2, upserts: [{ element: { id: 'a', x: 30 }, expected_rev: 10 }], deletes: [] }, { seq: 3, result_seq: 3, element_revs: { a: 11 }, undo_expires_at: expiry }, first.applied)
    const inverse = ready(second, second.applied)
    expect(() => recordSavedUndo([first, second], inverse, { seq: 4, result_seq: 4 })).toThrow('undo authority')
    expect(() => recordSavedUndo([first, second], inverse, { seq: 4, result_seq: 4, element_revs: { a: 11 } })).toThrow('undo authority')
    expect(() => recordSavedUndo([first, second], inverse, { seq: 5, result_seq: 5, element_revs: { a: 12 } })).toThrow('undo sequence')
    expect(() => recordSavedUndo([first, second], { ...inverse, upserts: [] }, { seq: 4, result_seq: 4, element_revs: { a: 12 } })).toThrow('saved inverse')
  })
})
