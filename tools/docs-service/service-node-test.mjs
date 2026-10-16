import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import * as Y from 'yjs';
import { mkdtemp, writeFile, stat, chmod, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { startService, PROTOCOL } from './service.mjs';
import { RICH_VERSION, create, snapshot, initialJSON, outline, load, anchor, find, encode } from './schema.mjs';
const secret = 'a'.repeat(48);
function call(socket, path, data, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: socket, path, method: data === undefined ? 'GET' : 'POST', headers: { 'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'workspace', Authorization: 'Bearer human', ...headers } }, res => { const chunks = []; res.on('data', c => chunks.push(c)); res.on('end', () => resolve({ status: res.statusCode, contentType: res.headers['content-type'], body: JSON.parse(Buffer.concat(chunks).toString()) })); });
    req.on('error', reject); req.end(data === undefined ? undefined : JSON.stringify(data));
  });
}
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
async function fixture(handler, options = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'docs-service-'));
  const paths = { socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile: join(dir, 't') };
  await writeFile(paths.tokenFile, secret, { mode: 0o600 });
  const authority = http.createServer(async (req, res) => {
    res.setHeader('Content-Type', 'application/json');
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
  const model = { state: initialState(), receipts: new Map(), seen: [], boots: new Set(), recoveries: 0, acquisitions: 0, releases: 0, commits: 0, checkpoints: 0, checkpointStates: new Map(), restores: 0, compatible: true, rejectCommit: false, revoked: new Set(), readOnly: new Set() };
  model.handle = async (op, data, req, res) => {
    model.seen.push({ op, data, bearer: req.headers.authorization });
    if (op === 'recover') { assert.equal(typeof data.boot, 'string'); if (!model.boots.has(data.boot)) { model.boots.add(data.boot); model.recoveries++; model.state.generation++; model.state.epoch++; } return { recovered: true, protocol: PROTOCOL, schema: RICH_VERSION }; }
    if (op === 'ready') return { protocol: model.compatible ? PROTOCOL : 99, schema: RICH_VERSION };
    assert.ok(req.headers.authorization?.startsWith('Bearer '));
    if (model.revoked.has(req.headers.authorization)) { res.writeHead(403); return { error: 'revoked principal' }; }
    if (op === 'state') return model.state;
    if (op === 'receipt') { const receipt = model.receipts.get(data.id); return receipt && receipt.fingerprint === data.fingerprint ? { ...receipt, found: true } : { found: false }; }
    if (model.readOnly.has(req.headers.authorization) && !['checkpoint-read','checkpoints','comments'].includes(op)) { res.writeHead(403); return { error: 'read-only principal' }; }
    if (op === 'lease') { model.acquisitions++; model.state.epoch++; return { token: `lease-${model.acquisitions}`, epoch: model.state.epoch, generation: model.state.generation, expiresAt: new Date(Date.now() + 30000).toISOString() }; }
    if (op === 'renew') { assert.equal(data.epoch, model.state.epoch); return { expiresAt: new Date(Date.now() + 30000).toISOString() }; }
    if (op === 'release') { model.releases++; return { released: true }; }
    if (op === 'checkpoint-read') return model.checkpointStates.get(data.id) ?? { schema: model.state.schema, snapshot: model.state.snapshot };
    if (op === 'commit') {
      if (model.rejectCommit) { res.writeHead(403); return { error: 'revoked' }; }
      assert.equal(data.epoch, model.state.epoch); assert.equal(data.generation, model.state.generation);
      assert.equal(data.leaseOwner.length, 36); assert.ok(data.update); model.commits++;
      model.state = { ...model.state, schema: data.schema, rev: model.state.rev + 1, snapshot: data.snapshot };
    } else if (op === 'checkpoint') { model.checkpoints++; model.checkpointStates.set(data.id,{schema:model.state.schema,snapshot:model.state.snapshot}); }
    else if (op === 'upgrade') { assert.equal(data.schema,model.state.schema); assert.equal(data.targetSchema,RICH_VERSION); assert.equal(data.ifRev,model.state.rev); assert.equal(data.generation,model.state.generation); model.state = {...model.state,schema:RICH_VERSION,rev:model.state.rev+1,epoch:model.state.epoch+1,generation:model.state.generation+1}; }
    else if (op === 'restore') { model.restores++; const source=model.checkpointStates.get(data.checkpoint)??model.state; model.state = { ...model.state,schema:source.schema,snapshot:source.snapshot, rev: model.state.rev + 1, epoch: model.state.epoch + 1, generation: model.state.generation + 1 }; }
    const receipt = { id: data.id, rev: model.state.rev, epoch: model.state.epoch, fingerprint: data.fingerprint };
    model.receipts.set(data.id, receipt); return receipt;
  };
  return model;
}
const read = (f, headers) => call(f.socket, '/v1/documents/p/read', {}, headers);
test('fixture error responses declare JSON instead of an HTML interpretation', async () => {
  const f = await fixture(() => { throw new Error('<script>literal diagnostic</script>'); });
  try {
    const response = await call(f.authoritySocket, '/internal/docs/v1/p/state', {});
    assert.equal(response.status, 500); assert.equal(response.contentType, 'application/json');
    assert.equal(response.body.error, '<script>literal diagnostic</script>');
  } finally { await f.close(); }
});
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
    assert.deepEqual(model.seen.find(entry => entry.op === 'commit').data.semantic, { target: 'intro', text: 'selected replacement', edit: { from: 0, to: 36, insert: 'selected replacement' } });
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
    assert.equal((await call(f.socket, '/health/ready')).body.schema, RICH_VERSION);
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
    seen.push(op); if (op === 'recover') return { recovered: true, protocol: PROTOCOL, schema: RICH_VERSION };
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
    await assert.rejects(startService({ socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile: link }), { code: 'ELOOP' });
    await assert.rejects(startService({ socket: tokenFile, authoritySocket: join(dir, 'a'), tokenFile }), /distinct/);
  } finally { await rm(dir, { recursive: true }); }
});

