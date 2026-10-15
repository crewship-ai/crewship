import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, load, snapshot, validate, project, initialJSON, outline, textNode, anchor, resolve, find } from './schema.mjs';
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
  const heading = initialJSON(); heading.content[0].attrs.level = 2; assert.throws(() => create(heading), /heading level/);
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
