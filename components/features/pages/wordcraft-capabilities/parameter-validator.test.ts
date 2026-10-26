// @vitest-environment node
import { expect, it } from 'vitest'
import { compileParameterValidator } from './parameter-validator'

const schema = {
  type: 'object', additionalProperties: false, properties: {
    levels: { type: 'array', maxItems: 9, items: {
      type: 'object', additionalProperties: false, properties: {
        format: { type: 'string', enum: ['bullet', 'decimal'] },
        start: { type: 'integer', minimum: 0, maximum: 32767 },
        color: { anyOf: [{ type: 'string', pattern: '^[0-9A-Fa-f]{6}$' }, { type: 'null' }] },
      },
    } },
  }, required: ['levels'],
}
it('checks enums, finite bounds, unions, arrays and nested unknown fields', () => {
  const validate = compileParameterValidator(schema)!
  expect(validate({ levels: [{ format: 'bullet', start: 1, color: null }] })).toBe(true)
  for (const value of [
    {}, { levels: [{ format: 'file.open' }] }, { levels: [{ start: -1 }] },
    { levels: [{ start: Infinity }] }, { levels: [{ start: 1.5 }] },
    { levels: [{ color: 'bad' }] }, { levels: [{ command: 'file.open' }] },
    { levels: Array(10).fill({}) }, { levels: [], extra: true },
  ]) expect(validate(value)).toBe(false)
})
it('fails closed on unsupported schema keywords and unresolved schemas', () => {
  for (const value of [false, { ...schema, unevaluatedProperties: false }, { type: 'object' }, { type: 'unknown' }, { anyOf: [] }]) {
    expect(compileParameterValidator(value)).toBeNull()
  }
})
it('rejects malformed schema nodes instead of ignoring their constraints', () => {
  expect(compileParameterValidator({ type: 'string', maxLength: 'ignored' })).toBeNull()
  expect(compileParameterValidator({ type: 'array', items: true })).toBeNull()
  expect(compileParameterValidator({ type: 'string', pattern: '[' })).toBeNull()
})
it('rejects enum sibling constraints and inconsistent types instead of ignoring them', () => {
  const property = (value: object) => ({ type: 'object', properties: { value }, additionalProperties: false })
  expect(compileParameterValidator(property({ type: 'string', enum: ['x'], minLength: 3 }))).toBeNull()
  expect(compileParameterValidator(property({ type: 'string', enum: [1] }))).toBeNull()
  expect(compileParameterValidator({ anyOf: [schema], oneOf: [schema] })).toBeNull()
  expect(compileParameterValidator({ const: {}, type: 'object' })).toBeNull()
})
it('rejects nested values whose JSON serialization would change the validated representation', () => {
  const validate = compileParameterValidator({ type: 'object', additionalProperties: false, properties: { nested: { type: 'object', properties: {}, additionalProperties: false } } })!
  class Custom { toJSON() { return { command: 'file.open' } } }
  for (const nested of [new Date(), new Custom(), Object.create({ command: 'file.open' }), { toJSON: () => ({ command: 'file.open' }) }]) expect(validate({ nested })).toBe(false)
  const hidden = Object.defineProperty({}, 'nested', { value: {}, enumerable: false })
  expect(validate(hidden)).toBe(false)
  const accessor = Object.defineProperty({}, 'nested', { get: () => ({}), enumerable: true })
  expect(validate(accessor)).toBe(false)
  expect(validate({ nested: {} })).toBe(true)
})
