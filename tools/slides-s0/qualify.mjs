import { mkdir, writeFile, readFile } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { fixture, Session, hash, clone, Conflict, canonical } from './model.mjs';
import { html } from './html.mjs';
import { typed } from './typed.mjs';
import { startEditor } from './editor.mjs';
import { exportPptx, inspectPptx } from './export.mjs';

const output = resolve(process.argv[2] ?? 'output');
await mkdir(output, { recursive: true });
const sessions = { html: new Session(html, html.encode(fixture())), typed: new Session(typed, typed.encode(fixture())) };
const server = await startEditor(sessions);
const report = { schema: 'slides-s0-evidence-v1', time: new Date().toISOString(), node: process.version, scope: 'Isolated synthetic fixture. Agent-like semantic patch, no model execution or production authority.', candidates: {}, externalRequests: [], gates: { browser: 'pending', libreOffice: 'not performed: no application available', powerPoint: 'not performed: PowerPoint unavailable; static OOXML checks do not replace it', applicationEditability: 'not performed: requires opening and modifying native text, shapes and table in each application', pdfFidelity: 'not qualified', decision: 'No winner selected; application fidelity gates remain mandatory.' } };
let browser;
try {
  browser = await chromium.launch({ headless: true }); const context = await browser.newContext({ viewport: { width: 1040, height: 820 }, deviceScaleFactor: 1 });
  context.on('request', req => { if (!req.url().startsWith(server.url + '/') && !req.url().startsWith('data:') && !req.url().startsWith('about:')) report.externalRequests.push(req.url()); });
  const page = await context.newPage(); const browserErrors = []; page.on('pageerror', error => browserErrors.push(error.message));
  await page.goto(server.url); await page.locator('#status').filter({ hasText: 'Version 1' }).waitFor();
  for (const [name, session] of Object.entries(sessions)) {
    const adapter = session.adapter; const before = session.read(); const originalTarget = clone(before.slides[0].elements[0]);
    await writeFile(join(output, `${name}-initial.json`), JSON.stringify(session.state, null, 2));
    await page.selectOption('#candidate', name); await page.locator('#status').filter({ hasText: `${name} Version 1` }).waitFor();
    await page.fill('#text', 'Ručně upraveno: český kůň'); await page.fill('#x', '52'); await page.fill('#y', '55');
    const manualStart = performance.now(); await page.click('#save'); await page.locator('#status').filter({ hasText: 'Version 2' }).waitFor();
    const manualMs = performance.now() - manualStart;
    assert.equal(session.target('slide-czech', 'title-czech').text, 'Ručně upraveno: český kůň'); assert.equal(session.target('slide-czech', 'title-czech').x, 52);
    const protectedState = clone(session.state);
    assert.throws(() => session.patch({ slideID: 'slide-czech', elementID: 'title-czech', ifVersion: 1, ifHash: hash(originalTarget), changes: { text: 'stale agent' } }), Conflict);
    assert.throws(() => session.patch({ slideID: 'slide-czech', elementID: 'title-czech', ifVersion: 2, ifHash: hash(originalTarget), changes: { text: 'stale target' } }), Conflict);
    assert.deepEqual(session.state, protectedState);
    const anchor = session.anchor('slide-czech', 'body-czech'); const target = session.target('slide-czech', 'body-czech');
    const agentStart = performance.now();
    session.patch({ slideID: 'slide-czech', elementID: 'body-czech', ifVersion: 2, ifHash: hash(target), changes: { text: 'Agent: cílená úprava stejného snímku.', x: 60, w: 840 } });
    const agentMs = performance.now() - agentStart;
    const order = session.read().slides.map(s => s.id); session.reorder([...order].reverse(), 3, hash(order));
    assert.equal(session.resolve(anchor), 'attached');
    const after = session.read();
    assert.deepEqual(after.slides.flatMap(s => s.elements.map(e => e.id)).sort(), before.slides.flatMap(s => s.elements.map(e => e.id)).sort());
    assert.deepEqual(adapter.project(JSON.parse(JSON.stringify(adapter.normalize(session.state)))), after);
    assert.deepEqual(adapter.normalize(adapter.normalize(session.state)), adapter.normalize(session.state));
    await writeFile(join(output, `${name}-final.json`), JSON.stringify(session.state, null, 2));
    await page.selectOption('#candidate', name); await page.locator('#status').filter({ hasText: `${name} Version 4` }).waitFor();
    await page.waitForFunction(() => document.querySelectorAll('iframe').length === 5);
    const overflow = []; const screenshots = [];
    for (let i = 0; i < after.slides.length; i++) {
      const locator = page.locator('iframe').nth(i); const frame = await locator.contentFrame();
      await frame.locator('body > [data-id]').first().waitFor();
      const result = await frame.locator('body').evaluate(body => [...body.children].map(e => ({ id: e.dataset.id, x: e.offsetLeft, y: e.offsetTop, w: e.offsetWidth, h: e.offsetHeight, overflow: e.scrollHeight > e.clientHeight || e.scrollWidth > e.clientWidth, type: e.tagName })));
      for (const measured of result) {
        const expected = after.slides[i].elements.find(e => e.id === measured.id); assert.ok(expected); assert.equal(measured.x, expected.x); assert.equal(measured.y, expected.y); assert.equal(measured.w, expected.w); assert.equal(measured.h, expected.h);
        if (measured.overflow) overflow.push({ slideID: after.slides[i].id, elementID: measured.id });
      }
      const screenshot = await locator.screenshot(); const file = `${name}-${after.slides[i].id}.png`; await writeFile(join(output, file), screenshot); screenshots.push({ slideID: after.slides[i].id, file, hash: hash([...screenshot]) });
      const sandbox = await locator.getAttribute('sandbox'); assert.equal(sandbox, '');
    }
    assert.ok(overflow.some(item => item.elementID === 'body-overflow'), 'intentional overflow detected');
    const pptxFile = join(output, `${name}.pptx`); const exportReport = await exportPptx(after, pptxFile, overflow); const ooxml = await inspectPptx(after, pptxFile);
    report.candidates[name] = { representation: adapter.name, operations: { manualTextAndGeometry: true, agentLikeTextAndGeometry: true, staleVersionRejected: true, staleHashRejected: true, rejectedPatchAtomic: true, reorderPreservesIDs: true, anchorAfterTextAndReorder: session.resolve(anchor), normalizedRoundtrip: true }, canonicalSemanticHash: hash(after), stateBytes: Buffer.byteLength(canonical(session.state)), measurement: { manualMs, agentMs, note: 'One observation including repeated bounded PNG/schema validation; not a benchmark.' }, screenshots, exportReport, ooxml };
  }
  assert.deepEqual(sessions.html.read(), sessions.typed.read());
  assert.deepEqual(report.candidates.html.screenshots.map(s => s.hash), report.candidates.typed.screenshots.map(s => s.hash), 'same browser render pixels');
  assert.deepEqual(report.externalRequests, []); assert.deepEqual(browserErrors, []);
  report.gates.browser = 'passed: identical manual controls, native patches, ID anchors, CAS, measured overflow, sandboxed previews, identical screenshot pixels, no observed external requests';
  const files = ['html.mjs', 'typed.mjs', 'model.mjs', 'render.mjs', 'export.mjs', 'editor.mjs', 'ui.mjs', 'qualify.mjs', 'test.mjs'];
  report.codeLines = Object.fromEntries(await Promise.all(files.map(async file => [file, (await readFile(new URL(file, import.meta.url), 'utf8')).trimEnd().split('\n').length])));
  report.codeBytes = Object.fromEntries(await Promise.all(files.map(async file => [file, (await readFile(new URL(file, import.meta.url))).length])));
  report.codeHashes = Object.fromEntries(await Promise.all(files.map(async file => [file, hash(await readFile(new URL(file, import.meta.url), 'utf8'))])));
  report.productCommit = spawnSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).stdout.trim();
  report.browserVersion = browser.version();
  report.codeCost = 'Shared validation/session/render/export/editor costs apply equally. HTML adds parser, whitelist, CSS normalization, DOM mutation and metadata synchronization. Typed tree adds exact schema validation and JSON mutation. Counts are physical lines, not engineering estimates.';
  const lo = spawnSync(process.env.S0_LIBREOFFICE ?? 'libreoffice', ['--version'], { encoding: 'utf8', timeout: 15000 });
  if (!lo.error && lo.status === 0) {
    report.gates.libreOffice = { version: lo.stdout.trim(), conversions: [] };
    for (const name of Object.keys(sessions)) {
      const directory = join(output, `libreoffice-${name}`); await mkdir(directory, { recursive: true });
      const converted = spawnSync(process.env.S0_LIBREOFFICE ?? 'libreoffice', [`-env:UserInstallation=file://${join(directory, 'profile')}`, '--headless', '--convert-to', 'pdf', '--outdir', directory, join(output, `${name}.pptx`)], { encoding: 'utf8', timeout: 120000 });
      assert.equal(converted.status, 0, converted.stderr); const pdf = await readFile(join(directory, `${name}.pdf`)); assert.ok(pdf.subarray(0, 5).equals(Buffer.from('%PDF-')));
      report.gates.libreOffice.conversions.push({ name, openedAndConverted: true, pdfBytes: pdf.length, editability: 'not performed: conversion alone does not verify manual editing' });
    }
  }
  await writeFile(join(output, 'report.json'), JSON.stringify(report, null, 2));
  const a = report.candidates.html; const b = report.candidates.typed;
  const table = `# Slides S0 decision observations\n\nNo winner selected; application fidelity/editability remains unqualified.\n\n| Gate / observation | Restricted HTML | Typed tree |\n| --- | --- | --- |\n| Manual text/geometry | Passed | Passed |\n| Targeted agent-like edit | Passed (no model) | Passed (no model) |\n| Stable IDs / reorder / element anchors | Passed | Passed |\n| Revision and target hash conflicts | Passed | Passed |\n| Native representation roundtrip | Passed | Passed |\n| Browser render parity | Same pixels | Same pixels |\n| Overflow / font fallback | Explicit report | Explicit report |\n| Native OOXML text, shapes, table | Structure checked | Structure checked |\n| Rasterized supported elements | ${a.exportReport.rasterizedElements.length} | ${b.exportReport.rasterizedElements.length} |\n| State bytes | ${a.stateBytes} | ${b.stateBytes} |\n| Candidate adapter code bytes | ${report.codeBytes['html.mjs']} | ${report.codeBytes['typed.mjs']} |\n| LibreOffice opening | See report.json application gate | See report.json application gate |\n| PowerPoint opening/editing | Not performed | Not performed |\n| Export visual fidelity / native editing | Not qualified | Not qualified |\n\nCosts for schema/session/render/export/editor apply equally. This is one narrow corpus and one operation timing observation, not a performance comparison. HTML authoring convenience and broad import remain unmeasured.\n`;
  await writeFile(join(output, 'decision.md'), table);
  console.log(JSON.stringify({ output, gates: report.gates, externalRequests: report.externalRequests, semanticHash: report.candidates.html.canonicalSemanticHash }, null, 2));
} catch (error) {
  report.failure = error.stack; await writeFile(join(output, 'report.failed.json'), JSON.stringify(report, null, 2)); throw error;
} finally { await browser?.close(); await server.close(); }
