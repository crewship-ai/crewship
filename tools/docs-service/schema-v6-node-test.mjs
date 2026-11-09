import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, validate, load, snapshot, outline, markdown, anchor, resolve, LATEST_VERSION, CANVAS_VERSION } from './schema.mjs';
import { createSurface } from './surface-core.mjs';

const surface = createSurface({ Y });
const p = (id, text) => ({ type: 'paragraph', attrs: { id }, content: [{ type: 'text', text }] });
const body = { type: 'doc', content: [
  { type: 'heading', attrs: { id: 'title', level: 1 }, content: [{ type: 'text', text: 'Plan', marks: [{ type: 'underline', attrs: {} }] }] },
  p('intro', 'Shared text for Page and Canvas.'), p('outro', 'Last.'),
] };
const at = (x, y) => ({ x, y, w: 320, h: 120, z: 1 });
const sync = (a, b) => { Y.applyUpdate(b, Y.encodeStateAsUpdate(a, Y.encodeStateVector(b))); Y.applyUpdate(a, Y.encodeStateAsUpdate(b, Y.encodeStateVector(a))); };

test('schema 6 is the latest schema and is schema 5 plus the Canvas surface', () => {
  assert.equal(CANVAS_VERSION, 6); assert.equal(LATEST_VERSION, 6);
  const doc = create(body, 6);
  surface.place(doc, 'intro', at(10, 20));
  surface.putItem(doc, 'n1', { kind: 'note', x: 0, y: 400, w: 200, h: 160, z: 2, tone: 'yellow', text: 'Remember' });
  const reloaded = load(snapshot(doc));
  assert.deepEqual(validate(reloaded, 6), validate(doc, 6));
  assert.deepEqual(surface.project(reloaded).placements.map(x => x.id), ['intro']);
  doc.destroy(); reloaded.destroy();
});

test('the surface never changes the Page: projection, hashes, Markdown and bytes of schema 5 content are identical at 6', async () => {
  const doc = create(body, 5), bytes = snapshot(doc);
  assert.deepEqual(validate(doc, 6), validate(doc, 5));
  assert.deepEqual((await outline(doc, 6)).map(b => [b.id, b.hash]), (await outline(doc, 5)).map(b => [b.id, b.hash]));
  assert.equal(markdown(validate(doc, 6), 6), markdown(validate(doc, 5), 5));
  assert.equal(snapshot(doc), bytes);
  const placed = create(body, 6), before = await outline(placed, 6);
  surface.place(placed, 'intro', at(0, 0)); surface.move(placed, 'intro', { x: 900 });
  assert.deepEqual(await outline(load(snapshot(placed)), 6), before, 'a move leaves every block hash, so agent ifHash guards stay valid');
  doc.destroy(); placed.destroy();
});

test('schemas 1-5 keep refusing the surface; schema 6 keeps refusing every other unknown root', () => {
  const doc = create({ type: 'doc', content: [p('intro', 'Plain text only, valid in every schema.')] }, 6); surface.place(doc, 'intro', at(0, 0));
  for (const version of [1, 2, 3, 4, 5]) assert.throws(() => validate(load(snapshot(doc)), version), /unsupported Yjs root/, String(version));
  const opaque = create(body, 6); opaque.getMap('opaque').set('k', 'v');
  assert.throws(() => validate(load(snapshot(opaque)), 6), /unsupported Yjs root/);
  const tabs = create(body, 6); tabs.getXmlFragment('tab:x').insert(0, [new Y.XmlElement('paragraph')]);
  assert.throws(() => validate(load(snapshot(tabs)), 6), /unsupported Yjs root/, 'tabs are schema 7, not 6');
  for (const d of [doc, opaque, tabs]) d.destroy();
});

test('schema 6 validation refuses invalid surface values, live and retained', () => {
  const attempts = [
    d => d.getMap(surface.PLACEMENTS).set('intro', { ...at(0, 0), html: '<b>' }),
    d => d.getMap(surface.ITEMS).set('n', { kind: 'image', x: 0, y: 0, w: 50, h: 50, z: 0 }),
    d => d.getMap(surface.TEXT).set('n', { text: 'object' }),
    d => d.getMap(surface.ITEMS).set('t', new Y.Text('nested')),
    d => { d.getMap(surface.ITEMS).set('n', { kind: 'note', x: 0, y: 0, w: 50, h: 50, z: 0, tone: 'yellow', css: 'x' }); d.getMap(surface.ITEMS).delete('n'); },
  ];
  for (const attempt of attempts) {
    const doc = create(body, 6); attempt(doc);
    assert.throws(() => validate(load(snapshot(doc)), 6));
    doc.destroy();
  }
});

