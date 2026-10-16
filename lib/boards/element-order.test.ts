import { expect, it } from 'vitest'
import { orderBoardElements } from './element-order'

it('restores fractional layer order independently of record IDs without mutating canonical bytes', () => {
  const elements = [{ id: 'a_front', index: 'a1', version: 1 }, { id: 'z_back', index: 'a0', version: 999 }]
  const original = JSON.stringify(elements)
  expect(orderBoardElements(elements).map(element => element.id)).toEqual(['z_back', 'a_front'])
  expect(JSON.stringify(elements)).toBe(original)
  // Fractional indexes use codepoint order, not locale collation.
  expect(orderBoardElements([{ id: 'lower', index: 'aa' }, { id: 'upper', index: 'aZ' }]).map(element => element.id)).toEqual(['upper', 'lower'])
})

it('orders duplicate, missing and invalid indexes deterministically without synthesizing indexes', () => {
  const elements = [{ id: 'z', index: 'a0' }, { id: 'a', index: 'a0' }, { id: 'missing' }, { id: 'invalid', index: '!' }, { id: 'null', index: null }]
  const expected = ['a', 'z', 'invalid', 'missing', 'null']
  expect(orderBoardElements(elements).map(element => element.id)).toEqual(expected)
  expect(orderBoardElements([...elements].reverse()).map(element => element.id)).toEqual(expected)
  expect(orderBoardElements(elements).find(element => element.id === 'missing')).not.toHaveProperty('index')
  expect(orderBoardElements(elements).find(element => element.id === 'invalid')?.index).toBe('!')
})