test('read dispatch cannot turn mutation fields into a write or bypass live state authorization', async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  try {
    const before = structuredClone(model.state);
    const malicious = { operation: 'patch', schema: 2, id: 'read-as-patch', epoch: 'forged', ifRev: 999, patch: { target: 'intro', ifHash: '0'.repeat(64), text: 'unauthorized replacement' }, snapshot: 'forged', targets: [] };
    const result = await call(f.socket, '/v1/documents/p/read', malicious);
    assert.equal(result.status, 200);
    assert.equal(result.body.snapshot, before.snapshot);
    assert.equal(model.acquisitions, 0); assert.equal(model.commits, 0);
    assert.deepEqual(model.state, before); assert.equal(model.receipts.size, 0);
    assert.equal((await call(f.socket, '/v1/documents/p/verify-targets', malicious)).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/verify-targets', malicious, { Authorization: 'Bearer agtv2.synthetic' })).status, 403);
    model.revoked.add('Bearer human');
    for (const operation of ['read', 'verify-targets', 'comments', 'checkpoints', 'checkpoint-read', 'export']) assert.equal((await call(f.socket, `/v1/documents/p/${operation}`, malicious)).status, 403);
    assert.equal((await call(f.socket, '/v1/documents/p/read/patch', malicious)).status, 404);
    assert.equal(model.acquisitions, 0); assert.equal(model.commits, 0); assert.equal(model.receipts.size, 0);
  } finally { await f.close(); }
});
test('every read route stays read-only with mutation-shaped fields and live authority', async () => {
  const model = authorityModel();
  const f = await fixture((op, data, req, res) => {
    if (op === 'comments') return [];
    if (op === 'checkpoints') return [];
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const state = (await read(f)).body;
    const block = state.outline.find(block => block.id === 'intro');
    const before = structuredClone(model.state);
    const malicious = { operation: 'patch', schema: 999, id: '../invalid', epoch: 'forged',
      ifRev: 999, snapshot: 'forged', base_rev: state.rev, checkpoint: 'fixture-checkpoint',
      targets: [{ id: block.id, hash: block.hash, from: 0, to: 8, quote: block.text.slice(0, 8) }] };
    for (const operation of ['read', 'verify-targets', 'comments', 'checkpoints', 'checkpoint-read', 'export']) {
      assert.equal((await call(f.socket, `/v1/documents/p/${operation}`, malicious)).status, 200, operation);
    }
    assert.deepEqual(model.state, before);
    assert.equal(model.acquisitions, 0); assert.equal(model.commits, 0);
    assert.equal(model.checkpoints, 0); assert.equal(model.restores, 0); assert.equal(model.receipts.size, 0);
    assert.equal(model.seen.filter(({ op }) => ['receipt', 'lease', 'renew', 'commit', 'checkpoint', 'restore', 'comment', 'resolve'].includes(op)).length, 0);
    model.revoked.add('Bearer human');
    for (const operation of ['read', 'verify-targets', 'comments', 'checkpoints', 'checkpoint-read', 'export']) {
      assert.equal((await call(f.socket, `/v1/documents/p/${operation}`, malicious)).status, 403, operation);
    }
  } finally { await f.close(); }
});
test('every mutation route requires its own valid schema and operation ID', async () => {
  const model = authorityModel(), f = await fixture(model.handle); await f.start();
  try {
    for (const operation of ['initialize', 'upgrade', 'patch', 'checkpoint', 'restore', 'comment', 'resolve']) {
      const invalid = { schema: 999, id: 'valid-id', body: initialJSON() };
      assert.equal((await call(f.socket, `/v1/documents/p/${operation}`, invalid)).status, operation === 'initialize' ? 400 : 409, operation);
      assert.equal((await call(f.socket, `/v1/documents/p/${operation}`, { ...invalid, schema: 2, id: '../invalid' })).status, 400, operation);
    }
    assert.equal(model.acquisitions, 0); assert.equal(model.commits, 0); assert.equal(model.receipts.size, 0);
    assert.equal(model.seen.filter(({ op }) => ['receipt', 'lease', 'renew', 'commit', 'checkpoint', 'restore', 'comment', 'resolve'].includes(op)).length, 0);
  } finally { await f.close(); }
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
    if (op === 'recover') return { recovered: true, protocol: PROTOCOL, schema: RICH_VERSION };
    if (op === 'ready') return { protocol: PROTOCOL, schema: RICH_VERSION };
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
    if (op === 'recover') { boots.push(data.boot); entered.resolve(); await proceed.promise; if (reject) { res.writeHead(503); return { error: 'recovery failed' }; } return { recovered: true, protocol: PROTOCOL, schema: RICH_VERSION }; }
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
    if (op === 'recover') return { recovered: true, protocol: PROTOCOL, schema: RICH_VERSION };
    if (op === 'ready') return { protocol: PROTOCOL, schema: RICH_VERSION };
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

test('trusted selected scope bounds repeated-text edits and stays private on public reads', async () => {
  const model = authorityModel(), json = initialJSON();
  json.content[1].content = [{ type: 'text', text: 'aaaa' }];
  const doc = create(json), target = (await outline(doc)).find(block => block.id === 'intro');
  model.state.snapshot = snapshot(doc); doc.destroy();
  model.state.requestScope = { baseRev: model.state.rev, target: { id: 'intro', hash: target.hash, text: 'aaaa', from: 0, to: 1 } };
  const f = await fixture(model.handle);
  try {
    await f.start(); const current = await read(f);
    assert.equal(current.status, 200); assert.equal(current.body.requestScope, undefined);
    const result = await call(f.socket, '/v1/documents/p/patch', {
      id: 'selected-repeat', schema: 2, epoch: current.body.epoch, target: 'intro',
      text: 'aaaaa', ifRev: model.state.rev, ifHash: target.hash,
      range: { from: 3, to: 4 }, requestScope: { target: { from: 3, to: 4 } },
    });
    assert.equal(result.status, 200);
    const commit = model.seen.find(call => call.op === 'commit').data;
    assert.deepEqual(commit.semantic.edit, { from: 1, to: 1, insert: 'a' });
    assert.equal(commit.semantic.text, 'aaaaa');
  } finally { await f.close(); }
});

test('public comment responses resolve spans without exposing the private anchor extension', async () => {
  const model = authorityModel(), doc = load(model.state.snapshot), thread = anchor(doc, 'intro', 0, 8, 'thread', 1); doc.destroy();
  assert.ok(thread.spans.length);
  const f = await fixture((op, data, req, res) => op === 'comments' ? [{ id: 'thread', body: 'Comment', anchor: thread, actor: 'user:human', rev: 1, version: 1, resolved: false, created_at: '2026-10-15T00:00:00Z' }] : model.handle(op, data, req, res));
  try {
    await f.start(); const response = await call(f.socket, '/v1/documents/p/comments', {});
    assert.equal(response.status, 200);
    const comment = response.body.comments[0];
    assert.equal(comment.status, 'attached'); assert.equal(comment.from, 0); assert.equal(comment.to, 8);
    assert.equal(comment.spans, undefined); assert.equal(comment.anchor.spans, undefined);
  } finally { await f.close(); }
});

test('startup rejects missing, old or future Go content capability before admitting documents', async () => {
 for (const schema of [undefined, 2, 4]) {
  const f = await fixture((op,data) => { assert.equal(op,'recover'); assert.equal(data.schema,RICH_VERSION); return {recovered:true,protocol:PROTOCOL,...(schema === undefined ? {} : {schema})}; });
  try { await assert.rejects(f.start(),/compatible engine recovery/); await assert.rejects(call(f.socket,'/v1/documents/p/read',{})); } finally { await f.close(); }
 }
});

test('explicit human upgrade binds original Page/revision, preserves bytes and retries without reapplying', async () => {
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try {
  const before=(await read(f)).body,request={id:'explicit-three',schema:2,target_schema:3,page_id:'p',epoch:before.epoch,ifRev:before.rev};
  for(const changed of [{...request,page_id:'foreign'},{...request,ifRev:before.rev-1},{...request,target_schema:4},{...request,update:'forged'}])assert.equal((await call(f.socket,'/v1/documents/p/upgrade',changed)).status,409);
  assert.equal((await call(f.socket,'/v1/documents/p/upgrade',request,{Authorization:'Bearer agtv2.synthetic'})).status,403);
  const receipt=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(receipt.status,200);assert.equal(receipt.body.schema,3);assert.equal(model.state.snapshot,before.snapshot);assert.equal(model.state.rev,before.rev+1);
  const after=(await read(f)).body;assert.equal(after.schema,3);assert.notEqual(after.epoch,before.epoch);assert.deepEqual(after.content,before.content);
  const retry=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(retry.status,200);assert.equal(retry.body.rev,receipt.body.rev);assert.equal(retry.body.duplicate,true);assert.equal(model.seen.filter(x=>x.op==='upgrade').length,1);
  model.revoked.add('Bearer human');assert.equal((await call(f.socket,'/v1/documents/p/upgrade',request)).status,403);assert.equal(model.state.rev,before.rev+1);
 } finally {await f.close()}
});
test('schema3 raw human marks are versioned; old clients and unqualified semantic clients fail closed',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try {
  const before=(await read(f)).body;const upgrade={id:'upgrade-marks',schema:2,target_schema:3,page_id:'p',epoch:before.epoch,ifRev:before.rev};assert.equal((await call(f.socket,'/v1/documents/p/upgrade',upgrade)).status,200);
  const current=(await read(f)).body,doc=load(current.snapshot),vector=Y.encodeStateVector(doc);find(doc,'intro').toArray()[0].format(0,8,{link:{href:'https://example.invalid/'}});const update=encode(Y.encodeStateAsUpdate(doc,vector));doc.destroy();
  assert.equal((await call(f.socket,'/v1/documents/p/patch',{id:'old-client',schema:2,epoch:current.epoch,kind:'human',update})).status,409);assert.equal(model.commits,0);
  assert.equal((await call(f.socket,'/v1/documents/p/verify-targets',{base_rev:current.rev,targets:[]})).status,400);
  assert.equal((await call(f.socket,'/v1/documents/p/patch',{id:'raw-agent',schema:3,page_id:'p',epoch:current.epoch,kind:'human',update},{Authorization:'Bearer agtv2.synthetic'})).status,403);assert.equal(model.commits,0);
  assert.equal((await call(f.socket,'/v1/documents/p/patch',{id:'foreign-page',schema:3,page_id:'foreign',epoch:current.epoch,kind:'human',update})).status,409);
  const saved=await call(f.socket,'/v1/documents/p/patch',{id:'human-link',schema:3,page_id:'p',epoch:current.epoch,kind:'human',update});assert.equal(saved.status,200);const rich=(await read(f)).body;assert.equal(rich.schema,3);assert.equal(rich.content.content.find(node=>node.attrs?.id==='intro').content[0].marks[0].attrs.href,'https://example.invalid/');
 }finally{await f.close()}
});
test('mixed schema checkpoint read and restore use the actual source version without a silent upgrade',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try {
  const old=(await read(f)).body;assert.equal((await call(f.socket,'/v1/documents/p/checkpoint',{id:'legacy-checkpoint',schema:2,epoch:old.epoch,ifRev:old.rev})).status,200);
  assert.equal((await call(f.socket,'/v1/documents/p/upgrade',{id:'mixed-upgrade',schema:2,target_schema:3,page_id:'p',epoch:old.epoch,ifRev:old.rev})).status,200);
  const current=(await read(f)).body;assert.equal((await call(f.socket,'/v1/documents/p/checkpoint',{id:'rich-checkpoint',schema:3,page_id:'p',epoch:current.epoch,ifRev:current.rev})).status,200);
  const request={id:'restore-legacy',schema:3,page_id:'p',epoch:current.epoch,ifRev:current.rev,checkpoint:'legacy-checkpoint'};const restored=await call(f.socket,'/v1/documents/p/restore',request);assert.equal(restored.status,200);const head=(await read(f)).body;assert.equal(head.schema,2);assert.notEqual(head.epoch,current.epoch);assert.equal(head.snapshot,old.snapshot);
  const retry=await call(f.socket,'/v1/documents/p/restore',request);assert.equal(retry.status,200);assert.equal(retry.body.schema,2);assert.equal(retry.body.rev,restored.body.rev);assert.equal(retry.body.duplicate,true);assert.equal(model.restores,1);
  const pinned=await call(f.socket,'/v1/documents/p/checkpoint-read',{checkpoint:'rich-checkpoint'});assert.equal(pinned.status,200);assert.equal(pinned.body.schema,3);
 }finally{await f.close()}
});

// Receipt lookup remains an authorized read of an existing durable result.
test('WRITE to READ downgrade permits original receipt but never a fresh upgrade',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try{
 const old=(await read(f)).body;const request={id:'readable-upgrade',schema:2,target_schema:3,page_id:'p',epoch:old.epoch,ifRev:old.rev};
 const original=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(original.status,200);
 const rich=(await read(f)).body;await call(f.socket,'/v1/documents/p/checkpoint',{id:'readable-rich',schema:3,page_id:'p',epoch:rich.epoch,ifRev:rich.rev});
 // A later confirmed restore changes the head, not the original receipt.
 model.state={...model.state,schema:2,rev:model.state.rev+1,snapshot:old.snapshot};model.readOnly.add('Bearer human');
 const rev=model.state.rev;const retry=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(retry.status,200);assert.equal(retry.body.duplicate,true);assert.equal(retry.body.rev,original.body.rev);assert.equal(retry.body.schema,3);assert.equal(model.state.rev,rev);
 const current=(await read(f)).body;assert.equal((await call(f.socket,'/v1/documents/p/upgrade',{...request,id:'fresh-read-only',epoch:current.epoch,ifRev:current.rev})).status,403);assert.equal(model.state.rev,rev);
 model.revoked.add('Bearer human');assert.equal((await call(f.socket,'/v1/documents/p/upgrade',request)).status,403);
 }finally{await f.close()}
});

