import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import * as Y from 'yjs';
import { mkdtemp, copyFile, appendFile, writeFile, readFile, stat, lstat, chmod, symlink, rm } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { startService, PROTOCOL } from './service.mjs';
import { createSurface } from './surface-core.mjs';
import { RICH_VERSION, LATEST_VERSION, CREATE_VERSION, create, snapshot, initialJSON, outline, load, validate, upgradeContent, anchor, find, encode } from './schema.mjs';
import { schemaAllows } from './schema-core.mjs';
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
    if (op === 'recover') { assert.equal(typeof data.boot, 'string'); if (!model.boots.has(data.boot)) { model.boots.add(data.boot); model.recoveries++; model.state.generation++; model.state.epoch++; } return { recovered: true, protocol: PROTOCOL, schema: LATEST_VERSION }; }
    if (op === 'ready') return { protocol: model.compatible ? PROTOCOL : 99, schema: LATEST_VERSION };
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
    else if (op === 'upgrade') { assert.equal(data.schema,model.state.schema); assert.equal(data.targetSchema,LATEST_VERSION); assert.equal(data.ifRev,model.state.rev); assert.equal(data.generation,model.state.generation); model.state = {...model.state,schema:LATEST_VERSION,rev:model.state.rev+1,epoch:model.state.epoch+1,generation:model.state.generation+1}; }
    else if (op === 'restore') { model.restores++; const source=model.checkpointStates.get(data.checkpoint)??model.state; model.state = { ...model.state,schema:model.state.schema>=5&&source.schema<model.state.schema?model.state.schema:source.schema,snapshot:source.snapshot, rev: model.state.rev + 1, epoch: model.state.epoch + 1, generation: model.state.generation + 1 }; }
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
    assert.equal((await call(f.socket, '/v1/documents/p/patch', request, { Authorization: 'Bearer agtv2.synthetic-run' })).status, 200);
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
    assert.equal((await call(f.socket, '/health/ready')).body.schema, LATEST_VERSION);
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
    seen.push(op); if (op === 'recover') return { recovered: true, protocol: PROTOCOL, schema: LATEST_VERSION };
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
    if (op === 'recover') return { recovered: true, protocol: PROTOCOL, schema: LATEST_VERSION };
    if (op === 'ready') return { protocol: PROTOCOL, schema: LATEST_VERSION };
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
// A real process binds the path and is killed: the kernel leaves the file.
async function crashedSocket(path) {
  const crashed = spawn(process.execPath, ['-e', "require('node:net').createServer().listen(process.argv[1], () => console.log('bound'))", path], { stdio: ['ignore', 'pipe', 'inherit'] });
  try { await once(crashed.stdout, 'data'); } finally { crashed.kill('SIGKILL'); }
  await once(crashed, 'exit');
  assert.ok((await lstat(path)).isSocket());
}
// Reclaiming needs the Linux startup lock; other platforms refuse any path.
const linux = process.platform === 'linux';
test('a socket left by a killed engine is reclaimed by its replacement; other paths stay refused', { timeout: 10000 }, async () => {
  const model = authorityModel(), f = await fixture(model.handle);
  try {
    await crashedSocket(f.socket);
    if (!linux) { await assert.rejects(f.start(), /already exists/); assert.ok((await lstat(f.socket)).isSocket()); assert.equal(model.recoveries, 0); return; }
    await f.start();
    assert.equal(model.recoveries, 1); assert.equal((await read(f)).status, 200);
  } finally { await f.close(); }
  const other = await fixture(authorityModel().handle);
  try {
    await writeFile(other.socket, 'not a socket');
    await assert.rejects(other.start(), /already exists/);
    assert.equal(await readFile(other.socket, 'utf8'), 'not a socket');
  } finally { await other.close(); }
});
test('concurrent replacements of a killed engine, also through a directory alias: exactly one reclaims, binds and recovers', { timeout: 10000, skip: !linux && 'startup lock is Linux-only' }, async () => {
  const model = authorityModel(), f = await fixture(model.handle);
  const alias = join(await mkdtemp(join(tmpdir(), 'docs-alias-')), 'd');
  await symlink(dirname(f.socket), alias);
  const through = path => ({ socket: path, authoritySocket: f.authoritySocket, tokenFile: f.tokenFile });
  let started = [];
  try {
    await crashedSocket(f.socket);
    const results = await Promise.allSettled([f.socket, join(alias, basename(f.socket))].flatMap(path => [startService(through(path)), startService(through(path))]));
    started = results.filter(result => result.status === 'fulfilled').map(result => result.value);
    assert.equal(started.length, 1);
    for (const result of results.filter(result => result.status === 'rejected')) assert.match(result.reason.message, /already exists/);
    assert.equal(model.recoveries, 1); assert.equal((await read(f)).status, 200);
  } finally { await Promise.all(started.map(service => service.close())); await rm(dirname(alias), { recursive: true }); await f.close(); }
});
test('recovery gates all admission and failure closes its socket; a fresh boot can retry', async () => {
  const entered = deferred(), proceed = deferred(); let reject = true, boots = [];
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'recover') { boots.push(data.boot); entered.resolve(); await proceed.promise; if (reject) { res.writeHead(503); return { error: 'recovery failed' }; } return { recovered: true, protocol: PROTOCOL, schema: LATEST_VERSION }; }
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
    if (op === 'recover') return { recovered: true, protocol: PROTOCOL, schema: LATEST_VERSION };
    if (op === 'ready') return { protocol: PROTOCOL, schema: LATEST_VERSION };
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
    }, { Authorization: 'Bearer agtv2.synthetic-run' });
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
 for (const schema of [undefined, 2, 3, 4, 5, 7]) {
  const f = await fixture((op,data) => { assert.equal(op,'recover'); assert.equal(data.schema,LATEST_VERSION); return {recovered:true,protocol:PROTOCOL,...(schema === undefined ? {} : {schema})}; });
  try { await assert.rejects(f.start(),/compatible engine recovery/); await assert.rejects(call(f.socket,'/v1/documents/p/read',{})); } finally { await f.close(); }
 }
});

