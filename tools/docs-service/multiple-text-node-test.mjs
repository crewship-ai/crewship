import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { validate, patch, outline, anchor, resolve, snapshot, load, find } from './schema.mjs';

function paragraph(parts) {
  const doc = new Y.Doc({ gc: false });
  const block = new Y.XmlElement('paragraph'); block.setAttribute('id', 'p');
  block.insert(0, parts.map(part => { const text = new Y.XmlText(); text.insert(0, part); return text; }));
  doc.getXmlFragment('default').insert(0, [block]); validate(doc);
  return doc;
}

test('semantic replacement spans adjacent CRDT text children', async () => {
  const doc = paragraph(['abc', 'def']);
  patch(doc, { op: 'replaceText', id: 'p', text: 'new' });
  assert.equal((await outline(doc))[0].text, 'new');
  validate(doc); doc.destroy();
});

test('comments use aggregate offsets across CRDT text children without changing the document', () => {
  const doc = paragraph(['abc', 'def', 'ghi']);
  const before = snapshot(doc);
  const thread = anchor(doc, 'p', 2, 7, 'cross', 1);
  assert.equal(thread.quote, 'cdefg');
  assert.deepEqual({ status: resolve(doc, thread).status, from: resolve(doc, thread).from, to: resolve(doc, thread).to }, { status: 'attached', from: 2, to: 7 });
  assert.equal(snapshot(doc), before);
  find(doc, 'p').toArray()[0].insert(0, 'X');
  assert.equal(resolve(doc, thread).from, 3); assert.equal(resolve(doc, thread).to, 8);
  const reloaded = load(snapshot(doc));
  assert.deepEqual(resolve(reloaded, thread), resolve(doc, thread));
  doc.destroy(); reloaded.destroy();
});

test('semantic middle replacement preserves comment identities in untouched prefix and suffix', async () => {
  for (const parts of [['prefix middle suffix'], ['prefix mi', 'dd', 'le suffix']]) {
    const doc = paragraph(parts);
    const prefix = anchor(doc, 'p', 0, 6, 'prefix', 1);
    const suffix = anchor(doc, 'p', 14, 20, 'suffix', 1);
    patch(doc, { op: 'replaceText', id: 'p', text: 'prefix changed suffix' });
    assert.equal((await outline(doc))[0].text, 'prefix changed suffix');
    for (const [thread, from, to] of [[prefix, 0, 6], [suffix, 15, 21]]) {
      const result = resolve(doc, thread);
      assert.equal(result.status, 'attached'); assert.equal(result.from, from); assert.equal(result.to, to);
    }
    doc.destroy();
  }
});

test('cross-child comments collapse on deletion and detach when their block disappears', () => {
  const doc = paragraph(['abc', 'def', 'ghi']);
  const thread = anchor(doc, 'p', 2, 7, 'cross', 1);
  patch(doc, { op: 'replaceText', id: 'p', text: 'abhi' });
  const collapsed = resolve(doc, thread);
  assert.equal(collapsed.status, 'collapsed'); assert.equal(collapsed.from, collapsed.to);
  const restored = load(snapshot(doc));
  assert.deepEqual(resolve(restored, thread), collapsed);
  restored.getXmlFragment('default').delete(0, 1);
  assert.equal(resolve(restored, thread).status, 'detached');
  doc.destroy(); restored.destroy();
});

test('replacement and aggregate anchors keep UTF16 boundaries and unchanged emoji identities', async () => {
  const doc = paragraph(['A😀', '', ' middle ', '😁Z']);
  const prefix = anchor(doc, 'p', 1, 3, 'emoji-prefix', 1);
  const suffix = anchor(doc, 'p', 11, 13, 'emoji-suffix', 1);
  assert.throws(() => anchor(doc, 'p', 2, 3, 'split', 1), /invalid range/);
  patch(doc, { op: 'replaceText', id: 'p', text: 'A😀 changed 😁Z' });
  assert.equal((await outline(doc))[0].text, 'A😀 changed 😁Z');
  assert.equal(resolve(doc, prefix).status, 'attached'); assert.equal(resolve(doc, prefix).from, 1);
  assert.equal(resolve(doc, suffix).status, 'attached'); assert.equal(resolve(doc, suffix).from, 12);
  patch(doc, { op: 'replaceText', id: 'p', text: 'A😃 changed 😁Z' });
  assert.equal((await outline(doc))[0].text, 'A😃 changed 😁Z');
  validate(doc); doc.destroy();
});

test('no-op replacement preserves binary state and relative endpoints', () => {
  const doc = paragraph(['abc', '', 'def']);
  const thread = anchor(doc, 'p', 3, 6, 'second', 1), before = snapshot(doc);
  patch(doc, { op: 'replaceText', id: 'p', text: 'abcdef' });
  assert.equal(snapshot(doc), before);
  assert.equal(resolve(doc, thread).status, 'attached'); assert.equal(resolve(doc, thread).from, 3);
  doc.destroy();
});

