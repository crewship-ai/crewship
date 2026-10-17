import assert from 'node:assert/strict';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { PNG } from 'pngjs';
import { createHash } from 'node:crypto';
import { html } from './html.mjs';
import { typed } from './typed.mjs';
import { hash } from './model.mjs';
import { effectiveFont } from './render.mjs';

const input = resolve(process.argv[2] ?? 'output');
const output = resolve(process.argv[3] ?? 'office-output');
for (const name of ['S0_LIBREOFFICE', 'S0_UNO_PYTHON', 'S0_PDF_PYTHON']) assert.ok(process.env[name], `${name} is required`);
await mkdir(output); // Refuse reuse; never overwrite a prior qualification directory.
function run(binary, args) {
  const result = spawnSync(binary, args, { encoding: 'utf8', timeout: 180000, env: { ...process.env, SAL_USE_VCLPLUGIN: 'svp' } });
  assert.equal(result.status, 0, `${binary}: ${result.error ?? result.stderr}`); return result.stdout;
}
const normalizeText = text => text.replace(/\s+/gu, ' ').trim();
function foreground(image, box, border = 0) {
  let count = 0;
  for (let y = box.y; y < box.y + box.h; y++) for (let x = box.x; x < box.x + box.w; x++) {
    const offset = ((y + border) * image.width + x + border) * 4;
    if (image.data[offset + 3] > 128 && Math.min(...image.data.subarray(offset, offset + 3)) < 245) count++;
  }
  return count;
}
function pixelDifferences(browser, office) {
  assert.ok(browser.width >= office.width + 2 && browser.width <= office.width + 3); assert.ok(browser.height >= office.height + 2 && browser.height <= office.height + 3);
  let changed = 0;
  for (let y = 0; y < office.height; y++) for (let x = 0; x < office.width; x++) {
    const a = ((y + 1) * browser.width + x + 1) * 4; const b = (y * office.width + x) * 4;
    if ([0, 1, 2, 3].some(channel => browser.data[a + channel] !== office.data[b + channel])) changed++;
  }
  return { changedPixels: changed, fraction: changed / (office.width * office.height), note: 'Browser iframe border is excluded with a one-pixel offset; fractional screenshot bounds can add one pixel. Mismatch is diagnostic, not a perceptual fidelity threshold.' };
}
const baseCommit = run('git', ['-C', fileURLToPath(new URL('.', import.meta.url)), 'rev-parse', 'HEAD']).trim();
const worktreeDirty = run('git', ['-C', fileURLToPath(new URL('.', import.meta.url)), 'status', '--porcelain', '--', '.']).trim() !== '';
const files = ['office.mjs', 'office-uno.py', 'office-pdf.py', 'xml.mjs', 'model.mjs', 'export.mjs', 'typed.mjs', 'html.mjs', 'render.mjs', 'qualify.mjs', 'test.mjs', 'package.json', 'pnpm-lock.yaml'];
const sourceHashes = Object.fromEntries(await Promise.all(files.map(async file => [file, createHash('sha256').update(await readFile(new URL(file, import.meta.url))).digest('hex')])));
const inputReport = JSON.parse(await readFile(join(input, 'report.json'), 'utf8'));
const report = { kind: 'slides-s0-office-v1', baseCommit, sourceCommit: worktreeDirty ? null : baseCommit, worktreeDirty, sourceHashes, inputQualifiedCommit: inputReport.productCommit,
  libreOffice: run(process.env.S0_LIBREOFFICE, ['--headless', '--version']).trim(),
  powerPoint: 'not performed; no PowerPoint environment available', candidates: {},
  decision: 'No canonical format winner. Equal candidates share the same export mapping; Office differences do not distinguish the formats.',
  nativeEditing: 'Actual application UNO objects edited, saved and reopened; manual UI editing not performed.',
};
try {
  for (const [name, adapter] of Object.entries({ html, typed })) {
    const deck = adapter.project(JSON.parse(await readFile(join(input, `${name}-final.json`), 'utf8')));
    const directory = join(output, name);
    run(process.env.S0_UNO_PYTHON, [fileURLToPath(new URL('./office-uno.py', import.meta.url)), '--soffice', process.env.S0_LIBREOFFICE, '--input', join(input, `${name}.pptx`), '--output', directory]);
    const native = JSON.parse(await readFile(join(directory, 'uno-report.json'), 'utf8'));
    const pdfDirectory = join(directory, 'original-pdf');
    run(process.env.S0_PDF_PYTHON, [fileURLToPath(new URL('./office-pdf.py', import.meta.url)), join(directory, 'original.pdf'), pdfDirectory]);
    run(process.env.S0_PDF_PYTHON, [fileURLToPath(new URL('./office-pdf.py', import.meta.url)), join(directory, 'edited.pdf'), join(directory, 'edited-pdf')]);
    const pdf = JSON.parse(await readFile(join(pdfDirectory, 'pdf-report.json'), 'utf8'));
    assert.equal(native.before.length, deck.slides.length); assert.equal(pdf.pages.length, deck.slides.length);
    const slides = [];
    for (let index = 0; index < deck.slides.length; index++) {
      const source = deck.slides[index]; const imported = native.before[index]; const after = native.after[index]; const page = pdf.pages[index];
      assert.equal(imported.shapes.length, source.elements.length); assert.equal(after.shapes.length, source.elements.length);
      assert.ok(Math.abs(page.widthPoints - deck.width * 0.75) < 0.1 && Math.abs(page.heightPoints - deck.height * 0.75) < 0.1, 'PDF dimensions');
      const geometry = []; const fonts = [];
      for (const element of source.elements) {
        const shape = imported.shapes.find(s => s.name === element.id); assert.ok(shape, `imported native ID ${element.id}`);
        const saved = after.shapes.find(s => s.name === element.id); assert.ok(saved, `roundtrip native ID ${element.id}`);
        const drift = Object.fromEntries([['x', 'x'], ['y', 'y'], ['width', 'w'], ['height', 'h']].map(([field, key]) => [field, shape[field] - element[key] * 2540 / 96]));
        assert.ok(Object.values(drift).every(value => Math.abs(value) <= 2), 'bounded initial geometry normalization in 1/100mm'); geometry.push({ id: element.id, driftHundredthsMM: drift });
        if (element.type === 'text') {
          assert.equal(shape.text, element.text, `Czech/LF text ${element.id}`);
          const expected = element.id === 'title-czech' ? element.text + '\nUNO: Upravený český text' : element.text;
          assert.equal(saved.text, expected, `roundtrip text ${element.id}`);
          for (const run of shape.textRuns.filter(r => r.text.length > 0)) { assert.equal(run.font, effectiveFont(element.font), 'actual text portion font'); assert.ok(Math.abs(run.points - element.size * 0.75) < 0.01, 'actual text portion size'); fonts.push({ id: element.id, requested: element.font, imported: run.font, points: run.points }); }
          if (element.id !== 'body-overflow') assert.ok(normalizeText(page.text).includes(normalizeText(element.text)), `PDF visible Czech text ${element.id}`);
        }
        if (element.type === 'table') { assert.deepEqual(shape.rows, element.rows); const expectedRows = structuredClone(element.rows); if (element.id === 'table-budget') expectedRows[1][0] = 'Plzeň – UNO'; assert.deepEqual(saved.rows, expectedRows, 'all roundtrip table cells'); for (const cell of element.rows.flat()) assert.ok(normalizeText(page.text).includes(normalizeText(cell)), 'PDF table text'); }
        if (element.type === 'image') { assert.deepEqual(shape.pixels, { width: 96, height: 64 }); assert.ok(page.images.some(image => image.width === 96 && image.height === 64), 'PDF contains source PNG'); }
      }
      const officeBytes = await readFile(join(directory, `original-slide-${index + 1}.png`)); const office = PNG.sync.read(officeBytes);
      const browser = PNG.sync.read(await readFile(join(input, `${name}-${source.id}.png`)));
      assert.equal(office.width, deck.width); assert.equal(office.height, deck.height);
      const overflow = source.elements.find(e => e.id === 'body-overflow');
      let clipping = null;
      if (overflow) {
        const box = { x: overflow.x, y: overflow.y + overflow.h, w: overflow.w, h: deck.height - overflow.y - overflow.h };
        clipping = { region: box, browserInkPixels: foreground(browser, box, 1), officeInkPixels: foreground(office, box), status: 'recorded, not silently accepted' };
        clipping.matches = clipping.browserInkPixels === clipping.officeInkPixels;
      }
      slides.push({ slideID: source.id, geometry, fonts, officePNGHash: hash([...officeBytes]), browserComparison: pixelDifferences(browser, office), clipping });
    }
    const editedPDF = JSON.parse(await readFile(join(directory, 'edited-pdf', 'pdf-report.json'), 'utf8'));
    assert.ok(editedPDF.pages.some(page => page.text.includes('UNO: Upravený český text')));
    assert.ok(editedPDF.pages.some(page => page.text.includes('Plzeň – UNO')));
    report.candidates[name] = { nativeOpenEditSaveReopen: 'passed', pdfDimensionsTextTablePNG: 'passed',
      nativeRoundtrip: 'unchanged text, stable object names and counts retained; selected Czech text, shape and table edits retained',
      notes: 'Whole-shape CharFontName is a default and not used as evidence of run fonts. Actual text portions are inspected.', slides };
  }
  assert.deepEqual(report.candidates.html.slides.map(s => s.officePNGHash), report.candidates.typed.slides.map(s => s.officePNGHash), 'A/B Office slide pixels');
  report.overflowFidelity = report.candidates.html.slides.some(s => s.clipping && !s.clipping.matches) ? 'FAILED: Office draws long text below the browser-clipped text box; fit:none does not establish clipping parity.' : 'no mismatch observed in corpus';
  await writeFile(join(output, 'office-report.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ output, libreOffice: report.libreOffice, native: 'both passed actual UNO edits/save/reopen', overflowFidelity: report.overflowFidelity, powerPoint: report.powerPoint }, null, 2));
} catch (error) { report.failure = error.stack; await writeFile(join(output, 'office-report.failed.json'), JSON.stringify(report, null, 2)); throw error; }