test('explicit human upgrade binds original Page/revision, preserves bytes and retries without reapplying', async () => {
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try {
  const before=(await read(f)).body,request={id:'explicit-three',schema:2,target_schema:LATEST_VERSION,page_id:'p',epoch:before.epoch,ifRev:before.rev};
  for(const changed of [{...request,page_id:'foreign'},{...request,ifRev:before.rev-1},{...request,target_schema:3},{...request,update:'forged'}])assert.equal((await call(f.socket,'/v1/documents/p/upgrade',changed)).status,409);
  assert.equal((await call(f.socket,'/v1/documents/p/upgrade',request,{Authorization:'Bearer agtv2.synthetic'})).status,403);
  const receipt=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(receipt.status,200);assert.equal(receipt.body.schema,LATEST_VERSION);assert.equal(model.state.snapshot,before.snapshot);assert.equal(model.state.rev,before.rev+1);
  const after=(await read(f)).body;assert.equal(after.schema,LATEST_VERSION);assert.notEqual(after.epoch,before.epoch);assert.deepEqual(after.content,before.content);
  const retry=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(retry.status,200);assert.equal(retry.body.rev,receipt.body.rev);assert.equal(retry.body.duplicate,true);assert.equal(model.seen.filter(x=>x.op==='upgrade').length,1);
  model.revoked.add('Bearer human');assert.equal((await call(f.socket,'/v1/documents/p/upgrade',request)).status,403);assert.equal(model.state.rev,before.rev+1);
 } finally {await f.close()}
});
test('schema3 raw human marks are versioned; old clients and unqualified semantic clients fail closed',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try {
  const before=(await read(f)).body;const upgrade={id:'upgrade-marks',schema:2,target_schema:LATEST_VERSION,page_id:'p',epoch:before.epoch,ifRev:before.rev};assert.equal((await call(f.socket,'/v1/documents/p/upgrade',upgrade)).status,200);
  const current=(await read(f)).body,doc=load(current.snapshot),vector=Y.encodeStateVector(doc);find(doc,'intro').toArray()[0].format(0,8,{link:{href:'https://example.invalid/'}});const update=encode(Y.encodeStateAsUpdate(doc,vector));doc.destroy();
  assert.equal((await call(f.socket,'/v1/documents/p/patch',{id:'old-client',schema:2,epoch:current.epoch,kind:'human',update})).status,409);assert.equal(model.commits,0);
  assert.equal((await call(f.socket,'/v1/documents/p/verify-targets',{base_rev:current.rev,targets:[]})).status,400);
  assert.equal((await call(f.socket,'/v1/documents/p/patch',{id:'raw-agent',schema:LATEST_VERSION,page_id:'p',epoch:current.epoch,kind:'human',update},{Authorization:'Bearer agtv2.synthetic'})).status,403);assert.equal(model.commits,0);
  assert.equal((await call(f.socket,'/v1/documents/p/patch',{id:'foreign-page',schema:LATEST_VERSION,page_id:'foreign',epoch:current.epoch,kind:'human',update})).status,409);
  const saved=await call(f.socket,'/v1/documents/p/patch',{id:'human-link',schema:LATEST_VERSION,page_id:'p',epoch:current.epoch,kind:'human',update});assert.equal(saved.status,200);const rich=(await read(f)).body;assert.equal(rich.schema,LATEST_VERSION);assert.equal(rich.content.content.find(node=>node.attrs?.id==='intro').content[0].marks[0].attrs.href,'https://example.invalid/');
 }finally{await f.close()}
});
test('mixed schema checkpoint read keeps the source version; restore onto schema 5 stays schema 5 and says so (#5155)',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try {
  const old=(await read(f)).body;assert.equal((await call(f.socket,'/v1/documents/p/checkpoint',{id:'legacy-checkpoint',schema:2,epoch:old.epoch,ifRev:old.rev})).status,200);
  assert.equal((await call(f.socket,'/v1/documents/p/upgrade',{id:'mixed-upgrade',schema:2,target_schema:LATEST_VERSION,page_id:'p',epoch:old.epoch,ifRev:old.rev})).status,200);
  const current=(await read(f)).body;assert.equal((await call(f.socket,'/v1/documents/p/checkpoint',{id:'rich-checkpoint',schema:LATEST_VERSION,page_id:'p',epoch:current.epoch,ifRev:current.rev})).status,200);
  const request={id:'restore-legacy',schema:LATEST_VERSION,page_id:'p',epoch:current.epoch,ifRev:current.rev,checkpoint:'legacy-checkpoint'};const restored=await call(f.socket,'/v1/documents/p/restore',request);assert.equal(restored.status,200);assert.equal(restored.body.schema,LATEST_VERSION);const head=(await read(f)).body;assert.equal(head.schema,LATEST_VERSION);assert.notEqual(head.epoch,current.epoch);assert.equal(head.snapshot,old.snapshot);
  const retry=await call(f.socket,'/v1/documents/p/restore',request);assert.equal(retry.status,200);assert.equal(retry.body.schema,LATEST_VERSION);assert.equal(retry.body.rev,restored.body.rev);assert.equal(retry.body.duplicate,true);assert.equal(model.restores,1);
  // A second client was offline during the restore and still holds the pre-restore schema-5 head with an unsent edit.
  // Its write names the old epoch and is refused; nothing removed by the restore comes back (#5233).
  const offline=load(current.snapshot),seen=Y.encodeStateVector(offline);find(offline,'intro').toArray()[0].insert(0,'offline ');const offlineUpdate=encode(Y.encodeStateAsUpdate(offline,seen));offline.destroy();
  const commits=model.commits,late=await call(f.socket,'/v1/documents/p/patch',{id:'offline-client',schema:LATEST_VERSION,page_id:'p',epoch:current.epoch,kind:'human',update:offlineUpdate});
  assert.equal(late.status,409);assert.equal(model.commits,commits);const after=(await read(f)).body;assert.equal(after.snapshot,head.snapshot);assert.equal(after.rev,head.rev);assert.equal(after.epoch,head.epoch);
  // Restoring pre-upgrade history keeps schema 5: a proposal binds to schema 5, never silently to 4 (#5233).
  const stale=await call(f.socket,'/v1/documents/p/propose',{id:'after-restore-4',schema:4,page_id:'p',epoch:head.epoch,ifRev:head.rev,target:'one',ifHash:'0'.repeat(64),text:'x'},{Authorization:'Bearer agtv2.synthetic-run'});assert.equal(stale.status,409);assert.match(stale.body.error,/unsupported client schema/);
  const proposed=await call(f.socket,'/v1/documents/p/propose',{id:'after-restore',schema:LATEST_VERSION,page_id:'p',epoch:head.epoch,ifRev:head.rev,target:'one',ifHash:'0'.repeat(64),text:'x'},{Authorization:'Bearer agtv2.synthetic-run'});assert.equal(proposed.status,409);assert.match(proposed.body.error,/semantic precondition conflict/);
  const pinned=await call(f.socket,'/v1/documents/p/checkpoint-read',{checkpoint:'rich-checkpoint'});assert.equal(pinned.status,200);assert.equal(pinned.body.schema,LATEST_VERSION);
 }finally{await f.close()}
});

// Receipt lookup remains an authorized read of an existing durable result.
test('WRITE to READ downgrade permits original receipt but never a fresh upgrade',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try{
 const old=(await read(f)).body;const request={id:'readable-upgrade',schema:2,target_schema:LATEST_VERSION,page_id:'p',epoch:old.epoch,ifRev:old.rev};
 const original=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(original.status,200);
 const rich=(await read(f)).body;await call(f.socket,'/v1/documents/p/checkpoint',{id:'readable-rich',schema:LATEST_VERSION,page_id:'p',epoch:rich.epoch,ifRev:rich.rev});
 // A later confirmed restore changes the head, not the original receipt.
 model.state={...model.state,schema:2,rev:model.state.rev+1,snapshot:old.snapshot};model.readOnly.add('Bearer human');
 const rev=model.state.rev;const retry=await call(f.socket,'/v1/documents/p/upgrade',request);assert.equal(retry.status,200);assert.equal(retry.body.duplicate,true);assert.equal(retry.body.rev,original.body.rev);assert.equal(retry.body.schema,LATEST_VERSION);assert.equal(model.state.rev,rev);
 const current=(await read(f)).body;assert.equal((await call(f.socket,'/v1/documents/p/upgrade',{...request,id:'fresh-read-only',epoch:current.epoch,ifRev:current.rev})).status,403);assert.equal(model.state.rev,rev);
 model.revoked.add('Bearer human');assert.equal((await call(f.socket,'/v1/documents/p/upgrade',request)).status,403);
 }finally{await f.close()}
});

