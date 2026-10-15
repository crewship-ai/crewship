import http from 'node:http';
import { readFile, chmod, lstat } from 'node:fs/promises';
import { createHash, randomUUID, timingSafeEqual } from 'node:crypto';
import { isAbsolute, normalize } from 'node:path';
import { pathToFileURL } from 'node:url';
import * as Y from 'yjs';
import { VERSION, create, load, snapshot, outline, validate, patch, hash, decode } from './schema.mjs';

export const PROTOCOL = 1;
const fault = (status, message) => Object.assign(new Error(message), { status });
const id = value => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,128}$/.test(value);
async function body(req) {
  let size = 0; const chunks = [];
  for await (const chunk of req) { size += chunk.length; if (size > 2 * 1024 * 1024) throw fault(413, 'request limit'); chunks.push(chunk); }
  try { return JSON.parse(Buffer.concat(chunks).toString()); } catch { throw fault(400, 'invalid JSON'); }
}
export async function startService({ socket, authoritySocket, tokenFile, idleMs = 30000, timeoutMs = 10000, drainTimeoutMs = 30000 }) {
  if (!socket || !authoritySocket || !tokenFile || socket === authoritySocket || tokenFile === socket || tokenFile === authoritySocket) throw new Error('distinct service and authority Unix sockets and token file required');
  for (const path of [socket, authoritySocket, tokenFile]) if (!isAbsolute(path) || normalize(path) !== path || Buffer.byteLength(path) > 100) throw new Error('clean absolute Unix socket path required (maximum 100 bytes)');
  const tokenStat = await lstat(tokenFile);
  if (!tokenStat.isFile() || (tokenStat.mode & 0o777) !== 0o600 || tokenStat.size > 512) throw new Error('service token must be a regular mode-0600 file <=512 bytes');
  const secret = (await readFile(tokenFile, 'utf8')).trim();
  if (Buffer.byteLength(secret) < 32 || Buffer.byteLength(secret) > 256) throw new Error('service secret must contain 32..256 bytes');
  const build = createHash('sha256');
  for (const file of ['pnpm-lock.yaml', 'schema.mjs', 'service.mjs']) build.update(await readFile(new URL(file, import.meta.url)));
  const identity = { protocol: PROTOCOL, schema: VERSION, build: build.digest('hex') };
  const owner = randomUUID(); const documents = new Map(); let queued = 0; let draining = false; let admitted = 0; const decoding = new Set();
  async function go(page, operation, context, data = {}) {
    return new Promise((resolve, reject) => {
      const bytes = JSON.stringify(data);
      const req = http.request({ socketPath: authoritySocket, path: operation === 'health' ? '/health/ready' : `/internal/docs/v1/${encodeURIComponent(page)}/${operation}`, method: operation === 'health' ? 'GET' : 'POST', headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(bytes), 'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': context.workspace, ...(context.authorization ? { Authorization: context.authorization } : {}) } }, res => {
        const chunks = []; let size = 0;
        res.on('data', chunk => { size += chunk.length; if (size > 4 * 1024 * 1024) { res.destroy(); reject(fault(502, 'authority response limit')); } else chunks.push(chunk); });
        res.on('end', () => { try { const result = JSON.parse(Buffer.concat(chunks).toString()); if (res.statusCode >= 400) reject(fault(res.statusCode, result.error || 'authority rejected request')); else resolve(result); } catch { reject(fault(502, 'invalid authority response')); } });
        res.on('error', () => reject(fault(502, 'authority unavailable')));
      });
      req.setTimeout(timeoutMs, () => req.destroy());
      req.on('error', () => reject(fault(503, 'authority unavailable'))); req.end(bytes);
    });
  }
  function enqueue(key, fn) {
    let entry = documents.get(key);
    if (draining) throw fault(503, 'service draining');
    if (!entry) { if (documents.size >= 32) throw fault(429, 'open document limit'); entry = { tail: Promise.resolve(), queued: 0, touched: Date.now() }; documents.set(key, entry); }
    if (queued >= 128 || entry.queued >= 32) throw fault(429, 'document queue full');
    queued++; entry.queued++; entry.touched = Date.now();
    const result = entry.tail.then(fn);
    entry.tail = result.catch(() => {}).finally(() => { queued--; entry.queued--; entry.touched = Date.now(); });
    return result;
  }
  async function execute(page, operation, context, request) {
    if (operation === 'initialize') {
      if (request.schema !== VERSION || !id(request.id) || !request.body) throw fault(400, 'schema and operation ID required');
      const fingerprint = await hash({ operation, request });
      try { const receipt = await go(page, 'receipt', context, { id: request.id, fingerprint }); if (receipt.found) return { ...receipt, duplicate: true }; } catch (error) { if (error.status !== 404) throw error; }
      const doc = create(request.body);
      try { return await go(page, 'initialize', context, { id: request.id, fingerprint, schema: VERSION, snapshot: snapshot(doc), blocks: (await outline(doc)).map(({ id, hash }) => ({ id, hash })) }); }
      finally { doc.destroy(); }
    }
    const state = await go(page, 'state', context);
    if (state.schema !== VERSION) throw fault(409, 'unsupported schema; content preserved');
    if (operation !== 'read' && request.schema !== VERSION) throw fault(409, 'unsupported client schema');
    if (operation !== 'read' && !id(request.id)) throw fault(400, 'operation ID required');
    const fingerprint = operation === 'read' ? undefined : await hash({ operation, request });
    if (fingerprint) { const receipt = await go(page, 'receipt', context, { id: request.id, fingerprint }); if (receipt.found) return { ...receipt, duplicate: true }; }
    let doc = load(state.snapshot); let lease;
    try {
      for (const update of state.updates || []) Y.applyUpdate(doc, decode(typeof update === 'string' ? update : update.update));
      const baseVector = Y.encodeStateVector(doc);
      if (operation === 'read') return { ...state, content: state.snapshot ? validate(doc) : null, outline: state.snapshot ? await outline(doc) : [] };
      if (request.epoch !== state.epoch) throw fault(409, 'old epoch; reload document');
      lease = await go(page, 'lease', context, { owner, epoch: state.epoch });
      if (!Number.isSafeInteger(lease.epoch) || lease.epoch < state.epoch || typeof lease.token !== 'string') throw fault(409, 'writer lease changed; reload document');
      const hashes = {};
      switch (operation) {
        case 'patch':
          if (request.kind === 'human') {
            if (context.authorization.startsWith('Bearer agtv2.')) throw fault(403, 'agent raw update forbidden');
            if (typeof request.update !== 'string' || request.update.length > 1400000) throw fault(400, 'bounded update required');
            Y.applyUpdate(doc, decode(request.update));
          } else {
            const target = (await outline(doc)).find(block => block.id === request.target);
            if (request.ifRev !== state.rev || !target || target.hash !== request.ifHash) throw fault(409, 'semantic precondition conflict');
            hashes[request.target] = request.ifHash;
            patch(doc, { op: 'replaceText', id: request.target, text: request.text });
          }
          break;
        case 'restore': {
          if (request.ifRev !== state.rev) throw fault(409, 'restore precondition conflict');
          const checkpoint = await go(page, 'checkpoint-read', context, { id: request.checkpoint });
          if (checkpoint.schema !== undefined && checkpoint.schema !== VERSION) throw fault(409, 'unsupported checkpoint schema');
          doc.destroy(); doc = load(checkpoint.snapshot); break;
        }
        case 'checkpoint':
          if (request.ifRev !== state.rev) throw fault(409, 'checkpoint precondition conflict');
          break;
        default: throw fault(404, 'unknown operation');
      }
      if (doc.store.pendingStructs || doc.store.pendingDs) throw fault(400, 'unresolved Yjs dependencies');
      validate(doc);
      const blocks = (await outline(doc)).map(({ id, hash }) => ({ id, hash }));
      await go(page, 'renew', context, { owner, epoch: lease.epoch, token: lease.token });
      if (operation === 'restore') return await go(page, 'restore', context, { id: request.id, fingerprint, checkpoint: request.checkpoint, ifRev: state.rev, epoch: lease.epoch, leaseToken: lease.token, leaseOwner: owner });
      if (operation === 'checkpoint') return await go(page, 'checkpoint', context, { id: request.id, fingerprint, epoch: lease.epoch, leaseToken: lease.token, leaseOwner: owner, ifRev: state.rev, snapshot: snapshot(doc) });
      return await go(page, 'commit', context, { id: request.id, fingerprint, kind: operation === 'patch' ? (request.kind === 'human' ? 'human' : 'agent') : operation, epoch: lease.epoch, leaseToken: lease.token, leaseOwner: owner, update: Buffer.from(Y.encodeStateAsUpdate(doc, baseVector)).toString('base64'), ifRev: state.rev, schema: VERSION, snapshot: snapshot(doc), blocks, ifHashes: hashes, checkpoint: request.checkpoint });
    } finally {
      doc.destroy();
      if (lease) await go(page, 'release', context, { owner, epoch: lease.epoch, token: lease.token }).catch(() => {});
    }
  }
  const timer = setInterval(() => { for (const [key, entry] of documents) if (!entry.queued && Date.now() - entry.touched >= idleMs) documents.delete(key); }, Math.min(idleMs, 1000)); timer.unref();
  async function drain() {
    draining = true; for (const request of decoding) request.destroy(); let timeout;
    try { await Promise.race([Promise.all([...documents.values()].map(entry => entry.tail)), new Promise((_, reject) => { timeout = setTimeout(() => reject(fault(503, 'drain timeout')), drainTimeoutMs); })]); documents.clear(); }
    finally { clearTimeout(timeout); }
  }
  const server = http.createServer(async (req, res) => {
    let counted = false;
    res.setHeader('Cache-Control', 'no-store');
    const reply = (status, value) => { if (!res.destroyed) { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); } };
    try {
      const supplied = Buffer.from(String(req.headers['x-crewship-docs-service'] || '')); const expected = Buffer.from(secret);
      if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected)) throw fault(403, 'service authentication required');
      if (req.method === 'GET' && req.url === '/health/live') return reply(200, { live: true, ...identity });
      if (req.method === 'GET' && req.url === '/health/ready') {
        if (draining) return reply(503, { ready: false, ...identity });
        const authority = await go('', 'health', { workspace: '' });
        if (authority.protocol !== PROTOCOL || authority.schema !== VERSION) throw fault(503, 'authority protocol or schema unsupported');
        return reply(200, { ready: true, ...identity });
      }
      if (req.method === 'POST' && req.url === '/drain') { await drain(); return reply(200, { drained: true }); }
      if(draining){res.setHeader('Connection','close');res.once('finish',()=>req.socket.destroy());throw fault(503,'service draining');}
      const match = /^\/v1\/documents\/([a-zA-Z0-9_-]{1,128})\/(initialize|read|patch|checkpoint|restore)$/.exec(req.url);
      if (req.method !== 'POST' || !match) throw fault(404, 'unknown operation');
      const context = { workspace: req.headers['x-workspace-id'], authorization: req.headers.authorization };
      if (!id(context.workspace) || typeof context.authorization !== 'string' || !/^Bearer \S+$/.test(context.authorization)) throw fault(401, 'workspace and bearer required');
      if (admitted >= 128) throw fault(429, 'request admission limit'); admitted++; counted = true;
      let request; decoding.add(req); try { request = await body(req); } finally { decoding.delete(req); }
      reply(200, await enqueue(`${context.workspace}/${match[1]}`, () => execute(match[1], match[2], context, request)));
    } catch (error) { reply(error.status || 400, { error: error.status ? error.message : 'invalid document operation' }); } finally { if (counted) admitted--; }
  });
  server.requestTimeout = timeoutMs * 8; server.headersTimeout = timeoutMs; server.keepAliveTimeout = 1000;
  // Refuse existing paths: deleting another listener's socket would bypass its writer fence.
  try { await lstat(socket); throw new Error('service socket already exists'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(socket, resolve); });
  await chmod(socket, 0o600);
  return { identity, socket, drain, async close() { await drain(); clearInterval(timer); await new Promise(resolve => server.close(resolve)); } };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const service = await startService({ socket: process.env.CREWSHIP_DOCS_SOCKET, authoritySocket: process.env.CREWSHIP_DOCS_AUTHORITY_SOCKET, tokenFile: process.env.CREWSHIP_DOCS_SERVICE_TOKEN_FILE });
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => service.close().then(() => process.exit(0)));
}
