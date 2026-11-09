import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, load, snapshot, validate, anchor, resolve } from './schema.mjs';
import { createTabs } from './tabs-core.mjs';

const tabs = createTabs({ Y });
const { tree, createTab, renameTab, moveTab, deleteTab, validateTabs, between, TABS } = tabs;
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
  const before = Y.encodeStateVector(doc);
  moveTab(doc, 'default', { parent: 'y' });
  moveTab(doc, 'x', { parent: null, before: 'y' });
  const changed = Y.decodeUpdate(Y.encodeStateAsUpdate(doc, before));
  assert.ok(changed.structs.every(s => s.parent === TABS || s.parent?.constructor?.name === 'ID' || s.parentSub !== undefined), 'only metadata entries were written');
  assert.equal(resolve(doc, thread, 4).status, 'attached');
  assert.deepEqual(shape(tree(doc)), [['x', 'X'], ['y', 'Y', [['default', 'Tab 1']]]]);
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
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { name: 'x', parent: null, order: 'U', color: 'red' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { name: '   ', parent: null, order: 'U' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { name: 'x'.repeat(121), parent: null, order: 'U' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { name: 'x', parent: 't', order: 'U' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { name: 'x', parent: null, order: 'U0' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', { name: 'x', parent: null, order: 'a b' })],
    [/invalid tab metadata/, d => d.getMap(TABS).set('t', new Y.Map())],
    [/invalid tab ID/, d => d.getMap(TABS).set('__proto__', { name: 'x', parent: null, order: 'U' })],
    [/invalid tab root/, d => d.getXmlFragment('tab:bad id!').insert(0, [new Y.XmlElement('paragraph')])],
    [/invalid tab root/, d => d.getMap('tab:m').set('k', 1)],
    [/invalid tab metadata/, d => { d.getMap(TABS).set('t', { name: 'x', parent: null, order: 'U', hidden: 1 }); d.getMap(TABS).delete('t'); }],
  ];
  for (const [reason, attempt] of bad) {
    const doc = fixture(); attempt(doc);
    assert.throws(() => validateTabs(load(snapshot(doc))), reason);
    doc.destroy();
  }
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
