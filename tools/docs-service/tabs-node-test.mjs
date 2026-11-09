import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, load, snapshot, validate, anchor, resolve } from './schema.mjs';
import { createTabs } from './tabs-core.mjs';

const tabs = createTabs({ Y });
const { tree, createTab, renameTab, moveTab, deleteTab, validateTabs, between, TABS, NAMES, LIMITS } = tabs;
const fixture = () => create({ type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'intro' }, content: [{ type: 'text', text: 'First tab text.' }] }] }, 4);
const shape = forest => forest.map(n => n.children.length ? [n.id, n.name, shape(n.children)] : [n.id, n.name]);
const sync = (a, b) => { Y.applyUpdate(b, Y.encodeStateAsUpdate(a, Y.encodeStateVector(b))); Y.applyUpdate(a, Y.encodeStateAsUpdate(b, Y.encodeStateVector(a))); };
const text = (doc, tab) => doc.getXmlFragment(tabs.fragmentName(tab)).toString();

test('every existing document is a one-tab document without migration, and no admitted schema accepts tab roots yet', () => {
  const doc = fixture(), bytes = snapshot(doc);
  assert.deepEqual(shape(tree(doc)), [['default', 'Tab 1']]);
  assert.equal(snapshot(doc), bytes, 'reading tabs writes nothing');
  createTab(doc, { id: 'notes', name: 'Notes', blockId: 'notes-p' });
  for (const version of [1, 2, 3, 4]) assert.throws(() => validate(load(snapshot(doc)), version), /unsupported Yjs root/);
  doc.destroy();
});

test('create, rename, reorder and nest tabs; order, names and content survive reload', () => {
  const doc = fixture();
  createTab(doc, { id: 'b', name: 'Budget', blockId: 'b-p' });
  createTab(doc, { id: 'a', name: 'Agenda', before: 'b', blockId: 'a-p' });
  createTab(doc, { id: 'a1', name: 'Day one', parent: 'a', blockId: 'a1-p' });
  renameTab(doc, 'default', 'Overview');
  moveTab(doc, 'b', { parent: null, before: 'default' });
  const reloaded = load(snapshot(doc));
  assert.deepEqual(shape(tree(reloaded)), [['b', 'Budget'], ['default', 'Overview'], ['a', 'Agenda', [['a1', 'Day one']]]]);
  assert.match(text(reloaded, 'a1'), /a1-p/, 'a new tab starts with one empty paragraph and does not disappear');
  assert.match(text(reloaded, 'default'), /First tab text/);
  doc.destroy(); reloaded.destroy();
});

test('reorder and nesting change metadata only: content identities and comment anchors survive', () => {
  const doc = fixture();
  createTab(doc, { id: 'x', name: 'X', blockId: 'x-p' });
  createTab(doc, { id: 'y', name: 'Y', blockId: 'y-p' });
  const thread = anchor(doc, 'intro', 0, 5, 'thread', 1, 4);
  const before = Y.encodeStateVector(doc), content = doc.getXmlFragment('default').toString();
  moveTab(doc, 'default', { parent: 'y' });
  moveTab(doc, 'x', { parent: null, before: 'y' });
  renameTab(doc, 'y', 'Why');
  const known = Y.decodeStateVector(before), maps = new Set([doc.getMap(TABS), doc.getMap(NAMES)]);
  const written = [...doc.store.clients.entries()].flatMap(([client, structs]) => structs.filter(item => item.id.clock >= (known.get(client) ?? 0)));
  assert.ok(written.length > 0 && written.every(item => maps.has(item.parent)), 'only placement and name entries were written');
  assert.equal(doc.getXmlFragment('default').toString(), content);
  assert.equal(resolve(doc, thread, 4).status, 'attached');
  assert.deepEqual(shape(tree(doc)), [['x', 'X'], ['y', 'Why', [['default', 'Tab 1']]]]);
  doc.destroy();
});

