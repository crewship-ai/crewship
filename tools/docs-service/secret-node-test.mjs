import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, mkdtemp, open, rename, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readSecretContents, readServiceSecret } from './secret.mjs';

const digest = value => createHash('sha256').update(value).digest('hex');
async function fixture(t) {
  const dir = await mkdtemp(join(tmpdir(), 'ds-secret-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  return { dir, path: join(dir, 'token') };
}
test('secret files reject mode, symlink, nonregular and bounded content violations', async t => {
  const { dir, path } = await fixture(t);
  for (const [content, mode, valid] of [['a'.repeat(32), 0o600, true], ['a'.repeat(256), 0o600, true], ['a'.repeat(32)+'\n', 0o600, true], ['a'.repeat(31), 0o600, false], ['a'.repeat(257), 0o600, false], [' '.repeat(513)+'a'.repeat(32), 0o600, false], ['a'.repeat(32), 0o644, false], ['a'.repeat(32), 0o640, false], ['a'.repeat(32), 0o700, false]]) {
    await writeFile(path, content); await chmod(path, mode);
    if (valid) assert.equal(digest(await readServiceSecret(path)), digest(content.trim()));
    else await assert.rejects(readServiceSecret(path));
  }
  await symlink(path, join(dir, 'link'));
  await assert.rejects(readServiceSecret(join(dir, 'link')));
  await assert.rejects(readServiceSecret(dir));
  await assert.rejects(readServiceSecret(join(dir, 'missing')));
  execFileSync('mkfifo', ['-m', '600', join(dir, 'fifo')]);
  await assert.rejects(readServiceSecret(join(dir, 'fifo')));
});
test('the opened descriptor keeps its original inode when the path is replaced', async t => {
  const { path } = await fixture(t), original = 'a'.repeat(32);
  await writeFile(path, original, { mode: 0o600 });
  const file = await open(path, 'r');
  try {
    await rename(path, path+'.original');
    await writeFile(path, 'b'.repeat(32), { mode: 0o644 });
    assert.equal((await file.stat()).mode & 0o777, 0o600);
    assert.equal(digest(await readSecretContents(file)), digest(original));
  } finally { await file.close(); }
});
test('growth after fstat is rejected after reading at most 513 bytes', async t => {
  const { path } = await fixture(t);
  await writeFile(path, 'a'.repeat(32), { mode: 0o600 });
  const file = await open(path, 'r');
  try {
    assert.equal((await file.stat()).size, 32);
    await writeFile(path, 'a'.repeat(4096));
    let read = 0;
    const reader = { read: async (...args) => { const result = await file.read(...args); read += result.bytesRead; return result; } };
    await assert.rejects(readSecretContents(reader));
    assert.equal(read, 513);
  } finally { await file.close(); }
});
