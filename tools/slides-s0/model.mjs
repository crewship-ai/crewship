import { createHash } from 'node:crypto';
import { PNG } from 'pngjs';

export const limits = Object.freeze({ slides: 20, elements: 50, text: 10000, bytes: 2 * 1024 * 1024, htmlBytes: 256 * 1024, assetBytes: 1024 * 1024, pixels: 2_000_000 });
export const fonts = ['DejaVu Sans', 'S0 Missing Font'];
export const canonical = value => JSON.stringify(value, (_, item) => item && typeof item === 'object' && !Array.isArray(item) ? Object.fromEntries(Object.keys(item).sort().map(key => [key, item[key]])) : item);
export const hash = value => createHash('sha256').update(canonical(value)).digest('hex');
export const clone = value => structuredClone(value);
export function check(ok, message) { if (!ok) throw new Error(message); }
export function keys(value, required) {
  check(value && typeof value === 'object' && !Array.isArray(value), 'expected object');
  check(Object.keys(value).sort().join('|') === [...required].sort().join('|'), 'unknown or missing field');
}
const id = value => check(typeof value === 'string' && /^[a-z][a-z0-9-]{0,63}$/.test(value), 'invalid stable ID');
const text = value => check(typeof value === 'string' && value.length <= limits.text && !/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/.test(value), 'invalid text');
const color = value => check(typeof value === 'string' && /^[A-F0-9]{6}$/.test(value), 'invalid color');
export function validate(deck) {
  check(Buffer.byteLength(JSON.stringify(deck)) <= limits.bytes, 'deck byte limit');
  keys(deck, ['id', 'version', 'width', 'height', 'theme', 'assets', 'slides']); id(deck.id);
  check(Number.isSafeInteger(deck.version) && deck.version >= 1, 'invalid version');
  check(deck.width === 960 && deck.height === 540, 'S0 requires 960x540');
  keys(deck.theme, ['background']); color(deck.theme.background);
  check(Array.isArray(deck.assets) && deck.assets.length <= 10, 'asset count');
  const assets = new Set();
  for (const asset of deck.assets) {
    keys(asset, ['id', 'sha256', 'data']); id(asset.id); check(!assets.has(asset.id), 'duplicate asset'); assets.add(asset.id);
    check(typeof asset.data === 'string' && /^[A-Za-z0-9+/]+={0,2}$/.test(asset.data), 'PNG base64 only');
    const data = Buffer.from(asset.data, 'base64');
    check(data.length <= limits.assetBytes && data.subarray(0, 8).equals(Buffer.from('89504e470d0a1a0a', 'hex')), 'PNG size/signature');
    // Bound dimensions BEFORE the decoder allocates the image buffer.
    check(data.length >= 24 && data.readUInt32BE(16) > 0 && data.readUInt32BE(20) > 0 && data.readUInt32BE(16) * data.readUInt32BE(20) <= limits.pixels, 'PNG pixel limit');
    PNG.sync.read(data, { checkCRC: true });
    check(asset.sha256 === createHash('sha256').update(data).digest('hex'), 'PNG digest');
  }
  check(Array.isArray(deck.slides) && deck.slides.length > 0 && deck.slides.length <= limits.slides, 'slide count');
  const ids = new Set([deck.id]);
  for (const slide of deck.slides) {
    keys(slide, ['id', 'notes', 'elements']); id(slide.id); text(slide.notes);
    check(!ids.has(slide.id), 'duplicate slide ID'); ids.add(slide.id);
    check(Array.isArray(slide.elements) && slide.elements.length <= limits.elements, 'element count');
    for (const element of slide.elements) {
      const fields = { text: ['text', 'font', 'size', 'color'], shape: ['shape', 'fill'], image: ['asset'], table: ['rows', 'font', 'size', 'color'] }[element.type];
      check(fields, 'unsupported element type'); keys(element, ['id', 'type', 'x', 'y', 'w', 'h', ...fields]); id(element.id);
      check(!ids.has(element.id), 'duplicate element ID'); ids.add(element.id);
      for (const field of ['x', 'y', 'w', 'h']) check(Number.isFinite(element[field]) && Math.abs(element[field]) <= 1920, 'invalid geometry');
      check(element.x >= 0 && element.y >= 0 && element.w > 0 && element.h > 0 && element.x + element.w <= deck.width && element.y + element.h <= deck.height, 'element outside slide');
      if (element.type === 'text' || element.type === 'table') {
        check(fonts.includes(element.font), 'font whitelist'); check(Number.isFinite(element.size) && element.size >= 8 && element.size <= 72, 'font size'); color(element.color);
      }
      if (element.type === 'text') text(element.text);
      if (element.type === 'image') check(assets.has(element.asset), 'unknown asset');
      if (element.type === 'shape') { check(['rect', 'ellipse'].includes(element.shape), 'shape whitelist'); color(element.fill); }
      if (element.type === 'table') {
        check(Array.isArray(element.rows) && element.rows.length > 0 && element.rows.length <= 10, 'table rows');
        const cols = element.rows[0]?.length; check(cols > 0 && cols <= 6, 'table columns');
        for (const row of element.rows) { check(Array.isArray(row) && row.length === cols, 'rectangular table'); row.forEach(text); }
      }
    }
  }
  return deck;
}

