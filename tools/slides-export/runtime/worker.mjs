import { readFile, writeFile, rename, stat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkedProfile } from './profile.mjs';
import PptxGenJS from 'pptxgenjs';
import PDFDocument from 'pdfkit';
import { create as createFont } from 'fontkit';

export const LIMITS = Object.freeze({ envelope: 64 << 20, output: 64 << 20, report: 1 << 20, slides: 100, elements: 1000, cells: 10000, assets: 32, assetBytes: 32 << 20, decodedPixels: 33554432, timeoutSeconds: 120, concurrency: 1, nodeHeapMiB: 512 });
const root = fileURLToPath(new URL('.', import.meta.url));
const digest = data => createHash('sha256').update(data).digest('hex');
export const stable = value => Array.isArray(value) ? value.map(stable) : value && typeof value === 'object' ? Object.fromEntries(Object.keys(value).sort().map(key => [key, stable(value[key])])) : value;
export function canonicalJSON(value) {
 let count = 0;
 const check = (v, depth) => {
  if (++count > 100000 || depth > 32) throw new Error('Report structural budget');
  if (typeof v === 'string') for (let i = 0; i < v.length; i++) { const c = v.charCodeAt(i); if (c >= 0xd800 && c <= 0xdbff) { const low = v.charCodeAt(++i); if (!(low >= 0xdc00 && low <= 0xdfff)) throw new Error('Report Unicode refused'); } else if (c >= 0xdc00 && c <= 0xdfff) throw new Error('Report Unicode refused'); }
  if (typeof v === 'number' && !Number.isFinite(v)) throw new Error('Report number refused');
  if (Array.isArray(v)) v.forEach(x => check(x, depth + 1));
  else if (v && typeof v === 'object') for (const [key, item] of Object.entries(v)) { if (!/^[\x00-\x7f]*$/.test(key) || /^[0-9]+$/.test(key)) throw new Error('Report key refused'); check(item, depth + 1); }
 };
 check(value, 0);
 return JSON.stringify(stable(value)).replace(/\u2028/g, '\\u2028').replace(/\u2029/g, '\\u2029');
}
export const reportHash = report => { const value = { ...report }; delete value.report_hash; return digest('crewship-slides-export-report-v2\n' + canonicalJSON(value)); };
const color = value => value.replace(/^#/, '');
const points = pixels => pixels * 0.75;
const box = element => ({ x: element.x, y: element.y, width: element.width, height: element.height });

export async function fontProfile() {
 const { manifest, fingerprint } = await checkedProfile();
 const provenance = JSON.parse(await readFile(join(root, 'fonts/provenance.json'), 'utf8'));
 const bytes = await readFile(join(root, 'fonts/DejaVuSans.ttf')), license = await readFile(join(root, 'fonts/LICENSE-DejaVu.txt'));
 if (bytes.length !== provenance.font_bytes || digest(bytes) !== provenance.font_sha256 || digest(license) !== provenance.license_sha256) throw new Error('Packaged font/license provenance mismatch');
 const versions = {};
 for (const name of ['pptxgenjs', 'pdfkit', 'fontkit']) versions[name] = JSON.parse(await readFile(join(root, 'node_modules', name, 'package.json'), 'utf8')).version;
 return { provenance, bytes, manifest, fingerprint, font: createFont(bytes), versions };
}
function textOptions(doc, size, width, height, align) {
 doc.font('ExportFont').fontSize(points(size));
 return { width: points(width), height: points(height), align, lineGap: points(size * 1.2) - doc.currentLineHeight(true), paragraphGap: 0, ellipsis: false };
}
export function reportBytes(report) {
 const bytes = Buffer.from(JSON.stringify(report, null, 2) + "\n");
 if (bytes.length > LIMITS.report) throw new Error("Export report exceeds1MiB serialized bytes");
 return bytes;
}
export async function preflight(input) {
 const profile = await fontProfile(), deck = input.deck;
 // The Go entry validates canonical model/asset digests and all aggregate
 // budgets. This lower boundary still refuses obvious direct worker misuse.
 if (input.schema_version !== 1 || !deck || ![1, 2].includes(deck.schema_version) || !Array.isArray(deck.slides) || deck.slides.length > LIMITS.slides || !Array.isArray(input.assets) || input.assets.length > LIMITS.assets) throw new Error('Validated bounded Go export envelope required');
 const report = { protocol_version: 2, page_id: deck.deck_id, revision: input.revision, content_hash: input.content_hash, deck_schema_version: deck.schema_version,
  generator: { profile_sha256: profile.fingerprint, mapping: 'crewship-slides-export-v1', pdf_layout: 'pdfkit-fixed-box-lineheight-1.2-v1', pptx_layout: 'native-office-absolute-lineheight-1.2-v2', versions: profile.versions, node: process.version, source_sha256: { worker: digest(await readFile(join(root, 'worker.mjs'))), admission: profile.manifest.admission_sha256 } }, font_profile: profile.provenance, limits: LIMITS,
  native_text: [], native_shapes: [], native_tables: [], source_raster_images: [], rasterized_elements: [], font_substitutions: [], issues: [], measurements: [],
  qualification: { pdf: 'server-generated fixed-page geometry; measured with the same PDFKit font/layout used for output', pptx: 'native editable mapping; application layout must be qualified separately', pptx_fonts: 'not embedded; recipients need the pinned font profile or separate substitution qualification', power_point: 'incomplete; no actual PowerPoint environment qualified' } };
 const doc = new PDFDocument({ size: [points(deck.width), points(deck.height)], margin: 0 }); doc.on('data', () => {}); doc.registerFont('ExportFont', profile.bytes);
 const checkText = (slideID, elementID, text, style, bounds, cell) => {
  if (style.font_family !== profile.provenance.family) report.font_substitutions.push({ slide_id: slideID, element_id: elementID, ...(cell ? { cell } : {}), requested: style.font_family, resolved: profile.provenance.family, reason: 'not_in_packaged_export_font_profile' });
  const missing = [...new Set([...text].filter(char => char !== '\n' && char !== '\t' && !profile.font.hasGlyphForCodePoint(char.codePointAt(0))))];
  if (missing.length) report.issues.push({ severity: 'error', kind: 'missing_glyph', slide_id: slideID, element_id: elementID, ...(cell ? { cell } : {}), code_points: missing.slice(0, 32).map(char => `U+${char.codePointAt(0).toString(16).toUpperCase()}`), message: 'Packaged fallback cannot render these glyphs; choose supported text/font before export.' });
  const options = textOptions(doc, style.font_size, bounds.width, bounds.height, style.align ?? 'left');
  const wide = [...new Set([...text])].filter(char => char !== "\n" && char !== "\t" && doc.widthOfString(char) > points(bounds.width) + 0.01);
  if (wide.length) report.issues.push({ severity: "error", kind: "horizontal_overflow", slide_id: slideID, element_id: elementID, ...(cell ? { cell } : {}), code_points: wide.slice(0, 32).map(char => `U+${char.codePointAt(0).toString(16).toUpperCase()}`), message: "A glyph is wider than its fixed box; resize before export." });
  const measured = doc.heightOfString(text || ' ', options);
  report.measurements.push({ slide_id: slideID, element_id: elementID, ...(cell ? { cell } : {}), engine: 'PDFKit', box_height_points: points(bounds.height), measured_height_points: measured });
  if (measured > points(bounds.height) + 0.01) report.issues.push({ severity: 'error', kind: 'text_overflow', slide_id: slideID, element_id: elementID, ...(cell ? { cell } : {}), message: 'Text exceeds the declared box in the explicit PDF layout. Resize or reduce text; PPTX clipping parity is not assumed.' });
 };
 for (const slideID of deck.slide_order) {
  const slide = deck.slides.find(item => item.id === slideID);
  for (const e of slide.elements) {
   const identity = { slide_id: slide.id, element_id: e.id };
   if (e.type === 'text') { report.native_text.push(identity); checkText(slide.id, e.id, e.text.value, e.text, box(e)); }
   else if (e.type === 'shape') report.native_shapes.push(identity);
   else if (e.type === 'table') {
    report.native_tables.push(identity);
    for (let row = 0; row < e.table.cells.length; row++) for (let col = 0; col < e.table.cells[row].length; col++) checkText(slide.id, e.id, e.table.cells[row][col], e.table, { width: e.width / e.table.cells[0].length, height: e.height / e.table.cells.length }, { row, col });
   } else if (e.type === 'image') report.source_raster_images.push({ ...identity, asset_id: e.image.asset_id, expected_sha256: e.image.expected_sha256 });
   else throw new Error('Unsupported canonical element');
  }
 }
 doc.end();
 report.can_export = report.issues.length === 0;
 // Report hash is an integrity/preflight binding, never authorization. Font
 // source+exact bytes/license and engine/layout versions participate.
 report.report_hash = reportHash(report);
 reportBytes(report);
 return { report, profile };
}
function asset(input, e) {
 const a = input.assets.find(item => item.asset_id === e.image.asset_id && item.expected_sha256 === e.image.expected_sha256);
 if (!a) throw new Error('Verified private asset missing');
 return a;
}
export async function renderPptx(input, profile) {
 const deck = input.deck, pptx = new PptxGenJS();
 pptx.defineLayout({ name: 'CREWSHIP', width: deck.width / 96, height: deck.height / 96 }); pptx.layout = 'CREWSHIP';
 pptx.author = 'Crewship'; pptx.title = `Slides revision ${input.revision}`; pptx.lang = 'cs-CZ';
 for (const id of deck.slide_order) {
  const source = deck.slides.find(item => item.id === id), slide = pptx.addSlide(); slide.background = { color: color(deck.theme.background) }; slide.addNotes(`[${id}] ${source.notes}`);
  for (const e of source.elements) {
   const bounds = { x: e.x / 96, y: e.y / 96, w: e.width / 96, h: e.height / 96, objectName: e.id };
   const style = e.text ?? e.table;
   const text = style ? { ...bounds, fontFace: profile.provenance.family, fontSize: points(style.font_size), color: color(style.color), margin: 0, valign: 'top', fit: 'none', breakLine: false, paraSpaceAfter: 0, lineSpacing: points(style.font_size * 1.2), lang: 'cs-CZ', align: style.align ?? 'left' } : null;
   if (e.type === 'text') slide.addText(e.text.value, text);
   if (e.type === 'shape') slide.addShape(e.shape.kind === 'ellipse' ? pptx.ShapeType.ellipse : pptx.ShapeType.rect, { ...bounds, fill: { color: color(e.shape.fill) }, line: { color: color(e.shape.stroke), width: points(e.shape.stroke_width), ...(e.shape.stroke_width === 0 ? { transparency: 100 } : {}) } });
   if (e.type === 'table') slide.addTable(e.table.cells, { ...text, autoPage: false, rowH: e.height / 96 / e.table.cells.length, border: { type: 'solid', color: color(e.table.border), pt: 0.75 }, fill: color(e.table.fill) });
   if (e.type === 'image') { const a = asset(input, e); slide.addImage({ ...bounds, ...pptxgenContain(a, bounds), data: `${a.mime};base64,${a.data}` }); }
  }
 }
 const bytes = await pptx.write({ outputType: 'nodebuffer' });
 if (bytes.length > LIMITS.output) throw new Error('PPTX exceeds64MiB');
 return bytes;
}
function pptxgenContain(a, b) {
 const ratio = Math.min(b.w / a.width, b.h / a.height), w = a.width * ratio, h = a.height * ratio;
 return { x: b.x + (b.w - w) / 2, y: b.y + (b.h - h) / 2, w, h };
}
export async function renderPdf(input, profile) {
 const deck = input.deck, doc = new PDFDocument({ autoFirstPage: false, margin: 0, info: { Title: `Slides revision ${input.revision}`, Author: 'Crewship' } });
 doc.registerFont('ExportFont', profile.bytes);
 const chunks = []; let length = 0;
 const completed = new Promise((resolve, reject) => { doc.on('data', chunk => { length += chunk.length; if (length > LIMITS.output) { doc.destroy(new Error('PDF exceeds64MiB')); return; } chunks.push(chunk); }); doc.on('error', reject); doc.on('end', () => resolve(Buffer.concat(chunks))); });
 for (const id of deck.slide_order) {
  const source = deck.slides.find(item => item.id === id); doc.addPage({ size: [points(deck.width), points(deck.height)], margin: 0 }); doc.rect(0, 0, points(deck.width), points(deck.height)).fill(deck.theme.background);
  for (const e of source.elements) {
   if (e.type === 'text') { const options = textOptions(doc, e.text.font_size, e.width, e.height, e.text.align); doc.fillColor(e.text.color).text(e.text.value, points(e.x), points(e.y), options); }
   if (e.type === 'shape') { doc.lineWidth(points(e.shape.stroke_width)); if (e.shape.kind === 'ellipse') doc.ellipse(points(e.x + e.width / 2), points(e.y + e.height / 2), points(e.width / 2), points(e.height / 2)); else doc.rect(points(e.x), points(e.y), points(e.width), points(e.height)); if (e.shape.stroke_width === 0) doc.fill(e.shape.fill); else doc.fillAndStroke(e.shape.fill, e.shape.stroke); }
   if (e.type === 'image') { const a = asset(input, e); doc.image(Buffer.from(a.data, 'base64'), points(e.x), points(e.y), { fit: [points(e.width), points(e.height)], align: 'center', valign: 'center' }); }
   if (e.type === 'table') {
    const w = e.width / e.table.cells[0].length, h = e.height / e.table.cells.length;
    for (let r = 0; r < e.table.cells.length; r++) for (let c = 0; c < e.table.cells[r].length; c++) { const x = e.x + c * w, y = e.y + r * h; doc.lineWidth(0.75).rect(points(x), points(y), points(w), points(h)).fillAndStroke(e.table.fill, e.table.border); const options = textOptions(doc, e.table.font_size, w, h, 'left'); doc.fillColor(e.table.color).text(e.table.cells[r][c], points(x), points(y), options); }
   }
  }
 }
 doc.end(); return completed;
}
async function main() {
 if (process.argv[2] !== '--output' || !process.argv[3]) throw new Error('Owned output directory required');
 const output = process.argv[3], info = await stat(output); if (!info.isDirectory()) throw new Error('Output directory required');
 let length = 0; const chunks = [];
 for await (const chunk of process.stdin) { length += chunk.length; if (length > LIMITS.envelope) throw new Error('Envelope exceeds64MiB'); chunks.push(chunk); }
 const input = JSON.parse(Buffer.concat(chunks).toString('utf8')), { report, profile } = await preflight(input);
 await writeFile(join(output, 'report.json'), reportBytes(report), { flag: 'wx', mode: 0o600 });
 if (!report.can_export) { process.stdout.write(JSON.stringify(report)); process.exitCode = 2; return; }
 // Build both outputs before publishing either, never mistake a half pair for
 // successful qualification. No caller-controlled output filename is accepted.
 const pptx = await renderPptx(input, profile), pdf = await renderPdf(input, profile);
 for (const [name, bytes] of [['deck.pptx', pptx], ['deck.pdf', pdf]]) { const temporary = join(output, name + '.tmp'); await writeFile(temporary, bytes, { flag: 'wx', mode: 0o600 }); await rename(temporary, join(output, name)); }
 process.stdout.write(JSON.stringify({ page_id: report.page_id, revision: report.revision, content_hash: report.content_hash, report_hash: report.report_hash, pptx_bytes: pptx.length, pdf_bytes: pdf.length }));
}
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) main().catch(error => { process.stderr.write(`${error.message}\n`); process.exitCode = 1; });
