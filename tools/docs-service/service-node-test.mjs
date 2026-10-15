import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, writeFile, stat, chmod, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { startService } from './service.mjs';
import { create, snapshot, initialJSON } from './schema.mjs';
const secret = 'a'.repeat(48);
function call(socket, path, data, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: socket, path, method: data === undefined ? 'GET' : 'POST', headers: { 'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'workspace', Authorization: 'Bearer human', ...headers } }, res => { const chunks = []; res.on('data', c => chunks.push(c)); res.on('end', () => resolve({ status: res.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) })); });
    req.on('error', reject); req.end(data === undefined ? undefined : JSON.stringify(data));
  });
}
test('Unix service authenticates, validates and only acknowledges durable receipt', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'docs-service-')); const socket = join(dir, 'service.sock'); const authoritySocket = join(dir, 'authority.sock'); const tokenFile = join(dir, 'secret');
  await writeFile(tokenFile, secret, { mode: 0o600 });
  const doc = create(initialJSON()); let state = { schema: 1, rev: 1, epoch: 3, snapshot: snapshot(doc), updates: [] }; doc.destroy();
  let rejectCommit = false; let commits = 0; let receipt;
  const authority = http.createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/health/ready') return res.end(JSON.stringify({ protocol: 1, schema: 1 }));
    assert.equal(req.headers['x-crewship-docs-service'], secret); assert.equal(req.headers.authorization, 'Bearer human');
    const chunks = []; for await (const c of req) chunks.push(c); const data = JSON.parse(Buffer.concat(chunks).toString());
    const op = req.url.split('/').at(-1); let value = {};
    if (op === 'state') value = state;
    if (op === 'lease') { state = { ...state, epoch: state.epoch + 1 }; value = { token: 'lease', epoch: state.epoch }; }
    if (op === 'receipt') value = receipt?.fingerprint === data.fingerprint ? { found: true, rev: receipt.rev } : { found: false };
    if (op === 'commit') {
      if (rejectCommit) { res.writeHead(403); return res.end(JSON.stringify({ error: 'revoked' })); }
      assert.equal(data.epoch, state.epoch); assert.equal(data.leaseOwner.length, 36); assert.ok(data.update); commits++;
      state = { ...state, rev: state.rev + 1, snapshot: data.snapshot }; receipt = { ...data, rev: state.rev }; value = { rev: state.rev, epoch: state.epoch };
    }
    res.end(JSON.stringify(value));
  });
  await new Promise(resolve => authority.listen(authoritySocket, resolve));
  const service = await startService({ socket, authoritySocket, tokenFile, idleMs: 10 });
  try {
    assert.equal((await stat(socket)).mode & 0o777, 0o600);
    assert.equal((await call(socket, '/health/ready')).body.protocol, 1);
    assert.equal((await call(socket, '/health/live', undefined, { 'X-Crewship-Docs-Service': 'bad' })).status, 403);
    const initial = await call(socket, '/v1/documents/page/read', {}); const block = initial.body.outline.find(b => b.id === 'intro');
    const patch = { id: 'one', schema: 1, epoch: 3, target: 'intro', text: 'durable', ifRev: 1, ifHash: block.hash };
    rejectCommit = true; assert.equal((await call(socket, '/v1/documents/page/patch', patch)).status, 403); assert.equal(state.rev, 1);
    patch.epoch = state.epoch; rejectCommit = false; assert.equal((await call(socket, '/v1/documents/page/patch', patch)).body.rev, 2);
    assert.equal((await call(socket, '/v1/documents/page/patch', patch)).body.duplicate, true); assert.equal(commits, 1);
    assert.equal((await call(socket, '/drain', {})).status, 200);
    assert.equal((await call(socket, '/health/ready')).status, 503);
    assert.equal((await call(socket, '/v1/documents/page/read', {})).status, 503);
  } finally { await service.close(); await new Promise(resolve => authority.close(resolve)); await rm(dir, { recursive: true }); }
});
test('initialization bootstraps through owner authority without state or lease', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'docs-bootstrap-')); const socket = join(dir, 's'); const authoritySocket = join(dir, 'a'); const tokenFile = join(dir, 't'); await writeFile(tokenFile, secret, { mode: 0o600 });
  const seen = [];
  const authority = http.createServer(async (req, res) => { const chunks = []; for await (const c of req) chunks.push(c); const data = JSON.parse(Buffer.concat(chunks).toString()); seen.push(req.url); if (req.url.endsWith('/receipt')) { res.writeHead(404); return res.end(JSON.stringify({error:'not found'})); } assert.equal(data.schema, 1); assert.ok(data.snapshot); res.end(JSON.stringify({ rev: 1, epoch: 1 })); });
  await new Promise(resolve => authority.listen(authoritySocket, resolve));
  const service = await startService({ socket, authoritySocket, tokenFile });
  try {
    const response = await call(socket, '/v1/documents/new/initialize', { id: 'init', schema: 1, body: initialJSON() });
    assert.equal(response.status, 200); assert.deepEqual(seen, ['/internal/docs/v1/new/receipt', '/internal/docs/v1/new/initialize']);
  } finally { await service.close(); await new Promise(resolve => authority.close(resolve)); await rm(dir, { recursive: true }); }
});
test('startup rejects unsafe secrets and noncanonical socket paths', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'docs-config-')); const tokenFile = join(dir, 'secret');
  try {
    await writeFile(tokenFile, secret, { mode: 0o600 }); await chmod(tokenFile, 0o644);
    await assert.rejects(startService({ socket: join(dir, 's'), authoritySocket: join(dir, 'a'), tokenFile }), /mode-0600/);
    await assert.rejects(startService({ socket: 'relative.sock', authoritySocket: join(dir, 'a'), tokenFile }), /absolute Unix/);
    await assert.rejects(startService({ socket: dir + '/sub/../s', authoritySocket: join(dir, 'a'), tokenFile }), /absolute Unix/);
    await chmod(tokenFile, 0o600); await writeFile(tokenFile, 'x'.repeat(513));
    await assert.rejects(startService({ socket: join(dir,'s'), authoritySocket: join(dir,'a'), tokenFile }), /512 bytes/);
    await writeFile(tokenFile, 'x'.repeat(257));
    await assert.rejects(startService({ socket: join(dir,'s'), authoritySocket: join(dir,'a'), tokenFile }), /32..256/);
    const link=join(dir,'link'); await symlink(tokenFile,link);
    await assert.rejects(startService({ socket: join(dir,'s'), authoritySocket: join(dir,'a'), tokenFile:link }), /regular mode-0600/);
    await assert.rejects(startService({ socket: tokenFile, authoritySocket: join(dir,'a'), tokenFile }), /distinct/);
  } finally { await rm(dir, { recursive: true }); }
});
test('checkpoint and restore retries return original durable receipt; authority readiness is live', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'docs-retry-')); const socket = join(dir,'s'); const authoritySocket = join(dir,'a'); const tokenFile = join(dir,'t'); await writeFile(tokenFile,secret,{mode:0o600});
  const doc = create(initialJSON()); let state = {schema:1,rev:1,epoch:1,snapshot:snapshot(doc)}; doc.destroy();
  const receipts = new Map(); let checkpoints = 0; let restores = 0; let compatible = true;
  const authority = http.createServer(async (req,res) => {
    if (req.method==='GET') return res.end(JSON.stringify({protocol:compatible?1:99,schema:1}));
    const chunks=[];for await(const c of req) chunks.push(c);const data=JSON.parse(Buffer.concat(chunks).toString());const op=req.url.split('/').at(-1);let value={};
    if(op==='state') value=state;
    if(op==='receipt') value=receipts.has(data.id)?{...receipts.get(data.id),found:true}:{found:false};
    if(op==='lease'){state={...state,epoch:state.epoch+1};value={token:'lease',epoch:state.epoch};}
    if(op==='checkpoint-read')value={schema:1,snapshot:state.snapshot};
    if(op==='checkpoint'){assert.equal(data.epoch,state.epoch);checkpoints++;value={id:data.id,rev:state.rev,epoch:state.epoch};receipts.set(data.id,value);}
    if(op==='restore'){assert.equal(data.epoch,state.epoch);assert.equal(data.checkpoint,'cp');restores++;state={...state,rev:state.rev+1,epoch:state.epoch+1};value={id:data.id,rev:state.rev,epoch:state.epoch};receipts.set(data.id,value);}
    res.end(JSON.stringify(value));
  });await new Promise(resolve=>authority.listen(authoritySocket,resolve));const service=await startService({socket,authoritySocket,tokenFile});
  try {
    const cp={id:'cp',schema:1,epoch:1,ifRev:1};assert.equal((await call(socket,'/v1/documents/p/checkpoint',cp)).status,200);assert.equal((await call(socket,'/v1/documents/p/checkpoint',cp)).body.duplicate,true);
    const restore={id:'restore',schema:1,epoch:state.epoch,ifRev:1,checkpoint:'cp'};assert.equal((await call(socket,'/v1/documents/p/restore',restore)).status,200);assert.equal((await call(socket,'/v1/documents/p/restore',restore)).body.duplicate,true);assert.equal(checkpoints,1);assert.equal(restores,1);
    compatible=false;assert.equal((await call(socket,'/health/ready')).status,503);compatible=true;assert.equal((await call(socket,'/health/ready')).status,200);
    await new Promise(resolve=>authority.close(resolve));assert.equal((await call(socket,'/health/ready')).status,503);
  }finally{await service.close();await new Promise(resolve=>authority.close(resolve));await rm(dir,{recursive:true});}
});