export function fixture() {
  const png = new PNG({ width: 96, height: 64 });
  for (let y = 0; y < png.height; y++) for (let x = 0; x < png.width; x++) {
    const offset = (png.width * y + x) * 4; png.data.set([x < 48 ? 35 : 236, y < 32 ? 125 : 69, 180, 255], offset);
  }
  const data = PNG.sync.write(png);
  const t = (id, text, y, extra = {}) => ({ id, type: 'text', x: 48, y, w: 864, h: 72, text, font: 'DejaVu Sans', size: 28, color: '203040', ...extra });
  return validate({ id: 'deck-s0', version: 1, width: 960, height: 540, theme: { background: 'FFFFFF' }, assets: [{ id: 'asset-png', sha256: createHash('sha256').update(data).digest('hex'), data: data.toString('base64') }], slides: [
    { id: 'slide-czech', notes: 'Čeština: poznámky přednášejícího.', elements: [t('title-czech', 'Příliš žluťoučký kůň úpěl ďábelské ódy', 48), t('body-czech', 'Společný obsah: člověk i agent mění stejný snímek.', 160, { size: 22 })] },
    { id: 'slide-overflow', notes: 'Úmyslné přetečení; nezmenšovat potichu.', elements: [t('title-overflow', 'Dlouhý text a přetečení', 48), t('body-overflow', 'Dlouhý český odstavec má zachovat obsah a přiznat přetečení. '.repeat(40), 160, { h: 96, size: 24 })] },
    { id: 'slide-table', notes: 'Nativní editovatelná tabulka.', elements: [t('title-table', 'Jednoduchá tabulka', 48), { id: 'table-budget', type: 'table', x: 48, y: 160, w: 864, h: 180, rows: [['Město', 'Výsledek'], ['Praha', '42'], ['Brno', '27']], font: 'DejaVu Sans', size: 22, color: '203040' }] },
    { id: 'slide-image', notes: 'Lokální PNG a nativní tvary.', elements: [t('title-image', 'PNG a tvary', 48), { id: 'image-local', type: 'image', x: 48, y: 160, w: 288, h: 192, asset: 'asset-png' }, { id: 'shape-rect', type: 'shape', x: 400, y: 160, w: 180, h: 120, shape: 'rect', fill: '2B88A8' }, { id: 'shape-ellipse', type: 'shape', x: 650, y: 160, w: 160, h: 160, shape: 'ellipse', fill: 'ECA545' }] },
    { id: 'slide-font', notes: 'Font není vložen; fallback musí být viditelný.', elements: [t('title-font', 'Chybějící font', 48), t('body-font', 'Čeština zůstává editovatelná: ěščřžýáíé.', 160, { font: 'S0 Missing Font' })] },
  ] });
}

export class Conflict extends Error { constructor(message) { super(message); this.name = 'Conflict'; } }
// Canonical state belongs to the adapter. Projection is ephemeral, never a second writable store.
export class Session {
  constructor(adapter, state) { this.adapter = adapter; this.state = adapter.normalize(state); }
  read() { return this.adapter.project(this.state); }
  target(slideID, elementID) { const slide = this.read().slides.find(s => s.id === slideID); check(slide, 'slide missing'); const element = slide.elements.find(e => e.id === elementID); check(element, 'element missing'); return element; }
  patch(request) {
    keys(request, ['slideID', 'elementID', 'ifVersion', 'ifHash', 'changes']);
    const { slideID, elementID, ifVersion, ifHash, changes } = request;
    const deck = this.read(); const target = this.target(slideID, elementID);
    if (ifVersion !== deck.version || ifHash !== hash(target)) throw new Conflict('stale version or target hash');
    check(Object.keys(changes).length > 0 && Object.keys(changes).every(k => ['text', 'x', 'y', 'w', 'h'].includes(k)), 'patch whitelist');
    if ('text' in changes) check(target.type === 'text', 'text target required');
    // Mutate each representation directly; validate and atomically swap only on success.
    this.state = this.adapter.patch(this.state, slideID, elementID, changes);
  }
  reorder(order, ifVersion, ifOrderHash) {
    const deck = this.read(); const before = deck.slides.map(s => s.id);
    if (ifVersion !== deck.version || ifOrderHash !== hash(before)) throw new Conflict('stale order');
    check(Array.isArray(order) && order.length === before.length && new Set(order).size === before.length && order.every(id => before.includes(id)), 'order permutation');
    this.state = this.adapter.reorder(this.state, order);
  }
  anchor(slideID, elementID) { this.target(slideID, elementID); return { slideID, elementID, originalVersion: this.read().version }; }
  resolve(anchor) { const slide = this.read().slides.find(s => s.id === anchor.slideID); return slide?.elements.some(e => e.id === anchor.elementID) ? 'attached' : 'detached'; }
}
