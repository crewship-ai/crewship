import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { loopbackOrigin, fixtureURL, fixtureFetch, evidenceRun, fixtureSockets, writeEvidence, boundedLog, validateFixture, parseFixture, fixtureLog } from './fixture-boundaries.mjs';

async function temporary(t) {
  const dir = await mkdtemp(join(tmpdir(), 'docs-boundary-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  return dir;
}
test('fixture network recipients require canonical numeric loopback origins', () => {
  assert.equal(loopbackOrigin('http://127.0.0.1:12345'), 'http://127.0.0.1:12345');
  for (const bad of ['https://127.0.0.1:80', 'http://localhost:80', 'http://127.1:80', 'http://[::1]:80', 'http://example.test:80', 'http://127.0.0.1:65536', 'http://127.0.0.1:0', 'http://x@127.0.0.1:80', 'http://127.0.0.1:80/', 'http://127.0.0.1:80?q=1']) assert.throws(() => loopbackOrigin(bad));
  for (const path of ['//example.test', '/\\example.test', 'http://example.test', '/a\r\nX: y']) assert.throws(() => fixtureURL('http://127.0.0.1:12345', path));
  assert.equal(fixtureURL('http://127.0.0.1:80', '/state'), 'http://127.0.0.1/state');
  assert.equal(fixtureURL('http://127.0.0.1:65535', '/state'), 'http://127.0.0.1:65535/state');
  assert.equal(fixtureURL('http://127.0.0.1:12345', '/state'), 'http://127.0.0.1:12345/state');
});
test('redirect refusal prevents fixture credentials reaching a redirected recipient', async t => {
  let received = 0;
  const target = http.createServer((req, res) => { received++; res.end('unexpected'); });
  await new Promise(resolve => target.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => target.close(resolve)));
  const redirect = http.createServer((req, res) => { res.writeHead(302, { Location: `http://127.0.0.1:${target.address().port}/capture` }); res.end(); });
  await new Promise(resolve => redirect.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => redirect.close(resolve)));
  const origin = `http://127.0.0.1:${redirect.address().port}`;
  await assert.rejects(fixtureFetch(origin, '/state', { redirect: 'follow', headers: { Authorization: 'Bearer synthetic-fixture-token' } }));
  assert.equal(received, 0);
});
test('fixture fields reject malformed primitive credentials and identifiers', () => {
  const f = { authority: 'http://127.0.0.1:1234', workspace: 'w', page: 'p', owner: 'owner', editor: 'editor', agent: 'agent', service: 'service' };
  assert.deepEqual(validateFixture(f), f);
  for (const [key, value] of [['owner', 'a\nb'], ['agent', ''], ['service', 'a'.repeat(8193)], ['page', '../p'], ['workspace', {}]]) assert.throws(() => validateFixture({ ...f, [key]: value }));
});
test('owned evidence runs preserve bytes and refuse overwrite, symlinks and oversized output', async t => {
  const dir = await temporary(t), run = await evidenceRun(join(dir, 'requested-report'));
  assert.equal((await stat(run)).mode & 0o777, 0o700);
  await writeEvidence(run, 'report.json', '{"status":"passed"}\n');
  assert.equal(await readFile(join(run, 'report.json'), 'utf8'), '{"status":"passed"}\n');
  assert.equal((await stat(join(run, 'report.json'))).mode & 0o777, 0o600);
  await assert.rejects(writeEvidence(run, 'report.json', 'overwrite'));
  const target = join(dir, 'target'); await writeFile(target, 'original');
  await symlink(target, join(run, 'linked'));
  await assert.rejects(writeEvidence(run, 'linked', 'changed'));
  await assert.rejects(writeEvidence(run, '../escape', 'changed'));
  await assert.rejects(writeEvidence(run, 'large', '12345', 4));
  assert.equal(await readFile(target, 'utf8'), 'original');
  await symlink(dir, join(dir, 'parent-link'));
  await assert.rejects(evidenceRun(join(dir, 'parent-link', 'escape')));
});
test('Unix fixture paths derive from the actual private directory and reject symlink components', async t => {
  const dir = await temporary(t);
  await writeFile(join(dir, 'token'), 'a'.repeat(32), { mode: 0o600 });
  const options = { directory: dir, authority: join(dir, 'authority.sock'), service: join(dir, 'service.sock'), token: join(dir, 'token') };
  await fixtureSockets(options);
  await assert.rejects(fixtureSockets({ ...options, service: join(tmpdir(), 'foreign.sock') }));
  const child = join(dir, 'child'); await mkdir(child, { mode: 0o700 });
  await symlink(join(dir, 'token'), join(child, 'token'));
  await assert.rejects(fixtureSockets({ directory: child, authority: join(child, 'authority.sock'), service: join(child, 'service.sock'), token: join(child, 'token') }));
  await symlink(dir, join(dir, 'alias'));
  const alias = join(dir, 'alias');
  await assert.rejects(fixtureSockets({ directory: alias, authority: join(alias, 'authority.sock'), service: join(alias, 'service.sock'), token: join(alias, 'token') }));
});
test('bounded log redaction discards a credential prefix straddling its collection boundary', () => {
  const secret = 'synthetic-long-secret';
  const prefix = 'safe first line\nAuthorization: Bearer ';
  const capture = boundedLog(Buffer.byteLength(prefix) + 8);
  capture.push(Buffer.from(prefix + secret.slice(0, 4)));
  capture.push(Buffer.from(secret.slice(4) + '\nlast line\n'));
  const result = capture.finish([secret]);
  assert.equal(result.text, 'safe first line\n');
  assert.ok(!result.text.includes(secret.slice(0, 8)));
  const bare = boundedLog(12); bare.push(Buffer.from('ok\n' + secret + '\n'));
  assert.equal(bare.finish([secret]).text, 'ok\n');
  assert.equal(result.retainedInputBytes, 16);
  assert.equal(result.droppedInputBytes, Buffer.byteLength('Authorization: Bearer ' + secret + '\nlast line\n'));
  assert.equal(result.truncated, true);
  const complete = boundedLog(512);
  complete.push(Buffer.from(`known ${secret}\nAuthorization: Bearer another-token\n`));
  const safe = complete.finish([secret]);
  assert.equal(safe.truncated, false);
  assert.ok(!safe.text.includes(secret)); assert.ok(!safe.text.includes('another-token'));
  const huge = boundedLog(16); huge.pushText('x'.repeat(100000));
  assert.deepEqual(huge.finish(), { text: '', retainedInputBytes: 0, droppedInputBytes: 100000, truncated: true });
});

test('fixture console and parse errors do not bypass report credential redaction', () => {
  const lines = [], secret = 'synthetic-private-secret';
  fixtureLog(JSON.stringify({ status: 'failed', failure: { message: `known ${secret}; Bearer another-fixture-token` } }), [secret], line => lines.push(line));
  assert.equal(lines.length, 1);
  assert.equal(JSON.parse(lines[0]).status, 'failed');
  assert.ok(!lines[0].includes(secret)); assert.ok(!lines[0].includes('another-fixture-token'));
  assert.throws(() => parseFixture(`{"owner":"${secret}`), error => error.message === 'invalid fixture JSON' && !error.message.includes(secret));
});
