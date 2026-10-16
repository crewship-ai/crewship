import { expect, it } from 'vitest'
import { selectionPreviewInput } from '../selection-preview'
import type { PageBoard } from '@/hooks/use-page-board'
import type { BinaryFileData } from '@excalidraw/excalidraw/types'

it('exports only selected content and strips outside relationships and pin source metadata', () => {
  const board: PageBoard = { format: 'test', seq: 2, can_write: true, records: [
    { element_id: 'selected', element_rev: 1, deleted: false, element: { id: 'selected', type: 'rectangle', frameId: 'private', boundElements: [{ id: 'private', type: 'text' }], link: 'https://private.invalid', customData: { secret: true } } },
    { element_id: 'private', element_rev: 1, deleted: false, element: { id: 'private', type: 'text', text: 'PRIVATE TEXT' } },
  ], placements: [{ id: 'pin', x: 1, y: 2, width: 300, height: 200, source: { page_slug: 'private-page', title: 'PRIVATE TITLE', revision: 1 } }] }
  const input = selectionPreviewInput(board, ['selected', 'board-placement:pin'], [])
  expect(input.elements).toHaveLength(1)
  expect(input.elements[0]).toMatchObject({ id: 'selected', frameId: null, boundElements: [] })
  expect(input.elements[0]).not.toHaveProperty('link')
  expect(input.elements[0]).not.toHaveProperty('customData')
  expect(JSON.stringify(input)).not.toMatch(/PRIVATE|private-page|https:/)
  expect(board.records[0].element?.frameId).toBe('private')
})

it('includes only the selected image files and refuses unavailable image bytes', () => {
  const board: PageBoard = { format: 'test', seq: 1, can_write: true, placements: [], records: [
    { element_id: 'image', element_rev: 1, deleted: false, element: { id: 'image', type: 'image', fileId: 'own' } },
  ] }
  const files = [{ id: 'own', dataURL: 'data:image/png;base64,AA==', mimeType: 'image/png', created: 1 }, { id: 'other', dataURL: 'data:image/png;base64,BB==', mimeType: 'image/png', created: 1 }] as BinaryFileData[]
  expect(Object.keys(selectionPreviewInput(board, ['image'], files).files)).toEqual(['own'])
  expect(() => selectionPreviewInput(board, ['image'], [])).toThrow('not available')
})

it('renders selected drawings in saved layer order while keeping pins separate and canonical bytes unchanged', () => {
  const board: PageBoard = { format: 'test', seq: 2, can_write: true, records: [
    { element_id: 'a_front', element_rev: 3, deleted: false, element: { id: 'a_front', type: 'rectangle', index: 'a1' } },
    { element_id: 'z_back', element_rev: 8, deleted: false, element: { id: 'z_back', type: 'rectangle', index: 'a0' } },
    { element_id: 'hidden', element_rev: 1, deleted: false, element: { id: 'hidden', type: 'text', index: 'Zz' } },
  ], placements: [{ id: 'pin', x: 1, y: 2, width: 300, height: 200 }] }
  const original = JSON.stringify(board)
  const input = selectionPreviewInput(board, ['a_front', 'z_back', 'board-placement:pin'], [])
  expect(input.elements.map(element => [element.id, element.index])).toEqual([['z_back', 'a0'], ['a_front', 'a1']])
  expect(input.pins.map(pin => pin.id)).toEqual(['preview-pin:pin'])
  expect(JSON.stringify(board)).toBe(original)
})
