import { fromJSONSchema } from 'zod'

export type ParameterSchema = Readonly<Record<string, unknown>>
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const scalarTypes = new Set(['string', 'boolean', 'integer', 'number', 'null'])
const hasOnly = (value: ParameterSchema, keys: string[]) => Object.keys(value).every(key => keys.includes(key))
const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)

/** Accept only combinations emitted by the catalog. The Zod converter can
 * short-circuit enum/const or overwrite composition siblings; reject those
 * combinations rather than silently dropping a JSON Schema constraint. */
function supported(schema: unknown, depth = 0): schema is ParameterSchema {
  if (!record(schema) || depth > 24) return false
  if ('anyOf' in schema) return hasOnly(schema, ['anyOf']) && Array.isArray(schema.anyOf) && schema.anyOf.length > 0 && schema.anyOf.every(value => supported(value, depth + 1))
  const type = schema.type
  if (typeof type !== 'string') return false
  if ('enum' in schema) {
    if (!hasOnly(schema, ['type', 'enum']) || !scalarTypes.has(type) || !Array.isArray(schema.enum) || !schema.enum.length) return false
    return schema.enum.every(value => type === 'null' ? value === null : type === 'integer' ? finite(value) && Number.isSafeInteger(value) : type === 'number' ? finite(value) : typeof value === type)
  }
  if (type === 'object') {
    if (!hasOnly(schema, ['type', 'properties', 'required', 'additionalProperties']) || schema.additionalProperties !== false || !record(schema.properties)) return false
    if (Object.values(schema.properties).some(value => !supported(value, depth + 1))) return false
    return !('required' in schema) || Array.isArray(schema.required) && schema.required.every(key => typeof key === 'string' && record(schema.properties) && Object.hasOwn(schema.properties, key))
  }
  if (type === 'array') return hasOnly(schema, ['type', 'items', 'minItems', 'maxItems']) && supported(schema.items, depth + 1) && ['minItems', 'maxItems'].every(key => !(key in schema) || finite(schema[key]) && Number.isSafeInteger(schema[key]) && schema[key] >= 0)
  if (type === 'string') return hasOnly(schema, ['type', 'minLength', 'maxLength', 'pattern']) && (!('pattern' in schema) || typeof schema.pattern === 'string') && ['minLength', 'maxLength'].every(key => !(key in schema) || finite(schema[key]) && Number.isSafeInteger(schema[key]) && schema[key] >= 0)
  if (type === 'number' || type === 'integer') return hasOnly(schema, ['type', 'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum']) && ['minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum'].every(key => !(key in schema) || finite(schema[key]))
  return (type === 'boolean' || type === 'null') && hasOnly(schema, ['type'])
}

/** Validate the same inert JSON values that will be serialized. No Date,
 * class/prototype, toJSON method, getter, symbol or hidden required field. */
function jsonValue(value: unknown, budget = { left: 10_000 }, depth = 0): boolean {
  if (--budget.left < 0 || depth > 24) return false
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return true
  if (typeof value === 'number') return Number.isFinite(value)
  if (typeof value !== 'object') return false
  const array = Array.isArray(value)
  const prototype = Object.getPrototypeOf(value)
  if (array ? prototype !== Array.prototype : prototype !== Object.prototype && prototype !== null) return false
  if ('toJSON' in value) return false
  const keys = Reflect.ownKeys(value)
  if (array && keys.length !== value.length + 1) return false
  return keys.every(key => {
    if (array && key === 'length') return true
    if (typeof key !== 'string') return false
    const descriptor = Object.getOwnPropertyDescriptor(value, key)
    return !!descriptor && descriptor.enumerable === true && 'value' in descriptor && jsonValue(descriptor.value, budget, depth + 1)
  })
}

export function compileParameterValidator(schema: unknown): ((value: unknown) => boolean) | null {
  try {
    if (!supported(schema)) return null
    const validator = fromJSONSchema(schema as Parameters<typeof fromJSONSchema>[0])
    return value => {
      try { return record(value) && jsonValue(value) && validator.safeParse(value).success } catch { return false }
    }
  } catch { return null }
}
