// End-to-end D0 acceptance. Credentials stay in memory and never enter evidence.
import assert from 'node:assert/strict';
import { readFile, writeFile, mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { chromium } from '@playwright/test';
import WebSocket from 'ws';

const fixturePath = process.argv[2];
const f = JSON.parse(await readFile(fixturePath, 'utf8'));
const report = { scenarios: [], measurements: {}, limits: { browserAckMs: 5000, representativeParagraphs: 200, snapshotBytes: 1048576, nodeServiceRSSBytes: 536870912, browserJSHeapBytes: 268435456 }, runtime: { node: process.version } };
let child, browser, url, port;
const browserDiagnostics = [];
const pause = ms => new Promise(r => setTimeout(r, ms));
async function until(fn, label, timeout = 10000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (await fn()) return; await pause(25); }
  throw new Error(`timeout: ${label}`);
}
async function start() {
  child = spawn(process.execPath, [new URL('./service.mjs', import.meta.url).pathname, fixturePath, String(port || 0)], { stdio: ['ignore', 'pipe', 'pipe', 'ipc'] });
  // Deliberately do not forward stderr: service diagnostics may contain credentials.
  let line = '';
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('service startup timeout')), 15000);
    child.once('exit', () => { clearTimeout(timer); reject(new Error('service exited before ready')); });
    child.stdout.on('data', chunk => { line += chunk; const end = line.indexOf('\n'); if (end >= 0) {
      try { const ready = JSON.parse(line.slice(0, end)); url = ready.url; port = new URL(url).port; clearTimeout(timer); resolve(); }
      catch { clearTimeout(timer); reject(new Error('invalid service ready message')); }
    } });
  });
}
async function kill() { if (child?.exitCode === null && child.signalCode === null) { const ended = once(child, 'exit'); child.kill('SIGKILL'); await ended; } }
async function request(path, token = f.owner, body, expected = 200) {
  const res = await fetch(url + path, { method: body === undefined ? 'GET' : 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json', 'X-D0-Service': f.service }, body: body === undefined ? undefined : JSON.stringify(body) });
  assert.equal(res.status, expected, `${path} HTTP status`);
  return res.status === 204 ? {} : res.json();
}
let seq = 0;
const op = (kind, fields = {}, token = f.owner, expected = 200) => request('/op', token, { id: `accept-${++seq}`, kind, schema: 1, ...fields }, expected);
const state = () => request('/state');
async function control(action) {
  const res = await fetch(`${f.authority}/control?action=${action}`, { method: 'POST', headers: { 'X-D0-Service': f.service, Authorization: `Bearer ${f.owner}`, 'X-Workspace-ID': f.workspace } });
  assert.equal(res.status, 200, `control ${action}`); return res.json();
}
async function scenario(name, fn) {
  const start = performance.now();
  try { await fn(); report.scenarios.push({ name, status: 'passed', durationMs: Math.round(performance.now() - start) }); }
  catch (error) { report.scenarios.push({ name, status: 'failed', durationMs: Math.round(performance.now() - start) }); throw error; }
}
async function openHuman(token, schema = 1) {
  const context = await browser.newContext();
  await context.addInitScript(() => {
    const NativeSocket = window.WebSocket;
    window.__d0Rejected = [];
    window.WebSocket = class extends NativeSocket {
      constructor(...args) { super(...args); this.addEventListener('message', event => { try { const message = JSON.parse(event.data); if (message.kind === 'rejected') window.__d0Rejected.push({ error: message.error, id: message.id }); } catch {} }); }
    };
  });
  await context.addInitScript(({ token, schema }) => { localStorage.setItem('d0-token', token); localStorage.setItem('d0-schema', String(schema)); }, { token, schema });
  const page = await context.newPage();
  page.on('console', message => { if (['warning', 'error'].includes(message.type())) browserDiagnostics.push({ type: message.type(), text: message.text() }); });
  page.on('pageerror', error => browserDiagnostics.push({ type: 'pageerror', text: error.message }));
  await page.goto(url);
  await page.waitForFunction(() => window.d0?.state);
  return page;
}
const edit = (page, id, text) => page.evaluate(({ id, text }) => { window.d0.edit(id, text); return window.d0.flush(); }, { id, text });
const readyPM = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
async function diagnostic(page, phase) {
  const value = await page.evaluate(() => ({ immediate: window.__pmAfter, rejected: window.__d0Rejected, roots: [...window.d0.doc.share.keys()], editor: window.d0.editor.getJSON(), xml: window.d0.doc.getXmlFragment('default').toString(), editable: window.d0.editor.isEditable, destroyed: window.d0.editor.isDestroyed, pending: window.d0.pending, bindingError: window.d0.bindingError, bindings: window.d0.editor.state.plugins.map(plugin => plugin.getState(window.d0.editor.state)).filter(value => value?.binding).map(value => ({ error: value.binding.projectionError?.message, snapshot: value.snapshot, prevSnapshot: value.prevSnapshot, isChangeOrigin: value.isChangeOrigin })), rev: window.d0.state.rev, status: document.querySelector('#status').textContent }));
  (report.diagnostics ||= []).push({ phase, ...value });
  report.browserDiagnostics = browserDiagnostics;
}
const saved = page => page.waitForFunction(() => document.querySelector('#status')?.textContent.includes('Uloženo'));
async function has(page, text) { await page.waitForFunction(text => window.d0.editor.getText().includes(text), text); }
try {
  await start();
  await op('create');
  await scenario('headless agent edits without browser', async () => {
    const before = await state(), block = before.outline.find(x => x.id === 'title');
    const committed = await op('agent', { target: block.id, text: 'Headless agent title', ifRev: before.rev, ifHash: block.hash }, f.agent);
    assert.ok(committed.checkpoint > before.rev);
    assert.equal((await state()).outline.find(x => x.id === 'title').text, 'Headless agent title');
  });
  browser = await chromium.launch({ headless: true }); // Missing browser is a failure.
  const a = await openHuman(f.owner), b = await openHuman(f.editor);
  await scenario('two browsers and semantic agent converge', async () => {
    const inputA = a.locator('[data-block-id="intro"]');
    const inputB = b.locator('[data-block-id="intro"]');
    await Promise.all([inputA.click(), inputB.click()]);
    await Promise.all([a.keyboard.press('End'), b.keyboard.press('End')]);
    await Promise.all([a.keyboard.type(' člověk A'), b.keyboard.type(' člověk B')]);
    await Promise.all([a.evaluate(() => window.d0.flush()), b.evaluate(() => window.d0.flush())]);
    await Promise.all([saved(a), saved(b)]);
    let s = await state(); const block = s.outline.find(x => x.id === 'cell-b-p');
    await op('agent', { target: block.id, text: 'Agent v tabulce', ifRev: s.rev, ifHash: block.hash }, f.agent);
    await Promise.all([has(a, 'člověk B'), has(b, 'člověk A'), has(a, 'Agent v tabulce'), has(b, 'Agent v tabulce')]);
    const ids = (await state()).outline.map(x => x.id); assert.equal(new Set(ids).size, ids.length);
  });
  await scenario('same block stale revision and hash reject', async () => {
    const s = await state(), block = s.outline.find(x => x.id === 'intro');
    await edit(a, 'intro', ' souběh'); await saved(a);
    await op('agent', { target: block.id, text: 'LOST', ifRev: s.rev, ifHash: block.hash }, f.agent, 409);
    const now = await state();
    await op('agent', { target: block.id, text: 'LOST', ifRev: now.rev, ifHash: block.hash }, f.agent, 409);
    assert.ok(!(await state()).outline.some(x => x.text.includes('LOST')));
  });
  await scenario('pending human commit precedes stale agent patch', async () => {
    const before = await state(), block = before.outline.find(x => x.id === 'intro');
    await request('/test/hold', f.owner, { phase: 'before' });
    await edit(a, 'intro', ' HUMAN-BARRIER');
    await until(async () => (await request('/test/status')).held, 'pending human barrier');
    assert.match(await a.locator('#status').textContent(), /Ukládám/);
    const stale = op('agent', { target: block.id, text: 'STALE-BARRIER', ifRev: before.rev, ifHash: block.hash }, f.agent, 409);
    await request('/test/release', f.owner, {}); await stale; await saved(a);
    const after = await state();
    assert.ok(after.outline.find(x => x.id === 'intro').text.includes('HUMAN-BARRIER'));
    assert.ok(!after.outline.find(x => x.id === 'intro').text.includes('STALE-BARRIER'));
  });
  await scenario('retry operation is idempotent', async () => {
    const s = await state(), block = s.outline.find(x => x.id === 'title');
    const body = { id: 'retry-fixed', kind: 'agent', schema: 1, target: block.id, text: 'Retry title', ifRev: s.rev, ifHash: block.hash };
    await request('/test/hold', f.owner, { phase: 'after' });
    const abort = new AbortController();
    const timed = fetch(url + '/op', { method: 'POST', headers: { Authorization: `Bearer ${f.agent}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal: abort.signal }).catch(() => null);
    await until(async () => (await request('/test/status')).held, 'timeout after durable commit');
    abort.abort(); await timed;
    await request('/test/release', f.owner, {});
    const committed = await state(); const second = await request('/op', f.agent, body);
    assert.equal(second.rev, committed.rev); assert.equal((await state()).rev, committed.rev);
    await request('/op', f.agent, { ...body, text: 'different payload' }, 409);
  });
  await scenario('kill before commit preserves durable state', async () => {
    const before = await state(); await request('/test/hold', f.owner, { phase: 'before' });
    const sending = edit(a, 'intro', ' BEFORE-KILL').catch(() => {});
    await until(async () => (await request('/test/status')).held, 'before gate');
    assert.ok(!(await b.evaluate(() => window.d0.editor.getText())).includes('BEFORE-KILL'));
    await kill(); await sending; await start(); const after = await state();
    assert.equal(after.rev, before.rev); assert.ok(!after.outline.some(x => x.text.includes('BEFORE-KILL')));
    await Promise.all([a.reload(), b.reload()]); await Promise.all([saved(a), saved(b)]);
  });
  await scenario('kill after commit recovers without acknowledged loss', async () => {
    await request('/test/hold', f.owner, { phase: 'after' });
    const sending = edit(a, 'intro', ' AFTER-KILL').catch(() => {});
    await until(async () => (await request('/test/status')).held, 'after gate');
    await kill(); await sending; await start(); assert.ok((await state()).outline.some(x => x.text.includes('AFTER-KILL')));
    await Promise.all([a.reload(), b.reload()]); await Promise.all([has(a, 'AFTER-KILL'), has(b, 'AFTER-KILL')]);
  });
  await scenario('revocation before commit blocks peer broadcast', async () => {
    const before = await state(); await request('/test/hold', f.owner, { phase: 'before' });
    const sending = edit(b, 'intro', ' REVOKED-PENDING').catch(() => {});
    await until(async () => (await request('/test/status')).held, 'revocation gate');
    assert.match(await b.locator('#status').textContent(), /Ukládám/);
    assert.ok(!(await a.evaluate(() => window.d0.editor.getText())).includes('REVOKED-PENDING'));
    await control('revoke'); await request('/test/release', f.owner, {}); await sending;
    await pause(200); assert.equal((await state()).rev, before.rev);
    assert.ok(!(await a.evaluate(() => window.d0.editor.getText())).includes('REVOKED-PENDING'));
    await control('grant'); await b.reload(); await saved(b);
  });
  await scenario('comments survive edit compaction deletion and restore', async () => {
    await op('comment', { block: 'intro', from: 0, to: 4, body: 'Review this range' });
    let s = await state(); assert.equal(s.threads.length, 1); assert.equal(s.threads[0].status, 'attached');
    const checkpoint = await op('checkpoint');
    await edit(a, 'intro', ' anchor suffix'); await saved(a);
    await op('compact'); s = await state(); assert.equal(s.threads[0].status, 'attached');
    await a.evaluate(() => { window.d0.replace('intro', ''); window.d0.flush(); }); await saved(a);
    assert.equal((await state()).threads[0].status, 'collapsed');
    await op('restore', { checkpoint: checkpoint.checkpoint ?? checkpoint.rev, ifRev: (await state()).rev });
    s = await state(); assert.equal(s.threads[0].status, 'attached');
    await a.waitForFunction(rev => window.d0.state.rev >= rev, s.rev);
    await readyPM(a);
    await a.evaluate(() => {
      const editor = window.d0.editor; let range;
      editor.state.doc.descendants((node, pos) => { if (node.attrs.id === 'intro') range = { from: pos, to: pos + node.nodeSize }; });
      if (!range) throw new Error('missing structural target');
      editor.view.dispatch(editor.state.tr.delete(range.from, range.to)); window.__pmAfter = { roots: [...window.d0.doc.share.keys()], editor: editor.getJSON(), pending: window.d0.pending }; window.d0.flush();
    });
    try { await until(async () => !(await state()).outline.some(x => x.id === 'intro'), 'structural block deletion durable'); }
    catch (error) { await diagnostic(a, 'PM structural delete'); throw error; }
    await saved(a);
    assert.equal((await state()).threads[0].status, 'detached');
    await op('restore', { checkpoint: checkpoint.checkpoint ?? checkpoint.rev, ifRev: (await state()).rev });
    assert.equal((await state()).threads[0].status, 'attached');
  });
  await scenario('old schema browser cannot mutate', async () => {
    await control('schema2'); const before = await state();
    await a.reload(); await a.waitForFunction(() => window.d0?.state?.unsupported && /Nepodporovaná|unsupported/i.test(document.querySelector('#status')?.textContent || ''));
    await request('/op', f.owner, { id: 'old-schema-op', kind: 'compact', schema: 1 }, 409);
    assert.equal((await state()).rev, before.rev);
    await control('schema1'); await Promise.all([a.reload(), b.reload()]); await Promise.all([saved(a), saved(b)]);
  });
  await scenario('Go backup restore preserves document and threads', async () => {
    const before = await state(); await control('backup');
    const block = before.outline.find(x => x.id === 'title');
    await op('agent', { target: block.id, text: 'after backup', ifRev: before.rev, ifHash: block.hash }, f.agent);
    await control('restore-backup'); await kill(); await start();
    const after = await state(); assert.deepEqual(after.outline, before.outline); assert.deepEqual(after.threads, before.threads);
    await Promise.all([a.reload(), b.reload()]); await Promise.all([saved(a), saved(b)]);
  });
  await scenario('Tiptap keyboard writes after schema and backup restore', async () => {
    await a.locator('[data-block-id="intro"]').click(); await a.keyboard.press('End');
    await a.keyboard.type(' POST-RESTORE'); await a.evaluate(() => window.d0.flush());
    await until(async () => (await state()).outline.some(x => x.id === 'intro' && x.text.includes('POST-RESTORE')), 'post restore keyboard durable');
    await saved(a); await has(b, 'POST-RESTORE');
  });
  await scenario('agent raw CRDT transport is rejected', async () => {
    const before = await state();
    const ws = new WebSocket(url.replace('http:', 'ws:') + '/ws');
    await once(ws, 'open');
    const closed = once(ws, 'close'); ws.send(JSON.stringify({ kind: 'auth', token: f.agent, schema: 1 }));
    await Promise.race([closed, pause(3000).then(() => { throw new Error('agent socket accepted'); })]);
    assert.equal((await state()).rev, before.rev);
  });
  await scenario('representative document latency memory and restart', async () => {
    const baselineService = await request('/test/status');
    if (process.env.CREWSHIP_DOCS_D0_OUTPUT) { const screenshot = process.env.CREWSHIP_DOCS_D0_OUTPUT + '.before.png'; await a.screenshot({ path: screenshot, fullPage: false }); report.beforeBenchmarkScreenshot = screenshot; }
    await readyPM(a);
    await a.evaluate(() => {
      const json = window.d0.editor.getJSON();
      for (let i = 0; i < 200; i++) json.content.push({ type: 'paragraph', attrs: { id: `bench-${i}` }, content: [{ type: 'text', text: `Paragraph ${i}: ` + 'Representative Czech document text. '.repeat(8) }] });
      window.d0.editor.commands.setContent(json); window.__pmAfter = { roots: [...window.d0.doc.share.keys()], editor: window.d0.editor.getJSON(), pending: window.d0.pending }; window.d0.flush();
    });
    try { await until(async () => (await state()).outline.some(x => x.id === 'bench-199'), 'representative document durable'); }
    catch (error) { await diagnostic(a, 'PM setContent benchmark'); throw error; }
    await saved(a);
    const samples = [];
    for (let i = 0; i < 12; i++) { const started = performance.now(); await edit(a, `bench-${i}`, ' edit'); await saved(a); await b.waitForFunction(id => window.d0.state.outline.find(x => x.id === id)?.text.endsWith(' edit'), `bench-${i}`); samples.push(performance.now() - started); }
    samples.sort((x, y) => x - y); const p95 = samples[Math.ceil(samples.length * .95) - 1];
    assert.ok(p95 < report.limits.browserAckMs, 'browser acknowledgement exceeds limit');
    const s = await state(); const bytes = Buffer.from(s.snapshot, 'base64').length; assert.ok(bytes < report.limits.snapshotBytes);
    const cdp = await a.context().newCDPSession(a); await cdp.send('Performance.enable'); const metrics = await cdp.send('Performance.getMetrics');
    const serviceMetrics = await request('/test/status');
    const commitSamples = serviceMetrics.commitTimings.slice(-12);
    assert.equal(commitSamples.length, 12, 'representative Go commit timing samples');
    const percentile = (values, p) => values.toSorted((x, y) => x - y)[Math.ceil(values.length * p) - 1];
    const commitGoMicros = commitSamples.map(x => x.goMicros);
    const commitRTTMs = commitSamples.map(x => x.roundTripMs);
    assert.ok(commitGoMicros.every(Number.isFinite) && commitRTTMs.every(Number.isFinite), 'finite commit timings');
    assert.ok(serviceMetrics.rss < report.limits.nodeServiceRSSBytes, 'Node service RSS exceeds limit');
    assert.ok(metrics.metrics.find(x => x.name === 'JSHeapUsedSize')?.value < report.limits.browserJSHeapBytes, 'browser heap exceeds limit');
    report.measurements = { goCommitSamples: commitSamples.length, goWriterFenceAuthSQLCommitP50Micros: percentile(commitGoMicros, .5), goWriterFenceAuthSQLCommitP95Micros: percentile(commitGoMicros, .95), nodeGoCommitRoundTripP95Ms: percentile(commitRTTMs, .95), memoryMeasurement: { forcedGC: false, includesRetainedSnapshotsAndTombstones: true, rssIsPerDocumentCapacity: false, baselineAndFinalSameServiceProcess: true }, nodeServiceBaselineRSSBytes: baselineService.rss, nodeServiceRSSBytes: serviceMetrics.rss, nodeServiceHeapBytes: serviceMetrics.heapUsed, paragraphs: 200, samples: samples.length, browserAckP95Ms: Math.round(p95), browserAckMaxMs: Math.round(samples.at(-1)), snapshotBytes: bytes, browserJSHeapBytes: metrics.metrics.find(x => x.name === 'JSHeapUsedSize')?.value, nodeHarnessRSSBytes: process.memoryUsage().rss };
    await kill(); await start(); const recovered = await state(); assert.deepEqual(recovered.outline, s.outline);
    await a.reload(); await saved(a);
    if (process.env.CREWSHIP_DOCS_D0_OUTPUT) { const screenshot = process.env.CREWSHIP_DOCS_D0_OUTPUT + '.png'; await a.screenshot({ path: screenshot, fullPage: false }); report.screenshot = screenshot; }
  });
  report.status = 'passed';
} catch (error) {
  report.status = 'failed';
  // Assertion metadata and response bodies are intentionally excluded.
  report.failure = { name: error.name, message: String(error.message).replaceAll(f.owner, '[redacted]').replaceAll(f.editor, '[redacted]').replaceAll(f.agent, '[redacted]').replaceAll(f.service, '[redacted]') };
  process.exitCode = 1;
} finally {
  await browser?.close(); await kill();
  const output = process.env.CREWSHIP_DOCS_D0_OUTPUT || join(await mkdtemp(join(tmpdir(), 'crewship-docs-d0-')), 'acceptance.json');
  await writeFile(output, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify({ status: report.status, scenarios: report.scenarios.length, evidence: output, ...(report.failure ? { failure: report.failure } : {}) }));
}
