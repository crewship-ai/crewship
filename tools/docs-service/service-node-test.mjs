import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, writeFile, stat, chmod, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { startService, PROTOCOL } from './service.mjs';
import { create, snapshot, initialJSON } from './schema.mjs';
const secret = 'a'.repeat(48);
function call(socket, path, data, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: socket, path, method: data === undefined ? 'GET' : 'POST', headers: { 'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'workspace', Authorization: 'Bearer human', ...headers } }, res => { const chunks = []; res.on('data', c => chunks.push(c)); res.on('end', () => resolve({ status: res.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) })); });
    req.on('error', reject); req.end(data === undefined ? undefined : JSON.stringify(data));
  });
}
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
async function fixture(handler, options = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'docs-service-'));
  const paths = { socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile: join(dir, 't') };
  await writeFile(paths.tokenFile, secret, { mode: 0o600 });
  const authority = http.createServer(async (req, res) => {
    try {
      assert.equal(req.headers['x-crewship-docs-service'], secret);
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      const data = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {};
      const value = await handler(req.url.split('/').at(-1), data, req, res);
      if (!res.writableEnded) res.end(JSON.stringify(value ?? {}));
    } catch (error) { res.writeHead(500); res.end(JSON.stringify({ error: error.message })); }
  });
  await new Promise(resolve => authority.listen(paths.authoritySocket, resolve));
  let service;
  return { ...paths, authority, async start() { service = await startService({ ...paths, ...options }); return service; },
    async close() { if (service) await service.close(); await new Promise(resolve => authority.close(resolve)); await rm(dir, { recursive: true }); } };
}
function initialState() {
  const doc = create(initialJSON()); const state = { schema: 2, rev: 1, epoch: 3, generation: 0, incarnation: 'go-boot', snapshot: snapshot(doc), updates: [] }; doc.destroy(); return state;
}
function authorityModel() {
  const model = { state: initialState(), receipts: new Map(), seen: [], boots: new Set(), recoveries: 0, acquisitions: 0, releases: 0, commits: 0, checkpoints: 0, restores: 0, compatible: true, rejectCommit: false, revoked: new Set() };
  model.handle = async (op, data, req, res) => {
    model.seen.push({ op, data, bearer: req.headers.authorization });
    if (op === 'recover') { assert.equal(typeof data.boot, 'string'); if (!model.boots.has(data.boot)) { model.boots.add(data.boot); model.recoveries++; model.state.generation++; model.state.epoch++; } return { recovered: true, protocol: PROTOCOL }; }
    if (op === 'ready') return { protocol: model.compatible ? PROTOCOL : 99, schema: 2 };
    assert.ok(req.headers.authorization?.startsWith('Bearer '));
    if (model.revoked.has(req.headers.authorization)) { res.writeHead(403); return { error: 'revoked principal' }; }
    if (op === 'state') return model.state;
    if (op === 'receipt') { const receipt = model.receipts.get(data.id); return receipt && receipt.fingerprint === data.fingerprint ? { ...receipt, found: true } : { found: false }; }
    if (op === 'lease') { model.acquisitions++; model.state.epoch++; return { token: `lease-${model.acquisitions}`, epoch: model.state.epoch, generation: model.state.generation, expiresAt: new Date(Date.now() + 30000).toISOString() }; }
    if (op === 'renew') { assert.equal(data.epoch, model.state.epoch); return { expiresAt: new Date(Date.now() + 30000).toISOString() }; }
    if (op === 'release') { model.releases++; return { released: true }; }
    if (op === 'checkpoint-read') return { schema: model.state.schema, snapshot: model.state.snapshot };
    if (op === 'commit') {
      if (model.rejectCommit) { res.writeHead(403); return { error: 'revoked' }; }
      assert.equal(data.epoch, model.state.epoch); assert.equal(data.generation, model.state.generation);
      assert.equal(data.leaseOwner.length, 36); assert.ok(data.update); model.commits++;
      model.state = { ...model.state, schema: data.schema, rev: model.state.rev + 1, snapshot: data.snapshot };
    } else if (op === 'checkpoint') model.checkpoints++;
    else if (op === 'restore') { model.restores++; model.state = { ...model.state, rev: model.state.rev + 1, epoch: model.state.epoch + 1, generation: model.state.generation + 1 }; }
    const receipt = { id: data.id, rev: model.state.rev, epoch: model.state.epoch, fingerprint: data.fingerprint };
    model.receipts.set(data.id, receipt); return receipt;
  };
  return model;
}
const read = (f, headers) => call(f.socket, '/v1/documents/p/read', {}, headers);
test('request targets are verified from confirmed text with no lease or content mutation', async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  try {
    const state = (await read(f)).body;
    const block = state.outline.find(block => block.id === 'intro');
    const target = { id: block.id, hash: block.hash, from: 0, to: 8, quote: block.text.slice(0, 8) };
    const verify = (targets, base_rev = state.rev, headers) => call(f.socket, '/v1/documents/p/verify-targets', { base_rev, targets }, headers);
    const verified = await verify([target]);
    assert.equal(verified.status, 200);
    assert.deepEqual(verified.body, { page_id: 'p', rev: state.rev, targets: [{ ...target, text: block.text, ancestors: [] }] });
    assert.equal((await verify([target], state.rev - 1)).status, 409);
    assert.equal((await verify([{ ...target, hash: '0'.repeat(64) }])).status, 409);
    assert.equal((await verify([{ ...target, quote: 'not the selected text' }])).status, 409);
    assert.equal((await verify([{ ...target, from: -1 }])).status, 400);
    assert.equal((await verify([{ ...target, text: 'client-authored baseline' }])).status, 400);
    assert.equal((await verify([target, target])).status, 400);
    assert.equal((await verify(Array(17).fill(target))).status, 400);
    assert.equal((await verify([])).status, 400);
    const table = state.outline.find(block => block.id === 'table');
    assert.equal((await verify([{ id: table.id, hash: table.hash, from: 0, to: 1, quote: table.text.slice(0, 1) }])).status, 400);
    assert.equal((await verify([target], state.rev, { Authorization: 'Bearer agtv2.not-a-human' })).status, 403);
    const nested = state.outline.find(block => block.id === 'cell-a-p');
    const nestedTarget = { id: nested.id, hash: nested.hash, from: 0, to: 1, quote: 'A' };
    assert.deepEqual((await verify([nestedTarget])).body.targets[0].ancestors, ['cell-a', 'row', 'table']);
    assert.equal(model.acquisitions, 0); assert.equal(model.commits, 0); assert.equal(model.state.rev, state.rev);
  } finally { await f.close(); }
});
test('semantic commits convey the exact replacement to Go for range authorization', async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  try {
    const state = (await read(f)).body;
    const request = patchRequest(state, 'selected-patch', 'selected replacement');
    assert.equal((await call(f.socket, '/v1/documents/p/patch', request)).status, 200);
    assert.deepEqual(model.seen.find(entry => entry.op === 'commit').data.semantic, { target: 'intro', text: 'selected replacement' });
  } finally { await f.close(); }
});
test('target ranges use UTF-16 without splitting supplementary characters or exceeding quote bounds', async () => {
  const model = authorityModel();
  const json = initialJSON(); json.content[1].content = [{ type: 'text', text: 'A😀B' }];
  const doc = create(json); model.state.snapshot = snapshot(doc); doc.destroy();
  const f = await fixture(model.handle); await f.start();
  try {
    const state = (await read(f)).body, block = state.outline.find(block => block.id === 'intro');
    const target = { id: block.id, hash: block.hash, from: 1, to: 3, quote: '😀' };
    const verify = targets => call(f.socket, '/v1/documents/p/verify-targets', { base_rev: state.rev, targets });
    assert.equal((await verify([target])).status, 200);
    assert.equal((await verify([{ ...target, to: 2, quote: '😀'.slice(0, 1) }])).status, 400);
    assert.equal((await verify([{ ...target, from: 2, quote: '😀'.slice(1) }])).status, 400);
    assert.equal((await verify([{ ...target, quote: 'x'.repeat(4097) }])).status, 400);
    assert.equal(model.commits, 0);
  } finally { await f.close(); }
});
function patchRequest(state, id, text = 'durable') {
  return { id, schema: 2, epoch: state.epoch, target: 'intro', text, ifRev: state.rev, ifHash: state.outline.find(b => b.id === 'intro').hash };
}
test('Unix service validates transport and acknowledges only durable receipts', async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  try {
    assert.equal((await stat(f.socket)).mode & 0o777, 0o600);
    assert.equal((await call(f.socket, '/health/ready')).body.schema, 2);
    assert.equal((await call(f.socket, '/health/live', undefined, { 'X-Crewship-Docs-Service': 'bad' })).status, 403);
    const state = (await read(f)).body; assert.equal(typeof state.epoch, 'string');
    const request = patchRequest(state, 'one');
    model.rejectCommit = true; assert.equal((await call(f.socket, '/v1/documents/p/patch', request)).status, 403); assert.equal(model.state.rev, 1);
    model.rejectCommit = false; assert.equal((await call(f.socket, '/v1/documents/p/patch', request)).body.rev, 2);
    assert.equal((await call(f.socket, '/v1/documents/p/patch', request)).body.duplicate, true); assert.equal(model.commits, 1);
    assert.equal(model.acquisitions, 1, 'lease acquisition is independent of public sync epoch');
    assert.equal((await call(f.socket, '/drain', {})).status, 200); assert.equal(model.releases, 1);
    assert.equal((await call(f.socket, '/health/ready')).status, 503);
    assert.equal((await read(f)).status, 503);
  } finally { await f.close(); }
});
test('initialization bootstraps without document state or lease', async () => {
  const seen = []; const f = await fixture((op, data, req, res) => {
    seen.push(op); if (op === 'recover') return { recovered: true, protocol: PROTOCOL };
    if (op === 'receipt') { res.writeHead(404); return { error: 'not found' }; }
    assert.equal(op, 'initialize'); assert.equal(data.schema, 2); assert.ok(data.snapshot); return { rev: 1, epoch: 0 };
  }); await f.start();
  try { assert.equal((await call(f.socket, '/v1/documents/new/initialize', { id: 'init', schema: 2, body: initialJSON() })).status, 200); assert.deepEqual(seen, ['recover', 'receipt', 'initialize']); }
  finally { await f.close(); }
});
test('startup rejects unsafe secrets and noncanonical socket paths', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'docs-config-')); const tokenFile = join(dir, 'secret');
  try {
    await writeFile(tokenFile, secret, { mode: 0o600 }); await chmod(tokenFile, 0o644);
    await assert.rejects(startService({ socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile }), /mode-0600/);
    await assert.rejects(startService({ socket: 'relative.sock', authoritySocket: join(dir, 'a'), tokenFile }), /absolute Unix/);
    await assert.rejects(startService({ socket: dir + '/sub/../s', authoritySocket: join(dir, 'a'), tokenFile }), /absolute Unix/);
    await chmod(tokenFile, 0o600); await writeFile(tokenFile, 'x'.repeat(513));
    await assert.rejects(startService({ socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile }), /512 bytes/);
    await writeFile(tokenFile, 'x'.repeat(257)); await assert.rejects(startService({ socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile }), /32..256/);
    const link = join(dir, 'link'); await symlink(tokenFile, link);
    await assert.rejects(startService({ socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile: link }), /regular mode-0600/);
    await assert.rejects(startService({ socket: tokenFile, authoritySocket: join(dir, 'a'), tokenFile }), /distinct/);
  } finally { await rm(dir, { recursive: true }); }
});
test('startup rejects an old Go recovery acknowledgement before admitting requests', async () => {
  let calls = 0;
  const f = await fixture((op, data) => {
    calls++; assert.equal(op, 'recover'); assert.equal(data.protocol, PROTOCOL);
    return { recovered: true, protocol: 1 };
  });
  try {
    await assert.rejects(f.start(), /compatible engine recovery/);
    assert.equal(calls, 1);
    await assert.rejects(call(f.socket, '/v1/documents/p/read', {}));
  } finally { await f.close(); }
});
test('checkpoint and restore retry receipts survive generation changes; readiness checks live authority', async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  try {
    const state = (await read(f)).body;
    const cp = { id: 'cp', schema: 2, epoch: state.epoch, ifRev: state.rev };
    assert.equal((await call(f.socket, '/v1/documents/p/checkpoint', cp)).status, 200);
    assert.equal((await call(f.socket, '/v1/documents/p/checkpoint', cp)).body.duplicate, true);
    const restore = { id: 'restore', schema: 2, epoch: state.epoch, ifRev: state.rev, checkpoint: 'cp' };
    const restored = await call(f.socket, '/v1/documents/p/restore', restore); assert.equal(restored.status, 200); assert.notEqual(restored.body.epoch, state.epoch);
    assert.equal((await call(f.socket, '/v1/documents/p/restore', restore)).body.duplicate, true);
    assert.equal(model.checkpoints, 1); assert.equal(model.restores, 1);
    model.compatible = false; assert.equal((await call(f.socket, '/health/ready')).status, 503);
    model.compatible = true; assert.equal((await call(f.socket, '/health/ready')).status, 200);
    await new Promise(resolve => f.authority.close(resolve)); assert.equal((await call(f.socket, '/health/ready')).status, 503);
  } finally { await f.close(); }
});
test('blocked persistence bounds document queue and drain waits for all failed acknowledgements', async () => {
  const blocked = deferred(), started = deferred(); let calls = 0;
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'recover') return { recovered: true, protocol: PROTOCOL };
    if (op === 'ready') return { protocol: PROTOCOL, schema: 2 };
    calls++; started.resolve(); await blocked.promise; res.writeHead(503); return { error: 'controlled persistence failure' };
  }); await f.start(); const pending = [];
  try {
    pending.push(read(f)); await started.promise;
    for (let i = 1; i < 32; i++) pending.push(read(f));
    assert.equal((await read(f)).status, 429); assert.equal(calls, 1);
    assert.equal((await call(f.socket, '/health/ready')).status, 200);
    let drained = false; const draining = call(f.socket, '/drain', {}).then(result => { drained = true; return result; });
    assert.equal((await call(f.socket, '/health/ready')).status, 503); assert.equal((await read(f)).status, 503); assert.equal(drained, false);
    blocked.resolve(); assert.ok((await Promise.all(pending)).every(result => result.status === 503));
    assert.equal((await draining).status, 200); assert.equal(calls, 32);
  } finally { blocked.resolve(); await Promise.allSettled(pending); await f.close(); }
});
test('duplicate socket bind cannot recover or fence the existing engine', async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  try {
    const before = (await read(f)).body;
    await assert.rejects(startService({ socket: f.socket, authoritySocket: f.authoritySocket, tokenFile: f.tokenFile }), /already exists/);
    assert.equal(model.recoveries, 1); assert.equal((await read(f)).body.epoch, before.epoch);
  } finally { await f.close(); }
});
test('recovery gates all admission and failure closes its socket; a fresh boot can retry', async () => {
  const entered = deferred(), proceed = deferred(); let reject = true, boots = [];
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'recover') { boots.push(data.boot); entered.resolve(); await proceed.promise; if (reject) { res.writeHead(503); return { error: 'recovery failed' }; } return { recovered: true, protocol: PROTOCOL }; }
    assert.fail('recovery must gate document authority calls');
  });
  const starting = f.start(); const rejected = assert.rejects(starting, /recovery failed/);
  try {
    await entered.promise;
    assert.equal((await call(f.socket, '/health/ready')).status, 503);
    assert.equal((await read(f)).status, 503);
    proceed.resolve(); await rejected;
    await assert.rejects(stat(f.socket), { code: 'ENOENT' });
    reject = false; await f.start(); assert.equal(boots.length, 2); assert.notEqual(boots[0], boots[1]);
  } finally { proceed.resolve(); await f.close(); }
});
test('pending recovery rejects an incomplete body and closes it before startup failure', { timeout: 5000 }, async () => {
  const entered = deferred(), proceed = deferred();
  const f = await fixture(async (op, data, req, res) => {
    assert.equal(op, 'recover'); entered.resolve(); await proceed.promise;
    res.writeHead(409); return { error: 'incompatible engine recovery' };
  });
  const starting = f.start(); starting.catch(() => {}); let request;
  try {
    await entered.promise;
    request = http.request({ socketPath: f.socket, path: '/v1/documents/p/read', method: 'POST', headers: {
      'X-Crewship-Docs-Service': secret, Authorization: 'Bearer human', 'Content-Length': 5000,
    } });
    request.on('error', () => {});
    const closed = new Promise(resolve => request.once('close', resolve));
    const response = new Promise(resolve => request.once('response', res => { res.resume(); res.once('end', () => resolve(res.statusCode)); }));
    request.write('{');
    assert.equal(await response, 503);
    let timer;
    try { await Promise.race([closed, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('startup rejection kept incomplete body open')), 1000); })]); }
    finally { clearTimeout(timer); }
    proceed.resolve(); await assert.rejects(starting, /incompatible engine recovery/);
  } finally { request?.destroy(); proceed.resolve(); await Promise.allSettled([starting]); await f.close(); }
});
test('cached writer lease rechecks each principal and releases on idle and drain', async () => {
  const model = authorityModel(), f = await fixture(model.handle, { idleMs: 500 }); await f.start();
  try {
    const alice = { Authorization: 'Bearer alice' }, bob = { Authorization: 'Bearer bob' };
    let state = (await read(f, alice)).body;
    assert.equal((await call(f.socket, '/v1/documents/p/patch', patchRequest(state, 'alice'), alice)).status, 200);
    state = (await read(f, bob)).body;
    assert.equal((await call(f.socket, '/v1/documents/p/patch', patchRequest(state, 'bob'), bob)).status, 200);
    assert.equal(model.acquisitions, 1);
    assert.deepEqual(model.seen.filter(x => x.op === 'renew').map(x => x.bearer), ['Bearer alice', 'Bearer bob']);
    model.revoked.add('Bearer bob');
    assert.equal((await call(f.socket, '/v1/documents/p/patch', patchRequest(state, 'revoked'), bob)).status, 403);
    model.revoked.delete('Bearer bob');
    for (let i = 0; i < 200 && model.releases === 0; i++) await new Promise(resolve => setTimeout(resolve, 10));
    assert.equal(model.releases, 1); assert.equal(model.seen.filter(x => x.op === 'release')[0].bearer, 'Bearer bob');
    state = (await read(f, alice)).body;
    assert.equal((await call(f.socket, '/v1/documents/p/patch', patchRequest(state, 'after-idle'), alice)).status, 200);
    assert.equal(model.acquisitions, 2);
    await call(f.socket, '/drain', {}); assert.equal(model.releases, 2);
  } finally { await f.close(); }
});
test('public epoch changes on either authority incarnation or engine boot even when generation rewinds', async () => {
  const model = authorityModel(), f = await fixture(model.handle); let service = await f.start();
  try {
    const old = (await read(f)).body;
    model.state.incarnation = 'restored-go-boot'; model.state.generation = 1;
    const restored = (await read(f)).body;
    assert.notEqual(restored.epoch, old.epoch);
    assert.equal((await call(f.socket, '/v1/documents/p/patch', patchRequest(old, 'stale'))).status, 409);
    await service.close(); service = await f.start();
    const rebooted = (await read(f)).body;
    assert.notEqual(rebooted.epoch, restored.epoch);
    assert.equal((await call(f.socket, '/v1/documents/p/patch', patchRequest(restored, 'stale-node'))).status, 409);
  } finally { await f.close(); }
});