test('resolver detaches reversed and foreign-parent endpoints', () => {
  const doc = paragraph(['abc', 'def']);
  const own = anchor(doc, 'p', 1, 5, 'own', 1);
  assert.equal(resolve(doc, { ...own, start: own.end, end: own.start }).status, 'detached');
  const other = new Y.XmlElement('paragraph'); other.setAttribute('id', 'q');
  const text = new Y.XmlText(); text.insert(0, 'other'); other.insert(0, [text]);
  doc.getXmlFragment('default').insert(1, [other]);
  const foreign = anchor(doc, 'q', 0, 3, 'foreign', 1);
  assert.equal(resolve(doc, { ...own, end: foreign.end }).status, 'detached');
  // Deleting a live text child cannot reattach its historical endpoint to a sibling.
  find(doc, 'p').delete(0, 1);
  assert.equal(resolve(doc, own).status, 'detached');
  doc.destroy();
});

test('complete replacement cannot reattach a cross-child comment to unrelated new text', () => {
  const doc = paragraph(['abc', 'def']);
  const thread = anchor(doc, 'p', 0, 6, 'whole', 1);
  patch(doc, { op: 'replaceText', id: 'p', text: 'XYZ' });
  const result = resolve(doc, thread);
  assert.equal(result.status, 'collapsed'); assert.equal(result.from, result.to);
  const restored = load(snapshot(doc));
  assert.deepEqual(resolve(restored, thread), result);
  doc.destroy(); restored.destroy();
});

test('partial cross-child deletion retains only surviving original comment positions', () => {
  const doc = paragraph(['abc', 'def', 'ghi']);
  const thread = anchor(doc, 'p', 1, 8, 'partial', 1);
  // Delete an interior text child without rewriting either endpoint child.
  find(doc, 'p').delete(1, 1);
  const result = resolve(doc, thread);
  assert.equal(result.status, 'attached'); assert.equal(result.from, 1); assert.equal(result.to, 5);
  const restored = load(snapshot(doc));
  assert.deepEqual(resolve(restored, thread), result);
  doc.destroy(); restored.destroy();
});

test('new resolver reads legacy one-child anchors and old resolver safely detaches new cross-child anchors', () => {
  const single = paragraph(['abcdef']);
  const { spans, ...legacy } = anchor(single, 'p', 1, 5, 'legacy', 1);
  assert.equal(resolve(single, legacy).status, 'attached');
  const cross = paragraph(['abc', 'def']), thread = anchor(cross, 'p', 1, 5, 'cross', 1);
  const absolute = encoded => Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(Buffer.from(encoded, 'base64')), cross);
  const start = absolute(thread.start), end = absolute(thread.end);
  // bf920ba1f's exact legacy resolver rejects different text types before
  // looking at their local indices; new span data is additive and ignored.
  assert.notEqual(start.type, end.type);
  const legacyStatus = !start || !end || start.type !== end.type || !start.type.parent || start.type.parent.getAttribute('id') !== thread.block || start.type.parent._item?.deleted ? 'detached' : start.index >= end.index ? 'collapsed' : 'attached';
  assert.equal(legacyStatus, 'detached');
  single.destroy(); cross.destroy();
});

test('comment span and byte limits reject fragmented anchors without mutating the document', () => {
  const doc = paragraph(Array.from({length: 65}, () => 'a')), before = snapshot(doc);
  assert.throws(() => anchor(doc, 'p', 0, 65, 'too-many', 1), /anchor limit/);
  assert.equal(snapshot(doc), before);
  doc.destroy();
});

test('checkpoint restoration resolves original spans and detaches anchors on later inserted identities', () => {
  const doc = paragraph(['abc', 'def']), pinned = snapshot(doc);
  const original = anchor(doc, 'p', 1, 5, 'original', 1);
  patch(doc, { op: 'replaceText', id: 'p', text: 'abcNEWdef' });
  const later = anchor(doc, 'p', 3, 6, 'later', 2);
  const restored = load(pinned);
  const result = resolve(restored, JSON.parse(JSON.stringify(original)));
  assert.equal(result.status, 'attached'); assert.equal(result.from, 1); assert.equal(result.to, 5);
  assert.equal(resolve(restored, JSON.parse(JSON.stringify(later))).status, 'detached');
  doc.destroy(); restored.destroy();
});

test('repeated-text edits stay inside the trusted original selected interval', async () => {
  const doc = paragraph(['aaaa']);
  const suffix = anchor(doc, 'p', 1, 4, 'outside-selection', 1);
  const edit = patch(doc, { op: 'replaceText', id: 'p', text: 'aaaaa', range: { from: 0, to: 1 } });
  assert.deepEqual(edit, { from: 1, to: 1, insert: 'a' });
  assert.equal((await outline(doc))[0].text, 'aaaaa');
  const result = resolve(doc, suffix);
  assert.equal(result.status, 'attached'); assert.equal(result.from, 2); assert.equal(result.to, 5);
  doc.destroy();
});
