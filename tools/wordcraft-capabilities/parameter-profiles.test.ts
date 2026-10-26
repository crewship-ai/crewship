// @vitest-environment node
import { expect, it } from 'vitest'
import { fromJSONSchema } from 'zod'
import { readFileSync } from 'node:fs'
import profiles from './parameter-profiles.json'
import catalog from '../../schemas/wordcraft/capabilities.json'
import { generate } from './generate.mjs'

for (const [id, profile] of Object.entries(profiles.commands)) {
  it(`${id}: accepts the documented source profile and rejects invalid or unknown parameters`, () => {
    const validate = fromJSONSchema(profile.parameterSchema as Parameters<typeof fromJSONSchema>[0])
    for (const value of profile.validExamples) expect(validate.safeParse(value).success, `${id}: valid profile`).toBe(true)
    for (const value of profile.invalidExamples) expect(validate.safeParse(value).success, `${id}: invalid profile`).toBe(false)
    for (const value of profile.validExamples) expect(validate.safeParse({ ...value, command: 'file.open' }).success).toBe(false)
    expect(catalog.commands.find(command => command.id === id)).toMatchObject({ schemaStatus: 'exact', schemaCoverage: 'bounded-source-profile', qualification: 'unqualified' })
  })
}
it('rejects stale parameter profiles instead of copying them to a changed registry', () => {
  const registry = JSON.parse(readFileSync(new URL('./registry.json', import.meta.url), 'utf8'))
  registry.find((command: { id: string }) => command.id === 'para.bullets').params = '{}'
  expect(() => generate(registry, catalog.sourceIdentity)).toThrow('parameter profile registry drift')
})
it('requires matching pinned implementation hashes for every source-reviewed profile', () => {
  const registry = JSON.parse(readFileSync(new URL('./registry.json', import.meta.url), 'utf8'))
  const identity = catalog.sourceIdentity.map(file => file.path.endsWith('/cmd/lists.rs') ? { ...file, sha256: 'changed' } : file)
  expect(() => generate(registry, identity)).toThrow('parameter profile source drift')
})
it('resolves every previously missing list schema without granting any capability', () => {
  const lists = catalog.commands.filter(command => command.group === 'lists')
  expect(lists).toHaveLength(9)
  expect(lists.every(command => command.schemaStatus === 'exact' && command.parameterSchema !== false && command.qualification === 'unqualified')).toBe(true)
})
it('has a closed parameter schema for every one of the 163 C-E commands', () => {
  const commands = catalog.commands.filter(command => ['text-enter', 'character-format', 'paragraphs', 'lists'].includes(command.group))
  expect(commands).toHaveLength(163)
  expect(commands.every(command => command.schemaStatus === 'exact' && command.parameterSchema !== false && command.qualification === 'unqualified')).toBe(true)
})
