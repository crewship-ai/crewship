import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { createPrivateArtifacts, readPrivateJson } from './private-artifacts.mjs';

async function fixture(t) {
  const directory = await createPrivateArtifacts('security-test');
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const filename = path.join(directory, 'state.json');
  await fs.writeFile(filename, '{"actor":"synthetic"}', { mode: 0o600 });
  return { directory, filename };
}

test('reads owner-only regular state', async t => {
  const { filename } = await fixture(t);
  assert.deepEqual(await readPrivateJson(filename), { actor: 'synthetic' });
});

test('rejects symlink state without following it', async t => {
  const { directory, filename } = await fixture(t);
  const link = path.join(directory, 'link.json');
  await fs.symlink(filename, link);
  await assert.rejects(readPrivateJson(link), { code: 'ELOOP' });
});

test('rejects world-readable state and directories', async t => {
  const { directory, filename } = await fixture(t);
  await fs.chmod(filename, 0o644);
  await assert.rejects(readPrivateJson(filename), /private regular file/);
  await assert.rejects(readPrivateJson(directory), /private regular file/);
});

test('reads the validated inode even when its path is replaced after fstat', async t => {
  const { directory, filename } = await fixture(t);
  const open = fs.open.bind(fs);
  let closed = false;
  t.mock.method(fs, 'open', async (...args) => {
    const handle = await open(...args);
    const stat = handle.stat.bind(handle);
    const close = handle.close.bind(handle);
    handle.stat = async () => {
      const validated = await stat();
      await fs.rename(filename, path.join(directory, 'original.json'));
      await fs.writeFile(filename, '{"actor":"replacement"}', { mode: 0o600 });
      return validated;
    };
    handle.close = async () => { closed = true; return close(); };
    return handle;
  });
  assert.deepEqual(await readPrivateJson(filename), { actor: 'synthetic' });
  assert.equal(closed, true);
});

test('closes the descriptor on rejection without exposing malformed content', async t => {
  const { filename } = await fixture(t);
  await fs.writeFile(filename, 'synthetic-sensitive-sentinel');
  const open = fs.open.bind(fs);
  let closed = false;
  t.mock.method(fs, 'open', async (...args) => {
    const handle = await open(...args);
    const close = handle.close.bind(handle);
    handle.close = async () => { closed = true; return close(); };
    return handle;
  });
  await assert.rejects(readPrivateJson(filename), error => {
    assert.equal(error.message, 'State file contains invalid JSON');
    assert.ok(!error.message.includes('sentinel'));
    return true;
  });
  assert.equal(closed, true);
});

test('run directories are distinct and owner-only, even with a permissive umask', async t => {
  const previous = process.umask(0);
  try {
    const first = await createPrivateArtifacts('security-test');
    const second = await createPrivateArtifacts('security-test');
    t.after(() => Promise.all([first, second].map(dir => fs.rm(dir, { recursive: true, force: true }))));
    assert.notEqual(first, second);
    for (const directory of [first, second]) assert.equal((await fs.stat(directory)).mode & 0o777, 0o700);
    await assert.rejects(createPrivateArtifacts('../escape'), /Invalid artifact label/);
  } finally { process.umask(previous); }
});
