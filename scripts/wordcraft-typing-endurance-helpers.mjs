import { createHash } from 'node:crypto'

export const ENGINE = '7ef2633a7e84956522d1ca47b99fe6feef16c1db'
export const FORMAT = 'wordcraft-c1-v2'
const object = value => !!value && typeof value === 'object' && !Array.isArray(value)
const counter = value => Number.isSafeInteger(value) && value >= 0
export const inputDigest = raw => 'sha256:' + createHash('sha256').update(raw).digest('hex')

export function configuration(env) {
  const required = name => { if (!env[name]) throw Error('missing_configuration'); return env[name] }
  let base
  try { base = new URL(required('WORDCRAFT_ENDURANCE_BASE_URL')) } catch { throw Error('invalid_configuration') }
  const result = {
    origin: base.origin, sha: required('WORDCRAFT_ENDURANCE_SHA'), auth: required('WORDCRAFT_ENDURANCE_AUTH'),
    workspace: required('WORDCRAFT_ENDURANCE_WORKSPACE'), output: required('WORDCRAFT_ENDURANCE_OUTPUT'),
    duration: Number(env.WORDCRAFT_ENDURANCE_SECONDS ?? 900), runs: Number(env.WORDCRAFT_ENDURANCE_RUNS ?? 3), interval: Number(env.WORDCRAFT_ENDURANCE_INTERVAL_MS ?? 250),
  }
  if (!['https:', 'http:'].includes(base.protocol) || base.username || base.password || !/^[a-f0-9]{40}$/.test(result.sha) || !Number.isInteger(result.duration) || result.duration < 1 || result.duration > 3600 || !Number.isInteger(result.runs) || result.runs < 1 || result.runs > 10 || !Number.isFinite(result.interval) || result.interval < 100 || result.interval > 10000) throw Error('invalid_configuration')
  return result
}

export function nativeText(projection) {
  if (!object(projection) || !counter(projection.revision) || typeof projection.generation !== 'string' || !projection.generation || typeof projection.native_json !== 'string') throw Error('invalid_projection')
  const document = JSON.parse(projection.native_json)
  if (!Array.isArray(document.body) || !document.body.length || document.body.length > 4096 || !Array.isArray(projection.ids) || projection.ids.length !== document.body.length || new Set(projection.ids).size !== projection.ids.length) throw Error('invalid_projection')
  return document.body.map(block => {
    if (!object(block) || block.kind !== 'para' || typeof block.text !== 'string') throw Error('non_plain_document')
    return block.text
  }).join('\n')
}

export function advanceProjection(previous, next) {
  nativeText(next)
  if (!previous) return next
  if (next.generation !== previous.generation) throw Error('projection_generation_changed')
  if (next.revision < previous.revision) return previous
  if (next.revision === previous.revision && (next.native_json !== previous.native_json || JSON.stringify(next.ids) !== JSON.stringify(previous.ids))) throw Error('conflicting_same_revision_projection')
  return next
}

export function validateResponse(value, actor) {
  if (!object(value) || value.engine !== ENGINE || value.format !== FORMAT || value.actor !== actor || typeof value.view !== 'string' || !value.view) throw Error('invalid_command_response')
  nativeText(value.projection)
  return value
}

/** All inputs are observed from the real HTTP request/response. No inferred ack. */
export function validateReceipt(value, request, actor) {
  validateResponse(value, actor)
  if (!object(request) || request.action !== 'submit' || typeof request.raw !== 'string' || request.view !== value.view) throw Error('invalid_receipt_binding')
  const original = JSON.parse(request.raw), outcome = value.outcome, receipt = outcome?.receipt
  if (!object(original) || !object(outcome) || !object(receipt) || typeof original.op_id !== 'string' || original.participant !== actor || !counter(original.base_revision)
    || original.generation !== value.projection.generation || outcome.op_id !== original.op_id || outcome.input_digest !== inputDigest(request.raw)
    || outcome.revision !== original.base_revision + 1 || outcome.revision !== value.projection.revision || !/^sha256:[0-9a-f]{64}$/.test(outcome.fingerprint)
    || receipt.state !== 'host_committed' || receipt.op_id !== original.op_id || receipt.revision !== outcome.revision || receipt.actor !== actor || receipt.author !== actor.slice('human:'.length) || receipt.fingerprint !== outcome.fingerprint) throw Error('invalid_receipt_binding')
  return { original, revision: outcome.revision, opId: outcome.op_id, digest: outcome.input_digest }
}

