import { readFileSync } from 'node:fs'
import path from 'node:path'
import { expect, it } from 'vitest'
import { BOARD_FORMAT, BOARD_EDITOR_VERSION, BOARD_CONVERTER_VERSION, assertBoardCompatibility } from './compatibility'
import { manualBoardBatch } from './manual-batch'
import { savedBoardCommand, planSavedUndo } from './command-history'
import { validateBoardDiagram } from './diagram-profile'
import type { BoardHead } from './collaboration-types'
import type { BoardElement } from './revision-adapter'

const file = (name: string) => readFileSync(path.resolve(name), 'utf8')
const roundTrip = <T>(value: T): T => JSON.parse(JSON.stringify(value))
const persisted = (): BoardHead => ({ format: BOARD_FORMAT, seq: 41, can_write: true, placements: [], placement_tombstones: [], records: (JSON.parse(file('internal/pages/testdata/board-excalidraw-0.18.1.json')) as BoardElement[]).map(element => ({ element_id: element.id, element_rev: 23, deleted: false, element, author_user_id: 'human-author' })) })

it('reloads the real pinned browser scene without changing bindings, editor clocks or application authority', () => {
  const before = persisted()
  const encoded = JSON.stringify(before)
  const reloaded = roundTrip(before)
  assertBoardCompatibility(reloaded)
  expect(JSON.stringify(reloaded)).toBe(encoded)
  expect(new Set(reloaded.records.map(record => record.element!.type))).toEqual(new Set(['rectangle', 'ellipse', 'diamond', 'text', 'line', 'arrow', 'frame']))
  const boundLabel = reloaded.records.find(record => record.element!.containerId === 'rect')!
  expect(reloaded.records.find(record => record.element_id === 'rect')!.element!.boundElements).toContainEqual({ type: 'text', id: boundLabel.element_id })
  expect(manualBoardBatch(reloaded, reloaded.records.map(record => ({ ...record.element!, version: 999999, versionNonce: 987654321, updated: 999999 })))).toEqual({ base_seq: 41, upserts: [], deletes: [] })
  const draft = reloaded.records.map(record => record.element_id === 'rect' ? { ...record.element!, x: 120, version: 999999 } : record.element!)
  const batch = manualBoardBatch(reloaded, draft)
  expect(batch.base_seq).toBe(41)
  expect(batch.upserts.map(item => [item.element.id, item.expected_rev])).toEqual([['rect', 23]])
  expect(JSON.stringify(before)).toBe(encoded)
})

it('preserves receipt-bound saved undo across serialization and detects foreign graph edits after reload', () => {
  const before = persisted()
  const rectangle = before.records.find(record => record.element_id === 'rect')!.element!
  const forward = { base_seq: before.seq, upserts: [{ element: { ...rectangle, x: 150 }, expected_rev: 23 }], deletes: [] }
  const receipt = { seq: 42, result_seq: 42, element_revs: { rect: 24 }, undo_expires_at: '2030-01-01T00:00:00Z' }
  const command = roundTrip(savedBoardCommand(before, forward, roundTrip(receipt), before))
  const latest = roundTrip(command.applied)
  expect(planSavedUndo(command, latest, 0)).toMatchObject({ kind: 'ready', batch: { base_seq: 42, upserts: [{ element: rectangle, expected_rev: 24 }] } })
  latest.seq = 43
  const label = latest.records.find(record => record.element!.containerId === 'rect')!
  label.element_rev = 25; label.element = { ...label.element!, text: 'Foreign label edit' }
  expect(planSavedUndo(command, latest, 0).kind).toBe('conflict')
  expect(planSavedUndo(command, command.applied, Date.parse(receipt.undo_expires_at))).toEqual({ kind: 'expired' })
  expect(() => savedBoardCommand(before, forward, { ...receipt, element_revs: { rect: 23 } }, before)).toThrow('element authority')
})

it('keeps the pinned converter grammar and dependency/license metadata explicit without upgrading them', () => {
  const packages = JSON.parse(file('package.json')).dependencies
  expect(packages['@excalidraw/excalidraw']).toBe(BOARD_EDITOR_VERSION)
  expect(packages['@excalidraw/mermaid-to-excalidraw']).toBe(BOARD_CONVERTER_VERSION)
  expect(file('internal/pages/board.go')).toContain(`BoardFormat             = "${BOARD_FORMAT}"`)
  expect(file('THIRD-PARTY-NOTICES.md')).toContain('`@excalidraw/excalidraw` 0.18.1')
  const source = roundTrip({ dialect: 'flowchart-v1', converter_version: BOARD_CONVERTER_VERSION, source: 'flowchart TD\nA[Plan] --> B[Build]\nB --> C[Review]' })
  expect(validateBoardDiagram(source.source)).toBe(source.source)
  expect(() => validateBoardDiagram('flowchart TD\nclick A "https://example.test"')).toThrow()
})

it('rejects a future or absent profile without migrating or changing the saved bytes', () => {
  for (const format of ['crewship-board-excalidraw-0.19.0-v2', '', undefined]) {
    const scene = { ...persisted(), format }
    const encoded = JSON.stringify(scene)
    expect(() => assertBoardCompatibility(scene)).toThrow('compatible Crewship release')
    expect(JSON.stringify(scene)).toBe(encoded)
  }
})