test('rich semantic proof is private, byte-bounded, and preserves nontext context',async()=>{
 const model=authorityModel(),f=await fixture(model.handle);await f.start();
 try{
 const doc=create({type:'doc',content:[{type:'paragraph',attrs:{id:'rich-target'},content:[{type:'text',text:'prefix ',marks:[{type:'link',attrs:{href:'https://example.invalid/'}}]},{type:'text',text:'middle suffix'}]}]},3);model.state={...model.state,schema:3,snapshot:snapshot(doc)};doc.destroy();
 const state=(await read(f)).body;const request={id:'rich-proof',schema:3,page_id:'p',epoch:state.epoch,ifRev:state.rev,ifHash:state.outline[0].hash,target:'rich-target',text:'prefix changed suffix',rich:{before:{type:'client-forgery'}}};
 assert.equal((await call(f.socket,'/v1/documents/p/patch',request,{Authorization:'Bearer agtv2.synthetic-run'})).status,200);const semantic=model.seen.find(x=>x.op==='commit').data.semantic;assert.equal(semantic.rich.before.type,'doc');assert.deepEqual(semantic.rich.before,state.content);assert.equal(semantic.rich.after.content[0].content[0].marks[0].type,'link');
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

// Full human CLI writes keep semantic preconditions without acquiring an agent realm.
test('semantic commit authority follows the authenticated realm', async () => {
  for (const [authorization, kind] of [['Bearer human', 'human'], ['Bearer agtv2.synthetic-run', 'agent']]) {
    const model = authorityModel(), f = await fixture(model.handle);
    try {
      await f.start();
      const state = (await read(f, { Authorization: authorization })).body;
      const original = state.outline.find(block => block.id === 'intro');
      const untouched = state.outline.filter(block => block.id !== 'intro');
      const request = patchRequest(state, `realm-${kind}`, 'realm replacement');
      assert.equal((await call(f.socket, '/v1/documents/p/patch', request, { Authorization: authorization })).status, 200);
      const commit = model.seen.find(entry => entry.op === 'commit');
      assert.equal(commit.bearer, authorization);
      assert.equal(commit.data.kind, kind);
      assert.equal(commit.data.ifRev, state.rev);
      assert.deepEqual(commit.data.ifHashes, { intro: original.hash });
      assert.equal(commit.data.generation, model.state.generation);
      if (kind === 'human') assert.equal(Object.hasOwn(commit.data, 'semantic'), false);
      else assert.equal(commit.data.semantic.text, 'realm replacement');
      const saved = (await read(f, { Authorization: authorization })).body;
      assert.equal(saved.outline.find(block => block.id === 'intro').text, 'realm replacement');
      assert.deepEqual(saved.outline.filter(block => block.id !== 'intro'), untouched);
    } finally { await f.close(); }
  }
});


test('prepare-create builds closed creation-schema content without Go data RPCs', async () => {
  const model=authorityModel(), f=await fixture(model.handle); await f.start();
  const baseline=model.seen.length, before=structuredClone(model.state);
  const blocks=[{type:'heading',level:1,text:'Prepared report'},{type:'paragraph',text:'Literal <script> & text'},{type:'bulletList',items:['First','Second']},{type:'orderedList',items:['Step']},{type:'codeBlock',text:'const literal = "<script>";\n'}];
  const prepare=(body,headers)=>call(f.socket,'/v1/documents/new/prepare-create',body,headers);
  try {
    const result=await prepare({blocks},{Authorization:'Bearer agtv2.synthetic-creation'});
    assert.equal(result.status,200,'private prepare-create dispatcher must admit bounded authoring');
    assert.deepEqual(Object.keys(result.body).sort(),['blocks','schema','snapshot']);
    assert.equal(result.body.schema,CREATE_VERSION);
    const doc=load(result.body.snapshot);
    try {
      const content=validate(doc,CREATE_VERSION), prepared=await outline(doc,CREATE_VERSION);
      assert.deepEqual(content.content.map(block=>block.type),['heading','paragraph','bulletList','orderedList','codeBlock']);
      assert.equal(content.content[1].content[0].text,'Literal <script> & text');
      assert.deepEqual(result.body.blocks,prepared.map(({id,hash})=>({id,hash})));
      assert.equal(new Set(prepared.map(block=>block.id)).size,prepared.length);
      assert.ok(prepared.every(block=>/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(block.id)));
      assert.ok(prepared.every(block=>/^[a-f0-9]{64}$/.test(block.hash)));
    } finally {doc.destroy()}
    const repeated=await prepare({blocks});
    assert.equal(repeated.status,200);
    assert.ok(repeated.body.blocks.every(block=>!result.body.blocks.some(previous=>previous.id===block.id)),'Node owns fresh block UUIDs');
    for(const invalid of [{blocks,operation:'initialize'},{blocks:[{type:'paragraph',id:'forged',text:'x'}]},{blocks:[{type:'paragraph',text:'x',marks:[]}]},{blocks:[{type:'image',src:'https://example.invalid'}]},{blocks:[{type:'paragraph',text:' '}]},null]) {
      assert.equal((await prepare(invalid)).status,400);
    }
    assert.equal((await prepare({blocks},{'X-Crewship-Docs-Service':'bad'})).status,403);
    assert.equal((await prepare({blocks},{Authorization:''})).status,401);
    assert.equal((await prepare({blocks},{'X-Workspace-ID':''})).status,401);
    assert.deepEqual(model.seen.slice(baseline),[],'preparation must not call Go state, receipt, lease or mutation RPCs');
    assert.deepEqual(model.state,before);
    assert.equal(model.acquisitions,0); assert.equal(model.commits,0); assert.equal(model.receipts.size,0);
  } finally {await f.close()}
});


test('authoring adapter bytes change the copied service build identity', async () => {
  const dir=await mkdtemp(join(tmpdir(),'docs-build-'));
  let f, service;
  try {
    for(const file of ['pnpm-lock.yaml','schema.mjs','schema-core.mjs','surface-core.mjs','position-migrations.mjs','service.mjs','create-document.mjs','secret.mjs'])await copyFile(new URL(file,import.meta.url),join(dir,file));
    await symlink(fileURLToPath(new URL('node_modules',import.meta.url)),join(dir,'node_modules'),'dir');
    const {startService:copiedStart}=await import(pathToFileURL(join(dir,'service.mjs')).href);
    f=await fixture(authorityModel().handle);
    const start=()=>copiedStart({socket:f.socket,authoritySocket:f.authoritySocket,tokenFile:f.tokenFile});
    service=await start(); const original=service.identity.build;
    await service.close(); service=undefined;
    service=await start(); assert.equal(service.identity.build,original,'restarting unchanged public bytes keeps identity');
    await service.close(); service=undefined;
    // Only an inactive, private copy changes; installed and repository bytes stay intact.
    await appendFile(join(dir,'create-document.mjs'),'\n// copied public fixture identity change\n');
    service=await start();
    assert.notEqual(service.identity.build,original,'authoring adapter changes must invalidate service identity');
    const authored=service.identity.build;
    await service.close(); service=undefined;
    await appendFile(join(dir,'position-migrations.mjs'),'\n// copied migration validator identity change\n');
    service=await start();
    assert.notEqual(service.identity.build,authored,'migration validator changes must invalidate service identity');
  } finally {
    try {if(service)await service.close()} finally {try {if(f)await f.close()} finally {await rm(dir,{recursive:true})}}
  }
});

// T-OPS-01: a busy dependency cannot retain a document scheduler tail forever.
test('authority deadline bounds a continuously arriving response and frees the document queue', { timeout: 5000 }, async () => {
  const model = authorityModel(); let slow = true;
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'state' && slow) {
      slow = false;
      res.writeHead(200);
      res.write(' ');
      const drip = setInterval(() => res.write(' '), 10);
      const finish = setTimeout(() => { clearInterval(drip); res.end(JSON.stringify(model.state)); }, 500);
      res.once('close', () => { clearInterval(drip); clearTimeout(finish); });
      await new Promise(resolve => res.once('close', resolve));
      return;
    }
    return model.handle(op, data, req, res);
  }, { timeoutMs: 100 });
  try {
    await f.start();
    const response = await read(f);
    assert.equal(response.status, 503, 'ongoing response bytes must not extend the authority deadline');
    const recovered = await read(f);
    assert.equal(recovered.status, 200, 'timed-out call retained the document scheduler tail');
    assert.equal(model.commits, 0);
  } finally { await f.close(); }
});

test('authority deadline after durable commit permits exact receipt retry without a second mutation', { timeout: 5000 }, async () => {
  const model = authorityModel(); let delayReceipt = true;
  const f = await fixture(async (op, data, req, res) => {
    const value = await model.handle(op, data, req, res);
    if (op === 'commit' && delayReceipt) {
      delayReceipt = false;
      res.writeHead(200); res.write(' ');
      const drip = setInterval(() => res.write(' '), 10);
      const finish = setTimeout(() => { clearInterval(drip); res.end(JSON.stringify(value)); }, 500);
      res.once('close', () => { clearInterval(drip); clearTimeout(finish); });
      await new Promise(resolve => res.once('close', resolve));
      return;
    }
    return value;
  }, { timeoutMs: 100 });
  try {
    await f.start();
    const state = (await read(f)).body;
    const target = state.outline.find(block => block.id === 'intro');
    const request = {id:'deadline-edit',schema:2,epoch:state.epoch,ifRev:state.rev,target:target.id,ifHash:target.hash,text:'Deadline-safe edit'};
    assert.equal((await call(f.socket, '/v1/documents/p/patch', request)).status, 503);
    assert.equal(model.commits, 1, 'fixture must commit before the confirmation deadline');
    const retry = await call(f.socket, '/v1/documents/p/patch', request);
    assert.equal(retry.status, 200); assert.equal(retry.body.duplicate, true);
    assert.equal(model.commits, 1, 'uncertain confirmation executed the original mutation again');
  } finally { await f.close(); }
});

test('invalid live or retained position migration updates never reach durable commit', async () => {
  for (const kind of ['malformed', 'retained', 'oversized', 'arbitrary-root']) {
    const model = authorityModel(), f = await fixture(model.handle); await f.start();
    try {
      const state = (await read(f)).body, before = structuredClone(model.state);
      const doc = load(state.snapshot);
      const key = '__tiptap_position_migrations:root:default';
      const map = doc.getMap(kind === 'arbitrary-root' ? 'arbitrary' : key);
      if (kind === 'oversized') {
        map.set('1:0', { version: 1, client: 1, clock: 0, seen: [], transfers: Array.from({ length: 4097 }, () => ({ source: { client: 1, clock: 0, length: 1 }, target: null })) });
      } else {
        map.set('1:0', { opaque: 'payload that must never be committed' });
        if (kind === 'retained') map.delete('1:0');
      }
      const update = snapshot(doc); doc.destroy();
      const result = await call(f.socket, '/v1/documents/p/patch', { id: `migration-${kind}`, schema: 2, kind: 'human', update, epoch: state.epoch, ifRev: state.rev });
      assert.equal(result.status, 400, kind);
      assert.deepEqual(result.body, { error: 'invalid document operation' });
      assert.equal(model.commits, 0); assert.equal(model.receipts.size, 0);
      // Taking the fenced writer lease advances its epoch even when validation
      // refuses content. No durable content/revision/generation may change.
      assert.equal(model.acquisitions, 1); assert.equal(model.state.epoch, before.epoch + 1);
      assert.deepEqual({ ...model.state, epoch: before.epoch }, before);
    } finally { await f.close(); }
  }
});

