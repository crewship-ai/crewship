import { expect, it } from 'vitest'
import { BoardRevisionAdapter, type BoardSnapshot } from '../revision-adapter'
const initial: BoardSnapshot = { seq: 1, records: [{ revision: 1, element: { id: 'one', x: 0, version: 1 } }, { revision: 1, element: { id: 'two', x: 0 } }] }
it('uses server revisions, ignoring forged editor metadata', () => {
  const adapter = new BoardRevisionAdapter(initial)
  const batch = adapter.prepare([{ id: 'one', x: 20, version: 999999 }, { id: 'two', x: 0 }])
  expect(batch.upserts[0].expected_rev).toBe(1)
  expect(batch.base_seq).toBe(1)
})
it('keeps rejected proposals independently from the canonical repair', () => {
  const adapter = new BoardRevisionAdapter(initial)
  const first = adapter.prepare([{ id: 'one', x: 10 }, { id: 'two', x: 0 }])
  const accepted = { seq: 2, records: [{ revision: 2, element: { id: 'one', x: 10 } }, initial.records[1]] }
  adapter.accepted(first, accepted)
  const remote = { seq: 3, records: [{ revision: 3, element: { id: 'one', x: 20 } }, initial.records[1]] }
  adapter.receive(remote)
  const undo = adapter.prepare([{ id: 'one', x: 0, version: 500 }, { id: 'two', x: 0 }])
  expect(undo.upserts[0].expected_rev).toBe(3)
  adapter.rejected(undo, remote)
  expect(adapter.canonical()).toEqual(remote)
  expect(adapter.proposal).toEqual(undo)
  undo.upserts[0].expected_rev = 999
  expect(adapter.proposal!.upserts[0].expected_rev).toBe(3)
})
it('groups deletion and retains tombstone preconditions', () => {
  const adapter = new BoardRevisionAdapter(initial)
  expect(adapter.prepare([]).deletes).toEqual([{ id: 'one', expected_rev: 1 }, { id: 'two', expected_rev: 1 }])
  expect(() => adapter.receive({ seq: 0, records: [] })).toThrow('backwards')
})
it('ignores editor null-to-empty binding normalization and repeated callback content', () => {
  const adapter = new BoardRevisionAdapter({ seq: 1, records: [{ revision: 1, element: { id: 'one', type: 'rectangle', x: 0, boundElements: null } }] })
  expect(adapter.prepare([{ id: 'one', type: 'rectangle', x: 0, boundElements: [], version: 900 }]).upserts).toEqual([])
  const proposed = [{ id: 'one', type: 'rectangle', x: 30, boundElements: [] }]
  expect(adapter.prepare(proposed).upserts).toHaveLength(1)
  expect(adapter.prepare(proposed).upserts).toEqual([])
})
