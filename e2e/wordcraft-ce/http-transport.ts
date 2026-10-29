import { createHash } from 'node:crypto'
import { performance } from 'node:perf_hooks'
import type { Page, Request, Response } from '@playwright/test'
import type { ObservedWordCraftCommand, ObservedWordCraftSaved } from './latency'

export const C1_ENGINE = '7ef2633a7e84956522d1ca47b99fe6feef16c1db'
const record = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const keys = (v: Record<string, unknown>, names: string[]) => Object.keys(v).length === names.length && names.every(k => Object.hasOwn(v, k))
const revision = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0
const id = (v: unknown): v is string => typeof v === 'string' && v.length > 0 && Buffer.byteLength(v) <= 200 && !v.includes('\0')
const digest = (v: unknown): v is string => typeof v === 'string' && /^sha256:[0-9a-f]{64}$/.test(v)
const hash = (v: string) => `sha256:${createHash('sha256').update(v).digest('hex')}`
export interface C1Projection { generation: string; revision: number; ids: string[]; native_json: string }
export interface C1Response {
  view: string; actor: string; format: 'wordcraft-c1-v2'; engine: typeof C1_ENGINE
  projection: C1Projection
  outcome: null | { op_id: string; fingerprint: string; input_digest: string; revision: number; receipt: Record<string, unknown> }
  editor: { bundle_digest: string; url: string }
}
export function parseC1Response(v: unknown): C1Response {
  if (!record(v) || !keys(v, ['view', 'actor', 'format', 'engine', 'projection', 'outcome', 'editor']) || !id(v.view) || !id(v.actor) || !v.actor.startsWith('human:') || v.format !== 'wordcraft-c1-v2' || v.engine !== C1_ENGINE) throw Error('Actual C1 response identity is invalid')
  const p = v.projection
  if (!record(p) || !keys(p, ['generation', 'revision', 'ids', 'native_json']) || !id(p.generation) || p.generation.includes(':') || !revision(p.revision) || !Array.isArray(p.ids) || !p.ids.length || p.ids.length > 4096 || !p.ids.every(id) || new Set(p.ids).size !== p.ids.length || typeof p.native_json !== 'string' || !p.native_json.length || Buffer.byteLength(p.native_json) > 4 * 1024 * 1024) throw Error('Actual C1 native projection is invalid')
  if (!record(v.editor) || !keys(v.editor, ['bundle_digest', 'url']) || typeof v.editor.bundle_digest !== 'string' || !/^[0-9a-f]{64}$/.test(v.editor.bundle_digest) || typeof v.editor.url !== 'string') throw Error('Actual C1 renderer identity is invalid')
  const o = v.outcome
  if (o !== null && (!record(o) || !keys(o, ['op_id', 'fingerprint', 'input_digest', 'revision', 'receipt']) || !id(o.op_id) || !digest(o.fingerprint) || !digest(o.input_digest) || !revision(o.revision) || !record(o.receipt))) throw Error('Actual C1 outcome is invalid')
  return v as unknown as C1Response
}
export function parseC1Original(raw: string) {
  if (typeof raw !== 'string' || Buffer.byteLength(raw) > 65536) throw Error('Actual C1 original is invalid')
  const v: unknown = JSON.parse(raw)
  if (!record(v) || !keys(v, ['op_id', 'generation', 'participant', 'base_revision', 'target', 'command', 'params']) || !id(v.op_id) || !id(v.generation) || !id(v.participant) || !v.participant.startsWith('human:') || !revision(v.base_revision) || !id(v.command) || !record(v.params) || !(v.target === null || record(v.target))) throw Error('Actual C1 original is invalid')
  return v as { op_id: string; generation: string; participant: string; base_revision: number; target: Record<string, unknown> | null; command: string; params: Record<string, unknown> }
}
export function committedC1Receipt(raw: string, value: unknown) {
  const original = parseC1Original(raw), response = parseC1Response(value), o = response.outcome
  if (!o || o.op_id !== original.op_id || o.input_digest !== hash(raw) || o.revision !== response.projection.revision || o.revision !== original.base_revision + 1 || original.generation !== response.projection.generation || original.participant !== response.actor) throw Error('Actual C1 result does not confirm this exact original')
  const r = o.receipt
  if (r.state !== 'host_committed' || r.op_id !== o.op_id || r.actor !== response.actor || r.author !== response.actor.slice(6) || r.fingerprint !== o.fingerprint || r.revision !== o.revision || !record(r.selection) || !keys(r.selection, ['anchor', 'focus'])) throw Error('Actual C1 receipt is not host committed')
  for (const point of [r.selection.anchor, r.selection.focus]) {
    if (!record(point) || !keys(point, ['story', 'path', 'off']) || point.story !== 'body' || !Array.isArray(point.path) || point.path.length !== 1 || !revision(point.path[0]) || point.path[0] >= response.projection.ids.length || !revision(point.off)) throw Error('Actual C1 native selection receipt is invalid')
  }
  return { original, response }
}