test('schema4 initialize is human-only, Page-bound and exports formatting to Markdown', async () => {
  const model = authorityModel();
  const body = { type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'p4' }, content: [{ type: 'text', text: 'bold', marks: [{ type: 'bold', attrs: {} }] }] }, { type: 'codeBlock', attrs: { id: 'c4', language: 'go' }, content: [{ type: 'text', text: 'x := 1' }] }] };
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'initialize') { model.state = { ...model.state, schema: data.schema, snapshot: data.snapshot, rev: 0 }; return model.state; }
    return model.handle(op, data, req, res);
  });
  try {
    await f.start();
    assert.equal((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-four', schema: 4, body })).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-four', schema: 4, page_id: 'p', body }, { Authorization: 'Bearer agtv2.synthetic' })).status, 403);
    assert.equal((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-seven', schema: 7, page_id: 'p', body })).status, 400);
    const bad = structuredClone(body); bad.content[1].attrs.language = 'Go<script>';
    assert.notEqual((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-bad', schema: 4, page_id: 'p', body: bad })).status, 200);
    assert.equal((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-four', schema: 4, page_id: 'p', body })).status, 200);
    const state = (await read(f)).body;
    assert.equal(state.schema, 4); assert.deepEqual(state.content, body);
    const exported = await call(f.socket, '/v1/documents/p/export', {});
    assert.equal(exported.body.markdown, '**bold**\n\n```go\nx := 1\n```\n');
  } finally { await f.close(); }
});

for (const [from, body] of [
  [3, { type: 'doc', content: [{ type: 'codeBlock', attrs: { id: 'c' }, content: [{ type: 'text', text: 'x' }] }] }],
  [4, { type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'b' }, content: [{ type: 'text', text: 'bold', marks: [{ type: 'bold', attrs: {} }] }] }] }],
  [5, { type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'u' }, content: [{ type: 'text', text: 'under', marks: [{ type: 'underline', attrs: {} }] }] }] }],
]) test(`schema${from} documents upgrade to ${LATEST_VERSION} explicitly with unchanged content; the latest cannot upgrade again or to 4`, async () => {
  const model = authorityModel();
  const doc = create(body, from);
  model.state = { ...model.state, schema: from, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(model.handle); await f.start();
  try {
    const before = (await read(f)).body, request = { id: 'upgrade-latest', schema: from, target_schema: LATEST_VERSION, page_id: 'p', epoch: before.epoch, ifRev: before.rev };
    if (from === 3) assert.equal((await call(f.socket, '/v1/documents/p/upgrade', { ...request, id: 'to-four', target_schema: 4 })).status, 409);
    const receipt = await call(f.socket, '/v1/documents/p/upgrade', request);
    assert.equal(receipt.status, 200); assert.equal(receipt.body.schema, LATEST_VERSION); assert.equal(model.state.snapshot, before.snapshot);
    const after = (await read(f)).body; assert.equal(after.schema, LATEST_VERSION); assert.deepEqual(after.content, before.content); assert.deepEqual(after.outline, before.outline);
    assert.equal((await call(f.socket, '/v1/documents/p/upgrade', { ...request, id: 'latest-again', schema: LATEST_VERSION, epoch: after.epoch, ifRev: after.rev })).status, 409);
    // A client still on the old schema cannot write over the upgraded document.
    assert.equal((await call(f.socket, '/v1/documents/p/checkpoint', { id: 'old-client', schema: from, page_id: 'p', epoch: after.epoch, ifRev: after.rev })).status, 409);
  } finally { await f.close(); }
});

test('schema5 initialize keeps underline, colour and highlight and exports them to Markdown; free CSS is refused', async () => {
  const model = authorityModel();
  const body = { type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'p5' }, content: [
    { type: 'text', text: 'under', marks: [{ type: 'underline', attrs: {} }] }, { type: 'text', text: ' ' },
    { type: 'text', text: 'red', marks: [{ type: 'textColor', attrs: { color: 'red' } }, { type: 'highlight', attrs: { color: 'yellow' } }] },
    { type: 'text', text: ' ' }, { type: 'date', attrs: { value: '2026-11-20' } }, { type: 'text', text: ' ' }, { type: 'mention', attrs: { userId: 'u1' } },
  ] }] };
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'initialize') { model.state = { ...model.state, schema: data.schema, snapshot: data.snapshot, rev: 0 }; return model.state; }
    return model.handle(op, data, req, res);
  });
  try {
    await f.start();
    const bad = structuredClone(body); bad.content[0].content[2].marks[0].attrs.color = '#ff0000';
    assert.notEqual((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-css', schema: 5, page_id: 'p', body: bad })).status, 200);
    assert.notEqual((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-four', schema: 4, page_id: 'p', body })).status, 200);
    assert.equal((await call(f.socket, '/v1/documents/p/initialize', { id: 'init-five', schema: 5, page_id: 'p', body })).status, 200);
    const state = (await read(f)).body;
    assert.equal(state.schema, 5); assert.deepEqual(state.content, body);
    assert.equal((await call(f.socket, '/v1/documents/p/export', {})).body.markdown, '<u>under</u> <span data-text-color="red"><mark data-highlight="yellow">red</mark></span> 2026-11-20 @Unknown member\n');
    // Go supplies the names this reader may see; the engine never looks them up.
    assert.equal((await call(f.socket, '/v1/documents/p/export', { people: { u1: 'Ada' } })).body.markdown, '<u>under</u> <span data-text-color="red"><mark data-highlight="yellow">red</mark></span> 2026-11-20 @Ada\n');
    // Names are bounded in code points, as Go bounds them: 200 emoji are one valid name.
    assert.equal((await call(f.socket, '/v1/documents/p/export', { people: { u1: '\u{1F600}'.repeat(200) } })).status, 200);
    for (const people of [[], 'Ada', { 'bad id': 'Ada' }, { u1: '' }, { u1: 7 }, { u1: 'line\nbreak' }, { u1: 'x'.repeat(201) }, { u1: '\u{1F600}'.repeat(201) }]) {
      assert.equal((await call(f.socket, '/v1/documents/p/export', { people })).status, 400, JSON.stringify(people));
    }
  } finally { await f.close(); }
});

test('comments, replies and resolve are closed human requests anchored by the engine', async () => {
  const model = authorityModel(), stored = [];
  const doc = create({ type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'q' }, content: [{ type: 'text', text: 'Bloky kódu' }] }] }, 4);
  model.state = { ...model.state, schema: 4, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'comment') { stored.push({ id: data.id, body: data.body, parent: data.parent, anchor: data.anchor, actor: 'user:human', rev: data.ifRev, version: 1, resolved: false, created_at: 'now' }); return { id: data.id, rev: data.ifRev, epoch: 1, kind: 'comment' }; }
    if (op === 'resolve') { const c = stored.find(x => x.id === data.comment); c.resolved = data.resolved; c.version++; return { id: data.id, rev: data.ifRev, epoch: 1, kind: 'resolve' }; }
    if (op === 'comments') return stored.map(c => c.parent ? { ...c, anchor: undefined } : c);
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const state = (await read(f)).body, base = { schema: 4, page_id: 'p', epoch: state.epoch, ifRev: state.rev };
    const root = { ...base, id: 'thread-1', block: 'q', from: 0, to: 5, text: 'Ukázka komentáře' };
    for (const bad of [{ ...root, text: '' }, { ...root, text: ' ' }, { ...root, extra: 1 }, { ...root, block: undefined }, { ...root, from: 3, to: 3 }, { ...root, block: 'missing' }]) {
      assert.equal((await call(f.socket, '/v1/documents/p/comment', JSON.parse(JSON.stringify(bad)))).status, 400, JSON.stringify(bad));
    }
    assert.equal((await call(f.socket, '/v1/documents/p/comment', { ...root, ifRev: state.rev - 1 })).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/comment', { ...root, page_id: 'other' })).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/comment', root, { Authorization: 'Bearer agtv2.synthetic' })).status, 403);
    assert.equal((await call(f.socket, '/v1/documents/p/comment', root)).status, 200);
    assert.equal(stored[0].anchor.quote, 'Bloky'); assert.equal(stored[0].anchor.block, 'q');
    assert.equal((await call(f.socket, '/v1/documents/p/comment', { ...base, id: 'reply-1', parent: 'thread-1', block: 'q', text: 'x' })).status, 400);
    assert.equal((await call(f.socket, '/v1/documents/p/comment', { ...base, id: 'reply-1', parent: 'thread-1', text: 'Odpověď' })).status, 200);
    assert.equal(stored[1].anchor, undefined); assert.equal(stored[1].parent, 'thread-1');
    assert.equal((await call(f.socket, '/v1/documents/p/resolve', { ...base, id: 'resolve-1', comment: 'thread-1', version: 1, resolved: 'yes' })).status, 400);
    assert.equal((await call(f.socket, '/v1/documents/p/resolve', { ...base, id: 'resolve-1', comment: 'thread-1', version: 1, resolved: true })).status, 200);
    const listed = (await call(f.socket, '/v1/documents/p/comments', {})).body.comments;
    assert.equal(listed[0].status, 'resolved'); assert.equal(listed[0].quote, 'Bloky'); assert.equal(listed[0].from, 0); assert.equal(listed[0].to, 5);
    assert.equal(listed[1].parent, 'thread-1'); assert.equal(listed[1].anchor, undefined); assert.equal(listed[1].status, undefined);
    assert.equal(model.acquisitions, 0); assert.equal(model.commits, 0);
  } finally { await f.close(); }
});

