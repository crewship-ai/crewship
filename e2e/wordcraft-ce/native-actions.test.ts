import { expect, it } from 'vitest'
import { parseNativeActions } from './native-actions'
it('requires calibrated real UI inputs without an injected command fallback', () => {
  expect(() => parseNativeActions(undefined, 'font')).toThrow()
  expect(() => parseNativeActions('{}', 'font')).toThrow()
  expect(parseNativeActions('{"font":[{"click":{"x":100,"y":40}},{"press":"Control+a"},{"type":"Arial"},{"press":"Enter"}]}', 'font')).toHaveLength(4)
})
it('refuses engine hooks, unbounded steps, and malformed coordinates', () => {
  for (const value of [[], [{ command: 'format.font' }], [{ click: { x: -1, y: 1 } }], [{ click: { x: 1, y: 1, extra: true } }], [{ type: '' }], Array(33).fill({ press: 'Enter' })]) {
    expect(() => parseNativeActions(JSON.stringify({ font: value }), 'font')).toThrow()
  }
})
