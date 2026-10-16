// Test harness capabilities only. Production Docs transport does not import this.
import { constants } from 'node:fs';
import { open, mkdtemp, chmod, lstat, realpath } from 'node:fs/promises';
import { resolve, dirname, basename, join, isAbsolute } from 'node:path';

export function loopbackOrigin(value) {
  if (typeof value !== 'string' || !/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(value)) throw new Error('numeric loopback fixture origin required');
  const port = Number(value.slice(value.lastIndexOf(':') + 1));
  if (port > 65535) throw new Error('invalid fixture port');
  return `http://127.0.0.1:${port}`;
}
export function fixtureURL(origin, path) {
  const base = loopbackOrigin(origin);
  if (typeof path !== 'string' || !path.startsWith('/') || path.startsWith('//') || /[\\\r\n]/.test(path)) throw new Error('local fixture path required');
  const url = new URL(path, base);
  if (url.origin !== base) throw new Error('fixture origin changed');
  return url.href;
}
export function fixtureWebSocketURL(origin) {
  const port = Number(new URL(loopbackOrigin(origin)).port);
  return `ws://127.0.0.1:${port}/ws`;
}
export function fixtureFetch(origin, path, options = {}) {
  return fetch(fixtureURL(origin, path), { ...options, redirect: 'error' });
}
export function parseFixture(raw) {
  let fixture;
  try { fixture = JSON.parse(raw); } catch { throw new Error('invalid fixture JSON'); }
  return validateFixture(fixture);
}
export function validateFixture(fixture) {
  const result = { ...fixture, authority: loopbackOrigin(fixture.authority) };
  for (const key of ['owner', 'editor', 'agent', 'service']) {
    if (typeof result[key] !== 'string' || !/^[\x21-\x7e]{1,8192}$/.test(result[key])) throw new Error(`invalid fixture ${key}`);
  }
  for (const key of ['workspace', 'page']) {
    if (typeof result[key] !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(result[key])) throw new Error(`invalid fixture ${key}`);
  }
  return result;
}
export async function canonicalDirectory(path, privateOnly = false) {
  if (!isAbsolute(path) || resolve(path) !== path || await realpath(path) !== path) throw new Error('canonical evidence directory required');
  const stat = await lstat(path);
  if (!stat.isDirectory() || stat.isSymbolicLink() || (privateOnly && ((stat.mode & 0o777) !== 0o700 || stat.uid !== process.getuid()))) throw new Error('owned private fixture directory required');
}
export async function evidenceRun(requestedPrefix) {
  if (!isAbsolute(requestedPrefix)) throw new Error('absolute evidence prefix required');
  const prefix = resolve(requestedPrefix);
  await canonicalDirectory(dirname(prefix));
  const directory = await mkdtemp(prefix + '-');
  await chmod(directory, 0o700);
  await canonicalDirectory(directory, true);
  return directory;
}
export async function fixtureSockets(options) {
  const directory = options.directory;
  await canonicalDirectory(directory, true);
  for (const [key, leaf] of [['authority', 'authority.sock'], ['service', 'service.sock'], ['token', 'token']]) {
    if (options[key] !== join(directory, leaf) || Buffer.byteLength(options[key]) > 100) throw new Error('fixture path outside owned directory');
  }
  const token = await lstat(options.token);
  if (!token.isFile() || token.isSymbolicLink() || (token.mode & 0o777) !== 0o600 || token.uid !== process.getuid() || token.size > 512) throw new Error('unsafe fixture secret');
  return options;
}
export async function writeEvidence(directory, leaf, data, limit = 8 * 1024 * 1024) {
  await canonicalDirectory(directory, true);
  if (!leaf || basename(leaf) !== leaf || leaf === '.' || leaf === '..') throw new Error('evidence filename required');
  if (Buffer.byteLength(data) > limit) throw new Error('evidence byte ceiling exceeded');
  const file = await open(join(directory, leaf), constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try {
    if (!(await file.stat()).isFile()) throw new Error('regular evidence file required');
    await file.writeFile(data);
  } finally { await file.close(); }
}
export function redactFixture(value, secrets = []) {
  let text = value;
  for (const secret of secrets) if (secret) text = text.replaceAll(secret, '[redacted]');
  return text.replace(/Bearer [^\s"\\]+/g, 'Bearer [redacted]');
}
export function fixtureLog(value, secrets, write = console.log) {
  write(redactFixture(value, secrets));
}
export function boundedLog(limit = 1024 * 1024) {
  const chunks = []; let retained = 0, seen = 0;
  return {
    push(chunk) {
      seen += chunk.length;
      const count = Math.min(chunk.length, limit - retained);
      if (count > 0) { chunks.push(Buffer.from(chunk.subarray(0, count))); retained += count; }
    },
    pushText(text) {
      const head = Buffer.from(text.slice(0, limit + 1));
      this.push(head);
      seen += Buffer.byteLength(text) - head.length;
    },
    finish(secrets = []) {
      const bytes = Buffer.concat(chunks, retained);
      // A truncated last line could contain only a credential prefix. Drop the
      // whole incomplete line before redaction, rather than persisting a prefix.
      const end = seen > retained ? bytes.lastIndexOf(10) + 1 : retained;
      const text = redactFixture(bytes.subarray(0, end).toString('utf8'), secrets);
      return { text, retainedInputBytes: end, droppedInputBytes: seen - end, truncated: seen > end };
    },
  };
}