test('schema4 agents propose without changing content; a human accepts exactly the proposal and formatting round-trips', async () => {
  const model = authorityModel(), proposals = new Map();
  const bold = { type: 'bold', attrs: {} }, italic = { type: 'italic', attrs: {} }, strike = { type: 'strike', attrs: {} };
  const body = { type: 'doc', content: [
    { type: 'paragraph', attrs: { id: 'lead' }, content: [{ type: 'text', text: 'Keep ' }, { type: 'text', text: 'bold', marks: [bold] }, { type: 'text', text: ' and ' }, { type: 'text', text: 'gone', marks: [strike] }, { type: 'text', text: ' words.' }] },
    { type: 'codeBlock', attrs: { id: 'code', language: 'python' }, content: [{ type: 'text', text: 'print("hi")' }] },
    { type: 'blockquote', attrs: { id: 'quote' }, content: [{ type: 'paragraph', attrs: { id: 'quote-p' }, content: [{ type: 'text', text: 'Quoted', marks: [italic] }] }] },
    { type: 'taskList', attrs: { id: 'tasks' }, content: [{ type: 'taskItem', attrs: { id: 'done', checked: true }, content: [{ type: 'paragraph', attrs: { id: 'done-p' }, content: [{ type: 'text', text: 'Ship it' }] }] }] },
  ] };
  const doc = create(body, 4); model.state = { ...model.state, schema: 4, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'propose') {
      assert.ok(req.headers.authorization.startsWith('Bearer agtv2.'));
      const before = data.semantic.rich.before, after = data.semantic.rich.after;
      const find = (node, id) => node.attrs?.id === id ? node : (node.content ?? []).map(child => find(child, id)).find(Boolean);
      proposals.set(data.id, { id: data.id, actor: 'agent:a:r', run_id: 'r', target: data.target, schema: data.schema, base_hash: data.ifHashes[data.target], text: data.semantic.text, generation: data.generation, base_rev: data.ifRev, status: 'open', created_at: 'now', base_content: find(before, data.target), proposed_content: find(after, data.target) });
      return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, actor: 'agent:a:r', kind: 'proposal' };
    }
    if (op === 'proposals') return [...proposals.values()].map(({ base_content: _b, proposed_content: _p, ...p }) => p);
    if (op === 'proposal') { const p = proposals.get(data.id); if (!p) { res.writeHead(404); return { error: 'proposal not found' }; } return p; }
    if (op === 'proposal-reject') { proposals.get(data.proposal).status = 'rejected'; return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, kind: 'proposal-reject' }; }
    if (op === 'commit' && data.proposal) proposals.get(data.proposal).status = 'accepted';
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const agent = { Authorization: 'Bearer agtv2.synthetic-run' };
    const state = (await read(f)).body, lead = state.outline.find(b => b.id === 'lead');
    const request = { id: 'proposal-1', schema: 4, page_id: 'p', epoch: state.epoch, ifRev: state.rev, target: 'lead', ifHash: lead.hash, text: 'Keep bold and gone words, now revised.' };
    for (const bad of [{ ...request, extra: 1 }, { ...request, text: undefined }]) assert.equal((await call(f.socket, '/v1/documents/p/propose', JSON.parse(JSON.stringify(bad)), agent)).status, 400);
    assert.equal((await call(f.socket, '/v1/documents/p/propose', request)).status, 403, 'a human session never proposes as an agent');
    assert.equal((await call(f.socket, '/v1/documents/p/propose', { ...request, ifHash: '0'.repeat(64) }, agent)).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/propose', { ...request, target: 'code', ifHash: state.outline.find(b => b.id === 'code').hash }, agent)).status, 400, 'code blocks stay outside agent admission');
    const proposed = await call(f.socket, '/v1/documents/p/propose', request, agent);
    assert.equal(proposed.status, 200); assert.equal(proposed.body.kind, 'proposal');
    assert.equal(model.commits, 0); assert.equal(model.acquisitions, 0); assert.equal(model.state.rev, state.rev);
    const sent = proposals.get('proposal-1');
    assert.deepEqual(sent.base_content, body.content[0]);
    assert.deepEqual(sent.proposed_content.content.slice(0, 2), [{ type: 'text', text: 'Keep ' }, { type: 'text', text: 'bold', marks: [bold] }]);
    assert.equal((await call(f.socket, '/v1/documents/p/proposals', {}, agent)).status, 403);
    const listed = (await call(f.socket, '/v1/documents/p/proposals', {})).body.proposals;
    assert.equal(listed.length, 1); assert.equal(listed[0].status, 'open');
    const detail = (await call(f.socket, '/v1/documents/p/proposal', { proposal: 'proposal-1' })).body;
    assert.deepEqual(detail.base_content, body.content[0]);
    // A decision is human, closed and against the head the reviewer saw.
    const decision = { id: 'accept-1', proposal: 'proposal-1', ifRev: state.rev };
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', decision, agent)).status, 403);
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', { ...decision, extra: 1 })).status, 400);
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', { ...decision, ifRev: state.rev - 1 })).status, 409);
    const accepted = await call(f.socket, '/v1/documents/p/proposal-accept', decision);
    assert.equal(accepted.status, 200); assert.equal(model.commits, 1);
    const commit = model.seen.find(entry => entry.op === 'commit').data;
    assert.equal(commit.kind, 'human'); assert.equal(commit.proposal, 'proposal-1'); assert.deepEqual(commit.ifHashes, { lead: lead.hash });
    const after = (await read(f)).body;
    assert.deepEqual(after.content.content.slice(1), body.content.slice(1), 'code language, quote and task list are untouched');
    assert.deepEqual(after.content.content[0].content.slice(0, 3), body.content[0].content.slice(0, 3));
    assert.equal(after.content.content[0].content.map(part => part.text).join(''), request.text);
    assert.equal(after.content.content[0].content.find(part => part.text === 'gone')?.marks?.[0]?.type, 'strike');
    assert.deepEqual(after.content.content[0], detail.proposed_content, 'the accepted block is exactly the reviewed proposal');
    // The exact decision retries to its receipt; a second accept is refused.
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', decision)).body.duplicate, true);
    assert.equal(model.commits, 1);
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', { ...decision, id: 'accept-2', ifRev: after.rev })).status, 409);
  } finally { await f.close(); }
});

test('schema 5 proposals bind to the document schema; marks and atoms survive accept; a schema 4 proposal is never accepted at 5 (#5233)', async () => {
  const model = authorityModel(), proposals = new Map(), sent = [];
  const under = { type: 'underline', attrs: {} }, red = { type: 'textColor', attrs: { color: 'red' } }, yellow = { type: 'highlight', attrs: { color: 'yellow' } };
  const body = { type: 'doc', content: [
    { type: 'paragraph', attrs: { id: 'lead' }, content: [{ type: 'text', text: 'Ship ' }, { type: 'text', text: 'under', marks: [under] }, { type: 'text', text: ' and ' }, { type: 'text', text: 'red', marks: [red, yellow] }, { type: 'text', text: ' by ' }, { type: 'date', attrs: { value: '2026-12-01' } }, { type: 'text', text: ' for ' }, { type: 'mention', attrs: { userId: 'u1' } }, { type: 'text', text: '.' }] },
    { type: 'paragraph', attrs: { id: 'other' }, content: [{ type: 'text', text: 'Other' }] },
  ] };
  const doc = create(body, 5); model.state = { ...model.state, schema: 5, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'propose') {
      sent.push(data);
      const find = (node, id) => node.attrs?.id === id ? node : (node.content ?? []).map(child => find(child, id)).find(Boolean);
      proposals.set(data.id, { id: data.id, actor: 'agent:a:r', run_id: 'r', target: data.target, schema: data.schema, base_hash: data.ifHashes[data.target], text: data.semantic.text, generation: data.generation, base_rev: data.ifRev, status: 'open', created_at: 'now', proposed_content: find(data.semantic.rich.after, data.target) });
      return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, actor: 'agent:a:r', kind: 'proposal' };
    }
    if (op === 'proposal') return proposals.get(data.id);
    if (op === 'proposal-reject') { proposals.get(data.proposal).status = 'rejected'; return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, kind: 'proposal-reject' }; }
    if (op === 'commit' && data.proposal) proposals.get(data.proposal).status = 'accepted';
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const agent = { Authorization: 'Bearer agtv2.synthetic-run' };
    const state = (await read(f)).body, lead = state.outline.find(b => b.id === 'lead');
    const request = { id: 'p5', schema: 5, page_id: 'p', epoch: state.epoch, ifRev: state.rev, target: 'lead', ifHash: lead.hash, text: 'Deliver under and red by  for .' };
    const old = await call(f.socket, '/v1/documents/p/propose', { ...request, id: 'p4', schema: 4 }, agent);
    assert.equal(old.status, 409); assert.match(old.body.error, /unsupported client schema/);
    const beside = await call(f.socket, '/v1/documents/p/propose', { ...request, id: 'beside', text: 'Ship under and red by X for .' }, agent);
    assert.equal(beside.status, 409); assert.equal(beside.body.code, 'ambiguous_inline_atom');
    const proposed = await call(f.socket, '/v1/documents/p/propose', request, agent);
    assert.equal(proposed.status, 200); assert.equal(model.commits, 0);
    assert.deepEqual(sent.map(data => data.schema), [5], 'Go is told the schema the proof was made at');
    // A schema 4 proposal on this schema 5 document can be rejected, never accepted.
    proposals.set('legacy', { ...proposals.get('p5'), id: 'legacy', schema: 4 });
    const legacy = await call(f.socket, '/v1/documents/p/proposal-accept', { id: 'accept-legacy', proposal: 'legacy', ifRev: state.rev });
    assert.equal(legacy.status, 409); assert.equal(model.commits, 0);
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-reject', { id: 'reject-legacy', proposal: 'legacy', ifRev: state.rev })).status, 200);
    const accepted = await call(f.socket, '/v1/documents/p/proposal-accept', { id: 'accept-1', proposal: 'p5', ifRev: state.rev });
    assert.equal(accepted.status, 200); assert.equal(model.commits, 1);
    assert.equal(model.seen.find(entry => entry.op === 'commit').data.schema, 5);
    const after = (await read(f)).body, content = after.content.content[0].content;
    assert.equal(after.schema, 5);
    assert.deepEqual(after.content.content[0], proposals.get('p5').proposed_content, 'the accepted block is exactly the reviewed proposal');
    assert.deepEqual(content.slice(1), [{ type: 'text', text: 'under', marks: [under] }, { type: 'text', text: ' and ' }, { type: 'text', text: 'red', marks: [red, yellow] }, { type: 'text', text: ' by ' }, { type: 'date', attrs: { value: '2026-12-01' } }, { type: 'text', text: ' for ' }, { type: 'mention', attrs: { userId: 'u1' } }, { type: 'text', text: '.' }]);
    assert.deepEqual(after.content.content[1], body.content[1]);
  } finally { await f.close(); }
});

