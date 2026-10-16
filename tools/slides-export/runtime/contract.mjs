import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { reportHash, canonicalJSON, preflight } from './worker.mjs';
const repo = fileURLToPath(new URL('../../../', import.meta.url));
const input = JSON.parse(await readFile(new URL('../testdata/supported.json', import.meta.url), 'utf8'));
const { report } = await preflight(input);
const values = [report, { text: 'Český 😀 <>&\u2028\u2029', fractional: 123.45678901234567, negative_zero: -0 }, { values: [1e-7, 1e-6, 1e20, 1e21, 0.30000000000000004, Number.MIN_VALUE, -1e-7] }];
const invalid = ['{"x":"\\ud800"}', '{"x":"\\udfff"}', '{"x":"\\ud800\\u0041"}', '{"x":{"a":1,"a":2}}', '{"2":"b","10":"a"}'];
const wire = '[' + [...values.map(v => JSON.stringify(v)), ...invalid].join(',') + ']';
const result = JSON.parse(execFileSync('go', ['run', './tools/slides-export/hash-check'], { cwd: repo, input: wire, encoding: 'utf8', timeout: 120000, maxBuffer: 1 << 20 }));
for (let i = 0; i < values.length; i++) assert.equal(result[i].hash, reportHash(values[i]), 'Go/Node canonical report hash differs');
for (let i = values.length; i < result.length; i++) assert.equal(result[i].error, 'refused');
for (const text of ['\ud800', '\udfff', '\ud800A']) assert.throws(() => canonicalJSON({ text }));
assert.throws(() => canonicalJSON({ '2': 'b', '10': 'a' }));
console.log(JSON.stringify({ cases: values.length + invalid.length, actualPreflight: true, fractionalExponentNegativeZeroUnicode: true, malformedSurrogatesAndNestedDuplicates: 'refused' }));
for (const mode of ['report', 'pptx', 'pdf']) {
 const expected = mode === 'report' ? '' : report.report_hash;
 const frame = execFileSync(process.execPath, ['--max-old-space-size=512', fileURLToPath(new URL('./entry.mjs', import.meta.url)), mode, expected], { input: JSON.stringify(input), timeout: 120000, maxBuffer: (65 << 20) + 4096 });
 const accepted = JSON.parse(execFileSync('go', ['run', './tools/slides-export/hash-check', 'frame', mode, input.deck.deck_id, String(input.revision), input.content_hash, expected], { cwd: repo, input: frame, encoding: 'utf8', timeout: 120000, maxBuffer: 1 << 20 }));
 assert.equal(accepted.report_hash, report.report_hash); assert.equal(accepted.binary_bytes > 0, mode !== 'report');
}
console.log(JSON.stringify({ actualFixedWorkerToGoFraming: ['report', 'pptx', 'pdf'], containerLifecycle: 'pending actual CI' }));
