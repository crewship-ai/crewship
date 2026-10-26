// @vitest-environment node
import { createHash } from 'node:crypto'
import { expect, it } from 'vitest'
import { C1_ENGINE, committedC1Receipt, parseC1Original, parseC1Response } from './http-transport'
const raw = JSON.stringify({ op_id: 'g1:one', generation: 'g1', participant: 'human:a', base_revision: 3, target: { block_id: 'p0', start: 0, end: 1, unit: 'utf8_bytes' }, command: 'format.bold', params: {} })
const fingerprint = `sha256:${'a'.repeat(64)}`
const response = () => ({ view: 'private-view', actor: 'human:a', format: 'wordcraft-c1-v2', engine: C1_ENGINE,
  projection: { generation: 'g1', revision: 4, ids: ['p0'], native_json: '{"body":[{"kind":"para","text":"x","runs":[{"len":1}]}]}' },
  editor: { bundle_digest: 'b'.repeat(64), url: '/_editor/b/bundle/index.html' },
  outcome: { op_id: 'g1:one', input_digest: `sha256:${createHash('sha256').update(raw).digest('hex')}`, fingerprint, revision: 4,
    receipt: { state: 'host_committed', op_id: 'g1:one', actor: 'human:a', author: 'a', fingerprint, revision: 4, selection: { anchor: { story: 'body', path: [0], off: 1 }, focus: { story: 'body', path: [0], off: 1 } } } } })
it('qualifies the real HTTP C1 original and its host committed receipt', () => {
  expect(parseC1Original(raw)).toMatchObject({ base_revision: 3, command: 'format.bold' })
  expect(committedC1Receipt(raw, response()).response.projection.revision).toBe(4)
})
it('refuses another original, actor, revision, generation or noncommitted state', () => {
  for (const change of [
    (r: ReturnType<typeof response>) => { r.outcome.input_digest = `sha256:${'c'.repeat(64)}` },
    (r: ReturnType<typeof response>) => { r.outcome.receipt.state = 'buffered' },
    (r: ReturnType<typeof response>) => { r.actor = 'human:other' },
    (r: ReturnType<typeof response>) => { r.projection.generation = 'g2' },
    (r: ReturnType<typeof response>) => { r.outcome.receipt.revision = 3 },
    (r: ReturnType<typeof response>) => { r.outcome.receipt.selection.focus.path = [1] },
  ]) { const r = response(); change(r); expect(() => committedC1Receipt(raw, r)).toThrow() }
})
it('refuses unsupported envelopes and unknown original authority fields', () => {
  expect(() => parseC1Response({ ...response(), engine: 'another' })).toThrow()
  expect(() => parseC1Original(JSON.stringify({ ...JSON.parse(raw), author: 'spoof' }))).toThrow()
  expect(() => parseC1Response({ ...response(), projection: { ...response().projection, revision: 1.5 } })).toThrow()
})

import { EventEmitter } from 'node:events'
import type { Page, Request, Response } from '@playwright/test'
import { WordCraftHTTPObserver } from './http-transport'
const endpoint = 'https://fixture.invalid/api/v1/pages/%40owned/mooring/command-session?workspace_id=w'
const request = (url = endpoint) => ({ method: () => 'POST', url: () => url, postDataJSON: () => ({ action: 'submit', view: 'private-view', raw }) }) as unknown as Request
const received = (req: Request, body: unknown, status = 200) => ({ request: () => req, status: () => status, json: async () => body }) as unknown as Response
it('correlates the exact HTTP request and never exposes the view or original wire string', async () => {
  const page = new EventEmitter(), observer = new WordCraftHTTPObserver(page as unknown as Page, endpoint, 100)
  await observer.start()
  const req = request(), cursor = observer.cursor()
  page.emit('request', req)
  const command = await observer.commandSince(cursor, 'format.bold')
  page.emit('response', received(req, response()))
  const saved = await observer.saved(command.op_id, command.transportId)
  expect(saved.transportId).toBe(command.transportId)
  expect(saved.revision).toBe('4')
  expect(saved.savedAt).toBeGreaterThanOrEqual(command.sentAt)
  expect(JSON.stringify([command, saved])).not.toContain('private-view')
  await observer.close()
  expect(page.listenerCount('request') + page.listenerCount('response')).toBe(0)
  await expect(observer.saved(command.op_id)).rejects.toThrow('closed')
})
it('does not accept an unobserved request, other endpoint, generic error or noncommitted receipt', async () => {
  for (const scenario of ['unobserved', 'endpoint', 'denial', 'noncommitted']) {
    const page = new EventEmitter(), observer = new WordCraftHTTPObserver(page as unknown as Page, endpoint, 20)
    await observer.start()
    const req = request(scenario === 'endpoint' ? `${endpoint}&other=1` : endpoint)
    page.emit('request', req)
    const pending = expect(observer.saved('g1:one')).rejects.toThrow()
    const body = response()
    if (scenario === 'noncommitted') body.outcome.receipt.state = 'buffered'
    page.emit('response', received(scenario === 'unobserved' ? request() : req, body, scenario === 'denial' ? 403 : 200))
    await pending
    await observer.close()
  }
})
