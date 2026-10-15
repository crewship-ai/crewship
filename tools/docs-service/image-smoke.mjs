// Qualify the built image's private transport and lifecycle. No database/model access.
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, writeFile, chmod, stat, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { create, initialJSON, snapshot } from './schema.mjs';

const image = process.env.DOCS_IMAGE;
assert.ok(image, 'DOCS_IMAGE must name the locally built exact-head image');
const dir = await mkdtemp(join(tmpdir(), 'docs-image-'));
const name = `docs-smoke-${process.pid}`;
const secret = randomBytes(32).toString('hex');
const socket = join(dir, 'service.sock');
const authoritySocket = join(dir, 'authority.sock');
const tokenFile = join(dir, 'token');
const doc = create(initialJSON());
const state = { schema: 1, rev: 1, epoch: 1, snapshot: snapshot(doc), updates: [] };
doc.destroy();
let available = true;
const authority = http.createServer(async (req, res) => {
  assert.equal(req.headers['x-crewship-docs-service'], secret);
  for await (const chunk of req) void chunk;
  if (!available) { res.writeHead(503); return res.end(JSON.stringify({ error: 'controlled unavailable authority' })); }
  if (req.method === 'GET') return res.end(JSON.stringify({ protocol: 1, schema: 1 }));
  assert.equal(req.headers.authorization, 'Bearer smoke');
  assert.equal(req.headers['x-workspace-id'], 'smoke');
  assert.ok(req.url.endsWith('/state'));
  res.end(JSON.stringify(state));
});
const docker = (...args) => execFileSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
function call(path, data) {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: socket, path, method: data ? 'POST' : 'GET', headers: {
      'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': 'smoke', Authorization: 'Bearer smoke',
    } }, res => { const chunks = []; res.on('data', chunk => chunks.push(chunk)); res.on('end', () => resolve({ status: res.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) })); });
    req.on('error', reject); req.end(data ? JSON.stringify(data) : undefined);
  });
}
async function waitReady() {
  for (let i = 0; i < 100; i++) {
    try { if ((await call('/health/ready')).status === 200) return; } catch { /* startup */ }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('image did not become ready within 10 seconds');
}
try {
  await chmod(dir, 0o700); await writeFile(tokenFile, secret, { mode: 0o600 });
  await new Promise(resolve => authority.listen(authoritySocket, resolve)); await chmod(authoritySocket, 0o600);
  docker('run', '-d', '--name', name, '--network', 'none', '--read-only', '--tmpfs', '/tmp:size=16m,mode=1777',
    '--memory', '512m', '--cpus', '1', '--pids-limit', '64', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
    '--user', `${process.getuid()}:${process.getgid()}`, '--mount', `type=bind,src=${dir},dst=/run/docs`,
    '-e', 'CREWSHIP_DOCS_SOCKET=/run/docs/service.sock', '-e', 'CREWSHIP_DOCS_AUTHORITY_SOCKET=/run/docs/authority.sock',
    '-e', 'CREWSHIP_DOCS_SERVICE_TOKEN_FILE=/run/docs/token', image);
  await waitReady();
  assert.equal((await stat(socket)).mode & 0o777, 0o600);
  assert.equal((await call('/v1/documents/p/read', {})).body.rev, 1);
  docker('exec', name, 'node', 'healthcheck.mjs');
  available = false; assert.equal((await call('/health/ready')).status, 503);
  available = true; assert.equal((await call('/health/ready')).status, 200);
  assert.equal((await call('/drain', {})).status, 200);
  assert.equal((await call('/health/ready')).status, 503);
  assert.equal((await call('/v1/documents/p/read', {})).status, 503);
  docker('stop', '--time', '35', name);
  assert.equal(docker('inspect', '--format', '{{.State.ExitCode}}', name), '0');
  docker('start', name); await waitReady();
  assert.equal((await call('/v1/documents/p/read', {})).body.rev, 1);
  console.log(JSON.stringify({ image: docker('inspect', '--format', '{{.Image}}', name), socketMode: '0600', readinessFailure: true, drain: true, gracefulRestart: true, authority: 'controlled fixture, not Go persistence' }));
} finally {
  try { docker('rm', '-f', name); } catch { /* absent after failed creation */ }
  await new Promise(resolve => authority.close(resolve)); await rm(dir, { recursive: true });
}
