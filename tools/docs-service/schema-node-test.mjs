import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, load, snapshot, validate, project, initialJSON, outline, textNode, anchor, resolve, find, markdown } from './schema.mjs';
test('binary reload and compaction preserve IDs, hashes and relative identities', async () => {
  const doc = create(), thread = anchor(doc, 'intro', 0, 8, 'thread', 1);
  const before = await outline(doc), restored = load(snapshot(doc));
  assert.equal(doc.gc, false); assert.equal(restored.gc, false);
  assert.deepEqual(project(restored), project(doc)); assert.deepEqual(await outline(restored), before);
  assert.equal(resolve(restored, thread).status, 'attached');
  textNode(restored, 'intro').insert(0, 'prefix ');
  assert.deepEqual((await outline(restored)).map(b => b.id), before.map(b => b.id));
  const compacted = load(snapshot(restored));
  assert.deepEqual(resolve(compacted, thread), resolve(restored, thread));
  assert.equal(resolve(compacted, thread).from, 7); assert.equal(resolve(compacted, thread).to, 15);
  for (const d of [doc, restored, compacted]) d.destroy();
});
test('validation rejects unknown content, duplicate IDs and opaque roots', () => {
  const duplicate = initialJSON(); duplicate.content[1].attrs.id = 'title'; assert.throws(() => create(duplicate), /duplicate block ID/);
  const unknown = initialJSON(); unknown.content.push({ type: 'unknown' }); assert.throws(() => create(unknown), /unsupported node/);
  const wrong = initialJSON(); wrong.content[1].content = [{ type: 'table', attrs: { id: 'nested' } }]; assert.throws(() => create(wrong));
  const heading = initialJSON(); heading.content[0].attrs.level = 4; assert.throws(() => create(heading), /heading level/);
  const doc = create(); doc.getMap('opaque').set('payload', 'hidden'); assert.throws(() => validate(doc), /unsupported Yjs root/); doc.destroy();
  const raw = create(); raw.getXmlFragment('default').insert(0, [new Y.XmlElement('unknown')]); assert.throws(() => validate(raw), /unsupported node/); raw.destroy();
});
test('comments follow edits, collapse on range deletion and detach with deleted blocks', () => {
  const doc = create(), text = textNode(doc, 'intro'), thread = anchor(doc, 'intro', 0, 8, 'comment', 12);
  assert.equal(thread.quote, 'Společný'); assert.equal(thread.checkpoint, 12); assert.equal(resolve(doc, thread).status, 'attached');
  text.insert(0, 'new '); assert.equal(resolve(doc, thread).from, 4); assert.equal(resolve(doc, thread).to, 12);
  text.delete(4, 8); assert.equal(resolve(doc, thread).status, 'collapsed');
  const compacted = load(snapshot(doc)); assert.equal(resolve(compacted, thread).status, 'collapsed');
  const fragment = compacted.getXmlFragment('default'); fragment.delete(fragment.toArray().indexOf(find(compacted, 'intro')), 1);
  assert.equal(resolve(compacted, thread).status, 'detached');
  const reloaded = load(snapshot(compacted)); assert.equal(resolve(reloaded, thread).status, 'detached');
  for (const d of [doc, compacted, reloaded]) d.destroy();
});

test('creation rejects future attributes before ProseMirror can discard them', () => {
  const json = initialJSON(); json.content[1].attrs.future = 'must preserve';
  assert.throws(() => create(json), /unsupported attributes/);
  const table = initialJSON(); table.content[2].content[0].content[0].attrs.colspan = 2;
  assert.throws(() => create(table), /simple table only/);
});
test('raw binary validation rejects metadata and deltas omitted or normalized by projection', () => {
  for (const [mutate, expected] of [
    [text => text.setAttribute('future', { payload: 'hidden' }), /unsupported text attributes/],
    [text => text.insertEmbed(0, { payload: 'embedded' }), /unsupported text delta/],
    [text => text.format(0, 1, { bold: {} }), /unsupported text delta/],
  ]) {
    const doc = create(); mutate(textNode(doc, 'intro'));
    const candidate = load(snapshot(doc));
    assert.throws(() => validate(candidate), expected);
    doc.destroy(); candidate.destroy();
  }
  const doc = create(); find(doc, 'cell-a').setAttribute('colspan', 2);
  assert.throws(() => validate(doc), /simple table only/); doc.destroy();
});