test('the surface has its own share of the document budget: merged copies past it are refused, the Page keeps room', () => {
  assert.ok(surface.LIMITS.retainedBytes <= 512 * 1024, 'the surface cannot take the whole 1 MiB document limit');
  const a = create(body, 6); surface.putItem(a, 'n', { kind: 'note', x: 0, y: 0, w: 200, h: 160, z: 0, tone: 'yellow', text: 'x' });
  const b = load(snapshot(a));
  const big = i => 'y'.repeat(3990) + (i % 10);
  // Each copy stays inside its own budget...
  for (let i = 0; i < 70; i++) { surface.setText(a, 'n', big(i)); surface.setText(b, 'n', big(i + 5)); }
  validate(load(snapshot(a)), 6); validate(load(snapshot(b)), 6);
  // ...but the merged state is refused by the validator every commit passes through.
  sync(a, b);
  assert.throws(() => validate(load(snapshot(a)), 6), /surface history limit/);
  // Page text still has room in a document whose surface is full up to its share.
  const c = create(body, 6); surface.putItem(c, 'n', { kind: 'note', x: 0, y: 0, w: 200, h: 160, z: 0, tone: 'yellow', text: 'x' });
  for (let i = 0; i < 1000; i++) { try { surface.setText(c, 'n', big(i)); } catch { break; } }
  const text = c.getXmlFragment('default').get(1).get(0); text.insert(0, 'z'.repeat(200000));
  validate(load(snapshot(c)), 6);
  for (const d of [a, b, c]) d.destroy();
});

test('the surface budget bounds its real encoded bytes for any writer pattern, so the Page always keeps half the document', () => {
  const patterns = {
    'one writer, set then delete under fresh keys, float geometry': (doc, i) => { const m = doc.getMap(surface.PLACEMENTS), k = i.toString(36); m.set(k, { x: 1e-7, y: 1e-7, w: 40.1, h: 24.1, z: 1 }); m.delete(k); },
    'a new writer for every write': (doc, i) => { doc.clientID = 2 ** 52 + i * 7919; const m = doc.getMap(surface.PLACEMENTS), k = i.toString(36); m.set(k, { x: 1e-7, y: 1e-7, w: 40.1, h: 24.1, z: 1 }); m.delete(k); },
    'long keys and long text, overwritten': (doc, i) => { doc.getMap(surface.TEXT).set('k'.repeat(70) + (i % 7), '\u00e9'.repeat(2000)); },
  };
  for (const [name, write] of Object.entries(patterns)) {
    const doc = create(body, 6), base = Y.encodeStateAsUpdate(doc).length;
    let accepted = 0, full = false;
    for (let i = 0; i < 20000 && !full; i++) {
      write(doc, i);
      if (i % 20) continue;
      let counted;
      try { counted = surface.validateSurface(doc).retained; } catch (error) { assert.match(error.message, /surface history limit/); full = true; break; }
      const real = Y.encodeStateAsUpdate(doc).length - base;
      assert.ok(real <= counted, `${name}: ${real} encoded bytes counted as ${counted}`);
      accepted = i + 1;
    }
    assert.ok(full && accepted > 20, `${name}: the share fills after ${accepted} accepted writes`);
    doc.destroy();
  }
  // A surface filled up to its share still leaves the Page room for 400 KB of text.
  const full = create(body, 6);
  for (let i = 0; i < 20000; i++) {
    full.clientID = 2 ** 52 + i * 7919;
    const m = full.getMap(surface.PLACEMENTS), k = i.toString(36);
    m.set(k, { x: 1e-7, y: 1e-7, w: 40.1, h: 24.1, z: 1 }); m.delete(k);
    if (i % 20 === 0 && surface.validateSurface(full).retained > surface.LIMITS.retainedBytes - 8000) break;
  }
  full.clientID = 7;
  full.getXmlFragment('default').get(1).get(0).insert(0, 'z'.repeat(400000));
  validate(load(snapshot(full)), 6);
  full.destroy();
});

test('comments stay block-anchored at schema 6 across moves and deletion', () => {
  const doc = create(body, 6), thread = anchor(doc, 'intro', 0, 6, 't', 1, 6);
  surface.place(doc, 'intro', at(0, 0)); surface.move(doc, 'intro', { x: 400 });
  assert.equal(resolve(doc, thread, 6).status, 'attached');
  surface.deleteBlock(doc, 'intro');
  const gone = resolve(load(snapshot(doc)), thread, 6);
  assert.equal(gone.status, 'detached'); assert.equal(gone.quote, 'Shared');
  validate(load(snapshot(doc)), 6);
  doc.destroy();
});