/** Passive observation of the product's real HTTP command-session requests.
 * It never sends a command or retains auth headers, view handles or native JSON
 * in returned command/receipt records. Request identity binds the response. */
export class WordCraftHTTPObserver {
  private commands: ObservedWordCraftCommand[] = []
  private receipts: ObservedWordCraftSaved[] = []
  private requests = new Map<Request, { raw: string; command: ObservedWordCraftCommand }>()
  private fatal?: Error
  private closed = false
  private nextId = 0
  private listeners = new Set<() => void>()
  initialRevision?: string
  constructor(private page: Page, private endpoint: string, private timeoutMs = 30_000) {}
  private sent = (request: Request) => {
    if (request.method() !== 'POST' || request.url() !== this.endpoint) return
    try {
      const envelope: unknown = request.postDataJSON()
      if (!record(envelope) || envelope.action !== 'submit') return
      if (!keys(envelope, ['action', 'view', 'raw']) || !id(envelope.view) || typeof envelope.raw !== 'string') throw Error('Actual C1 submit envelope is invalid')
      const original = parseC1Original(envelope.raw)
      const command: ObservedWordCraftCommand = { type: 'command', op_id: original.op_id, command: original.command, base_revision: String(original.base_revision), target: original.target ?? {}, params: original.params, sentAt: performance.now(), transportId: `http-${++this.nextId}`, transportURL: new URL(this.endpoint).origin + new URL(this.endpoint).pathname }
      this.requests.set(request, { raw: envelope.raw, command }); this.commands.push(command); this.notify()
    } catch { this.fail('Actual C1 submit could not be qualified') }
  }
  private received = (response: Response) => {
    const request = response.request()
    if (request.method() !== 'POST' || request.url() !== this.endpoint) return
    void (async () => {
      const envelope = request.postDataJSON()
      if (!record(envelope)) return
      if (response.status() !== 200) { if (envelope.action === 'submit') throw Error('Actual C1 submit was refused'); return }
      const value: unknown = await response.json()
      if (this.closed) return
      if (envelope.action === 'open') { const opened = parseC1Response(value); if (opened.outcome !== null) throw Error('Opening unexpectedly mutated the document'); this.initialRevision = String(opened.projection.revision); return }
      const pending = this.requests.get(request)
      if (!pending) return
      const { response: confirmed } = committedC1Receipt(pending.raw, value)
      this.receipts.push({ type: 'command_result', op_id: pending.command.op_id, revision: String(confirmed.projection.revision), status: 'saved', durability: 'committed', savedAt: performance.now(), transportId: pending.command.transportId, transportURL: pending.command.transportURL })
      this.requests.delete(request); this.notify()
    })().catch(() => this.fail('Actual C1 response failed original/host-commit correlation'))
  }
  async start() { this.page.on('request', this.sent); this.page.on('response', this.received) }
  cursor() { return this.commands.length }
  commandSince(cursor: number, command: string) { return this.wait(() => this.commands.slice(cursor).find(value => value.command === command), 'No actual C1 HTTP command was observed') }
  saved(op: string, source?: string) { return this.wait(() => this.receipts.find(value => value.op_id === op && (source === undefined || value.transportId === source)), 'No actual C1 host-committed receipt was observed') }
  async close() { this.closed = true; this.requests.clear(); this.page.off('request', this.sent); this.page.off('response', this.received); this.notify() }
  private fail(reason: string) { this.fatal = new Error(reason); this.notify() }
  private notify() { this.listeners.forEach(listener => listener()) }
  private wait<T>(find: () => T | undefined, message: string): Promise<T> {
    return new Promise((resolve, reject) => {
      const check = () => { if (this.closed || this.fatal) { cleanup(); reject(this.fatal ?? Error('C1 observer closed')); return }; const value = find(); if (value) { cleanup(); resolve(value) } }
      const cleanup = () => { clearTimeout(timer); this.listeners.delete(check) }
      const timer = setTimeout(() => { cleanup(); reject(Error(message)) }, this.timeoutMs)
      this.listeners.add(check); check()
    })
  }
}