test('rich semantic proof is private, byte-bounded, and preserves nontext context',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try{
 const doc=create({type:'doc',content:[{type:'paragraph',attrs:{id:'rich-target'},content:[{type:'text',text:'prefix ',marks:[{type:'link',attrs:{href:'https://example.invalid/'}}]},{type:'text',text:'middle suffix'}]}]},3);model.state={...model.state,schema:3,snapshot:snapshot(doc)};doc.destroy();
 const state=(await read(f)).body;const request={id:'rich-proof',schema:3,page_id:'p',epoch:state.epoch,ifRev:state.rev,ifHash:state.outline[0].hash,target:'rich-target',text:'prefix changed suffix',rich:{before:{type:'client-forgery'}}};
 assert.equal((await call(f.socket,'/v1/documents/p/patch',request)).status,200);const semantic=model.seen.find(x=>x.op==='commit').data.semantic;assert.equal(semantic.rich.before.type,'doc');assert.deepEqual(semantic.rich.before,state.content);assert.equal(semantic.rich.after.content[0].content[0].marks[0].type,'link');
 const huge=create({type:'doc',content:[{type:'paragraph',attrs:{id:'rich-target'},content:[{type:'text',text:'界'.repeat(170000)}]}]},3);model.state={...model.state,snapshot:snapshot(huge)};huge.destroy();const large=(await read(f)).body;const commits=model.commits;assert.equal((await call(f.socket,'/v1/documents/p/patch',{...request,id:'oversized-rich',epoch:large.epoch,ifRev:large.rev,ifHash:large.outline[0].hash,text:'new'})).status,413);assert.equal(model.commits,commits);assert.equal(model.state.rev,large.rev);
 }finally{await f.close()}
});

