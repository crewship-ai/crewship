import { readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { join, resolve } from 'node:path';
const root = fileURLToPath(new URL('.', import.meta.url));
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const stable = value => Array.isArray(value) ? value.map(stable) : value && typeof value === 'object' ? Object.fromEntries(Object.keys(value).sort().map(key => [key, stable(value[key])])) : value;
const names = ['package.json', 'pnpm-lock.yaml', 'pnpm-workspace.yaml', 'worker.mjs', 'entry.mjs', 'profile.mjs', 'profile.go', 'Dockerfile', 'fonts/DejaVuSans.ttf', 'fonts/LICENSE-DejaVu.txt', 'fonts/provenance.json'];
export async function checkedProfile() {
 const manifest = JSON.parse(await readFile(join(root, 'profile.json'), 'utf8'));
 if (manifest.protocol_version !== 1 || manifest.files.length !== names.length || new Set(manifest.files.map(f => f.path)).size !== names.length) throw new Error('Export profile refused');
 for (const f of manifest.files) { if (!names.includes(f.path)) throw new Error('Export profile refused'); const bytes = await readFile(join(root, f.path)); if (bytes.length !== f.bytes || sha(bytes) !== f.sha256) throw new Error('Export profile refused'); }
 return { manifest, fingerprint: sha('crewship-slides-export-profile/v1\n' + JSON.stringify(stable(manifest))) };
}
async function generate() {
 const manifest = { protocol_version: 1, base_image: 'node:24-trixie-slim@sha256:7f486cabe892bff8fef04c0f7da8dba84a560be3e4e2740b7a86afc57ebec315', admission_sha256: sha(await readFile(resolve(root, '../../../internal/slidesexport/input.go'))), license_generator_sha256: sha(await readFile(resolve(root, '../../../scripts/gen-frontend-licenses.mjs'))), files: [] };
 for (const path of names.sort()) { const bytes = await readFile(join(root, path)); manifest.files.push({ path, bytes: bytes.length, sha256: sha(bytes) }); }
 await writeFile(join(root, 'profile.json'), JSON.stringify(stable(manifest), null, 2) + '\n');
}
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
 if (process.argv[2] === '--generate') await generate();
 else if (process.argv[2] === '--verify-admission') { const { manifest } = await checkedProfile(); if (sha(await readFile(process.argv[3])) !== manifest.admission_sha256) throw new Error('Export admission mismatch'); }
 else if (process.argv[2] === '--verify-license-generator') { const { manifest } = await checkedProfile(); if (sha(await readFile(process.argv[3])) !== manifest.license_generator_sha256) throw new Error('Export build input mismatch'); }
 else process.stdout.write(JSON.stringify(await checkedProfile()));
}
