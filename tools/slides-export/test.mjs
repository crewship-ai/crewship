import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import JSZip from 'jszip';
import { DOMParser } from '@xmldom/xmldom';
import { LIMITS, fontProfile, preflight, renderPdf, renderPptx, reportBytes } from './worker.mjs';
const corpus = async name => JSON.parse(await readFile(new URL(`./testdata/${name}.json`, import.meta.url), 'utf8'));
const sha = data => createHash('sha256').update(data).digest('hex');
const text = node => [...Array(node.childNodes.length)].map((_, i) => node.childNodes.item(i)).map(child => child.nodeName === 'a:t' ? child.textContent : child.nodeName === 'a:br' ? '\n' : child.childNodes ? text(child) : '').join('');
function named(document, kind, id) { return [...Array(document.getElementsByTagName(kind).length)].map((_, i) => document.getElementsByTagName(kind).item(i)).find(node => [...Array(node.getElementsByTagName('p:cNvPr').length)].some((_, i) => node.getElementsByTagName('p:cNvPr').item(i).getAttribute('name') === id)); }
function shapeText(shape) { const paragraphs = shape.getElementsByTagName('a:p'); return [...Array(paragraphs.length)].map((_, i) => text(paragraphs.item(i))).join('\n'); }

test('packaged font and licence are the pinned official release bytes', async () => {
 const p = await fontProfile();
 assert.equal(p.provenance.font_sha256, '7da195a74c55bef988d0d48f9508bd5d849425c1770dba5d7bfc6ce9ed848954');
 assert.equal(p.provenance.license_sha256, '7a083b136e64d064794c3419751e5c7dd10d2f64c108fe5ba161eae5e5958a93');
 assert.equal(sha(p.bytes), p.provenance.font_sha256); assert.equal(p.font.familyName, 'DejaVu Sans');
});
test('same pinned revision yields a deterministic version/font-bound report', async () => {
 const input = await corpus('supported'), a = await preflight(input), b = await preflight(input);
 assert.equal(a.report.report_hash, b.report.report_hash); assert.equal(a.report.can_export, true); assert.equal(a.report.native_text.length, 4); assert.equal(a.report.native_tables.length, 1); assert.equal(a.report.source_raster_images.length, 1); assert.deepEqual(a.report.rasterized_elements, []);
 assert.equal(a.report.font_substitutions.length, 1); assert.equal(a.report.font_substitutions[0].requested, 'Missing Export Font'); assert.equal(a.report.font_substitutions[0].resolved, 'DejaVu Sans');
 const changed = structuredClone(input); changed.revision++;
 assert.notEqual((await preflight(changed)).report.report_hash, a.report.report_hash);
});
test('actual serialized report bytes, including pretty whitespace/newline, obey cap', () => {
 const value = { data: Array.from({ length: 180000 }, () => 0) };
 assert.ok(Buffer.byteLength(JSON.stringify(value)) < LIMITS.report);
 assert.throws(() => reportBytes(value), /serialized bytes/);
});
test('vertical overflow, missing glyph and single overwide glyph refuse export', async () => {
 for (const [name, kind] of [['overflow', 'text_overflow'], ['missing_glyph', 'missing_glyph'], ['wide_glyph', 'horizontal_overflow']]) {
  const { report } = await preflight(await corpus(name)); assert.equal(report.can_export, false); assert.ok(report.issues.some(item => item.kind === kind), name);
  if (name === 'wide_glyph') { const m = report.measurements.find(item => item.element_id === 'czech_title'); assert.ok(m.measured_height_points <= m.box_height_points, 'horizontal-only overflow regression'); }
 }
});
test('real PPTX native objects preserve exact LF/text/table/IDs and contain no external relationships', async () => {
 const input = await corpus('supported'), { report, profile } = await preflight(input); assert.equal(report.can_export, true);
 const bytes = await renderPptx(input, profile); assert.ok(bytes.length > 1000 && bytes.length < LIMITS.output); const zip = await JSZip.loadAsync(bytes);
 const files = Object.keys(zip.files).filter(name => /^ppt\/slides\/slide\d+\.xml$/.test(name)); assert.equal(files.length, 5);
 for (let index = 0; index < input.deck.slide_order.length; index++) {
  const slide = input.deck.slides.find(item => item.id === input.deck.slide_order[index]);
  const xml = new DOMParser().parseFromString(await zip.file(`ppt/slides/slide${index + 1}.xml`).async('string'), 'application/xml');
  for (const e of slide.elements) {
   if (e.type === 'text') { const shape = named(xml, 'p:sp', e.id); assert.ok(shape, e.id); assert.equal(shapeText(shape), e.text.value); }
   if (e.type === 'shape') { const shape = named(xml, 'p:sp', e.id); assert.ok(shape, e.id); if (e.shape.stroke_width === 0) assert.ok(shape.toString().includes('a:alpha val="0"'), 'zero-stroke native border explicitly transparent'); }
   if (e.type === 'table') { const frame = named(xml, 'p:graphicFrame', e.id); assert.ok(frame, e.id); const extent = frame.getElementsByTagName('a:ext').item(0); assert.equal(Number(extent.getAttribute('cx')), e.width * 9525, 'fractional-inch table width must not be truncated by scalar colW'); const rows = frame.getElementsByTagName('a:tr'); assert.equal(rows.length, e.table.cells.length); for (let r = 0; r < rows.length; r++) { const cells = rows.item(r).getElementsByTagName('a:tc'); for (let c = 0; c < cells.length; c++) assert.equal(shapeText(cells.item(c)), e.table.cells[r][c]); } }
   if (e.type === 'image') assert.ok(named(xml, 'p:pic', e.id), e.id);
  }
 }
 for (const name of Object.keys(zip.files).filter(name => name.endsWith('.rels'))) assert.ok(!(await zip.file(name).async('string')).includes('TargetMode="External"'));
});
test('near-boundary text uses absolute PPTX line spacing, not font-native multiplier', async () => {
 const input = await corpus('near_boundary'), { report, profile } = await preflight(input); assert.equal(report.can_export, true);
 const zip = await JSZip.loadAsync(await renderPptx(input, profile));
 const xml = await zip.file('ppt/slides/slide2.xml').async('string'); assert.ok(xml.includes('a:spcPts val="1440"')); assert.ok(!xml.includes('a:spcPct'));
});
test('actual PDF generator produces five fixed-size PDF pages', async () => {
 const input = await corpus('supported'), { profile } = await preflight(input), bytes = await renderPdf(input, profile);
 assert.ok(bytes.subarray(0, 5).equals(Buffer.from('%PDF-'))); assert.ok(bytes.length < LIMITS.output);
 const objects = bytes.toString('latin1'); assert.equal([...objects.matchAll(/\/Type \/Page\b/g)].length, 5); assert.ok(objects.includes('/MediaBox [0 0 720 405]'));
 // Parsed fonts/text/geometry/pixels and actual LibreOffice editing are separate
 // application qualification, never inferred from these package structure checks.
});