test('a tab cannot move into itself or its descendants, and nesting is bounded', () => {
  const doc = fixture();
  createTab(doc, { id: 'p', name: 'Parent', blockId: 'p-p' });
  createTab(doc, { id: 'c', name: 'Child', parent: 'p', blockId: 'c-p' });
  assert.throws(() => moveTab(doc, 'p', { parent: 'c' }), /into itself/);
  assert.throws(() => moveTab(doc, 'p', { parent: 'p' }), /into itself/);
  let parent = 'c';
  for (let i = 0; i < 6; i++) { createTab(doc, { id: 'd' + i, name: 'Deep ' + i, parent, blockId: 'd' + i + '-p' }); parent = 'd' + i; }
  assert.throws(() => createTab(doc, { id: 'too-deep', name: 'Too deep', parent, blockId: 'td-p' }), /nesting limit/);
  doc.destroy();
});

test('concurrent nesting that forms a cycle converges to the same tree on every replica', () => {
  const a = fixture();
  createTab(a, { id: 'p', name: 'P', blockId: 'p-p' }); createTab(a, { id: 'q', name: 'Q', blockId: 'q-p' });
  const b = load(snapshot(a));
  moveTab(a, 'p', { parent: 'q' }); moveTab(b, 'q', { parent: 'p' });
  sync(a, b);
  assert.deepEqual(tree(a), tree(b));
  const all = JSON.stringify(tree(a));
  for (const id of ['default', 'p', 'q']) assert.ok(all.includes(`"${id}"`), `${id} stays reachable`);
  a.destroy(); b.destroy();
});

test('a deleted tab removes its content and metadata; children move up; the first tab cannot be deleted', () => {
  const doc = fixture();
  createTab(doc, { id: 'p', name: 'P', blockId: 'p-p' });
  createTab(doc, { id: 'c', name: 'C', parent: 'p', blockId: 'c-p' });
  assert.throws(() => deleteTab(doc, 'default'), /first tab/);
  deleteTab(doc, 'p');
  assert.deepEqual(shape(tree(load(snapshot(doc)))), [['default', 'Tab 1'], ['c', 'C']]);
  doc.destroy();
});

test('content written into a tab whose metadata was removed concurrently stays visible', () => {
  const a = fixture(); createTab(a, { id: 't', name: 'Team', blockId: 't-p' });
  const b = load(snapshot(a));
  deleteTab(a, 't');
  const p = new Y.XmlElement('paragraph'); p.setAttribute('id', 'late'); b.getXmlFragment('tab:t').insert(0, [p]);
  sync(a, b);
  const view = tree(a);
  assert.deepEqual(view.find(n => n.id === 't'), { id: 't', name: 'Untitled tab', children: [] });
  assert.match(text(a, 't'), /late/);
  a.destroy(); b.destroy();
});