test('prepare-create builds at the schema Go names and refuses one without drafting (#5233)', async () => {
  const model=authorityModel(), f=await fixture(model.handle); await f.start();
  const prepare=body=>call(f.socket,'/v1/documents/new/prepare-create',body,{Authorization:'Bearer agtv2.synthetic-creation'});
  try {
    for (const schema of [3,4,5]) { const result=await prepare({schema,blocks:[{type:'paragraph',text:'Hello'}]}); assert.equal(result.status,200); assert.equal(result.body.schema,schema); const doc=load(result.body.snapshot); try { validate(doc,schema); } finally { doc.destroy(); } }
    for (const schema of [1,2,7,'5']) assert.equal((await prepare({schema,blocks:[{type:'paragraph',text:'x'}]})).status,409,`schema ${schema}`);
    assert.equal((await prepare({schema:5,blocks:[],extra:1})).status,400);
  } finally { await f.close(); }
});

test('schema4 proposals go stale when their target changes and are rejected without a content write', async () => {
  const model = authorityModel(), proposals = new Map();
  const doc = create({ type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'a' }, content: [{ type: 'text', text: 'Alpha' }] }, { type: 'paragraph', attrs: { id: 'b' }, content: [{ type: 'text', text: 'Beta' }] }] }, 4);
  model.state = { ...model.state, schema: 4, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'propose') { proposals.set(data.id, { id: data.id, actor: 'agent:a:r', target: data.target, base_hash: data.ifHashes[data.target], text: data.semantic.text, generation: data.generation, base_rev: data.ifRev, status: 'open', created_at: 'now' }); return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, kind: 'proposal' }; }
    if (op === 'proposals') return [...proposals.values()];
    if (op === 'proposal') return proposals.get(data.id);
    if (op === 'proposal-reject') { proposals.get(data.proposal).status = 'rejected'; return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, kind: 'proposal-reject' }; }
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const agent = { Authorization: 'Bearer agtv2.synthetic-run' }, state = (await read(f)).body;
    const hash = id => state.outline.find(b => b.id === id).hash;
    for (const [id, target] of [['p-a', 'a'], ['p-b', 'b']]) assert.equal((await call(f.socket, '/v1/documents/p/propose', { id, schema: 4, page_id: 'p', epoch: state.epoch, ifRev: state.rev, target, ifHash: hash(target), text: target + ' changed' }, agent)).status, 200);
    // A human edits block a directly: the proposal on a is stale, b stays open.
    const human = await call(f.socket, '/v1/documents/p/patch', { id: 'human-edit', schema: 4, page_id: 'p', epoch: state.epoch, ifRev: state.rev, target: 'a', ifHash: hash('a'), text: 'Alpha by hand' });
    assert.equal(human.status, 200);
    const now = (await read(f)).body;
    const listed = Object.fromEntries((await call(f.socket, '/v1/documents/p/proposals', {})).body.proposals.map(p => [p.id, p.status]));
    assert.deepEqual(listed, { 'p-a': 'stale', 'p-b': 'open' });
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', { id: 'accept-a', proposal: 'p-a', ifRev: now.rev })).status, 409);
    const commits = model.commits;
    // An engine restart moved the generation: p-b can no longer be accepted, but it can still be closed.
    proposals.get('p-b').generation = -1;
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', { id: 'accept-b', proposal: 'p-b', ifRev: now.rev })).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-reject', { id: 'reject-b', proposal: 'p-b', ifRev: now.rev })).status, 200);
    assert.equal(model.commits, commits); assert.equal(proposals.get('p-b').status, 'rejected');
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-reject', { id: 'reject-b2', proposal: 'p-b', ifRev: now.rev })).status, 409);
  } finally { await f.close(); }
});

test('schema3 documents refuse proposals; agent patch and checkpoint paths are unchanged', async () => {
  const model = authorityModel();
  const doc = create({ type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'a' }, content: [{ type: 'text', text: 'Alpha' }] }] }, 3);
  model.state = { ...model.state, schema: 3, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(model.handle); await f.start();
  try {
    const state = (await read(f)).body, hash = state.outline[0].hash;
    const agent = { Authorization: 'Bearer agtv2.synthetic-run' };
    assert.equal((await call(f.socket, '/v1/documents/p/propose', { id: 'p3', schema: 3, page_id: 'p', epoch: state.epoch, ifRev: state.rev, target: 'a', ifHash: hash, text: 'x' }, agent)).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-accept', { id: 'acc3', proposal: 'p3', ifRev: state.rev })).status, 409);
    assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'd3', schema: 3, page_id: 'p', epoch: state.epoch, ifRev: state.rev, target: 'a', ifHash: hash, text: 'Alpha two' }, agent)).status, 200);
    assert.equal(model.commits, 1);
  } finally { await f.close(); }
});

// Schema 6: the Canvas surface through the real engine write path.
const surface = createSurface({ Y });
const canvasModel = schema => {
  const model = authorityModel(), doc = create({ type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'intro' }, content: [{ type: 'text', text: 'Shared' }] }, { type: 'paragraph', attrs: { id: 'outro' }, content: [{ type: 'text', text: 'Last' }] }] }, schema);
  model.state = { ...model.state, schema, snapshot: snapshot(doc) }; doc.destroy(); return model;
};
const placeUpdate = (state, mutate) => { const doc = load(state.snapshot), vector = Y.encodeStateVector(doc); mutate(doc); const update = encode(Y.encodeStateAsUpdate(doc, vector)); doc.destroy(); return update; };

test('schema6: a placement commit is refused on schema 5, saved after the explicit human upgrade, and reads back after reload', async () => {
  const model = canvasModel(5), f = await fixture(model.handle); await f.start();
  try {
    const old = (await read(f)).body;
    const update = placeUpdate(old, doc => surface.place(doc, 'intro', { x: 40, y: 60, w: 320, h: 120, z: 1 }));
    assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'place-on-five', schema: 5, page_id: 'p', epoch: old.epoch, kind: 'human', update })).status, 400);
    assert.equal(model.commits, 0, 'schema 5 never stores a surface');
    const upgrade = { id: 'enable-canvas', schema: 5, target_schema: 6, page_id: 'p', epoch: old.epoch, ifRev: old.rev };
    assert.equal((await call(f.socket, '/v1/documents/p/upgrade', upgrade, { Authorization: 'Bearer agtv2.synthetic' })).status, 403, 'agents cannot enable Canvas');
    assert.equal((await call(f.socket, '/v1/documents/p/upgrade', upgrade)).status, 200);
    const six = (await read(f)).body; assert.equal(six.schema, 6); assert.deepEqual(six.content, old.content); assert.deepEqual(six.outline, old.outline);
    const placed = placeUpdate(six, doc => surface.place(doc, 'intro', { x: 40, y: 60, w: 320, h: 120, z: 1 }));
    assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'place-on-six', schema: 6, page_id: 'p', epoch: six.epoch, kind: 'human', update: placed })).status, 200);
    const reloaded = (await read(f)).body, doc = load(reloaded.snapshot);
    assert.deepEqual(surface.project(doc).placements, [{ id: 'intro', x: 40, y: 60, w: 320, h: 120, z: 1 }]);
    assert.deepEqual(reloaded.outline, six.outline, 'placing a block changes no block hash');
    doc.destroy();
    assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'old-editor', schema: 5, page_id: 'p', epoch: reloaded.epoch, kind: 'human', update: placed })).status, 409, 'an older editor cannot write over a Canvas document');
  } finally { await f.close(); }
});