test('rich selection confirms plaintext and immutable schema while code blocks stay outside agent admission',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try{
 const doc=create({type:'doc',content:[{type:'paragraph',attrs:{id:'target'},content:[{type:'text',text:'linked',marks:[{type:'link',attrs:{href:'https://example.invalid/'}}]}]},{type:'codeBlock',attrs:{id:'code'},content:[{type:'text',text:'literal'}]}]},3);model.state={...model.state,schema:3,snapshot:snapshot(doc)};doc.destroy();const state=(await read(f)).body;
 const selected={id:'target',hash:state.outline[0].hash,from:0,to:6,quote:'linked'};const response=await call(f.socket,'/v1/documents/p/verify-targets',{base_rev:state.rev,targets:[selected]});assert.equal(response.status,200);assert.equal(response.body.schema,3);assert.deepEqual(response.body.targets,[{...selected,text:'linked',ancestors:[],schema:3}]);
 assert.equal((await call(f.socket,'/v1/documents/p/verify-targets',{base_rev:state.rev,targets:[{id:'code',hash:state.outline[1].hash,from:0,to:7,quote:'literal'}]})).status,400);assert.equal(model.commits,0);assert.equal(model.acquisitions,0);
 }finally{await f.close()}
});

test('table code with an unrepresentable backslash pipe returns actionable 422 without a write',async()=>{
 const model=authorityModel();
 const paragraph={type:'paragraph',attrs:{id:'cell-text'},content:[{type:'text',text:'a\\|b',marks:[{type:'code',attrs:{}}]}]};
 const cell={type:'tableCell',attrs:{id:'cell'},content:[paragraph]},row={type:'tableRow',attrs:{id:'row'},content:[cell]};
 const body={type:'doc',content:[{type:'table',attrs:{id:'table'},content:[row]}]},doc=create(body,3);
 model.state={...model.state,schema:3,snapshot:snapshot(doc)};doc.destroy();const f=await fixture(model.handle);await f.start();
 try{const before=structuredClone(model.state),result=await call(f.socket,'/v1/documents/p/export',{});assert.equal(result.status,422);assert.equal(result.body.code,'unsupported_markdown');assert.match(result.body.error,/move this code outside the table/);assert.deepEqual(model.state,before);assert.equal(model.commits,0);assert.equal(model.receipts.size,0);assert.equal(model.acquisitions,0)}finally{await f.close()}
});