test('tab metadata is closed and bounded, including retained values', () => {
  const bad = [
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { parent: null, order: 'U', color: 'red' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { parent: null, order: 'U', name: 'x' })],
    [/invalid tab metadata/, d => d.getMap(NAMES).set('t', '   ')],
    [/invalid tab metadata/, d => d.getMap(NAMES).set('t', 'x'.repeat(121))],
    [/invalid tab metadata/, d => d.getMap(NAMES).set('t', { name: 'x' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { parent: 't', order: 'U' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { parent: null, order: 'U0' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { parent: null, order: 'a b' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', new Y.Map())],
    [/invalid tab ID/, d => d.getMap(TABS).set('__proto__', { parent: null, order: 'U' })],
    [/invalid tab root/, d => d.getXmlFragment('tab:bad id!').insert(0, [new Y.XmlElement('paragraph')])],
    [/invalid tab root/, d => d.getMap('tab:m').set('k', 1)],
    [/invalid tab metadata/, d => { d.getMap(TABS).set('t', { parent: null, order: 'U', hidden: 1 }); d.getMap(TABS).delete('t'); }],
  ];
  for (const [reason, attempt] of bad) {
    const doc = fixture(); attempt(doc);
    assert.throws(() => validateTabs(load(snapshot(doc))), reason);
    doc.destroy();
  }
});

test('deleted tabs never count toward the tab limit, and mutators stop before any limit', () => {
  const doc = fixture();
  for (let i = 0; i < LIMITS.create + 20; i++) { createTab(doc, { id: 'c' + i, name: 'C', blockId: 'c' + i + '-p' }); deleteTab(doc, 'c' + i); }
  assert.deepEqual(shape(tree(load(snapshot(doc)))), [['default', 'Tab 1']]);
  for (let i = 1; i < LIMITS.create; i++) createTab(doc, { id: 't' + i, name: 'T', blockId: 't' + i + '-p' });
  assert.throws(() => createTab(doc, { id: 'over', name: 'Over', blockId: 'over-p' }), /tab limit/);
  validateTabs(load(snapshot(doc)));
  // Renaming on every keystroke stops at the history budget instead of breaking the document.
  const typing = fixture(); createTab(typing, { id: 'r', name: 'R', blockId: 'r-p' });
  let refused = false;
  for (let i = 0; i < 20000 && !refused; i++) { try { renameTab(typing, 'r', 'Name ' + i); } catch (error) { assert.match(error.message, /tab history limit/); refused = true; } }
  assert.ok(refused);
  assert.equal(tree(load(snapshot(typing)))[1].id, 'r', 'the document still validates and renders');
  for (const d of [doc, typing]) d.destroy();
});

test('appends on two replicas get the same order key, and the next insert between them still works', () => {
  const a = fixture(), b = load(snapshot(a));
  createTab(a, { id: 'y', name: 'Y', blockId: 'y-p' }); createTab(b, { id: 'z', name: 'Z', blockId: 'z-p' });
  sync(a, b);
  assert.equal(a.getMap(TABS).get('y').order, a.getMap(TABS).get('z').order);
  moveTab(a, 'default', { parent: null, before: 'z' });
  createTab(a, { id: 'w', name: 'W', before: 'z', blockId: 'w-p' });
  assert.deepEqual(tree(a).map(n => n.id), ['y', 'default', 'w', 'z']);
  a.destroy(); b.destroy();
});

test('order keys never run out: alternating moves to the front rebalance instead of failing', () => {
  const doc = fixture(); createTab(doc, { id: 'p', name: 'P', blockId: 'p-p' }); createTab(doc, { id: 'q', name: 'Q', blockId: 'q-p' });
  for (let i = 0; i < 1000; i++) moveTab(doc, i % 2 ? 'p' : 'q', { parent: null, before: tree(doc)[0].id });
  assert.deepEqual(tree(doc).map(n => n.id), ['p', 'q', 'default']);
  assert.ok([...doc.getMap(TABS).values()].every(v => v.order.length < 64));
  doc.destroy();
});

test('concurrent nesting past the depth limit is shown at the deepest level on every replica', () => {
  const a = fixture();
  let parent = null;
  for (let i = 0; i < 4; i++) { createTab(a, { id: 'a' + i, name: 'A' + i, parent, blockId: 'a' + i + '-p' }); parent = 'a' + i; }
  createTab(a, { id: 'b0', name: 'B0', blockId: 'b0-p' }); parent = 'b0';
  for (let i = 1; i < 4; i++) { createTab(a, { id: 'b' + i, name: 'B' + i, parent, blockId: 'b' + i + '-p' }); parent = 'b' + i; }
  const b = load(snapshot(a));
  moveTab(a, 'b0', { parent: 'a3' }); moveTab(b, 'a0', { parent: 'default' });
  sync(a, b);
  assert.deepEqual(tree(a), tree(b));
  const depth = (forest, d = 1) => Math.max(0, ...forest.map(n => Math.max(d, depth(n.children, d + 1))));
  assert.ok(depth(tree(a)) <= LIMITS.depth, `depth ${depth(tree(a))}`);
  a.destroy(); b.destroy();
});

test('a concurrent rename and move of the same tab both survive', () => {
  const a = fixture(); createTab(a, { id: 'p', name: 'P', blockId: 'p-p' }); createTab(a, { id: 't', name: 'Old', blockId: 't-p' });
  const b = load(snapshot(a));
  renameTab(a, 't', 'New'); moveTab(b, 't', { parent: 'p' });
  sync(a, b);
  assert.deepEqual(shape(tree(a)), [['default', 'Tab 1'], ['p', 'P', [['t', 'New']]]]);
  assert.deepEqual(tree(a), tree(b));
  a.destroy(); b.destroy();
});

test('a new tab refuses a block ID another tab already uses, and reads never create roots', () => {
  const doc = fixture();
  assert.throws(() => createTab(doc, { id: 'n', name: 'N', blockId: 'intro' }), /new block ID required/);
  assert.throws(() => moveTab(doc, 'missing', { parent: null }), /tab required/);
  assert.equal(doc.share.has(TABS), false);
  assert.equal(doc.share.has(NAMES), false);
  doc.destroy();
});

test('every mutator works on a freshly loaded document, whose maps are still untyped', () => {
  const a = fixture();
  createTab(a, { id: 'p', name: 'P', blockId: 'p-p' }); createTab(a, { id: 'c', name: 'C', parent: 'p', blockId: 'c-p' }); createTab(a, { id: 'q', name: 'Q', blockId: 'q-p' });
  const moved = load(snapshot(a));
  renameTab(moved, 'q', 'Queue'); moveTab(moved, 'q', { parent: null, before: 'p' });
  assert.deepEqual(shape(tree(moved)), [['default', 'Tab 1'], ['q', 'Queue'], ['p', 'P', [['c', 'C']]]]);
  const deleted = load(snapshot(a));
  renameTab(deleted, 'q', 'Queue'); deleteTab(deleted, 'p');
  assert.deepEqual(shape(tree(load(snapshot(deleted)))), [['default', 'Tab 1'], ['c', 'C'], ['q', 'Queue']], 'p is gone with its metadata; c moved up and keeps its own order');
  const reused = load(snapshot(a));
  assert.throws(() => createTab(reused, { id: 'n', name: 'N', blockId: 'c-p' }), /new block ID required/);
  for (const d of [a, moved, deleted, reused]) d.destroy();
});

test('deleting a tab moves its stored children up, even one the depth clamp shows elsewhere', () => {
  const a = fixture();
  let parent = null;
  for (let i = 0; i < 4; i++) { createTab(a, { id: 'a' + i, name: 'A' + i, parent, blockId: 'a' + i + '-p' }); parent = 'a' + i; }
  createTab(a, { id: 'b0', name: 'B0', blockId: 'b0-p' }); parent = 'b0';
  for (let i = 1; i < 4; i++) { createTab(a, { id: 'b' + i, name: 'B' + i, parent, blockId: 'b' + i + '-p' }); parent = 'b' + i; }
  const b = load(snapshot(a));
  moveTab(a, 'b0', { parent: 'a3' }); moveTab(b, 'a0', { parent: 'default' });
  sync(a, b);
  deleteTab(a, 'b2');
  assert.equal(a.getMap(TABS).get('b3').parent, 'b1');
  a.destroy(); b.destroy();
});

test('renames are refused near the history budget by the real count, not an estimate', () => {
  const doc = fixture(); createTab(doc, { id: 'r', name: 'R', blockId: 'r-p' });
  let n = 0;
  try { for (; n < 100000; n++) renameTab(doc, 'r', 'Name ' + n); } catch (error) { assert.match(error.message, /tab history limit/); }
  const used = validateTabs(load(snapshot(doc))).retained;
  assert.ok(used > LIMITS.retainedBytes * 0.7 && used <= LIMITS.retainedBytes * 0.75, `refused at ${used} counted bytes after ${n} renames`);
  doc.destroy();
});

test('deleting the tab where a concurrent cycle was broken leaves a valid tree', () => {
  const a = fixture();
  createTab(a, { id: 'p', name: 'P', blockId: 'p-p' }); createTab(a, { id: 'q', name: 'Q', blockId: 'q-p' });
  const b = load(snapshot(a));
  moveTab(a, 'p', { parent: 'q' }); moveTab(b, 'q', { parent: 'p' });
  sync(a, b);
  const top = tree(a).find(n => n.children.length).id;
  deleteTab(a, top);
  validateTabs(load(snapshot(a)));
  assert.deepEqual(shape(tree(load(snapshot(a)))).map(n => n[0]).sort(), ['default', top === 'p' ? 'q' : 'p']);
  a.destroy(); b.destroy();
});

test('fractional order keys stay strictly ordered under repeated inserts', () => {
  const keys = ['1'];
  for (let i = 0; i < 150; i++) {
    keys.unshift(between('', keys[0])); keys.push(between(keys.at(-1), ''));
    const j = (i * 7) % (keys.length - 1); keys.splice(j + 1, 0, between(keys[j], keys[j + 1]));
  }
  for (let i = 1; i < keys.length; i++) assert.ok(keys[i - 1] < keys[i], `${keys[i - 1]} < ${keys[i]}`);
  assert.throws(() => between('b', 'a'), /invalid tab order/);
});
