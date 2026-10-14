import { describe, expect, it } from 'vitest'
import { affectedClosure, draftBatch, planBoardMerge } from './collaboration-plan'
import type { BoardHead } from './collaboration-types'
import type { BoardElement } from './revision-adapter'
const element = (id: string, x = 0, extra: Record<string, unknown> = {}): BoardElement => ({ id, type: 'rectangle', x, ...extra })
const head = (elements: BoardElement[], seq = 1): BoardHead => ({ format: 'excalidraw@0.18.1', seq, can_write: true, placements: [], records: elements.map(item => ({ element_id: item.id, element_rev: 1, deleted: false, element: item })) })
describe('conservative board rebase', () => {
  it('preserves disjoint remote geometry and original local authority', () => {
    const base = head([element('a'), element('b')])
    const latest = head([element('a'), element('b', 90)], 2)
    latest.records[1].element_rev = 2
    const result = planBoardMerge(base, { elements: [element('a', 30), element('b')], placements: [] }, latest)
    expect(result.kind).toBe('merged')
    expect(result.draft.elements.map(item => [item.id, item.x])).toEqual([['a', 30], ['b', 90]])
    expect(draftBatch(result.canonical, result.draft)).toMatchObject({ base_seq: 2, upserts: [{ expected_rev: 1, element: { id: 'a', x: 30 } }], deletes: [] })
  })
  it('retains overlapping proposal without silently advancing its base', () => {
    const base = head([element('a')]); const latest = head([element('a', 99)], 2); latest.records[0].element_rev = 2
    const result = planBoardMerge(base, { elements: [element('a', 20)], placements: [] }, latest)
    expect(result.kind).toBe('conflict'); expect(result.canonical.seq).toBe(1); expect(result.draft.elements[0].x).toBe(20)
  })
  it.each(['frameId', 'containerId'])('conflicts through reciprocal %s dependencies', field => {
    const base = head([element('a'), element('b', 0, { [field]: 'a' })]); const latest = structuredClone(base); latest.seq = 2; latest.records[1].element_rev++
    expect(planBoardMerge(base, { elements: [element('a', 20), base.records[1].element!], placements: [] }, latest).kind).toBe('conflict')
  })
  it('includes newly attached remote arrows and groups in closure', () => {
    const base = head([element('a')]); const latest = head([element('a'), element('arrow', 0, { startBinding: { elementId: 'a' } })], 2)
    expect(planBoardMerge(base, { elements: [element('a', 20)], placements: [] }, latest).kind).toBe('conflict')
    expect(affectedClosure(['element:a'], [[element('a', 0, { groupIds: ['g'] }), element('b', 0, { groupIds: ['g'] })]])).toContain('element:b')
  })
  it('detects server revision ABA even when geometry returned unchanged', () => {
    const base = head([element('a')]); const latest = structuredClone(base); latest.seq = 3; latest.records[0].element_rev = 3
    expect(planBoardMerge(base, { elements: [element('a', 20)], placements: [] }, latest).kind).toBe('conflict')
  })
  it('ignores stale delivery and rejects inconsistent canonical sequence reuse', () => {
    const base = head([element('a')], 2); const draft = { elements: [element('a', 20)], placements: [] }
    expect(planBoardMerge(base, draft, head([element('a')], 1)).kind).toBe('stale')
    expect(() => planBoardMerge(base, draft, head([element('a', 10)], 2))).toThrow('sequence reused')
  })
  it('redacts revoked pin source without treating it as a geometry edit', () => {
    const base = head([]); base.placements = [{ id: 'p', placement_rev: 1, x: 0, y: 0, width: 100, height: 100, source: { page_slug: 'private', revision: 1, title: 'Private' } }]
    const latest = structuredClone(base); delete latest.placements[0].source
    const result = planBoardMerge(base, { elements: [], placements: base.placements }, latest)
    expect(result.kind).toBe('merged'); expect(result.draft.placements[0].source).toBeUndefined()
  })
  it('fences pin deletion ABA and legacy unversioned placement edits', () => {
    const base = head([]); base.placements = [{ id: 'p', placement_rev: 1, x: 0, y: 0, width: 100, height: 100 }]
    const latest = structuredClone(base); latest.seq = 3; latest.placements[0].placement_rev = 3
    const draft = { elements: [], placements: [{ ...base.placements[0], x: 30 }] }
    expect(planBoardMerge(base, draft, latest).kind).toBe('conflict')
    delete base.placements[0].placement_rev
    expect(planBoardMerge(base, draft, latest).kind).toBe('conflict')
  })
})
