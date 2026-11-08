import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, load, snapshot, validate, validateJSON, project as projectPage, hash, outline, anchor, resolve, textNode, LATEST_VERSION } from './schema.mjs';
import { createSurface } from './surface-core.mjs';

const surface = createSurface({ Y });
// No admitted schema accepts surface roots yet, so the Page is checked through
// its own closed projection: the same JSON and block hashes validate() and
// outline() produce for a document without them.
const page = doc => validateJSON(projectPage(doc), 4);
async function blocks(doc) {
  const out = [];
  const visit = async node => { if (node.attrs?.id) out.push([node.attrs.id, node.type, await hash(node)]); for (const child of node.content || []) await visit(child); };
  await visit(page(doc)); return out;
}
const { place, move, unplace, putItem, setText, removeItem, project, validateSurface, blockImpact, deleteBlock, PLACEMENTS, ITEMS, TEXT, CANVAS_SCHEMA } = surface;
const p = (id, text) => ({ type: 'paragraph', attrs: { id }, content: [{ type: 'text', text }] });
const fixture = () => create({ type: 'doc', content: [
  { type: 'heading', attrs: { id: 'title', level: 1 }, content: [{ type: 'text', text: 'Plan' }] },
  p('intro', 'Shared text for Page and Canvas.'),
  { type: 'bulletList', attrs: { id: 'list' }, content: [{ type: 'listItem', attrs: { id: 'li' }, content: [p('li-p', 'Nested')] }] },
  p('outro', 'Last paragraph.'),
] }, 4);
const at = (x, y, z = 0) => ({ x, y, w: 320, h: 120, z });
const box = (z = 1) => ({ kind: 'note', x: 0, y: 400, w: 200, h: 160, z, tone: 'yellow' });
const note = (text, z = 1) => ({ ...box(z), text });
const sync = (a, b) => { Y.applyUpdate(b, Y.encodeStateAsUpdate(a, Y.encodeStateVector(b))); Y.applyUpdate(a, Y.encodeStateAsUpdate(b, Y.encodeStateVector(a))); };

test('surface roots stay refused by every admitted content schema until schema 6', () => {
  assert.equal(CANVAS_SCHEMA, 6);
  assert.ok(CANVAS_SCHEMA > LATEST_VERSION + 1, 'schema 5 is reserved for formatting marks');
  for (const root of [PLACEMENTS, ITEMS, TEXT]) {
    const doc = fixture();
    doc.getMap(root).set('intro', root === TEXT ? 'text' : at(0, 0));
    for (const version of [1, 2, 3, 4]) assert.throws(() => validate(load(snapshot(doc)), version), /unsupported Yjs root/);
    doc.destroy();
  }
});

test('a placement references the block: moving changes geometry only, never content, IDs or hashes', async () => {
  const doc = fixture();
  const before = await blocks(doc), content = page(doc);
  assert.deepEqual(before, (await outline(doc, 4)).map(b => [b.id, b.type, b.hash]));
  place(doc, 'intro', at(10, 20));
  move(doc, 'intro', { x: 400, y: -80, w: 480 });
  const reloaded = load(snapshot(doc));
  assert.deepEqual(page(reloaded), content);
  assert.deepEqual(await blocks(reloaded), before);
  assert.deepEqual(project(reloaded).placements, [{ id: 'intro', x: 400, y: -80, w: 480, h: 120, z: 0 }]);
  // The same block edited "on the canvas" is the Page block: one text, not a copy.
  textNode(reloaded, 'intro').insert(0, 'Edited: ');
  assert.equal(page(reloaded).content[1].content[0].text, 'Edited: Shared text for Page and Canvas.');
  assert.deepEqual(project(reloaded).placements.map(x => x.id), ['intro']);
  doc.destroy(); reloaded.destroy();
});