test('retained binary rejects deleted unsupported content after snapshot reload', () => {
  const cases = [
    doc => { const text = textNode(doc, 'intro'); text.setAttribute('future', 'hidden'); text.removeAttribute('future'); },
    doc => { const text = textNode(doc, 'intro'); text.format(0, 1, { bold: 'hidden' }); text.format(0, 1, { bold: null }); },
    doc => { const root = doc.getXmlFragment('default'); root.insert(root.length, [new Y.XmlElement('futureNode')]); root.delete(root.length - 1, 1); },
    doc => { const map = doc.getMap('pastRoot'); map.set('payload', 'hidden'); map.delete('payload'); },
    doc => { const block = find(doc, 'intro'); block.setAttribute('future', 'hidden'); block.removeAttribute('future'); },
    doc => { const block = find(doc, 'intro'); block.setAttribute('id', { hidden: true }); block.setAttribute('id', 'intro'); },
    doc => { const text = textNode(doc, 'intro'); text.insertEmbed(0, { hidden: true }); text.delete(0, 1); },
    doc => { const root = doc.getXmlFragment('default'); root.insert(root.length, [new Y.Map()]); root.delete(root.length - 1, 1); },
    doc => { const map = new Y.Map(); map.set('subdocument', new Y.Doc()); const root = doc.getXmlFragment('default'); root.insert(root.length, [map]); root.delete(root.length - 1, 1); },
  ];
  for (const mutate of cases) {
    const doc = create(); mutate(doc);
    const restored = load(snapshot(doc));
    assert.throws(() => validate(restored), /unsupported/);
    restored.destroy(); doc.destroy();
  }
});
test('retained supported text and deleted blocks preserve IDs and anchors', () => {
  const doc = create(); const thread = anchor(doc, 'intro', 0, 8, 'retained-comment', 1);
  const text = textNode(doc, 'intro'); text.delete(0, 8); text.insert(0, 'Replacement');
  const root = doc.getXmlFragment('default'); root.delete(root.toArray().indexOf(find(doc, 'intro')), 1);
  const restored = load(snapshot(doc));
  assert.doesNotThrow(() => validate(restored));
  assert.equal(resolve(restored, thread).status, 'detached');
  assert.ok(Buffer.from(Y.encodeStateAsUpdate(restored)).includes(Buffer.from('Replacement')));
  const original = create(); const old = find(original, 'intro'); old.setAttribute('id', 'historic-id'); old.setAttribute('id', 'intro');
  const renamed = load(snapshot(original)); assert.doesNotThrow(() => validate(renamed));
  for (const d of [doc, restored, original, renamed]) d.destroy();
});

test('schema two round trips nested lists and heading levels without changing stable IDs', async () => {
  const json = initialJSON(); json.content[0].attrs.level = 2;
  const paragraph = (id, text) => ({ type: 'paragraph', attrs: { id }, content: [{ type: 'text', text }] });
  json.content.push({ type: 'orderedList', attrs: { id: 'ordered', start: 3 }, content: [
    { type: 'listItem', attrs: { id: 'item' }, content: [paragraph('item-text', 'First'),
      { type: 'bulletList', attrs: { id: 'nested' }, content: [{ type: 'listItem', attrs: { id: 'nested-item' }, content: [paragraph('nested-text', 'Nested')] }] },
    ] },
  ] });
  const doc = create(json), reloaded = load(snapshot(doc));
  try {
    assert.deepEqual(project(reloaded), project(doc));
    assert.deepEqual(await outline(reloaded), await outline(doc));
    assert.equal((await outline(reloaded)).find(block => block.id === 'nested-text').parent, 'nested-item');
    const invalid = structuredClone(json); invalid.content.at(-1).attrs.start = 0;
    assert.throws(() => create(invalid), /ordered list start/);
  } finally { doc.destroy(); reloaded.destroy(); }
});


test('Markdown export preserves list numbering and simple tables while escaping inline syntax', () => {
  const json = initialJSON();
  json.content = [
    { type: 'orderedList', attrs: { id: 'export-list', start: 3 }, content: [
      { type: 'listItem', attrs: { id: 'export-item' }, content: [{ type: 'paragraph', attrs: { id: 'export-text' }, content: [{ type: 'text', text: 'literal [link](x) *star*' }] }] },
    ] },
    json.content[2],
  ];
  json.content[1].content.push({ type: 'tableRow', attrs: { id: 'export-second-row' }, content: [{ type: 'tableCell', attrs: { id: 'export-second-cell' }, content: [{ type: 'paragraph', attrs: { id: 'export-cell-first-paragraph' }, content: [{ type: 'text', text: '1' }] }, { type: 'paragraph', attrs: { id: 'export-cell-second-paragraph' }, content: [{ type: 'text', text: 'second paragraph' }] }] }] });
  const exported = markdown(json);
  assert.ok(exported.startsWith('3. literal \\[link\\](x) \\*star\\*'));
  assert.ok(exported.includes('| A | B |\n| --- | --- |'));
  assert.ok(exported.includes('| 1 second paragraph |'));
  assert.ok(!exported.includes('1second paragraph'));
  assert.ok(exported.endsWith('\n'));
});
