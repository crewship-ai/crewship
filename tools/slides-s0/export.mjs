import PptxGenJS from 'pptxgenjs';
import JSZip from 'jszip';
import { readFile } from 'node:fs/promises';
import { effectiveFont } from './render.mjs';
import { validate, check } from './model.mjs';
import { escape } from './html.mjs';
export async function exportPptx(deck, file, overflow = []) {
  validate(deck);
  const pptx = new PptxGenJS();
  pptx.defineLayout({ name: 'S0', width: deck.width / 96, height: deck.height / 96 }); pptx.layout = 'S0';
  pptx.author = 'Crewship S0 fixture'; pptx.subject = 'Isolated format experiment'; pptx.title = 'Slides S0'; pptx.lang = 'cs-CZ';
  const report = { nativeText: [], nativeShapes: [], nativeTables: [], sourceRasterImages: [], rasterizedElements: [], fontSubstitutions: [], overflow, notes: 'Overflow is preserved, never silently resized; browser measurements are not PowerPoint layout evidence.' };
  for (const source of deck.slides) {
    const slide = pptx.addSlide(); slide.background = { color: deck.theme.background }; slide.addNotes(`[${source.id}] ${source.notes}`);
    for (const e of source.elements) {
      const box = { x: e.x / 96, y: e.y / 96, w: e.w / 96, h: e.h / 96, objectName: e.id };
      const text = { ...box, fontFace: effectiveFont(e.font), fontSize: e.size * 72 / 96, color: e.color, margin: 0, breakLine: false, valign: 'top', lang: 'cs-CZ', fit: 'none', paraSpaceAfter: 0, lineSpacingMultiple: 1.2 };
      if (e.font && effectiveFont(e.font) !== e.font) report.fontSubstitutions.push({ id: e.id, requested: e.font, effective: effectiveFont(e.font) });
      if (e.type === 'text') { slide.addText(e.text, text); report.nativeText.push(e.id); }
      if (e.type === 'shape') { slide.addShape(e.shape === 'ellipse' ? pptx.ShapeType.ellipse : pptx.ShapeType.rect, { ...box, fill: { color: e.fill }, line: { color: e.fill, transparency: 100 } }); report.nativeShapes.push(e.id); }
      if (e.type === 'image') { slide.addImage({ ...box, data: `image/png;base64,${deck.assets.find(a => a.id === e.asset).data}` }); report.sourceRasterImages.push(e.id); }
      if (e.type === 'table') {
        slide.addTable(e.rows, { ...text, autoPage: false, rowH: e.h / 96 / e.rows.length, colW: e.w / 96 / e.rows[0].length, border: { type: 'solid', color: '203040', pt: 0.75 }, fill: 'FFFFFF' }); report.nativeTables.push(e.id);
      }
    }
  }
  await pptx.writeFile({ fileName: file });
  return report;
}
export async function inspectPptx(deck, file) {
  const zip = await JSZip.loadAsync(await readFile(file)); const slides = Object.keys(zip.files).filter(p => /^ppt\/slides\/slide\d+\.xml$/.test(p));
  check(slides.length === deck.slides.length, 'OOXML slide count');
  const detail = [];
  for (let i = 0; i < deck.slides.length; i++) {
    const xml = await zip.file(`ppt/slides/slide${i + 1}.xml`).async('string');
    check(!xml.includes('TargetMode="External"'), 'external slide resource');
    const source = deck.slides[i];
    const notes = await zip.file(`ppt/notesSlides/notesSlide${i + 1}.xml`).async('string'); check(notes.includes(escape(source.id)) && notes.includes(escape(source.notes)), 'speaker notes and slide ID');
    for (const e of source.elements) {
      if (e.type === 'text') { check(xml.includes('name="' + e.id + '"') && xml.includes(`<a:t>${escape(e.text)}</a:t>`), `native text ${e.id}`); }
      if (e.type === 'shape') check(xml.includes('name="' + e.id + '"'), `native shape ${e.id}`);
      if (e.type === 'table') { check(xml.includes('<a:tbl>'), 'native table'); for (const cell of e.rows.flat()) check(xml.includes(`<a:t>${escape(cell)}</a:t>`), 'native table cell'); }
      if (e.type === 'image') check(xml.includes('<p:pic>'), 'PNG picture');
    }
    detail.push({ slideID: source.id, textRuns: (xml.match(/<a:t>/g) ?? []).length, nativeShapes: (xml.match(/<p:sp>/g) ?? []).length, tables: (xml.match(/<a:tbl>/g) ?? []).length, images: (xml.match(/<p:pic>/g) ?? []).length });
  }
  for (const name of Object.keys(zip.files).filter(n => n.endsWith('.rels'))) check(!(await zip.file(name).async('string')).includes('TargetMode="External"'), 'external OOXML relationship');
  return { kind: 'static OOXML structure only; not application opening or editability acceptance', slides: detail };
}