test('v1 allows at most one placement per block and only top-level Page blocks', () => {
  const doc = fixture();
  place(doc, 'intro', at(0, 0));
  assert.throws(() => place(doc, 'intro', at(50, 50)), /already placed/);
  assert.throws(() => place(doc, 'li-p', at(0, 0)), /top-level/);
  assert.throws(() => place(doc, 'missing', at(0, 0)), /top-level/);
  // Concurrent placements of one block converge to one value: the key is the block ID.
  const a = fixture(), b = load(snapshot(a));
  place(a, 'outro', at(0, 0)); place(b, 'outro', at(900, 900));
  sync(a, b);
  assert.equal(a.getMap(PLACEMENTS).size, 1); assert.deepEqual(project(a).placements, project(b).placements);
  for (const d of [doc, a, b]) d.destroy();
});

test('remove from canvas keeps the block; deleting a block removes it from Page and Canvas together', () => {
  const doc = fixture();
  place(doc, 'intro', at(0, 0)); place(doc, 'outro', at(0, 200));
  putItem(doc, 'edge', { kind: 'connector', from: { ref: 'intro' }, to: { ref: 'outro' }, z: 2 });
  unplace(doc, 'intro');
  assert.ok(page(doc).content.some(n => n.attrs.id === 'intro'), 'block stays in the Page');
  assert.deepEqual(project(doc).detached, ['edge'], 'connector to an unplaced block is shown detached, not re-attached');
  assert.deepEqual(blockImpact(doc, 'outro'), { page: true, placed: true, connectors: ['edge'] });
  deleteBlock(doc, 'outro');
  const reloaded = load(snapshot(doc));
  assert.ok(!page(reloaded).content.some(n => n.attrs.id === 'outro'));
  assert.equal(reloaded.getMap(PLACEMENTS).has('outro'), false);
  assert.throws(() => deleteBlock(reloaded, 'li-p'), /top-level/);
  doc.destroy(); reloaded.destroy();
});

test('a block comment survives a move and stays detached with its quote after the block is deleted', () => {
  const doc = fixture();
  const thread = anchor(doc, 'intro', 0, 6, 'thread', 3, 4);
  place(doc, 'intro', at(0, 0));
  move(doc, 'intro', { x: 700, y: 300 });
  assert.equal(resolve(doc, thread, 4).status, 'attached');
  assert.deepEqual([resolve(doc, thread, 4).from, resolve(doc, thread, 4).to], [0, 6]);
  deleteBlock(doc, 'intro');
  const gone = resolve(load(snapshot(doc)), thread, 4);
  assert.equal(gone.status, 'detached'); assert.equal(gone.quote, 'Shared');
  doc.destroy();
});

test('a concurrent move and block delete leave a valid orphan, not a wedged room', () => {
  const a = fixture(); place(a, 'outro', at(0, 0));
  const b = load(snapshot(a));
  move(a, 'outro', { x: 50 });
  deleteBlock(b, 'outro');
  sync(a, b);
  for (const d of [a, b]) {
    const view = project(load(snapshot(d)));
    assert.deepEqual(view.placements, []); assert.deepEqual(view.orphans, ['outro']);
  }
  a.destroy(); b.destroy();
});

test('concurrent moves of different blocks merge; of one block the last value wins whole', () => {
  const a = fixture(); place(a, 'intro', at(0, 0)); place(a, 'outro', at(0, 200));
  const b = load(snapshot(a));
  move(a, 'intro', { x: 111 }); move(b, 'outro', { y: 999 });
  move(a, 'intro', { y: 5 }); move(b, 'intro', { w: 600 });
  sync(a, b);
  assert.deepEqual(project(a), project(b));
  assert.equal(project(a).placements.find(x => x.id === 'outro').y, 999);
  const intro = project(a).placements.find(x => x.id === 'intro');
  assert.ok((intro.x === 111 && intro.y === 5 && intro.w === 320) || (intro.x === 0 && intro.w === 600), 'one writer\'s geometry, never a mix');
  a.destroy(); b.destroy();
});

