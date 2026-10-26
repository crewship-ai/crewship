// @vitest-environment node
import { expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { applyEngineQualifications } from './engine-qualification.mjs'
import catalog from '../../schemas/wordcraft/capabilities.json'
import policy from './engine-qualification-policy.json'

const profiles = readFileSync(new URL('./parameter-profiles.json', import.meta.url))
const readSource = (path: string) => readFileSync(new URL('../../' + path, import.meta.url))
it('records eight actual engine fixtures without opening the product qualification gate', () => {
  expect(catalog.commands.every(command => command.qualification === 'unqualified')).toBe(true)
  const qualified = catalog.commands.filter(command => 'engineQualification' in command)
  expect(qualified.map(command => command.id).sort()).toEqual(Object.keys(policy.commands).sort())
  expect(qualified).toHaveLength(8)
  for (const command of qualified) expect(command.engineQualification).toMatchObject({ status: 'qualified', scope: 'isolated-c1-fixture', coverage: 'tested-fixture-only' })
})
it('refuses stale registry, profile, test, fixture or host identity', () => {
  for (const change of [{ registryDigest: 'changed' }, { profileSha256: 'changed' }, { c1HostSha: 'changed' }, { scope: 'product' }]) {
    expect(() => applyEngineQualifications(catalog, { ...policy, ...change }, profiles, readSource)).toThrow('qualification policy drift')
  }
  expect(() => applyEngineQualifications(catalog, policy, profiles, () => Buffer.from('changed'))).toThrow('qualification policy drift')
})
it('refuses widening a proven fixture or qualifying an untested command', () => {
  const widened = structuredClone(policy)
  widened.commands['format.font'].testedParameters.name = 'different'
  expect(() => applyEngineQualifications(catalog, widened, profiles, readSource)).toThrow('qualification policy drift')
  const extra = { ...policy, commands: { ...policy.commands, 'file.open': policy.commands['text.insert'] } }
  expect(() => applyEngineQualifications(catalog, extra, profiles, readSource)).toThrow('qualification policy drift')
})
