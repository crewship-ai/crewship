import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { WebSocketServer, WebSocket } from 'ws';
import * as Y from 'yjs';
import { VERSION, create, load, snapshot, outline, project, validate, patch, hash, decode, anchor, resolve } from './schema.mjs';

// Experimental transport: one serialized document authority, durable before
// broadcast. Hocuspocus's normal sync protocol is evaluated separately.
export async function startService(fixture, { port = 0 } = {}) {
  const peers = new Set(); let tail = Promise.resolve(); let queued = 0;
  let holdPhase = null; let held = false; let releaseHold;
  let state;
  const commitTimings = [];
  const headers = token => ({ 'Content-Type': 'application/json', 'X-D0-Service': fixture.service, 'X-Workspace-ID': fixture.workspace, Authorization: `Bearer ${token}` });
  async function go(path, token, body) {
    const response = await fetch(fixture.authority + path, { method: body === undefined ? 'GET' : 'POST', headers: headers(token), body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000) });
    const data = await response.json();
    if (!response.ok) throw Object.assign(new Error(data.error || 'authority error'), { status: response.status });
    return data;
  }
  const supported = s => { if (s.schema !== VERSION) throw Object.assign(new Error('unsupported schema version; content preserved'), { status: 409 }); };
  const read = async token => { const s = await go('/state', token); supported(s); return s; };
  const publicState = async s => {
    if (s.schema !== VERSION) return { page: s.page, rev: s.rev, epoch: s.epoch, schema: s.schema, unsupported: true };
    const doc = load(s.snapshot);
    try { return { ...s, outline: s.snapshot ? await outline(doc) : [], threads: s.threads.map(t => resolve(doc, t)), content: s.snapshot ? project(doc) : null }; }
    finally { doc.destroy(); }
  };
  const send = (peer, value) => { if (peer.readyState === WebSocket.OPEN) peer.send(JSON.stringify(value)); };
  async function publish(reset = false) {
    for (const peer of peers) {
      if (!peer.token) continue;
      try { const s = await go('/state', peer.token); send(peer, { kind: reset ? 'reset' : 'state', ...await publicState(s) }); if (s.schema !== VERSION) peer.close(4409, 'unsupported schema'); }
      catch { send(peer, { kind: 'forbidden' }); peer.close(4403, 'authority revoked'); }
    }
  }
  async function gate(phase) {
    if (holdPhase !== phase) return;
    held = true;
    await new Promise(resolve => { releaseHold = resolve; });
    held = false; holdPhase = null; releaseHold = undefined;
  }
  function enqueue(fn) {
    if (queued >= 64) return Promise.reject(Object.assign(new Error('document queue full'), { status: 429 }));
    queued++;
    const result = tail.then(fn);
    tail = result.catch(() => {}).finally(() => { queued--; });
    return result;
  }
  async function apply(token, request, human = false) {
    state = await read(token);
    if (request.schema !== VERSION) throw Object.assign(new Error('unsupported client schema'), { status: 409 });
    if (typeof request.id !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(request.id)) throw Object.assign(new Error('request ID required'), { status: 400 });
    if (human && token.startsWith('agtv2.')) throw Object.assign(new Error('agent raw update forbidden'), { status: 403 });
    if (human && request.epoch !== state.epoch) throw Object.assign(new Error('old epoch; discard local Y.Doc and reload'), { status: 409 });
    if (!human && token.startsWith('agtv2.') && request.kind !== 'agent') throw Object.assign(new Error('agent semantic patch only'), { status: 403 });
    const fingerprint = await hash(request);
    const receipt = await go(`/receipt?id=${encodeURIComponent(request.id)}&fingerprint=${fingerprint}&epoch=${state.epoch}`, token);
    if (receipt.found) return { ...await publicState(state), rev: receipt.rev, durableRev: state.rev, checkpoint: receipt.rev, duplicate: true };
    const doc = load(state.snapshot);
    let thread; let hashes = {}; let candidate = doc;
    try {
      if (human) {
        if (typeof request.update !== 'string' || request.update.length > 1400000) throw new Error('bounded update required');
        Y.applyUpdate(doc, decode(request.update));
        if (doc.store.pendingStructs || doc.store.pendingDs) throw new Error('unresolved Yjs dependencies');
      } else {
        switch (request.kind) {
          case 'create':
            if (state.rev !== 0 || state.snapshot) throw Object.assign(new Error('document exists'), { status: 409 });
            candidate = create(request.body); break;
          case 'agent': {
            if (!Number.isInteger(request.ifRev) || typeof request.ifHash !== 'string') throw Object.assign(new Error('ifRev and ifHash required'), { status: 400 });
            const target = (await outline(doc)).find(b => b.id === request.target);
            if (request.ifRev !== state.rev || !target || target.hash !== request.ifHash) throw Object.assign(new Error('semantic precondition conflict'), { status: 409 });
            hashes = { [request.target]: request.ifHash };
            patch(doc, { op: 'replaceText', id: request.target, text: request.text }); break;
          }
          case 'comment':
            if (typeof request.body !== 'string' || !request.body.trim() || request.body.length > 4000) throw new Error('bounded comment body required');
            thread = { ...anchor(doc, request.block, request.from, request.to, request.id, state.rev), body: request.body }; break;
          case 'restore': {
            if (!Number.isInteger(request.ifRev) || request.ifRev !== state.rev) throw Object.assign(new Error('restore precondition conflict'), { status: 409 });
            const checkpoint = await go('/checkpoint?rev=' + request.checkpoint, token);
            candidate = load(checkpoint.snapshot); break;
          }
          case 'checkpoint': case 'compact': break;
          default: throw Object.assign(new Error('unsupported semantic operation'), { status: 400 });
        }
        if (request.ifRev !== undefined && request.ifRev !== state.rev) throw Object.assign(new Error('revision conflict'), { status: 409 });
      }
      validate(candidate);
      const blocks = (await outline(candidate)).map(({ id, hash }) => ({ id, hash }));
      await gate('before');
      // Go repeats live authority and CAS/hash validation after its SQL writer
      // fence; no local engine state becomes shared before this ack.
      const commitStarted = performance.now();
      const committed = await go('/commit', token, {
        id: request.id, fingerprint, kind: human ? 'human' : request.kind,
        epoch: state.epoch, schema: VERSION, ifRev: state.rev, ifHashes: hashes,
        snapshot: snapshot(candidate), blocks, thread, checkpoint: request.checkpoint,
      });
      commitTimings.push({ goMicros: committed.commitMicros, roundTripMs: performance.now() - commitStarted });
      if (commitTimings.length > 128) commitTimings.shift();
      await gate('after');
      state = await read(token);
      await publish(request.kind === 'restore');
      return { ...await publicState(state), ...committed, checkpoint: ['checkpoint', 'agent', 'restore'].includes(request.kind) ? committed.rev : undefined };
    } finally { if (candidate !== doc) candidate.destroy(); doc.destroy(); }
  }
  state = await go('/epoch', fixture.owner, {});
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    const reply = (code, value) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); };
    try {
      if (url.pathname.startsWith('/test/')) {
        if (req.headers['x-d0-service'] !== fixture.service) return reply(403, { error: 'fixture identity required' });
        if (url.pathname === '/test/status') return reply(200, { held, phase: holdPhase, queued, rss: process.memoryUsage().rss, heapUsed: process.memoryUsage().heapUsed, commitTimings });
        if (url.pathname === '/test/hold') { const body = await bodyJSON(req); if (held || !['before', 'after', null].includes(body.phase)) return reply(409, { error: 'invalid gate' }); holdPhase = body.phase; return reply(200, { ok: true }); }
        if (url.pathname === '/test/release') { releaseHold?.(); return reply(200, { ok: true }); }
      }
      if (url.pathname === '/' || url.pathname === '/browser.js') {
        const file = url.pathname === '/' ? 'index.html' : 'dist/browser.js';
        res.writeHead(200, { 'Content-Type': file.endsWith('html') ? 'text/html' : 'text/javascript', 'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws://127.0.0.1:*; img-src 'none'; frame-src 'none'; base-uri 'none'" });
        return res.end(await readFile(new URL(file, import.meta.url)));
      }
      const token = req.headers.authorization?.replace(/^Bearer /, '');
      if (!token) return reply(401, { error: 'authenticated bearer required' });
      if (req.method === 'GET' && url.pathname === '/state') return reply(200, await publicState(await go('/state', token)));
      if (req.method === 'POST' && url.pathname === '/op') {
        const request = await bodyJSON(req);
        return reply(200, await enqueue(() => apply(token, request)));
      }
      reply(404, { error: 'unknown operation' });
    } catch (error) { reply(error.status || 400, { error: error.message }); }
  });
  const wss = new WebSocketServer({ server, path: '/ws', maxPayload: 1500000 });
  wss.on('connection', peer => {
    if (peers.size >= 16) return peer.close(4429, 'connection limit');
    peers.add(peer);
    const timer = setTimeout(() => { if (!peer.token) peer.close(4401, 'auth timeout'); }, 2000);
    peer.on('close', () => { clearTimeout(timer); peers.delete(peer); });
    peer.on('message', async bytes => {
      let message;
      try {
        message = JSON.parse(bytes.toString());
        if (!peer.token) {
          if (message.kind !== 'auth' || message.schema !== VERSION || typeof message.token !== 'string' || message.token.startsWith('agtv2.')) throw new Error('human auth and matching schema required');
          const s = await go('/state', message.token);
          if (s.schema !== VERSION) { send(peer, { kind: 'state', ...await publicState(s) }); return peer.close(4409, 'unsupported schema'); }
          peer.token = message.token;
          return send(peer, { kind: 'reset', ...await publicState(s) });
        }
        if (message.kind !== 'update') throw new Error('human update required');
        const result = await enqueue(() => apply(peer.token, message, true));
        send(peer, { kind: 'ack', id: message.id, rev: result.rev, epoch: result.epoch });
      } catch (error) {
        send(peer, { kind: 'rejected', id: message?.id, error: error.message });
        if (!peer.token) return peer.close(4403, 'human authorization required');
        // Durable-before-broadcast kept this proposal private to its sender.
        // Reset and close only that sender; invalid input must not discard
        // another writer's optimistic work. Restore/restart fence all peers.
        try { send(peer, { kind: 'reset', ...await publicState(await read(peer.token)) }); }
        catch { send(peer, { kind: 'forbidden' }); }
        peer.close(4409, 'rejected state; reconnect from durable snapshot');
      }
    });
  });
  // Idle sockets lose read access without waiting for their next edit. The
  // transaction check remains the actual write/revocation ordering boundary.
  let checking = false;
  const poll = setInterval(async () => {
    if (checking) return; checking = true;
    try {
      for (const peer of peers) if (peer.token) {
        try { const s = await go('/state', peer.token); if (s.schema !== VERSION) { send(peer, { kind: 'state', ...await publicState(s) }); peer.close(4409, 'unsupported schema'); } }
        catch { send(peer, { kind: 'forbidden' }); peer.close(4403, 'authority revoked'); }
      }
    } finally { checking = false; }
  }, 150);
  await new Promise(resolve => server.listen(port, '127.0.0.1', resolve));
  const address = server.address();
  return { url: `http://127.0.0.1:${address.port}`, async close() { clearInterval(poll); releaseHold?.(); for (const peer of peers) peer.terminate(); await tail; wss.close(); await new Promise(resolve => server.close(resolve)); } };
}
async function bodyJSON(req) {
  const chunks = []; let size = 0;
  for await (const chunk of req) { size += chunk.length; if (size > 2000000) throw new Error('request limit'); chunks.push(chunk); }
  return JSON.parse(Buffer.concat(chunks).toString());
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const fixture = JSON.parse(await readFile(process.argv[2], 'utf8'));
  const service = await startService(fixture, { port: Number(process.argv[3]) || 0 });
  process.stdout.write(JSON.stringify({ url: service.url }) + '\n');
  process.on('SIGTERM', () => { service.close().then(() => process.exit(0)); });
  process.on('disconnect', () => { service.close().then(() => process.exit(0)); });
}
