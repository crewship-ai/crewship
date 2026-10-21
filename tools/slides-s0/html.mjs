import { parseFragment, serialize } from 'parse5';
import { check, clone, keys, limits, validate } from './model.mjs';
export const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
const attr = node => Object.fromEntries(node.attrs.map(a => [a.name, a.value]));
function parse(source) {
  check(typeof source === 'string' && Buffer.byteLength(source) <= limits.htmlBytes, 'HTML size limit');
  const errors = [];
  const root = parseFragment(source, { onParseError: e => errors.push(e.code) });
  check(errors.length === 0, `malformed HTML: ${errors.join(',')}`);
  let count = 0;
  function visit(node, depth) {
    check(depth <= 6 && ++count <= 2000, 'HTML depth/node limit');
    if (node.nodeName === '#text') return;
    check(node === root || ['section', 'p', 'div', 'img', 'table', 'tbody', 'tr', 'td'].includes(node.tagName), 'HTML tag whitelist');
    if (node.attrs) {
      check(node.namespaceURI === 'http://www.w3.org/1999/xhtml', 'HTML namespace');
      check(node.attrs.every(a => !a.namespace && ['data-id', 'data-shape', 'data-asset', 'style'].includes(a.name)), 'HTML attribute whitelist');
    }
    node.childNodes?.forEach(child => visit(child, depth + 1));
  }
  visit(root, 0); return root;
}
function onlyElements(node) { return node.childNodes.filter(n => n.nodeName !== '#text' || n.value.trim()).map(n => { check(n.tagName, 'unexpected text'); return n; }); }
function plain(node) { check(node.childNodes.every(n => n.nodeName === '#text'), 'plain text only'); return node.childNodes.map(n => n.value).join(''); }
function css(node, text) {
  const attrs = attr(node); const style = {};
  for (const item of (attrs.style ?? '').split(';').filter(Boolean)) {
    const parts = item.split(':'); check(parts.length === 2, 'CSS declaration'); const key = parts[0].trim(); const value = parts[1].trim();
    check(!Object.hasOwn(style, key), 'duplicate CSS'); style[key] = value;
  }
  keys(style, ['left', 'top', 'width', 'height', ...(text ? ['font-family', 'font-size', 'color'] : node.tagName === 'div' ? ['background-color'] : [])]);
  function numeric(key) { check(/^\d+(?:\.\d+)?px$/.test(style[key]), 'CSS pixels only'); return Number(style[key].slice(0, -2)); }
  const geometry = { x: numeric('left'), y: numeric('top'), w: numeric('width'), h: numeric('height') };
  if (text) {
    check(/^#[A-F0-9]{6}$/.test(style.color), 'CSS color');
    return { ...geometry, font: style['font-family'], size: numeric('font-size'), color: style.color.slice(1) };
  }
  if (node.tagName === 'div') { check(/^#[A-F0-9]{6}$/.test(style['background-color']), 'CSS shape color'); geometry.fill = style['background-color'].slice(1); }
  return geometry;
}
function element(node) {
  const a = attr(node); const base = { id: a['data-id'] };
  if (node.tagName === 'p') { keys(a, ['data-id', 'style']); return { ...base, type: 'text', ...css(node, true), text: plain(node) }; }
  if (node.tagName === 'div') { keys(a, ['data-id', 'data-shape', 'style']); check(node.childNodes.length === 0, 'empty shape required'); return { ...base, type: 'shape', shape: a['data-shape'], ...css(node, false) }; }
  if (node.tagName === 'img') { keys(a, ['data-id', 'data-asset', 'style']); return { ...base, type: 'image', asset: a['data-asset'], ...css(node, false) }; }
  check(node.tagName === 'table', 'element tag'); keys(a, ['data-id', 'style']); const bodies = onlyElements(node);
  check(bodies.length === 1 && bodies[0].tagName === 'tbody' && bodies[0].attrs.length === 0, 'table tbody');
  const rows = onlyElements(bodies[0]).map(tr => { check(tr.tagName === 'tr' && tr.attrs.length === 0, 'table tr'); return onlyElements(tr).map(td => { check(td.tagName === 'td' && td.attrs.length === 0, 'table td'); return plain(td); }); });
  return { ...base, type: 'table', ...css(node, true), rows };
}
function slideNodes(source, slideID) {
  const root = parse(source); const children = onlyElements(root);
  check(children.length === 1 && children[0].tagName === 'section', 'one section'); const section = children[0];
  keys(attr(section), ['data-id']); check(attr(section)['data-id'] === slideID, 'slide identity mismatch');
  return { root, section, elements: onlyElements(section) };
}
function style(element) {
  const values = { left: `${element.x}px`, top: `${element.y}px`, width: `${element.w}px`, height: `${element.h}px` };
  if (element.type === 'text' || element.type === 'table') Object.assign(values, { 'font-family': element.font, 'font-size': `${element.size}px`, color: `#${element.color}` });
  if (element.type === 'shape') values['background-color'] = `#${element.fill}`;
  return Object.entries(values).map(([k, v]) => `${k}:${v}`).join(';');
}
function encodeElement(e) {
  const common = `data-id="${escape(e.id)}" style="${escape(style(e))}"`;
  if (e.type === 'text') return `<p ${common}>${escape(e.text)}</p>`;
  if (e.type === 'image') return `<img ${common} data-asset="${escape(e.asset)}">`;
  if (e.type === 'shape') return `<div ${common} data-shape="${escape(e.shape)}"></div>`;
  return `<table ${common}><tbody>${e.rows.map(row => `<tr>${row.map(cell => `<td>${escape(cell)}</td>`).join('')}</tr>`).join('')}</tbody></table>`;
}
function project(state) {
  keys(state, ['metadata', 'slides']); keys(state.metadata, ['id', 'version', 'width', 'height', 'theme', 'assets', 'slides']);
  const metadata = clone(state.metadata); check(Array.isArray(state.slides), 'HTML slide list');
  check(state.slides.length === metadata.slides.length, 'HTML slide count mismatch');
  const deck = { ...metadata, slides: metadata.slides.map((slide, index) => {
    keys(slide, ['id', 'notes']); const source = state.slides[index]; keys(source, ['id', 'html']); check(source.id === slide.id, 'HTML slide order mismatch');
    return { ...slide, elements: slideNodes(source.html, slide.id).elements.map(element) };
  }) };
  return validate(deck);
}
export const html = {
  name: 'restricted-html',
  encode(deck) {
    validate(deck); return { metadata: { ...clone(deck), slides: deck.slides.map(({ id, notes }) => ({ id, notes })) }, slides: deck.slides.map(s => ({ id: s.id, html: `<section data-id="${escape(s.id)}">${s.elements.map(encodeElement).join('')}</section>` })) };
  },
  project,
  normalize(state) { return this.encode(project(state)); },
  patch(state, slideID, elementID, changes) {
    const next = clone(state); const slide = next.slides.find(s => s.id === slideID); const { root, elements } = slideNodes(slide.html, slideID);
    const node = elements.find(n => attr(n)['data-id'] === elementID); const before = element(node); const after = { ...before, ...changes };
    // Edit native HTML DOM/CSS; no projection re-encode as the write path.
    node.attrs.find(a => a.name === 'style').value = style(after);
    if ('text' in changes) node.childNodes = [{ nodeName: '#text', value: changes.text, parentNode: node }];
    slide.html = serialize(root); next.metadata.version++; project(next); return next;
  },
  reorder(state, order) { const next = clone(state); next.metadata.slides = order.map(id => next.metadata.slides.find(s => s.id === id)); next.slides = order.map(id => next.slides.find(s => s.id === id)); next.metadata.version++; project(next); return next; },
};