test('schema6: an invalid surface value is refused and nothing is stored', async () => {
  const model = canvasModel(6), f = await fixture(model.handle); await f.start();
  try {
    const state = (await read(f)).body;
    const update = placeUpdate(state, doc => doc.getMap(surface.PLACEMENTS).set('intro', { x: 0, y: 0, w: 320, h: 120, z: 1, html: '<img>' }));
    assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'bad-surface', schema: 6, page_id: 'p', epoch: state.epoch, kind: 'human', update })).status, 400);
    assert.equal(model.commits, 0);
  } finally { await f.close(); }
});

test('schema6: copies that each stayed inside the Canvas budget are refused together once merged (server-side quota)', async () => {
  const model = canvasModel(6), f = await fixture(model.handle); await f.start();
  try {
    const base = (await read(f)).body;
    const a = load(base.snapshot), b = load(base.snapshot), vector = Y.encodeStateVector(a);
    surface.putItem(a, 'n', { kind: 'note', x: 0, y: 0, w: 200, h: 160, z: 0, tone: 'yellow', text: 'start' });
    Y.applyUpdate(b, Y.encodeStateAsUpdate(a, vector));
    const shared = Y.encodeStateVector(b);
    const big = i => 'q'.repeat(3990) + (i % 10);
    for (let i = 0; i < 70; i++) { surface.setText(a, 'n', big(i)); surface.setText(b, 'n', big(i + 3)); }
    const first = await call(f.socket, '/v1/documents/p/patch', { id: 'copy-a', schema: 6, page_id: 'p', epoch: base.epoch, kind: 'human', update: encode(Y.encodeStateAsUpdate(a, vector)) });
    assert.equal(first.status, 200);
    const stored = model.state.snapshot, commits = model.commits;
    const second = await call(f.socket, '/v1/documents/p/patch', { id: 'copy-b', schema: 6, page_id: 'p', epoch: base.epoch, kind: 'human', update: encode(Y.encodeStateAsUpdate(b, shared)) });
    assert.equal(second.status, 413); assert.equal(second.body.code, 'surface_quota'); assert.match(second.body.error, /Canvas history is full/);
    assert.equal(model.commits, commits); assert.equal(model.state.snapshot, stored, 'the stored document stays valid and unchanged');
    validate(load(model.state.snapshot), 6);
    a.destroy(); b.destroy();
  } finally { await f.close(); }
});

test('schema6: after Canvas is enabled an agent proposes on a placed block; accept keeps formatting and geometry, reject writes nothing (#5149)', async () => {
  const model = authorityModel(), proposals = new Map(), sent = [];
  const under = { type: 'underline', attrs: {} }, red = { type: 'textColor', attrs: { color: 'red' } }, bold = { type: 'bold', attrs: {} };
  const body = { type: 'doc', content: [
    { type: 'paragraph', attrs: { id: 'lead' }, content: [{ type: 'text', text: 'Ship ' }, { type: 'text', text: 'under', marks: [under] }, { type: 'text', text: ' and ' }, { type: 'text', text: 'red', marks: [red, bold] }, { type: 'text', text: ' by ' }, { type: 'date', attrs: { value: '2026-12-01' } }, { type: 'text', text: '.' }] },
    { type: 'paragraph', attrs: { id: 'other' }, content: [{ type: 'text', text: 'Other' }] },
  ] };
  const doc = create(body, 5); model.state = { ...model.state, schema: 5, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'propose') {
      sent.push(data);
      const find = (node, id) => node.attrs?.id === id ? node : (node.content ?? []).map(child => find(child, id)).find(Boolean);
      proposals.set(data.id, { id: data.id, actor: 'agent:a:r', run_id: 'r', target: data.target, schema: data.schema, base_hash: data.ifHashes[data.target], text: data.semantic.text, generation: data.generation, base_rev: data.ifRev, status: 'open', created_at: 'now', proposed_content: find(data.semantic.rich.after, data.target) });
      return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, actor: 'agent:a:r', kind: 'proposal' };
    }
    if (op === 'proposal') return proposals.get(data.id);
    if (op === 'proposal-reject') { proposals.get(data.proposal).status = 'rejected'; return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, kind: 'proposal-reject' }; }
    if (op === 'commit' && data.proposal) proposals.get(data.proposal).status = 'accepted';
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const agent = { Authorization: 'Bearer agtv2.synthetic-run' };
    const five = (await read(f)).body;
    assert.equal((await call(f.socket, '/v1/documents/p/upgrade', { id: 'enable-canvas', schema: 5, target_schema: 6, page_id: 'p', epoch: five.epoch, ifRev: five.rev })).status, 200);
    const six = (await read(f)).body; assert.equal(six.schema, 6); assert.deepEqual(six.content, five.content, 'enabling Canvas keeps the Page exactly');
    const geometry = { lead: { x: 40, y: 60, w: 320, h: 120, z: 1 }, other: { x: 480, y: 60, w: 240, h: 96, z: 2 } };
    const placed = placeUpdate(six, d => { surface.place(d, 'lead', geometry.lead); surface.place(d, 'other', geometry.other); });
    assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'place-both', schema: 6, page_id: 'p', epoch: six.epoch, kind: 'human', update: placed })).status, 200);
    const state = (await read(f)).body, lead = state.outline.find(b => b.id === 'lead');
    const request = { id: 'p6', schema: 6, page_id: 'p', epoch: state.epoch, ifRev: state.rev, target: 'lead', ifHash: lead.hash, text: 'Deliver under and red by .' };
    assert.equal((await call(f.socket, '/v1/documents/p/propose', { ...request, id: 'p5', schema: 5 }, agent)).status, 409, 'a proposal made at the old schema is refused');
    const proposed = await call(f.socket, '/v1/documents/p/propose', request, agent);
    assert.equal(proposed.status, 200, JSON.stringify(proposed.body)); assert.deepEqual(sent.map(d => d.schema), [6]);
    const commits = model.commits;
    const accepted = await call(f.socket, '/v1/documents/p/proposal-accept', { id: 'accept-6', proposal: 'p6', ifRev: state.rev });
    assert.equal(accepted.status, 200, JSON.stringify(accepted.body)); assert.equal(model.commits, commits + 1);
    const after = (await read(f)).body, d = load(after.snapshot);
    try {
      assert.equal(after.schema, 6);
      assert.deepEqual(after.content.content[0].content, [{ type: 'text', text: 'Deliver ' }, { type: 'text', text: 'under', marks: [under] }, { type: 'text', text: ' and ' }, { type: 'text', text: 'red', marks: [bold, red] }, { type: 'text', text: ' by ' }, { type: 'date', attrs: { value: '2026-12-01' } }, { type: 'text', text: '.' }], 'marks come back in schema order');
      assert.deepEqual(after.content.content[1], body.content[1]);
      assert.deepEqual(surface.project(d).placements, [{ id: 'lead', ...geometry.lead }, { id: 'other', ...geometry.other }], 'accepting a proposal keeps every placement');
      validate(d, 6);
    } finally { d.destroy(); }
    const otherHash = after.outline.find(b => b.id === 'other').hash;
    assert.equal((await call(f.socket, '/v1/documents/p/propose', { ...request, id: 'p6b', epoch: after.epoch, ifRev: after.rev, target: 'other', ifHash: otherHash, text: 'Rejected' }, agent)).status, 200);
    const before = model.commits, snap = model.state.snapshot;
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-reject', { id: 'reject-6', proposal: 'p6b', ifRev: after.rev })).status, 200);
    assert.equal(model.commits, before); assert.equal(model.state.snapshot, snap, 'reject changes neither text nor geometry');
  } finally { await f.close(); }
});