test('idle scheduler keys do not deny the 33rd sequential document read', async () => {
  const model = authorityModel(), f = await fixture(model.handle, { idleMs: 60000 });
  await f.start();
  try {
    for (let i = 0; i < 32; i++) {
      const response = await call(f.socket, `/v1/documents/page-${i}/read`, {});
      assert.equal(response.status, 200, `sequential read ${i}`);
    }
    const response = await call(f.socket, '/v1/documents/page-32/read', {});
    assert.equal(response.status, 200, `33rd idle document: ${JSON.stringify(response.body)}`);
    assert.equal(model.acquisitions, 0);
    assert.equal(model.commits, 0);
  } finally { await f.close(); }
});

test('scheduler never evicts busy document tails', async () => {
  const model = authorityModel(), held = deferred(), allStarted = deferred();
  let active = 0;
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'state') { if (++active === 32) allStarted.resolve(); await held.promise; }
    return model.handle(op, data, req, res);
  }, { idleMs: 60000 });
  await f.start();
  const pending = [];
  try {
    for (let i = 0; i < 32; i++) pending.push(call(f.socket, `/v1/documents/busy-${i}/read`, {}));
    await allStarted.promise;
    assert.equal((await call(f.socket, '/v1/documents/overflow/read', {})).status, 429);
    const sameKey = call(f.socket, '/v1/documents/busy-0/read', {});
    await new Promise(resolve => setTimeout(resolve, 20));
    assert.equal(active, 32, 'same-key request must remain behind its original busy tail');
    held.resolve();
    assert.ok((await Promise.all([...pending, sameKey])).every(response => response.status === 200));
    assert.equal((await call(f.socket, '/v1/documents/overflow/read', {})).status, 200);
  } finally { held.resolve(); await Promise.allSettled(pending); await f.close(); }
});

