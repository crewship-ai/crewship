import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFile, mkdir, writeFile, stat, readdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { MissingContract, requireHook } from './run.mjs'
import { localURL } from './local.mjs'

const hash = value => createHash('sha256').update(value).digest('hex')
export function random(seed) {
  let state = seed >>> 0
  return () => { state = (Math.imul(state, 1664525) + 1013904223) >>> 0; return state / 4294967296 }
}
export function safeCatalog(catalogRaw, policy) {
  const catalog = JSON.parse(catalogRaw)
  assert.equal(policy.catalogSha256, hash(catalogRaw), 'Safety policy must match exact machine-derived catalog')
  assert.equal(policy.engineSHA, catalog.engine.sha)
  assert.equal(policy.machineGenerated, true)
  assert.ok(policy.classifier?.path && /^[a-f0-9]{64}$/.test(policy.classifier.sha256))
  const entries = new Map(policy.commands.map(entry => [entry.id, entry]))
  assert.equal(entries.size, policy.commands.length)
  const allowed = []
  for (const command of catalog.commands) {
    const proof = entries.get(command.id)
    // A pure or document-candidate tag never opens the execution gate.
    if (!proof || proof.externalEffects !== 'none' || proof.parameterClosure !== 'qualified') continue
    assert.equal(command.permanentlyForbidden, false)
    assert.equal(proof.registryDescriptorSha256, hash(command.parameterDescriptor))
    assert.ok(proof.sourceProof?.length > 0)
    for (const source of proof.sourceProof) assert.ok(catalog.sourceIdentity.some(file => file.path === source.path && file.sha256 === source.sha256), 'Safety source proof drift')
    assert.ok(proof.parameterSchema && proof.meaningOracle, 'Closed schema and semantic oracle are required')
    // Validate that every parameter can actually be generated within a closed
    // schema before admitting even one execution.
    value(proof.parameterSchema, random(1))
    allowed.push({ id: command.id, schema: proof.parameterSchema, meaningOracle: proof.meaningOracle })
  }
  if (!allowed.length) throw new MissingContract('No source-qualified command without external effects; engine execution blocked')
  return allowed
}
export function value(schema, rng) {
  if (Array.isArray(schema.enum) && schema.enum.length) return structuredClone(schema.enum[Math.floor(rng() * schema.enum.length)])
  if (schema.type === 'object') {
    assert.equal(schema.additionalProperties, false, 'Object parameters must be closed')
    const result = {}
    for (const [key, child] of Object.entries(schema.properties ?? {})) {
      if (schema.required?.includes(key) || rng() < .5) result[key] = value(child, rng)
    }
    return result
  }
  if (schema.type === 'integer') {
    assert.ok(Number.isSafeInteger(schema.minimum) && Number.isSafeInteger(schema.maximum) && schema.minimum <= schema.maximum)
    return schema.minimum + Math.floor(rng() * (schema.maximum - schema.minimum + 1))
  }
  if (schema.type === 'boolean') return rng() < .5
  if (schema.type === 'string') {
    assert.ok(typeof schema.alphabet === 'string' && schema.alphabet.length && Number.isInteger(schema.maxLength) && schema.maxLength <= 256)
    const minimum = schema.minLength ?? 0
    assert.ok(Number.isInteger(minimum) && minimum >= 0 && minimum <= schema.maxLength)
    const length = minimum + Math.floor(rng() * (schema.maxLength - minimum + 1))
    const alphabet = [...schema.alphabet]
    return Array.from({ length }, () => alphabet[Math.floor(rng() * alphabet.length)]).join('')
  }
  if (schema.type === 'array') {
    assert.ok(Number.isInteger(schema.minItems) && Number.isInteger(schema.maxItems) && schema.minItems >= 0 && schema.minItems <= schema.maxItems && schema.maxItems <= 32)
    return Array.from({ length: schema.minItems + Math.floor(rng() * (schema.maxItems - schema.minItems + 1)) }, () => value(schema.items, rng))
  }
  throw new MissingContract('Unbounded or unsupported parameter schema blocks execution')
}
export function generate(catalog, seed, rounds = 10) {
  const rng = random(seed)
  const clients = 2 + Math.floor(rng() * 2)
  return Array.from({ length: rounds }, (_, round) => Array.from({ length: clients }, (_, client) => {
    const command = catalog[Math.floor(rng() * catalog.length)]
    return { opId: `s${seed}:r${round}:p${client}`, participant: client, command: command.id, params: value(command.schema, rng), meaningOracle: command.meaningOracle }
  }))
}