test('schema6: a proposal made before Canvas was enabled can only be rejected afterwards; restoring a pre-Canvas version keeps schema 6 with an empty layout (#5149)', async () => {
  const model = authorityModel(), proposals = new Map();
  const body = { type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'lead' }, content: [{ type: 'text', text: 'Ship it.' }] }] };
  const doc = create(body, 5); model.state = { ...model.state, schema: 5, snapshot: snapshot(doc) }; doc.destroy();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'propose') { proposals.set(data.id, { id: data.id, actor: 'agent:a:r', run_id: 'r', target: data.target, schema: data.schema, base_hash: data.ifHashes[data.target], text: data.semantic.text, generation: data.generation, base_rev: data.ifRev, status: 'open', created_at: 'now' }); return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, actor: 'agent:a:r', kind: 'proposal' }; }
    if (op === 'proposal') return proposals.get(data.id);
    if (op === 'proposal-reject') { proposals.get(data.proposal).status = 'rejected'; return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, kind: 'proposal-reject' }; }
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const agent = { Authorization: 'Bearer agtv2.synthetic-run' };
    const five = (await read(f)).body, lead = five.outline.find(b => b.id === 'lead');
    assert.equal((await call(f.socket, '/v1/documents/p/checkpoint', { id: 'before-canvas', schema: 5, page_id: 'p', epoch: five.epoch, ifRev: five.rev })).status, 200);
    assert.equal((await call(f.socket, '/v1/documents/p/propose', { id: 'old', schema: 5, page_id: 'p', epoch: five.epoch, ifRev: five.rev, target: 'lead', ifHash: lead.hash, text: 'Ship it now.' }, agent)).status, 200);
    assert.equal((await call(f.socket, '/v1/documents/p/upgrade', { id: 'enable-canvas', schema: 5, target_schema: 6, page_id: 'p', epoch: five.epoch, ifRev: five.rev })).status, 200);
    const six = (await read(f)).body; assert.equal(six.schema, 6);
    const commits = model.commits;
    const stale = await call(f.socket, '/v1/documents/p/proposal-accept', { id: 'accept-old', proposal: 'old', ifRev: six.rev });
    assert.equal(stale.status, 409, 'a pre-Canvas proposal is never accepted at 6'); assert.match(stale.body.error, /proposal is no longer open/);
    assert.equal(model.commits, commits);
    assert.equal((await call(f.socket, '/v1/documents/p/proposal-reject', { id: 'reject-old', proposal: 'old', ifRev: six.rev })).status, 200, 'but it can always be closed');
    const placed = placeUpdate(six, d => surface.place(d, 'lead', { x: 40, y: 60, w: 320, h: 120, z: 1 }));
    assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'place', schema: 6, page_id: 'p', epoch: six.epoch, kind: 'human', update: placed })).status, 200);
    const head = (await read(f)).body;
    const restored = await call(f.socket, '/v1/documents/p/restore', { id: 'restore-before-canvas', schema: 6, page_id: 'p', epoch: head.epoch, ifRev: head.rev, checkpoint: 'before-canvas' });
    assert.equal(restored.status, 200, JSON.stringify(restored.body)); assert.equal(restored.body.schema, 6);
    const back = (await read(f)).body, d = load(back.snapshot);
    try {
      assert.equal(back.schema, 6, 'Canvas stays enabled'); assert.deepEqual(back.content, five.content);
      assert.deepEqual(surface.project(d).placements, [], 'the pre-Canvas version has no layout');
      validate(d, 6);
    } finally { d.destroy(); }
  } finally { await f.close(); }
});

test('schema6: schema 4 and schema 5 history restored on a Canvas document is converted like Enable Canvas: content kept, valid at 6, editable, placeable and open to agent proposals (#5149)', async () => {
  const model = authorityModel(), proposals = new Map();
  const bold = { type: 'bold', attrs: {} }, red = { type: 'textColor', attrs: { color: 'red' } };
  const four = { type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'lead' }, content: [{ type: 'text', text: 'Ship ' }, { type: 'text', text: 'it', marks: [bold] }, { type: 'text', text: '.' }] }, { type: 'paragraph', attrs: { id: 'note' }, content: [{ type: 'text', text: 'Notes' }] }] };
  const five = { type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'lead' }, content: [{ type: 'text', text: 'Ship ' }, { type: 'text', text: 'it', marks: [bold, red] }, { type: 'text', text: ' by ' }, { type: 'date', attrs: { value: '2026-12-01' } }, { type: 'text', text: ' with ' }, { type: 'mention', attrs: { userId: 'ada' } }] }, { type: 'paragraph', attrs: { id: 'note' }, content: [{ type: 'text', text: 'Notes' }] }] };
  const doc4 = create(four, 4); model.state = { ...model.state, schema: 4, snapshot: snapshot(doc4) }; doc4.destroy();
  const f = await fixture(async (op, data, req, res) => {
    if (op === 'propose') { proposals.set(data.id, { id: data.id, schema: data.schema }); return { id: data.id, rev: data.ifRev, epoch: model.state.epoch, actor: 'agent:a:r', kind: 'proposal' }; }
    return model.handle(op, data, req, res);
  });
  await f.start();
  try {
    const agent = { Authorization: 'Bearer agtv2.synthetic-run' };
    const at4 = (await read(f)).body; assert.equal(at4.schema, 4);
    assert.equal((await call(f.socket, '/v1/documents/p/checkpoint', { id: 'four', schema: 4, page_id: 'p', epoch: at4.epoch, ifRev: at4.rev })).status, 200);
    // The Document reached schema 5 while 5 was the latest schema, with dates, mentions and colour.
    const doc5 = create(five, 5); model.state = { ...model.state, schema: 5, rev: model.state.rev + 1, snapshot: snapshot(doc5) }; doc5.destroy();
    const at5 = (await read(f)).body; assert.equal(at5.schema, 5);
    assert.equal((await call(f.socket, '/v1/documents/p/checkpoint', { id: 'five', schema: 5, page_id: 'p', epoch: at5.epoch, ifRev: at5.rev })).status, 200);
    assert.equal((await call(f.socket, '/v1/documents/p/upgrade', { id: 'enable-canvas', schema: 5, target_schema: 6, page_id: 'p', epoch: at5.epoch, ifRev: at5.rev })).status, 200);
    const geometry = { x: 40, y: 60, w: 320, h: 120, z: 1 };
    for (const [checkpoint, source] of [['four', at4], ['five', at5]]) {
      // The head has a layout before each restore (placed now, or after the previous restore).
      const head = (await read(f)).body; assert.equal(head.schema, 6);
      if (checkpoint === 'four') assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: 'place-before', schema: 6, page_id: 'p', epoch: head.epoch, kind: 'human', update: placeUpdate(head, d => surface.place(d, 'lead', geometry)) })).status, 200);
      const placed = (await read(f)).body, layout = load(placed.snapshot);
      try { assert.deepEqual(surface.project(layout).placements, [{ id: 'lead', ...geometry }]); } finally { layout.destroy(); }
      const restored = await call(f.socket, '/v1/documents/p/restore', { id: `restore-${checkpoint}`, schema: 6, page_id: 'p', epoch: placed.epoch, ifRev: placed.rev, checkpoint });
      assert.equal(restored.status, 200, JSON.stringify(restored.body)); assert.equal(restored.body.schema, 6);
      const back = (await read(f)).body, d = load(back.snapshot);
      try {
        assert.equal(back.schema, 6, `schema ${source.schema} history keeps Canvas enabled`);
        assert.deepEqual(back.content, source.content, `schema ${source.schema} content, marks and atoms are kept`);
        assert.deepEqual(surface.project(d).placements, [], 'the older version has no layout');
        validate(d, 6); upgradeContent(d, source.schema, 6);
      } finally { d.destroy(); }
      // Still editable at 6: a block can be placed again, with valid geometry.
      assert.equal((await call(f.socket, '/v1/documents/p/patch', { id: `place-after-${checkpoint}`, schema: 6, page_id: 'p', epoch: back.epoch, kind: 'human', update: placeUpdate(back, d => surface.place(d, 'lead', geometry)) })).status, 200);
      const edited = (await read(f)).body, e = load(edited.snapshot);
      try { assert.deepEqual(surface.project(e).placements, [{ id: 'lead', ...geometry }]); validate(e, 6); } finally { e.destroy(); }
      // And agents may still propose at 6.
      assert.equal(schemaAllows(6, 'propose'), true);
      const target = edited.outline.find(b => b.id === 'note');
      const proposed = await call(f.socket, '/v1/documents/p/propose', { id: `propose-after-${checkpoint}`, schema: 6, page_id: 'p', epoch: edited.epoch, ifRev: edited.rev, target: 'note', ifHash: target.hash, text: 'Notes for today.' }, agent);
      assert.equal(proposed.status, 200, JSON.stringify(proposed.body)); assert.equal(proposals.get(`propose-after-${checkpoint}`).schema, 6);
    }
    // The conversion goes only upwards, between known schemas.
    const probe = create(five, 5);
    try {
      for (const [from, to] of [[6, 5], [5, 4], [7, 7], [5, 7]]) assert.throws(() => upgradeContent(probe, from, to), /unsupported schema conversion/);
      assert.ok(upgradeContent(probe, 5, 6));
    } finally { probe.destroy(); }
  } finally { await f.close(); }
});
