import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { checkedProfile } from './profile.mjs';
import { LIMITS, preflight, renderPdf, renderPptx, reportBytes } from './worker.mjs';
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
async function output(bytes) { if (!process.stdout.write(bytes)) await once(process.stdout, 'drain'); }
async function main() {
 const mode = process.argv[2], expectedReport = process.argv[3] ?? '';
 if (process.argv.length > 4 || !['report', 'pptx', 'pdf'].includes(mode) || (mode === 'report' ? expectedReport !== '' : !/^[a-f0-9]{64}$/.test(expectedReport))) throw new Error('Export mode refused');
 const profileInfo = await checkedProfile();
 let length = 0; const chunks = [];
 for await (const chunk of process.stdin) { length += chunk.length; if (length > LIMITS.envelope) throw new Error('Export input refused'); chunks.push(chunk); }
 const input = JSON.parse(Buffer.concat(chunks).toString('utf8'));
 if (!Number.isSafeInteger(input.revision) || input.revision < 1) throw new Error('Export revision refused');
 const { report, profile } = await preflight(input); reportBytes(report);
 let binary = Buffer.alloc(0), mime = '', status = 'ok';
 if (mode !== 'report') {
  if (report.report_hash !== expectedReport) status = 'report_mismatch';
  else if (!report.can_export) status = 'unsupported';
  else { binary = mode === 'pptx' ? await renderPptx(input, profile) : await renderPdf(input, profile); mime = mode === 'pptx' ? 'application/vnd.openxmlformats-officedocument.presentationml.presentation' : 'application/pdf'; }
 }
 if (binary.length > LIMITS.output) throw new Error('Export output refused');
 const header = Buffer.from(JSON.stringify({ protocol_version: 1, mode, status, profile_sha256: profileInfo.fingerprint, report, binary_mime: mime, binary_bytes: binary.length, binary_sha256: sha(binary) }));
 if (header.length > LIMITS.report + 4096) throw new Error('Export header refused');
 const prefix = Buffer.alloc(20); prefix.write('CSLEXP1\n', 0, 'ascii'); prefix.writeUInt32BE(header.length, 8); prefix.writeBigUInt64BE(BigInt(binary.length), 12);
 // Complete generation and validation precedes the first framed output byte.
 await output(prefix); await output(header); await output(binary);
}
main().catch(() => { process.stderr.write('Export worker refused\n'); process.exitCode = 1; });