// Deterministic delta debugging. The result is 1-minimal for deleting rounds or
// individual operations with the same failure signature, not a global minimum.
export async function shrink(trace, reproduces) {
  let current = structuredClone(trace)
  let changed = true
  while (changed) {
    changed = false
    for (let size = Math.max(1, Math.floor(current.length / 2)); size >= 1; size = Math.floor(size / 2)) {
      for (let start = 0; start + size <= current.length; start++) {
        const candidate = current.filter((_, index) => index < start || index >= start + size)
        if (candidate.length && await reproduces(candidate)) { current = candidate; changed = true; break }
      }
      if (changed) break
    }
    if (changed) continue
    outer: for (let round = 0; round < current.length; round++) {
      for (let op = 0; op < current[round].length; op++) {
        const candidate = structuredClone(current)
        candidate[round].splice(op, 1)
        if (!candidate[round].length) candidate.splice(round, 1)
        if (candidate.length && await reproduces(candidate)) { current = candidate; changed = true; break outer }
      }
    }
  }
  return current
}
export async function sameFailure(run, signature) {
  try { await run(); return false } catch (error) {
    if (error instanceof MissingContract) throw error
    return error.message === signature
  }
}
function validValue(schema, input) {
  if (schema.enum) return schema.enum.some(item => JSON.stringify(item) === JSON.stringify(input))
  if (schema.type === 'object') return input !== null && typeof input === 'object' && !Array.isArray(input) && Object.keys(input).every(key => schema.properties?.[key] && validValue(schema.properties[key], input[key])) && (schema.required ?? []).every(key => key in input)
  if (schema.type === 'integer') return Number.isSafeInteger(input) && input >= schema.minimum && input <= schema.maximum
  if (schema.type === 'boolean') return typeof input === 'boolean'
  if (schema.type === 'string') return typeof input === 'string' && [...input].length >= (schema.minLength ?? 0) && [...input].length <= schema.maxLength && [...input].every(character => schema.alphabet.includes(character))
  if (schema.type === 'array') return Array.isArray(input) && input.length >= schema.minItems && input.length <= schema.maxItems && input.every(item => validValue(schema.items, item))
  return false
}
export function assertQualifiedTrace(trace, catalog) {
  assert.ok(Array.isArray(trace) && trace.length > 0)
  const ids = new Set()
  for (const round of trace) {
    assert.ok(Array.isArray(round) && round.length > 0 && round.length <= 3)
    for (const op of round) {
      const command = catalog.find(item => item.id === op.command)
      assert.ok(command && op.meaningOracle === command.meaningOracle && validValue(command.schema, op.params), 'Regression operation is outside the qualified profile')
      assert.ok(typeof op.opId === 'string' && !ids.has(op.opId) && Number.isInteger(op.participant) && op.participant >= 0 && op.participant <= 2)
      ids.add(op.opId)
    }
  }
}
export async function exercise(adapter, trace, seed) {
  // isValid uses the actual engine's input/model preconditions in isolation.
  // An invalid shrink candidate is rejected, never treated as a reproduction.
  if (!await requireHook(adapter, 'isValid')(trace)) return { valid: false }
  const standalone = await requireHook(adapter, 'execute')({ mode: 'standalone', trace, seed })
  const serial = await adapter.execute({ mode: 'binding-serial', trace, seed })
  assert.deepEqual(serial.documents[0], standalone.documents[0], 'Serial binding changed native command meaning')
  const concurrent = await adapter.execute({ mode: 'binding-concurrent', trace, seed })
  assert.ok(concurrent.documents.length >= 2 && concurrent.documents.length <= 3)
  for (const model of concurrent.documents) assert.deepEqual(model, concurrent.documents[0], 'Clients diverged')
  for (const result of [standalone, serial, concurrent]) {
    assert.deepEqual(result.invariantErrors, [], 'Native document model invariant failed')
    assert.deepEqual(result.reloaded, result.documents[0], 'Saved/reload lost work')
    assert.deepEqual(result.restarted, result.documents[0], 'Restart lost confirmed work')
    const expected = trace.flat()
    assert.equal(result.receipts.length, expected.length)
    assert.equal(new Set(result.receipts.map(r => r.opId)).size, expected.length)
    for (const operation of expected) {
      const receipt = result.receipts.find(r => r.opId === operation.opId)
      assert.equal(receipt?.durability, 'committed')
      assert.equal(receipt?.participant, operation.participant)
    }
    const proof = await requireHook(adapter, 'semanticConservation')({ trace, result })
    assert.equal(proof.length, expected.length, 'Every participant operation needs a meaning oracle')
    for (const operation of expected) {
      const effect = proof.find(item => item.opId === operation.opId)
      assert.equal(effect?.participant, operation.participant)
      assert.equal(effect?.preserved, true, 'Participant work lost despite convergence')
      assert.ok(effect && Object.hasOwn(effect, 'expectedMeaning') && Object.hasOwn(effect, 'observedMeaning'))
      assert.deepEqual(effect.observedMeaning, effect.expectedMeaning, 'Unexpected command meaning')
    }
  }
  return { valid: true }
}

