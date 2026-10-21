// Test-only process boundary: browser bytes cross the production Unix service.
// Go durability/auth is controlled here; this is not a Go authority acceptance.
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { startService, PROTOCOL } from './service.mjs';
import { load, validate } from './schema.mjs';

let input = ''; for await (const chunk of process.stdin) input += chunk;
const { before, update, schema = 2 } = JSON.parse(input);
const dir = await mkdtemp(join(tmpdir(), 'editor-service-'));
const socket = join(dir, 's'), authoritySocket = join(dir, 'a'), tokenFile = join(dir, 't');
const secret = 'editor-service-fixture-secret'.repeat(2);
await writeFile(tokenFile, secret, { mode: 0o600 });
let state = { schema, rev: 1, epoch: 1, generation: 0, incarnation: 'fixture', snapshot: before, updates: [] };
let commits = 0;
const authority = http.createServer(async (req, res) => {
  try {
    assert.equal(req.headers['x-crewship-docs-service'], secret);
    let text = ''; for await (const chunk of req) text += chunk;
    const data = JSON.parse(text || '{}');
    const op = req.url.split('/').at(-1);
    let result;
    switch (op) {
      case 'recover': state.generation++; state.epoch++; result = { recovered: true, protocol: PROTOCOL, schema: 3 }; break;
      case 'ready': result = { protocol: PROTOCOL, schema: 3 }; break;
      case 'state': result = state; break;
      case 'lease': result = { epoch: state.epoch, token: 'fixture-lease', expiresAt: new Date(Date.now() + 30000).toISOString() }; break;
      case 'renew': result = { expiresAt: new Date(Date.now() + 30000).toISOString() }; break;
      case 'receipt': result = { found: false }; break;
      case 'release': result = { released: true }; break;
      case 'commit': {
        assert.equal(data.ifRev, state.rev); assert.equal(data.kind, 'human');
        assert.equal(data.epoch, state.epoch); assert.equal(data.generation, state.generation);
        const doc = load(data.snapshot);
        try { validate(doc, schema); } finally { doc.destroy(); }
        state = { ...state, rev: state.rev + 1, snapshot: data.snapshot }; commits++;
        result = { id: data.id, rev: state.rev, epoch: state.epoch, fingerprint: data.fingerprint }; break;
      }
      default: throw new Error(`unexpected authority operation ${op}`);
    }
    res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(result));
  } catch (error) { res.writeHead(500); res.end(JSON.stringify({ error: error.message })); }
});
let service;
async function call(operation, body) {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: socket, path: `/v1/documents/p/${operation}`, method: 'POST', headers: {
      'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'workspace', Authorization: 'Bearer human',
    } }, res => {
      let text = ''; res.on('data', chunk => { text += chunk; });
      res.on('end', () => {
        try { assert.equal(res.statusCode, 200, text); resolve(JSON.parse(text)); } catch (error) { reject(error); }
      });
    });
    req.on('error', reject); req.end(JSON.stringify(body));
  });
}
try {
  await new Promise(resolve => authority.listen(authoritySocket, resolve));
  service = await startService({ socket, authoritySocket, tokenFile });
  const original = await call('read', {});
  await call('patch', { id: 'actual-editor-formatting', schema, ...(schema === 3 ? { page_id: 'p' } : {}), kind: 'human', update, ifRev: original.rev, epoch: original.epoch });
  const confirmed = await call('read', {});
  assert.equal(commits, 1); assert.equal(confirmed.rev, original.rev + 1);
  process.stdout.write(JSON.stringify(confirmed.content));
} finally {
  if (service) await service.close();
  await new Promise(resolve => authority.close(resolve));
  await rm(dir, { recursive: true, force: true });
}
