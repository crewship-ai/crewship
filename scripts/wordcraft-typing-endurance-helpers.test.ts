// @vitest-environment node
import { describe, expect, it } from 'vitest'
import { ENGINE, FORMAT, advanceProjection, canonicalRefreshChallenge, configuration, failureCode, hasUnsettledInput, hostStatusKind, inputDigest, observeFullWindow, progress, reloadVerdict, runVerdict, syntheticOriginal, validateReceipt } from './wordcraft-typing-endurance-helpers.mjs'

const projection = (revision = 0, text = '') => ({ generation: 'g1', revision, ids: ['p1'], native_json: JSON.stringify({ body: [{ kind: 'para', text }] }) })
function committed() {
  const original = { op_id: 'g1:operation', generation: 'g1', participant: 'human:alice', base_revision: 0, command: 'text.insert', params: { text: 'A' }, target: { block_id: 'p1', start: 0, end: 0 } }
  const request = { action: 'submit', view: 'view1', raw: JSON.stringify(original) }
  const fingerprint = 'sha256:' + 'a'.repeat(64)
  const value = { format: FORMAT, engine: ENGINE, actor: 'human:alice', view: 'view1', projection: projection(1, 'A'), outcome: { op_id: original.op_id, revision: 1, input_digest: inputDigest(request.raw), fingerprint, receipt: { state: 'host_committed', op_id: original.op_id, revision: 1, actor: 'human:alice', author: 'alice', fingerprint } } }
  return { value, request }
}
describe('endurance evidence guards (no browser/server)', () => {
  it('keeps 3×900s defaults and rejects nonfinite pacing', () => {
    const env = { WORDCRAFT_ENDURANCE_BASE_URL: 'https://example.test', WORDCRAFT_ENDURANCE_SHA: 'a'.repeat(40), WORDCRAFT_ENDURANCE_AUTH: '/fixture/auth.json', WORDCRAFT_ENDURANCE_WORKSPACE: 'workspace', WORDCRAFT_ENDURANCE_OUTPUT: '/fixture/new-output' }
    expect(configuration(env)).toMatchObject({ duration: 900, runs: 3, interval: 250 })
    expect(() => configuration({ ...env, WORDCRAFT_ENDURANCE_INTERVAL_MS: 'NaN' })).toThrow('invalid_configuration')
  })
  it('ignores cancelled stale reads but refuses generation changes and same-revision forks', () => {
    const latest = projection(2, 'AA')
    expect(advanceProjection(latest, projection(1, 'A'))).toBe(latest)
    expect(advanceProjection(latest, projection(3, 'AAA')).revision).toBe(3)
    expect(() => advanceProjection(latest, { ...latest, generation: 'other' })).toThrow('generation_changed')
    expect(() => advanceProjection(latest, projection(2, 'wrong'))).toThrow('conflicting_same_revision')
  })
  it('requires the exact original digest, authority, revision and durable receipt', () => {
    const f = committed()
    expect(validateReceipt(f.value, f.request, 'human:alice')).toMatchObject({ opId: 'g1:operation', revision: 1, digest: inputDigest(f.request.raw) })
    for (const change of [
      (v: typeof f.value) => { v.outcome.input_digest = 'sha256:' + 'b'.repeat(64) },
      (v: typeof f.value) => { v.outcome.receipt.author = 'mallory' },
      (v: typeof f.value) => { v.outcome.receipt.state = 'staged' },
      (v: typeof f.value) => { v.projection.revision = 2 },
      (v: typeof f.value) => { v.outcome.op_id = 'other' },
      (v: typeof f.value) => { v.view = 'other-view' },
    ]) {
      const candidate = structuredClone(f.value)
      change(candidate)
      expect(() => validateReceipt(candidate, f.request, 'human:alice')).toThrow('invalid_receipt_binding')
    }
  })
  it('archives only the exact synthetic fixture input, not extra authority or arbitrary text', () => {
    const f = committed(), original = JSON.parse(f.request.raw)
    original.target = { block_id: 'p1', unit: 'utf8_bytes', start: 0, end: 0 }
    expect(syntheticOriginal(original, 'human:alice', projection())).toBe(true)
    expect(syntheticOriginal({ ...original, authorization: 'never-archive' }, 'human:alice', projection())).toBe(false)
    expect(syntheticOriginal({ ...original, params: { text: 'arbitrary' } }, 'human:alice', projection())).toBe(false)
    expect(syntheticOriginal(original, 'human:other', projection())).toBe(false)
  })
  it('only classifies canonical pre-handler session expiry as a refresh challenge', () => {
    expect(canonicalRefreshChallenge(401, { error: 'session_expired' })).toBe(true)
    expect(canonicalRefreshChallenge(401, { error: 'session_revoked' })).toBe(false)
    expect(canonicalRefreshChallenge(403, { error: 'session_expired' })).toBe(false)
  })
  it('continues the entire clock window after a browser step throws', async () => {
    let clock = 0, errors = 0, steps = 0
    await observeFullWindow({ start: 0, durationMs: 900_000, intervalMs: 1_000, now: () => clock, sleep: async (ms: number) => { clock += ms }, step: async () => { steps++; if (steps === 2) throw Error('sensitive arbitrary exception') }, onStepError: () => { errors++ } })
    expect(clock).toBe(900_000)
    expect(steps).toBe(900)
    expect(errors).toBe(1)
    expect(progress(0, 900_000, clock, 1_000)).toEqual({ completedWindow: true, elapsedMs: 900_000, typingDurationMs: 1_000 })
  })
  it('does not turn startup failure or a shortened horizon into a passed run', () => {
    expect(runVerdict({ initialization: { ready: false }, failure: null, offered: 0, confirmed: 0, completedWindow: false })).toBe('initialization_failed')
    expect(runVerdict({ initialization: { ready: true }, failure: null, offered: 1, confirmed: 1, completedWindow: false, reload: { state: 'confirmed_data_preserved' } })).toBe('failed')
    expect(progress(10, 910, 909).completedWindow).toBe(false)
  })
  it('cannot pass a complete timer window with zero typing', () => {
    const run = { initialization: { ready: true }, failure: null, completedWindow: true, offered: 0, confirmed: 0, reload: { state: 'confirmed_data_preserved' } }
    expect(runVerdict(run)).toBe('failed')
    expect(runVerdict({ ...run, offered: 1, confirmed: 1 })).toBe('passed')
  })
  it('treats an offered key without HTTP or a durable receipt without apply as unsettled', () => {
    const settled = { offered: 1, confirmed: 1, applied: 1, activeAttempt: null, operations: [{ committed: true }] }
    expect(hasUnsettledInput(settled)).toBe(false)
    expect(hasUnsettledInput({ ...settled, confirmed: 0, applied: 0, operations: [] })).toBe(true)
    expect(hasUnsettledInput({ ...settled, applied: 0 })).toBe(true)
    expect(hasUnsettledInput({ ...settled, activeAttempt: { opId: null } })).toBe(true)
    expect(hasUnsettledInput({ ...settled, operations: [{ committed: false }] })).toBe(true)
    expect(hasUnsettledInput({ offered: 0, confirmed: 0, applied: 0, activeAttempt: null, operations: [] })).toBe(false)
  })
  it('never labels unknown late commits a persistence loss or reloads them', () => {
    expect(reloadVerdict({ unsettled: true, before: 'A', after: 'AA', opened: true, rendererReady: true })).toEqual({ attempted: false, state: 'skipped_unknown_original', dataPreserved: null, rendererReady: null })
    expect(reloadVerdict({ unsettled: false, before: 'A', after: 'A', opened: true, rendererReady: false }).state).toBe('reload_unconfirmed')
    expect(reloadVerdict({ unsettled: false, before: 'A', after: 'A', opened: true, rendererReady: true }).state).toBe('confirmed_data_preserved')
  })
  it('emits only fixed codes even for unknown phase or arbitrary UI text', () => {
    expect(failureCode('initialize')).toBe('native_initialization_failed')
    expect(failureCode('https://user:secret@example.test')).toBe('unclassified_harness_failure')
    expect(hostStatusKind('Saved')).toBe('saved')
    expect(hostStatusKind('secret arbitrary server message')).toBe('other')
  })
})