test('slow incomplete bodies consume bounded admission while health remains responsive', { timeout: 10000 }, async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  const held = [];
  try {
    for (let i = 0; i < 128; i++) {
      const req = http.request({ socketPath: f.socket, path: '/v1/documents/p/read', method: 'POST', headers: {
        'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'workspace', Authorization: 'Bearer human',
        'Content-Type': 'application/json', 'Content-Length': 10,
      } });
      req.on('error', () => {}); req.on('response', response => response.resume()); held.push(req);
      await new Promise((resolve, reject) => { req.once('error', reject); req.write('{', error => error ? reject(error) : resolve()); });
      // An independent health round trip lets each incomplete document request
      // enter its body reader before the next connection is introduced.
      assert.equal((await call(f.socket, '/health/ready')).status, 200);
    }
    assert.equal((await read(f)).status, 429);
    assert.equal((await call(f.socket, '/health/live')).status, 200);
    assert.equal(model.seen.filter(event => event.op === 'state').length, 0, 'partial bodies must not reach authority');
    for (const req of held) req.destroy();
    let recovered;
    for (let i = 0; i < 100; i++) {
      recovered = await read(f);
      if (recovered.status === 200) break;
      await new Promise(resolve => setTimeout(resolve, 10));
    }
    assert.equal(recovered.status, 200, 'disconnect must release admission slots');
    const partial = http.request({ socketPath: f.socket, path: '/v1/documents/p/read', method: 'POST', headers: {
      'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'workspace', Authorization: 'Bearer human', 'Content-Length': 10,
    } });
    partial.on('error', () => {}); held.push(partial);
    const closed = new Promise(resolve => partial.once('close', resolve));
    await new Promise(resolve => partial.write('{', resolve));
    assert.equal((await call(f.socket, '/health/ready')).status, 200);
    assert.equal((await call(f.socket, '/drain', {})).status, 200);
    await closed;
    assert.equal((await read(f)).status, 503, 'drain must terminate partial-body admission');
  } finally { for (const req of held) req.destroy(); await f.close(); }
});

