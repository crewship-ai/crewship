import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import os from 'node:os'
import { performance } from 'node:perf_hooks'
import { scenarios } from './scenarios.mjs'

export class MissingContract extends Error {}
export class InconclusiveMeasurement extends Error {}
export function requireHook(object, name) {
  if (typeof object?.[name] !== 'function') throw new MissingContract(`Missing real integration hook: ${name}`)
  return object[name].bind(object)
}
export function summarize(values) {
  assert.ok(values.length)
  const ordered = [...values].sort((a, b) => a - b)
  return { n: values.length, p50: ordered[Math.ceil(ordered.length * .5) - 1], p95: ordered[Math.ceil(ordered.length * .95) - 1], max: ordered.at(-1) }
}
export async function eventually(check, message, timeout = 30_000) {
  const deadline = performance.now() + timeout
  let failure
  do {
    try { return await check() } catch (error) { failure = error }
    await new Promise(resolve => setTimeout(resolve, 20))
  } while (performance.now() < deadline)
  throw new Error(message, { cause: failure })
}
export function text(state) { return state.paragraphs.map(p => p.text).join('\n') }
export function document(state) { return state.paragraphs }
export async function convergence(session) {
  const read = requireHook(session, 'read')
  return eventually(async () => {
    const states = await Promise.all(session.pages.map(page => read(page)))
    for (const state of states) {
      assert.ok(Array.isArray(state.paragraphs) && state.paragraphs.length)
      assert.equal(new Set(state.paragraphs.map(p => p.id)).size, state.paragraphs.length)
      assert.ok(state.paragraphs.every(p => typeof p.id === 'string' && typeof p.text === 'string'))
      assert.deepEqual(state.clientParagraphs, state.paragraphs, 'Rendered client model converges to independent committed model')
      assert.equal(state.revision, states[0].revision)
      assert.deepEqual(document(state), document(states[0]))
    }
    return states[0]
  }, 'Two clients did not converge with the authoritative document')
}
export async function saved(session, page, action) {
  const before = await requireHook(session, 'receiptCursor')(page)
  const start = performance.now()
  await action()
  const receipt = await requireHook(session, 'waitSaved')(page, before)
  const verified = await verifySaved(session, page, receipt)
  assert.ok(verified.savedObservedAt >= start, 'Saved event predates original input')
  return { ...verified, milliseconds: verified.savedObservedAt - start }
}
export async function verifySaved(session, page, receipt) {
  assert.equal(receipt.durability, 'committed')
  assert.ok(typeof receipt.op_id === 'string' && receipt.op_id)
  assert.ok(typeof receipt.revision === 'string' && /^\d+$/.test(receipt.revision))
  const records = await requireHook(session, 'operationRecords')(receipt.op_id)
  assert.equal(records.method, 'read-only-sql')
  assert.ok(typeof receipt.principal === 'string' && receipt.principal)
  for (const name of ['changes', 'receipts', 'audit']) {
    assert.equal(records[name].length, 1, `Saved lacks unique SQL ${name}`)
    assert.equal(records[name][0].op_id, receipt.op_id)
    assert.equal(records[name][0].revision, receipt.revision)
    assert.equal(records[name][0].principal, receipt.principal)
  }
  const ui = await requireHook(session, 'savedEvidence')(page, receipt.op_id)
  assert.equal(ui.state, 'Saved')
  assert.equal(ui.op_id, receipt.op_id)
  assert.equal(ui.revision, receipt.revision)
  assert.equal(ui.clock, 'node-monotonic')
  assert.equal(ui.prematureSavedObserved, false)
  assert.ok(Number.isFinite(ui.receiptReceivedAt) && Number.isFinite(ui.enteredSavedAt) && ui.enteredSavedAt >= ui.receiptReceivedAt)
  assert.ok(ui.enteredSavedAt <= performance.now(), 'Saved timestamp must use this Node monotonic clock')
  return { ...receipt, savedObservedAt: ui.enteredSavedAt }
}

