// Synthetic harness unit tests only. No engine, authority or product acceptance
// claim follows from this in-memory model.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'
import { generate, shrink, exercise, safeCatalog, sameFailure, assertQualifiedTrace } from './fuzz.mjs'

const profile = [{ id: 'unit.insert', schema: { type: 'object', additionalProperties: false, required: ['text'], properties: { text: { type: 'string', minLength: 1, maxLength: 8, alphabet: 'abcčř' } } }, meaningOracle: 'unit-append' }]
function unitAdapter(drop = false) {
  return {
    isValid: async () => true,
    execute: async ({ mode, trace }) => {
      const operations = trace.flat()
      const included = operations.filter(op => !(drop && mode === 'binding-concurrent' && op.participant === 1))
      const model = included.map(op => ({ opId: op.opId, text: op.params.text }))
      return {
        mode, documents: Array.from({ length: mode === 'standalone' ? 1 : 3 }, () => structuredClone(model)),
        reloaded: model, restarted: model, invariantErrors: [],
        receipts: operations.map(op => ({ opId: op.opId, participant: op.participant, durability: 'committed' })),
      }
    },
    semanticConservation: async ({ trace, result }) => trace.flat().map(op => ({
      opId: op.opId, participant: op.participant,
      preserved: result.documents[0].some(item => item.opId === op.opId),
      expectedMeaning: op.params.text,
      observedMeaning: result.documents[0].find(item => item.opId === op.opId)?.text,
    })),
  }
}
test('fixed seed generates identical bounded commands and 2–3 client rounds', () => {
  const trace = generate(profile, 1234, 20)
  assert.deepEqual(generate(profile, 1234, 20), trace)
  assert.notDeepEqual(generate(profile, 1235, 20), trace)
  assert.ok(trace.every(round => round.length >= 2 && round.length <= 3 && round.every(op => op.params.text.length >= 1 && op.params.text.length <= 8)))
})
test('unit serial/concurrent model passes harness meaning, receipt and persistence assertions', async () => {
  assert.deepEqual(await exercise(unitAdapter(), generate(profile, 1234, 4), 1234), { valid: true })
})
test('converged data loss fails conservation and shrinks to the offending participant operation', async () => {
  const trace = generate(profile, 5, 5)
  const adapter = unitAdapter(true)
  await assert.rejects(exercise(adapter, trace, 5), /Participant work lost/)
  const minimal = await shrink(trace, async candidate => {
    try { await exercise(adapter, candidate, 5); return false } catch (error) { return error.message.includes('Participant work lost') }
  })
  assert.equal(minimal.length, 1)
  assert.equal(minimal[0].length, 1)
  assert.equal(minimal[0][0].participant, 1)
})
test('pure and document-candidate flags cannot authorize external-effect-unknown commands', () => {
  const catalog = JSON.stringify({ engine: { sha: 'unit' }, sourceIdentity: [], commands: [{ id: 'unit.insert', pure: true, effect: 'document-candidate' }] })
  const policy = { engineSHA: 'unit', machineGenerated: true, catalogSha256: createHash('sha256').update(catalog).digest('hex'), classifier: { path: 'unit', sha256: 'a'.repeat(64) }, commands: [] }
  assert.throws(() => safeCatalog(catalog, policy), /No source-qualified command/)
})
test('shrinker preserves original failure signature instead of switching bugs', async () => {
  const trace = [[{ id: 'A' }, { id: 'B' }], [{ id: 'C' }]]
  const minimal = await shrink(trace, candidate => sameFailure(async () => {
    throw new Error(candidate.flat().some(op => op.id === 'A') ? 'original-A' : 'different-B')
  }, 'original-A'))
  assert.deepEqual(minimal, [[{ id: 'A' }]])
})
test('regression replay revalidates every command and parameter against safe profile', () => {
  const trace = generate(profile, 7, 2)
  assert.doesNotThrow(() => assertQualifiedTrace(trace, profile))
  trace[0][0].command = 'file.external-effect'
  assert.throws(() => assertQualifiedTrace(trace, profile), /outside the qualified profile/)
})
test('legitimate zero, false and empty meanings are accepted as present values', async () => {
  for (const meaning of [0, false, '']) {
    const adapter = unitAdapter()
    adapter.semanticConservation = async ({ trace }) => trace.flat().map(op => ({ opId: op.opId, participant: op.participant, preserved: true, expectedMeaning: meaning, observedMeaning: meaning }))
    assert.deepEqual(await exercise(adapter, generate(profile, 19, 1), 19), { valid: true })
  }
})
test('synthetic CLI regression directory replays after profile validation and preserves failure signature', async () => {
  const root = await mkdtemp(join(tmpdir(), 'wordcraft-fuzz-cli-unit-'))
  const digest = text => createHash('sha256').update(text).digest('hex')
  const engine = 'a'.repeat(40)
  try {
    const classifier = join(root, 'classifier.mjs')
    const classifierBytes = '// Synthetic unit classifier; never a qualified product classifier.\n'
    await writeFile(classifier, classifierBytes)
    const source = { path: 'unit-source.rs', sha256: 'b'.repeat(64) }
    const catalog = JSON.stringify({ engine: { sha: engine }, sourceIdentity: [source], commands: [{ id: 'unit.noop', parameterDescriptor: '{}', permanentlyForbidden: false }] })
    const policy = { engineSHA: engine, catalogSha256: digest(catalog), machineGenerated: true, classifier: { path: classifier, sha256: digest(classifierBytes) }, commands: [{ id: 'unit.noop', externalEffects: 'none', parameterClosure: 'qualified', registryDescriptorSha256: digest('{}'), sourceProof: [source], parameterSchema: { type: 'object', properties: {}, additionalProperties: false }, meaningOracle: 'unit' }] }
    const fixture = { seed: 1, signature: 'unit-regression', engineSHA: engine, catalogSha256: digest(catalog), policySha256: digest(JSON.stringify(policy)), classifierSha256: policy.classifier.sha256, trace: [[{ opId: 'unit1', participant: 0, command: 'unit.noop', params: {}, meaningOracle: 'unit' }]] }
    await Promise.all([
      writeFile(join(root, 'catalog.json'), catalog),
      writeFile(join(root, 'policy.json'), JSON.stringify(policy)),
      writeFile(join(root, 'regression-1.json'), JSON.stringify(fixture)),
      writeFile(join(root, 'manifest.json'), JSON.stringify({ state: 'running', port: 48081 })),
      writeFile(join(root, 'adapter.mjs'), `export async function preflight(){return {standaloneEngineSHA:'${engine}',bindingEngineSHA:'${engine}',modelEngineSHA:'${engine}'}}; export async function isValid(){return true}; export async function semanticConservation(){return []}; export async function execute(){throw new Error('unit-regression')}`),
    ])
    const error = await promisify(execFile)(process.execPath, [fileURLToPath(new URL('./fuzz.mjs', import.meta.url))], { env: {
      PATH: process.env.PATH, WORDCRAFT_FUZZ_POLICY: join(root, 'policy.json'), WORDCRAFT_FUZZ_CATALOG: join(root, 'catalog.json'),
      WORDCRAFT_FUZZ_ADAPTER: join(root, 'adapter.mjs'), WORDCRAFT_FUZZ_REPLAY: root, WORDCRAFT_FUZZ_OUTPUT: join(root, 'output'),
      CREWSHIP_SERVER: 'http://127.0.0.1:48081', CREWSHIP_THROWAWAY_MANIFEST: join(root, 'manifest.json'),
    } }).then(() => null, error => error)
    assert.equal(error.code, 1)
    const report = JSON.parse(await readFile(join(root, 'output', 'report.json'), 'utf8'))
    assert.equal(report.status, 'failed')
    assert.equal(report.replays[0].outcome, 'reproduced')
    assert.equal(report.replays[0].signature, fixture.signature)
    assert.equal(report.combinations, 1)
  } finally { await rm(root, { recursive: true, force: true }) }
})