test('canvas-only notes, shapes and connectors are surface items, never Page blocks', async () => {
  const doc = fixture(); const before = await blocks(doc);
  putItem(doc, 'n1', note('Remember the budget'));
  putItem(doc, 's1', { kind: 'shape', x: 10, y: 10, w: 80, h: 80, z: 0, shape: 'diamond', tone: 'blue', text: 'Go?' });
  putItem(doc, 'c1', { kind: 'connector', from: { ref: 'n1' }, to: { ref: 's1' }, z: 3, label: 'then' });
  assert.deepEqual(await blocks(load(snapshot(doc))), before);
  assert.deepEqual(project(doc).items.map(i => [i.id, i.kind]), [['s1', 'shape'], ['n1', 'note'], ['c1', 'connector']]);
  assert.deepEqual(project(doc).detached, []);
  move(doc, 'n1', { x: 99 }); assert.equal(project(doc).items.find(i => i.id === 'n1').x, 99);
  assert.throws(() => move(doc, 'c1', { x: 1 }), /follows its endpoints/);
  assert.throws(() => putItem(doc, 'intro', note('x')), /already used/);
  removeItem(doc, 's1'); assert.deepEqual(project(doc).detached, ['c1']);
  doc.destroy();
});

test('the validator is closed and bounded, including retained history', () => {
  const P = /invalid placement/, I = /invalid surface item/, C = /unsupported surface content/;
  const bad = [
    [P, m => m.getMap(PLACEMENTS).set('intro', { ...at(0, 0), text: 'copy of content' })],
    [P, m => m.getMap(PLACEMENTS).set('intro', { ...at(0, 0), x: Infinity })],
    [P, m => m.getMap(PLACEMENTS).set('intro', { ...at(0, 0), w: 1 })],
    [P, m => m.getMap(PLACEMENTS).set('intro', { ...at(0, 0), z: 0.5 })],
    [/invalid surface key/, m => m.getMap(PLACEMENTS).set('bad key!', at(0, 0))],
    [C, m => m.getMap(PLACEMENTS).set('intro', new Y.Map())],
    [P, m => m.getMap(PLACEMENTS).set('intro', 'string')],
    [I, m => m.getMap(ITEMS).set('n', { ...box(), tone: '#ff0000' })],
    [I, m => m.getMap(ITEMS).set('n', { ...box(), html: '<b>' })],
    [I, m => m.getMap(ITEMS).set('n', note('text belongs in surface:text'))],
    [I, m => m.getMap(ITEMS).set('n', { ...box(), x: -0 })],
    [I, m => m.getMap(ITEMS).set('n', { ...box(), z: 10n })],
    [I, m => m.getMap(ITEMS).set('c', { kind: 'connector', from: { ref: 'a' }, to: { ref: 'b' }, z: 0, label: undefined })],
    [/invalid surface key/, m => m.getMap(ITEMS).set('__proto__', box())],
    [/invalid surface key/, m => m.getMap(TEXT).set('constructor', 'x')],
    [/invalid surface text/, m => m.getMap(TEXT).set('n', 'x'.repeat(4001))],
    [/invalid surface text/, m => m.getMap(TEXT).set('n', 'bell\u0007')],
    [/invalid surface text/, m => m.getMap(TEXT).set('n', { text: 'x' })],
    [I, m => m.getMap(ITEMS).set('n', { kind: 'image', x: 0, y: 0, w: 50, h: 50, z: 0, src: 'https://example.invalid/' })],
    [I, m => m.getMap(ITEMS).set('c', { kind: 'connector', from: { ref: 'a' }, to: { ref: 'a' }, z: 0 })],
    [I, m => m.getMap(ITEMS).set('c', { kind: 'connector', from: { ref: 'a' }, to: { ref: 'b', side: 'left' }, z: 0 })],
    [I, m => m.getMap(ITEMS).set('c', { kind: 'connector', from: { ref: '__proto__' }, to: { ref: 'b' }, z: 0 })],
    [C, m => m.getMap(ITEMS).set('t', new Y.Text('nested'))],
    [C, m => { const nested = new Y.Map(); m.getMap(ITEMS).set('t', nested); nested.set('a', 1); }],
    [/unsupported surface root/, m => m.getArray(PLACEMENTS).insert(0, [1])],
  ];
  for (const [reason, attempt] of bad) {
    const doc = fixture(); attempt(doc);
    assert.throws(() => validateSurface(load(snapshot(doc))), reason);
    doc.destroy();
  }
  // A deleted invalid value is still retained by gc:false and still refused.
  const doc = fixture(); doc.getMap(ITEMS).set('n', { ...box(), payload: 'hidden' }); doc.getMap(ITEMS).delete('n');
  assert.throws(() => validateSurface(load(snapshot(doc))), /invalid surface item/);
  doc.destroy();
  // The live entry limit.
  const many = fixture();
  many.transact(() => { for (let i = 0; i <= surface.LIMITS.entries; i++) many.getMap(ITEMS).set('n' + i, box()); });
  assert.throws(() => validateSurface(load(snapshot(many))), /entry limit/);
  assert.throws(() => putItem(many, 'one-more', box()), /entry limit/, 'mutators never write into an invalid surface');
  many.destroy();
});