async function main() {
  const output = resolve(process.env.WORDCRAFT_NIGHT_OUTPUT ?? 'test-results/wordcraft-yrs-night')
  await mkdir(output, { recursive: true, mode: 0o700 })
  const selected = (process.env.WORDCRAFT_NIGHT_SCENARIOS ?? '1,2,3,4,5,6,7,8,9').split(',').map(Number)
  assert.ok(selected.length && selected.every(n => Number.isInteger(n) && n >= 1 && n <= 9), 'Choose scenario numbers 1–9')
  const report = { started: new Date().toISOString(), device: { platform: os.platform(), arch: os.arch(), cpu: os.cpus()[0]?.model, cpus: os.cpus().length, memory: os.totalmem() }, results: [] }
  let browser, adapter, preflight
  try {
    if (!process.env.CREWSHIP_SERVER) throw new MissingContract('No local disposable product server supplied (CREWSHIP_SERVER missing)')
    const base = new URL(process.env.CREWSHIP_SERVER ?? '')
    assert.equal(base.protocol, 'http:')
    assert.equal(base.hostname, '127.0.0.1', 'Only a local disposable server is allowed')
    assert.equal(base.username + base.password + base.search + base.hash, '')
    const manifestPath = process.env.CREWSHIP_THROWAWAY_MANIFEST
    if (!manifestPath) throw new MissingContract('CREWSHIP_THROWAWAY_MANIFEST is required')
    const manifest = JSON.parse(await readFile(manifestPath, 'utf8'))
    assert.equal(manifest.state, 'running')
    // Match the exact URL written by the supported throwaway helper, rather
    // than trusting an arbitrary localhost URL or a shared development port.
    assert.equal(`http://127.0.0.1:${manifest.port}`, base.origin, 'Manifest must identify this exact server')
    if (!process.env.WORDCRAFT_NIGHT_ADAPTER) throw new MissingContract('WORDCRAFT_NIGHT_ADAPTER must name a real product integration module')
    adapter = await import(pathToFileURL(resolve(process.env.WORDCRAFT_NIGHT_ADAPTER)).href)
    const dependencyRoot = resolve(process.env.WORDCRAFT_NIGHT_DEPENDENCIES ?? '.')
    const require = createRequire(resolve(dependencyRoot, 'package.json'))
    const { chromium } = require('@playwright/test')
    browser = await chromium.launch({ headless: true })
    report.browser = browser.version()
    report.base = base.origin
    report.productSHA = process.env.WORDCRAFT_NIGHT_PRODUCT_SHA
    assert.match(report.productSHA ?? '', /^[a-f0-9]{40}$/, 'Record exact tested product SHA')
    const response = await fetch(new URL('/api/v1/system/version', base), { redirect: 'manual' })
    assert.equal(response.status, 200)
    assert.equal((await response.json()).commit, report.productSHA, 'Unexpected product binary')
  } catch (error) { preflight = error }
  for (const number of selected) {
    const result = { scenario: number, status: 'not-run', started: new Date().toISOString() }
    let session
    try {
      if (preflight) throw preflight
      session = await requireHook(adapter, 'setup')({ browser, scenario: number, baseURL: report.base, output })
      assert.equal(session.pages.length, 2)
      assert.notEqual(session.pages[0].context(), session.pages[1].context())
      assert.equal(session.identities.length, 2)
      assert.notEqual(session.identities[0], session.identities[1], 'Two people must be separate authenticated identities')
      requireHook(session, 'close')
      result.status = 'running'
      result.measurements = await scenarios[number](session, { output, summarize, convergence, saved, verifySaved, requireHook, eventually, text, document })
      result.status = 'passed'
    } catch (error) {
      result.status = error instanceof InconclusiveMeasurement ? 'inconclusive' : preflight || error instanceof MissingContract ? 'not-run' : 'failed'
      result.reason = error.message
      result.cause = error.cause?.message
    } finally {
      try { await session?.close?.() } catch (error) { result.status = 'failed'; result.cleanupError = error.message }
      result.finished = new Date().toISOString()
      report.results.push(result)
      await writeFile(resolve(output, 'report.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 })
      console.log(JSON.stringify(result))
    }
  }
  await browser?.close()
  process.exitCode = report.results.every(result => result.status === 'passed') ? 0 : 1
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main()