async function main() {
  const output = resolve(process.env.WORDCRAFT_FUZZ_OUTPUT ?? 'test-results/wordcraft-yrs-night/fuzz')
  await mkdir(output, { recursive: true, mode: 0o700 })
  const seed = Number(process.env.WORDCRAFT_FUZZ_SEED ?? 20261027)
  const count = Number(process.env.WORDCRAFT_FUZZ_COUNT ?? 100)
  const report = { seed, requestedCombinations: count, combinations: 0, rejectedInvalid: 0, failures: [], status: 'blocked' }
  try {
    assert.ok(Number.isSafeInteger(seed) && seed >= 0 && seed <= 4294967295 && Number.isInteger(count) && count > 0)
    if (!process.env.WORDCRAFT_FUZZ_POLICY || !process.env.WORDCRAFT_FUZZ_ADAPTER) throw new MissingContract('Source-qualified external-effect policy and real engine/binding adapter required')
    const raw = await readFile(resolve(process.env.WORDCRAFT_FUZZ_CATALOG ?? 'schemas/wordcraft/capabilities.json'), 'utf8')
    const policy = JSON.parse(await readFile(resolve(process.env.WORDCRAFT_FUZZ_POLICY), 'utf8'))
    // Classifier and source-proof identities are part of qualification evidence;
    // policy consumers must never infer safety solely from a pure flag.
    const classifier = await readFile(resolve(policy.classifier.path))
    assert.equal(hash(classifier), policy.classifier.sha256)
    const catalog = safeCatalog(raw, policy)
    if (!process.env.CREWSHIP_SERVER || !process.env.CREWSHIP_THROWAWAY_MANIFEST) throw new MissingContract('Real binding comparison requires an owned disposable local server manifest')
    const base = localURL(process.env.CREWSHIP_SERVER, process.env.CREWSHIP_SERVER)
    const manifest = JSON.parse(await readFile(process.env.CREWSHIP_THROWAWAY_MANIFEST, 'utf8'))
    assert.equal(manifest.state, 'running')
    assert.equal(base.origin, `http://127.0.0.1:${manifest.port}`)
    const adapter = await import(pathToFileURL(resolve(process.env.WORDCRAFT_FUZZ_ADAPTER)).href)
    for (const hook of ['isValid', 'execute', 'semanticConservation']) requireHook(adapter, hook)
    const actual = await requireHook(adapter, 'preflight')({ engineSHA: policy.engineSHA, localOnly: true, baseURL: base.origin })
    assert.equal(actual.standaloneEngineSHA, policy.engineSHA, 'Standalone engine/catalog SHA mismatch blocks execution')
    assert.equal(actual.bindingEngineSHA, policy.engineSHA, 'Binding engine/catalog SHA mismatch blocks execution')
    assert.equal(actual.modelEngineSHA, policy.engineSHA, 'Model dependency/catalog SHA mismatch blocks execution')
    report.engineSHA = policy.engineSHA
    if (process.env.WORDCRAFT_FUZZ_REPLAY) {
      const path = resolve(process.env.WORDCRAFT_FUZZ_REPLAY)
      const files = (await stat(path)).isDirectory() ? (await readdir(path)).filter(name => /^regression-.*\.json$/.test(name)).sort().map(name => resolve(path, name)) : [path]
      assert.ok(files.length, 'Replay directory has no regression fixtures')
      report.replays = []
      for (const file of files) {
        const fixture = JSON.parse(await readFile(file, 'utf8'))
        assert.equal(fixture.engineSHA, policy.engineSHA, 'Regression engine identity changed')
        assert.equal(fixture.catalogSha256, hash(raw), 'Regression catalog identity changed')
        assert.equal(fixture.policySha256, hash(JSON.stringify(policy)), 'Regression safety profile changed')
        assert.equal(fixture.classifierSha256, policy.classifier.sha256, 'Regression classifier identity changed')
        assertQualifiedTrace(fixture.trace, catalog)
        let outcome = 'resolved', signature
        try {
          const result = await exercise(adapter, fixture.trace, fixture.seed)
          assert.equal(result.valid, true, 'Saved regression became invalid')
        } catch (error) {
          if (error instanceof MissingContract) throw error
          signature = error.message
          outcome = signature === fixture.signature ? 'reproduced' : 'different-failure'
        }
        report.replays.push({ file: file.split('/').at(-1), seed: fixture.seed, outcome, expectedSignature: fixture.signature, signature })
        report.combinations++
      }
      report.status = report.replays.every(item => item.outcome === 'resolved') ? 'passed' : 'failed'
      await writeFile(resolve(output, 'report.json'), JSON.stringify(report, null, 2))
      console.log(JSON.stringify(report))
      process.exitCode = report.status === 'passed' ? 0 : 1
      return
    }
    let attempts = 0
    while (report.combinations < count && attempts < count * 20) {
      const caseSeed = (seed + attempts++) >>> 0
      const trace = generate(catalog, caseSeed)
      assertQualifiedTrace(trace, catalog)
      try {
        const result = await exercise(adapter, trace, caseSeed)
        if (!result.valid) { report.rejectedInvalid++; continue }
        report.combinations++
      } catch (error) {
        if (error instanceof MissingContract) throw error
        const signature = error.message
        const minimized = await shrink(trace, candidate => sameFailure(() => exercise(adapter, candidate, caseSeed), signature))
        const file = `regression-${caseSeed}.json`
        await writeFile(resolve(output, file), JSON.stringify({ seed: caseSeed, signature, original: trace, trace: minimized, engineSHA: policy.engineSHA, catalogSha256: hash(raw), policySha256: hash(JSON.stringify(policy)), classifierSha256: policy.classifier.sha256, minimality: '1-minimal under round/operation deletion' }, null, 2))
        report.failures.push({ seed: caseSeed, signature, file })
        report.combinations++
      }
      await writeFile(resolve(output, 'report.json'), JSON.stringify(report, null, 2))
    }
    report.status = report.failures.length || report.combinations !== count ? 'failed' : 'passed'
  } catch (error) { report.reason = error.message }
  await writeFile(resolve(output, 'report.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify(report))
  process.exitCode = report.status === 'passed' ? 0 : 1
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main()
