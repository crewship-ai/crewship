import fs from 'node:fs/promises';
import { constants } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

/** Validate and read the same open inode, never re-open a checked pathname. */
export async function readPrivateJson(filename) {
  const handle = await fs.open(filename, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || (stat.mode & 0o077) !== 0 ||
        (typeof process.getuid === 'function' && stat.uid !== process.getuid())) {
      throw new Error('State must be a private regular file owned by the current user');
    }
    if (stat.size > 1024 * 1024) throw new Error('State file exceeds the size limit');
    const content = await handle.readFile('utf8');
    try { return JSON.parse(content); }
    catch { throw new Error('State file contains invalid JSON'); }
  } finally {
    await handle.close();
  }
}

/** All reports/screenshots stay in a fresh owner-only directory until reviewed. */
export async function createPrivateArtifacts(label) {
  if (!/^[a-z0-9-]+$/.test(label)) throw new Error('Invalid artifact label');
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), `crewship-${label}-`));
  // mkdtemp creates mode 0700 (possibly more restrictive under the process umask).
  console.log(`Artifacts: ${directory}`);
  return directory;
}