export function syntheticOriginal(original, actor, projection) {
  const keys = object(original) ? Object.keys(original).sort().join(',') : ''
  const target = original?.target, params = original?.params
  return object(projection) && keys === 'base_revision,command,generation,op_id,params,participant,target'
    && typeof original.op_id === 'string' && original.op_id.length > 0 && original.op_id.length <= 200
    && original.participant === actor && original.generation === projection?.generation && counter(original.base_revision)
    && object(target) && Object.keys(target).sort().join(',') === 'block_id,end,start,unit'
    && projection.ids.includes(target.block_id) && target.unit === 'utf8_bytes' && counter(target.start) && counter(target.end) && target.end >= target.start
    && object(params) && (original.command === 'text.insert' && Object.keys(params).join(',') === 'text' && params.text === 'A'
      || original.command === 'text.newParagraph' && Object.keys(params).length === 0)
}

export function canonicalRefreshChallenge(status, body) { return status === 401 && object(body) && body.error === 'session_expired' }

export function hostStatusKind(text) {
  if (typeof text !== 'string') return 'missing'
  const known = { 'Connecting…': 'connecting', 'No unsaved changes': 'ready', Saved: 'saved', 'Saving…': 'saving', 'Read only': 'reader', 'Not confirmed · input paused': 'unknown', 'Editor unavailable': 'stopped' }
  return known[text.trim()] ?? 'other'
}

const phaseFailures = {
  version: 'deployed_sha_unavailable_or_mismatched', auth: 'authentication_unavailable', navigate: 'workspace_navigation_failed',
  new_document: 'new_document_form_failed', create: 'new_document_identity_unconfirmed', route: 'new_document_route_unconfirmed',
  initialize: 'native_initialization_failed', focus: 'native_focus_failed', typing: 'keyboard_or_observation_failed',
  snapshot: 'screenshot_unavailable', reload: 'reload_observation_failed', carry_auth: 'auth_state_carry_failed',
}
/** Arbitrary exceptions, server strings and URLs never become report codes. */
export function failureCode(phase) { return phaseFailures[phase] ?? 'unclassified_harness_failure' }

/** @param {number | null} typingStoppedAt */
export function progress(start, deadline, now, typingStoppedAt = null) {
  if (![start, deadline, now].every(Number.isFinite) || deadline < start || now < start || (typingStoppedAt !== null && (!Number.isFinite(typingStoppedAt) || typingStoppedAt < start))) throw Error('invalid_observation_clock')
  return {
    elapsedMs: now - start, completedWindow: now >= deadline,
    typingDurationMs: Math.max(0, Math.min(typingStoppedAt ?? now, deadline) - start),
  }
}

/** Injected monotonic clock/sleep lets tests prove that a throwing browser step
 * cannot truncate an already-started observation window. */
export async function observeFullWindow({ start, durationMs, intervalMs, now, sleep, step, onStepError }) {
  if (!Number.isFinite(durationMs) || durationMs <= 0 || !Number.isFinite(intervalMs) || intervalMs <= 0) throw Error('invalid_observation_window')
  const deadline = start + durationMs
  while (now() < deadline) {
    try { await step({ now: now(), deadline }) } catch { onStepError() }
    const remaining = deadline - now()
    if (remaining > 0) await sleep(Math.min(intervalMs, remaining))
  }
  return { start, deadline, ended: now() }
}

export function hasUnsettledInput({ offered, confirmed, applied, activeAttempt, operations }) {
  if (![offered, confirmed, applied].every(counter)) throw Error('invalid_input_counters')
  return offered !== confirmed || offered !== applied || activeAttempt !== null || operations.some(operation => !operation.committed)
}

export function reloadVerdict({ unsettled, before, after, opened, rendererReady }) {
  if (unsettled) return { attempted: false, state: 'skipped_unknown_original', dataPreserved: null, rendererReady: null }
  return { attempted: true, state: opened && rendererReady && after === before ? 'confirmed_data_preserved' : 'reload_unconfirmed', dataPreserved: opened && after === before, rendererReady: !!rendererReady }
}

export function runVerdict(run) {
  if (!run.initialization?.ready) return 'initialization_failed'
  return !run.failure && run.completedWindow && run.offered > 0 && run.confirmed > 0 && run.offered === run.confirmed && run.reload?.state === 'confirmed_data_preserved' ? 'passed' : 'failed'
}
