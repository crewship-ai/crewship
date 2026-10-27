import assert from 'node:assert/strict'
import { test } from 'node:test'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { localURL } from './local.mjs'
import { summarize, requireHook, MissingContract, verifySaved } from './run.mjs'
import { scenarios } from './scenarios.mjs'

test('unconfigured real product records all nine scenarios not-run and exits nonzero', async () => {
  const output = await mkdtemp(join(tmpdir(), 'wordcraft-night-runner-'))
  try {
    const error = await promisify(execFile)(process.execPath, [fileURLToPath(new URL('./run.mjs', import.meta.url))], {
      env: { PATH: process.env.PATH, WORDCRAFT_NIGHT_OUTPUT: output },
    }).then(() => null, error => error)
    assert.equal(error.code, 1)
    const report = JSON.parse(await readFile(join(output, 'report.json'), 'utf8'))
    assert.deepEqual(report.results.map(r => r.scenario), [1, 2, 3, 4, 5, 6, 7, 8, 9])
    assert.ok(report.results.every(r => r.status === 'not-run' && !r.measurements))
  } finally { await rm(output, { recursive: true, force: true }) }
})
test('origin guard forbids remote targets, sibling ports, redirect escapes and credentials', () => {
  const base = 'http://127.0.0.1:48081'
  for (const target of ['https://example.invalid', 'http://127.0.0.1:8080', '//example.invalid/path', 'http://localhost:48081', 'http://user:secret@127.0.0.1:48081', 'file:///tmp/example']) assert.throws(() => localURL(target, base))
  assert.equal(localURL('/api/v1/system/version', base).origin, base)
  assert.equal(localURL('ws://127.0.0.1:48081/session', base).port, '48081')
})
test('missing authority hooks fail explicitly and percentiles use nearest rank', () => {
  assert.throws(() => requireHook({}, 'read'), MissingContract)
  assert.deepEqual(summarize([5, 1, 3, 2, 4]), { n: 5, p50: 3, p95: 5, max: 5 })
})
test('unit Saved control rejects a durability label without SQL/audit and premature UI', async () => {
  const receipt = { op_id: 'unit-op', revision: '1', durability: 'committed', principal: 'unit-human' }
  await assert.rejects(verifySaved({}, {}, receipt), MissingContract)
  const rows = [{ op_id: 'unit-op', revision: '1', principal: 'unit-human' }]
  const synthetic = {
    operationRecords: async () => ({ method: 'read-only-sql', changes: rows, receipts: rows, audit: rows }),
    savedEvidence: async () => ({ state: 'Saved', op_id: 'unit-op', revision: '1', clock: 'node-monotonic', receiptReceivedAt: 20, enteredSavedAt: 10, prematureSavedObserved: true }),
  }
  await assert.rejects(verifySaved(synthetic, {}, receipt))
  synthetic.operationRecords = async () => ({ method: 'read-only-sql', changes: rows, receipts: rows, audit: [] })
  await assert.rejects(verifySaved(synthetic, {}, receipt), /Saved lacks unique SQL audit/)
})
test('unit capacity control rejects a no-op Saved after the first operation', async () => {
  let inputs = 0
  const synthetic = {
    pages: [{}], coldRestart: async () => { throw new Error('Reached restart without detecting no-op') },
    metrics: async () => Object.fromEntries(['documentBytes', 'snapshotCount', 'snapshotBytes', 'journalBytes', 'receiptBytes', 'historyBytes', 'sqlBytes', 'walBytes', 'rssBytes', 'revision'].map(key => [key, 0])),
    selectParagraph: async () => {}, receiptLedger: async () => [], retryCommittedOriginal: async () => {},
  }
  const helpers = {
    requireHook,
    convergence: async () => ({ revision: String(inputs), paragraphs: [{ id: 'unit-p', text: '' }] }),
    saved: async () => { inputs++; return { op_id: `unit-${inputs}`, revision: String(inputs), durability: 'committed' } },
  }
  await assert.rejects(scenarios[8](synthetic, helpers), /Saved operation lost\/duplicated text/)
  assert.equal(inputs, 1)
})
import { classifyClockWindow } from './latency-observer.mjs'

test('unit clock oracle retains uncertain fast-glyph timing as inconclusive', () => {
  assert.equal(classifyClockWindow({ earliest: 12, latest: 16 }, 10, 20), 'consistent')
  assert.equal(classifyClockWindow({ earliest: 12, latest: 22 }, 10, 20), 'overlap')
  assert.equal(classifyClockWindow({ earliest: 8, latest: 16 }, 10, 20), 'overlap')
  assert.equal(classifyClockWindow({ earliest: 21, latest: 22 }, 10, 20), 'inconsistent')
})