test('undo is per user origin and spans Page and Canvas', () => {
  const doc = fixture();
  const mine = 'me', theirs = 'them';
  const undo = new Y.UndoManager([doc.getXmlFragment('default'), doc.getMap(PLACEMENTS), doc.getMap(ITEMS)], { trackedOrigins: new Set([mine]), captureTimeout: 0 });
  place(doc, 'intro', at(0, 0), mine);
  place(doc, 'outro', at(0, 300), theirs);
  move(doc, 'intro', { x: 500 }, mine);
  deleteBlock(doc, 'title', mine);
  undo.undo(); // restores the block (Page) — one stack across both views
  assert.ok(page(doc).content.some(n => n.attrs.id === 'title'));
  undo.undo(); assert.equal(project(doc).placements.find(x => x.id === 'intro').x, 0);
  undo.undo(); assert.equal(doc.getMap(PLACEMENTS).has('intro'), false);
  assert.equal(doc.getMap(PLACEMENTS).has('outro'), true, 'another person\'s placement is not undone');
  doc.destroy();
});

test('text and geometry are separate values: a concurrent move and text edit both survive', () => {
  const a = fixture(); putItem(a, 'n1', note('draft'));
  const b = load(snapshot(a));
  move(a, 'n1', { x: 500 }); setText(b, 'n1', 'final text');
  sync(a, b);
  for (const d of [a, b]) { const n = project(d).items.find(i => i.id === 'n1'); assert.equal(n.x, 500); assert.equal(n.text, 'final text'); }
  removeItem(a, 'n1'); assert.equal(a.getMap(TEXT).has('n1'), false);
  a.destroy(); b.destroy();
});

test('a move concurrent with remove-from-canvas keeps the placement visibly; nothing is lost', () => {
  const a = fixture(); place(a, 'intro', at(0, 0));
  const b = load(snapshot(a));
  move(a, 'intro', { x: 40 }); unplace(b, 'intro');
  sync(a, b);
  assert.deepEqual(project(a).placements.map(p => [p.id, p.x]), [['intro', 40]]);
  assert.deepEqual(project(b), project(a));
  a.destroy(); b.destroy();
});