test('drain aborts existing body readers and promptly closes new incomplete requests while persistence waits', { timeout: 10000 }, async () => {
  const blocked = deferred(), entered = deferred();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'recover') return { recovered: true, protocol: PROTOCOL };
    if (op === 'ready') return { protocol: PROTOCOL, schema: 2 };
    entered.resolve(); await blocked.promise; res.writeHead(503); return { error: 'controlled persistence failure' };
  }); await f.start();
  const held = [];
  const bounded = async promise => {
    let timeout;
    try { return await Promise.race([promise, new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('drain socket did not finish within one second')), 1000); })]); }
    finally { clearTimeout(timeout); }
  };
  function incomplete() {
    let status;
    const req = http.request({ socketPath: f.socket, path: '/v1/documents/p/read', method: 'POST', headers: {
      'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'workspace', Authorization: 'Bearer human', 'Content-Length': 10,
    } }); held.push(req);
    const response = new Promise((resolve, reject) => {
      req.on('error', reject); req.on('response', res => { status = res.statusCode; res.resume(); res.on('end', () => resolve(status)); });
    }); response.catch(() => {});
    const closed = new Promise(resolve => req.once('close', resolve));
    const written = new Promise((resolve, reject) => { req.once('error', reject); req.write('{', error => error ? reject(error) : resolve()); });
    return { response, closed, written };
  }
  const waiting = read(f); let draining, closed = false;
  try {
    await entered.promise;
    const before = incomplete(); await before.written;
    assert.equal((await call(f.socket, '/health/ready')).status, 200);
    draining = call(f.socket, '/drain', {});
    await bounded(before.closed);
    const after = incomplete(); await after.written;
    assert.equal(await bounded(after.response), 503);
    await bounded(after.closed);
    assert.equal((await call(f.socket, '/health/ready')).status, 503);
    blocked.resolve(); assert.equal((await waiting).status, 503);
    assert.equal((await draining).status, 200);
    await bounded(f.close()); closed = true;
  } finally {
    for (const req of held) req.destroy(); blocked.resolve();
    await Promise.allSettled([waiting, draining]); if (!closed) await f.close();
  }
});
