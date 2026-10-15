import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, Session, hash, clone, validate, Conflict, limits } from './model.mjs';
import { html } from './html.mjs';
import { typed } from './typed.mjs';
import { render } from './render.mjs';

for (const adapter of [html, typed]) {
  test(`${adapter.name}: same corpus, direct native edits, CAS atomicity, reorder, anchors`, () => {
    const initial = fixture(); const session = new Session(adapter, adapter.encode(initial));
    assert.deepEqual(session.read(), initial);
    const anchor = session.anchor('slide-czech', 'title-czech');
    const target = session.target('slide-czech', 'title-czech');
    session.patch({ slideID: 'slide-czech', elementID: target.id, ifVersion: 1, ifHash: hash(target), changes: { text: 'Ručně: český kůň', x: 52, y: 55 } });
    const edited = clone(session.state);
    assert.throws(() => session.patch({ slideID: 'slide-czech', elementID: target.id, ifVersion: 1, ifHash: hash(target), changes: { text: 'stale agent' } }), Conflict);
    assert.deepEqual(session.state, edited);
    const current = session.target('slide-czech', target.id);
    assert.throws(() => session.patch({ slideID: 'slide-czech', elementID: target.id, ifVersion: 2, ifHash: hash(target), changes: { text: 'wrong hash' } }), Conflict);
    session.patch({ slideID: 'slide-czech', elementID: target.id, ifVersion: 2, ifHash: hash(current), changes: { text: 'Agent: cílená česká změna' } });
    const order = session.read().slides.map(s => s.id);
    session.reorder([...order].reverse(), 3, hash(order));
    assert.equal(session.resolve(anchor), 'attached');
    assert.equal(session.target('slide-czech', target.id).text, 'Agent: cílená česká změna');
    assert.deepEqual(session.read().slides.flatMap(s => s.elements.map(e => e.id)).sort(), initial.slides.flatMap(s => s.elements.map(e => e.id)).sort());
    assert.throws(() => session.reorder(order, 3, hash(order)), Conflict);
    assert.throws(() => session.reorder(Array(5).fill(order[0]), 4, hash([...order].reverse())), /permutation/);
    const beforeInvalid = clone(session.state);
    assert.throws(() => session.patch({ slideID: 'slide-czech', elementID: target.id, ifVersion: 4, ifHash: hash(session.target('slide-czech', target.id)), changes: { x: -1 } }), /outside|pixels/);
    assert.deepEqual(session.state, beforeInvalid);
    assert.deepEqual(adapter.normalize(adapter.normalize(session.state)), adapter.normalize(session.state));
    assert.deepEqual(adapter.project(adapter.encode(session.read())), session.read());
    assert.equal(session.resolve({ slideID: 'missing', elementID: target.id }), 'detached');
  });
  test(`${adapter.name}: rejects unsupported semantic data before normalization`, () => {
    for (const change of [d => d.slides[0].elements[0].onclick = 'alert(1)', d => d.slides[0].elements[0].font = 'https://evil/font', d => d.slides[0].elements[0].text = 'a'.repeat(limits.text + 1), d => d.slides[0].elements[0].w = Infinity, d => d.slides[0].elements[1].id = d.slides[0].elements[0].id, d => d.slides[0].elements[0].size = 1000, d => d.assets[0].sha256 = 'no']) {
      const deck = fixture(); change(deck); assert.throws(() => adapter.encode(deck));
    }
  });
}
test('HTML rejects active content, remote resources, CSS, malformed nesting and hidden metadata', () => {
  const initial = html.encode(fixture());
  for (const hostile of [
    '<script>alert(1)</script>', '<iframe src="https://example.test"></iframe>', '<!--hidden-->', '<svg><script>alert(1)</script></svg>',
    '<p data-id="evil" onclick="alert(1)">hello</p>', '<img data-id="evil" src="https://example.test/x.png">',
    '<p data-id="evil" style="left:0px;top:0px;width:100px;height:50px;font-family:DejaVu Sans;font-size:20px;color:#000000;background:url(https://example.test)">hello</p>',
    '<p data-id="evil" style="@import url(https://example.test)">hello</p>', '<div data-id="evil"><div><div><div><div><div><div>x</div></div></div></div></div></div></div>',
    '<p data-id="a" data-id="b">duplicate</p>', '<table><tbody><tr><td><p>nested</p></td></tr></tbody></table>',
  ]) {
    const candidate = clone(initial); candidate.slides[0].html = `<section data-id="slide-czech">${hostile}</section>`;
    assert.throws(() => html.normalize(candidate), hostile.slice(0, 80));
  }
});
test('HTML normalization retains entities and geometry with canonical equivalent CSS', () => {
  const source = fixture(); source.slides[0].elements[0].text = '<Čeština> & "žluťoučký"';
  const encoded = html.encode(source); const changed = clone(encoded);
  changed.slides[0].html = changed.slides[0].html.replace('left:48px;top:48px', 'top:48px;left:48px');
  assert.deepEqual(html.normalize(changed), encoded); assert.deepEqual(html.project(encoded), source);
});
test('renderer escapes text and admits only verified data PNG', () => {
  const source = fixture(); source.slides[0].elements[0].text = '<script>fetch("https://example.test")</script>';
  const output = render(source, source.slides[0]); assert.ok(output.includes('&lt;script&gt;')); assert.ok(!output.includes('<script>')); assert.ok(output.includes("default-src 'none'"));
});
test('PNG dimensions and count limits reject before decoding', () => {
  const source = fixture(); const bytes = Buffer.from(source.assets[0].data, 'base64'); bytes.writeUInt32BE(65535, 16); bytes.writeUInt32BE(65535, 20); source.assets[0].data = bytes.toString('base64'); assert.throws(() => validate(source), /pixel limit/);
  const many = fixture(); many.slides = Array(limits.slides + 1).fill(many.slides[0]); assert.throws(() => validate(many), /slide count/);
});