test('an item ID that a concurrent writer also gave a block is reported, not a wedged room', () => {
  const a = fixture(), b = load(snapshot(a));
  putItem(a, 'dup', note('canvas note'));
  b.getXmlFragment('default').insert(0, [(() => { const el = new Y.XmlElement('paragraph'); el.setAttribute('id', 'dup'); return el; })()]);
  sync(a, b);
  for (const d of [a, b]) {
    const view = project(load(snapshot(d)));
    assert.deepEqual(view.conflicts, ['dup']); assert.ok(!view.items.some(i => i.id === 'dup'));
  }
  // The same state reached through undo of a block deletion.
  const c = fixture(); place(c, 'outro', at(0, 0));
  const undo = new Y.UndoManager([c.getXmlFragment('default'), c.getMap(PLACEMENTS)], { trackedOrigins: new Set(['me']), captureTimeout: 0 });
  deleteBlock(c, 'outro', 'me');
  assert.throws(() => place(c, 'outro', at(0, 0)), /top-level/);
  putItem(c, 'outro', note('reuses the deleted block ID'), 'them');
  undo.undo();
  assert.deepEqual(project(c).conflicts, ['outro']);
  assert.deepEqual(project(c).placements.map(p => p.id), ['outro'], 'undo of the deletion restores the placement too');
  for (const d of [a, b, c]) d.destroy();
});

test('validation is read-only, bounds deleted keys, and the history cap equals the document limit', () => {
  const doc = fixture(); place(doc, 'intro', at(0, 0));
  const decoded = load(snapshot(doc));
  const before = decoded.share.get(PLACEMENTS);
  validateSurface(decoded); project(decoded);
  assert.equal(decoded.share.get(PLACEMENTS), before, 'the decoded root was not specialized');
  assert.equal(surface.LIMITS.retainedBytes, 1 << 20);
  const keys = fixture();
  keys.transact(() => { for (let i = 0; i < 14000; i++) { keys.getMap(TEXT).set('k'.repeat(70) + i, 'x'); keys.getMap(TEXT).delete('k'.repeat(70) + i); } });
  assert.throws(() => validateSurface(load(snapshot(keys))), /history limit/);
  for (const d of [doc, decoded, keys]) d.destroy();
});

test('one key never names both a placement and an item', () => {
  const doc = fixture(); place(doc, 'outro', at(0, 0)); deleteBlock(doc, 'title');
  assert.throws(() => putItem(doc, 'outro', note('x')), /already used/);
  putItem(doc, 'title', note('a deleted block ID is free for an item'));
  doc.destroy();
});

test('a stray text from a concurrent removal never carries over to a new item', () => {
  const a = fixture(); putItem(a, 'n1', note('first'));
  const b = load(snapshot(a));
  removeItem(a, 'n1'); setText(b, 'n1', 'secret draft');
  sync(a, b);
  assert.deepEqual(project(a).strays, ['n1']);
  putItem(a, 'n1', { kind: 'shape', x: 0, y: 0, w: 80, h: 80, z: 0, shape: 'rect', tone: 'default' });
  assert.equal(project(a).items.find(i => i.id === 'n1').text, '');
  assert.deepEqual(project(a).strays, []);
  a.destroy(); b.destroy();
});

test('writes are refused before they cross the history cap, and stay cheap as history grows', () => {
  const doc = fixture(); putItem(doc, 'n1', note('x'));
  const big = 'x'.repeat(3990);
  let accepted = 0;
  assert.throws(() => { for (let k = 0; k < 1000; k++) { setText(doc, 'n1', big + (accepted % 10)); accepted++; } }, /history limit/);
  assert.ok(accepted > 200);
  validateSurface(load(snapshot(doc))); // the refused write left a valid document
  const timed = fixture(); place(timed, 'intro', at(0, 0));
  const run = () => { const start = performance.now(); for (let i = 0; i < 1000; i++) move(timed, 'intro', { x: i % 500 }); return performance.now() - start; };
  const first = run(), second = run();
  assert.ok(second < first * 3 + 50, `move cost grew with history: ${first} ms then ${second} ms`);
  for (const d of [doc, timed]) d.destroy();
});

test('text is bounded to 4000 UTF-16 units, which is at most 12 KiB of UTF-8', () => {
  const doc = fixture(); putItem(doc, 'n1', note(''));
  setText(doc, 'n1', '€'.repeat(4000)); // 12000 bytes, the worst case
  assert.equal(surface.LIMITS.textBytes, 12288);
  assert.throws(() => setText(doc, 'n1', '€'.repeat(4001)), /invalid surface text/);
  doc.destroy();
});
