import { createHash } from 'node:crypto'
import { afterEach, beforeEach, vi } from 'vitest'
import { createCommandSession } from '../wordcraft-command-session'
import type { CommandResponse } from '../wordcraft-command-contract'

const mocks = vi.hoisted(() => ({ fetcher: vi.fn() }))
export const fetcher = mocks.fetcher
vi.mock('@/lib/api-fetch', () => ({ apiFetch: mocks.fetcher }))
vi.mock('@/lib/server-base', () => ({ getAuthMode: () => 'cookie' }))
const hash = (raw: string) => 'sha256:' + createHash('sha256').update(raw).digest('hex')
export const opened = (): CommandResponse => ({ view: 'original-view', actor: 'human:alice', format: 'wordcraft-c1-v2', engine: '7ef2633a7e84956522d1ca47b99fe6feef16c1db', projection: { generation: 'g1', revision: 0, ids: ['doc:g1:block:1'], native_json: '{"body":[{"kind":"para","text":""}]}' }, outcome: null, editor: { bundle_digest: 'a'.repeat(64), url: `/_editor/b/${'a'.repeat(64)}/index.html` } })
export const proposal = JSON.stringify({ registry_id: 'text.insert', command: 'text.insert', params: { text: 'A' }, target: { block_id: 'doc:g1:block:1', unit: 'utf8_bytes', start: 0, end: 0 } })
export const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
export function committed(raw: string): CommandResponse {
  const input = JSON.parse(raw), value = opened(), revision = input.base_revision + 1
  value.projection.revision = revision
  value.projection.native_json = JSON.stringify({ body: [{ kind: 'para', text: 'A'.repeat(revision) }] })
  value.outcome = { op_id: input.op_id, input_digest: hash(raw), fingerprint: 'sha256:' + 'b'.repeat(64), revision, receipt: { state: 'host_committed', op_id: input.op_id, revision, actor: 'human:alice', author: 'alice', fingerprint: 'sha256:' + 'b'.repeat(64), selection: { anchor: { story: 'body', path: [0], off: 0 }, focus: { story: 'body', path: [0], off: 0 } } } }
  return value
}
const controls: ReturnType<typeof createCommandSession>[] = []
export function setup() {
  const project = vi.fn(), state = vi.fn(), stopped = vi.fn()
  const control = createCommandSession('ws', 'page', opened(), true, { project, state, stopped })
  controls.push(control); control.start()
  return { control, project, state, stopped }
}
export const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve() }
beforeEach(() => {
  vi.useFakeTimers(); fetcher.mockReset()
  vi.spyOn(crypto.subtle, 'digest').mockImplementation(async (_algorithm, input) => Uint8Array.from(createHash('sha256').update(Buffer.from(input as ArrayBuffer)).digest()).buffer)
})
afterEach(() => { controls.splice(0).forEach(control => control.close()); vi.useRealTimers(); vi.restoreAllMocks() })