test('structural human admission captures only explicit schema3 root blocks',async()=>{
 const model=authorityModel(),doc=create(initialJSON(),3);model.state.schema=3;model.state.snapshot=snapshot(doc);doc.destroy();
 const f=await fixture(model.handle);await f.start();try{
  const response=await call(f.socket,'/v1/documents/p/verify-structure',{base_rev:model.state.rev,structure:{block_ids:['title','intro','table'],allow_insert_after:true}});
  assert.equal(response.status,200);assert.equal(response.body.targets[0].schema,3);assert.deepEqual(response.body.targets[0].structure.blocks.map(block=>block.id),['title','intro','table']);
  assert.equal((await call(f.socket,'/v1/documents/p/verify-structure',{base_rev:model.state.rev,structure:{block_ids:['intro'],allow_insert_after:true}},{Authorization:'Bearer agtv2.agent'})).status,403);
  assert.equal(model.acquisitions,0);assert.equal(model.commits,0);
 }finally{await f.close()}
});

test('failed second structural operation never commits or reuses its mutated scratch',async()=>{
 const model=authorityModel(),doc=create(initialJSON(),3);model.state.schema=3;model.state.snapshot=snapshot(doc);doc.destroy();
 const f=await fixture(model.handle);await f.start();try{
  const selected=await call(f.socket,'/v1/documents/p/verify-structure',{base_rev:model.state.rev,structure:{block_ids:['intro'],allow_insert_after:true}});
  model.state.requestScope={requestID:'structural-request',baseRev:model.state.rev,target:selected.body.targets[0]};
  const confirmed=(await read(f,{Authorization:'Bearer agtv2.agent'})).body;
  const request={id:'structural-request-patch',schema:3,page_id:'p',request_id:'structural-request',kind:'agent',epoch:confirmed.epoch,ifRev:confirmed.rev,operations:[{op:'insert_after',after_id:'intro',node:{type:'paragraph',content:[{type:'text',text:'must never be published'}]}},{op:'delete',id:'title'}]};
  const original=model.state.snapshot;
  assert.equal((await call(f.socket,'/v1/documents/p/patch',request,{Authorization:'Bearer agtv2.agent'})).status,400);
  assert.equal(model.commits,0);assert.equal(model.checkpoints,0);assert.equal(model.receipts.size,0);assert.equal(model.state.snapshot,original);assert.equal(model.state.rev,confirmed.rev);
  const fresh=(await read(f,{Authorization:'Bearer agtv2.agent'})).body;
  assert.equal(fresh.outline.some(block=>block.text.includes('must never be published')),false);
  request.operations=request.operations.slice(0,1);
  assert.equal((await call(f.socket,'/v1/documents/p/patch',request,{Authorization:'Bearer agtv2.agent'})).status,200);
  assert.equal(model.commits,1);
  assert.equal((await read(f,{Authorization:'Bearer agtv2.agent'})).body.outline.filter(block=>block.text==='must never be published').length,1);
 }finally{await f.close()}
});
