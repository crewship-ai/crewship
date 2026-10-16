import { constants } from 'node:fs';
import { open } from 'node:fs/promises';

export async function readSecretContents(file) {
  // Read at most one byte beyond the ceiling, including growth after fstat.
  const buffer = Buffer.alloc(513); let size = 0;
  while (size < buffer.length) {
    const { bytesRead } = await file.read(buffer, size, buffer.length - size, size);
    if (bytesRead === 0) break;
    size += bytesRead;
  }
  if (size > 512) throw new Error('service secret file exceeds 512 bytes');
  const secret = buffer.subarray(0, size).toString('utf8').trim();
  if (Buffer.byteLength(secret) < 32 || Buffer.byteLength(secret) > 256) throw new Error('service secret must contain 32..256 bytes');
  return secret;
}

export async function readServiceSecret(path) {
  if (typeof constants.O_NOFOLLOW !== 'number' || typeof constants.O_NONBLOCK !== 'number') throw new Error('Docs service secret files require Unix support');
  const file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const stat = await file.stat();
    if (!stat.isFile() || (stat.mode & 0o777) !== 0o600 || stat.size > 512) throw new Error('service token must be a regular mode-0600 file <=512 bytes');
    return await readSecretContents(file);
  } finally { await file.close(); }
}
