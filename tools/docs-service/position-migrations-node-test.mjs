import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, load, snapshot, validate, project, outline, textNode, find } from './schema.mjs';

const root = '__tiptap_position_migrations:root:default';
function entry(doc) {
  const id = textNode(doc, 'intro')._start.id;
  const clock = Y.decodeStateVector(Y.encodeStateVector(doc)).get(doc.clientID);
  return { key: `${doc.clientID}:${clock}`, value: { version: 1, client: doc.clientID, clock,
    seen: [...Y.decodeStateVector(Y.encodeStateVector(doc))],
    transfers: [{ source: { client: id.client, clock: id.clock, length: 1 }, target: { client: id.client, clock: id.clock, type: textNode(doc, 'intro')._item.id } }] } };
}
function put(doc, mutate = () => {}) {
  const e = entry(doc); mutate(e.value); doc.getMap(root).set(e.key, e.value); return e;
}
function roundtrip(doc) { return load(snapshot(doc)); }

test('position migration root is narrowly admitted without changing content, IDs or hashes', async () => {
  const doc = create(); const content = project(doc), before = await outline(doc);
  doc.getMap(root);
  assert.deepEqual(validate(doc), content);
  put(doc);
  const restored = roundtrip(doc);
  try {
    assert.deepEqual(validate(restored), content);
    assert.deepEqual(await outline(restored), before);
    assert.equal(restored.getMap(root).size, 1);
  } finally { doc.destroy(); restored.destroy(); }
});

test('deleted and overwritten valid migrations survive binary reload under the same contract', () => {
  const doc = create(); const e = put(doc);
  doc.getMap(root).set(e.key, e.value); doc.getMap(root).delete(e.key);
  const restored = roundtrip(doc);
  try { assert.doesNotThrow(() => validate(restored)); } finally { doc.destroy(); restored.destroy(); }
});

test('migration support does not admit arbitrary, nested-fragment or incorrectly typed roots', () => {
  for (const key of ['opaque', '__tiptap_position_migrations:root:other', '__tiptap_position_migrations:item:1:2', `${root}:extra`]) {
    const doc = create(); doc.getMap(key).set('payload', 'hidden');
    assert.throws(() => validate(doc), /unsupported Yjs root/); doc.destroy();
  }
  const doc = create(); doc.getArray(root).insert(0, [1]);
  assert.throws(() => validate(doc), /position migration/); doc.destroy();
});

test('migration records and all referenced position shapes are closed and bounded', () => {
  const cases = [
    v => { v.version = 2; }, v => { v.payload = 'hidden'; }, v => { v.client = -1; },
    v => { v.clock = 1.5; }, v => { v.seen = [[1, 'opaque']]; }, v => { v.seen = [[1, 0], [1, 1]]; },
    v => { v.transfers = 'payload'; }, v => { v.transfers[0].source.length = 0; },
    v => { v.transfers[0].source.clock = Number.MAX_SAFE_INTEGER; },
    v => { v.transfers[0].source.boundary = 'other'; },
    v => { v.transfers[0].target.type = { client: 1, clock: 0, payload: 'hidden' }; },
    v => { v.transfers[0].target.tname = 'opaque'; },
    v => { v.transfers[0].target.assoc = 500000; },
    v => { v.transfers[0].payload = 'hidden'; },
  ];
  for (const mutate of cases) {
    const doc = create(); put(doc, mutate); const restored = roundtrip(doc);
    assert.throws(() => validate(restored), /position migration/); doc.destroy(); restored.destroy();
  }
  const doc = create(); const e = entry(doc); doc.getMap(root).set('wrong-key', e.value);
  assert.throws(() => validate(doc), /position migration/); doc.destroy();
});

test('malformed migration payloads cannot hide in deleted or overwritten retained Items', () => {
  for (const action of ['delete', 'overwrite']) {
    const doc = create(); const e = put(doc, v => { v.hidden = 'retained secret'; });
    if (action === 'delete') doc.getMap(root).delete(e.key);
    else { const good = entry(doc).value; good.client = e.value.client; good.clock = e.value.clock; doc.getMap(root).set(e.key, good); }
    const restored = roundtrip(doc);
    assert.throws(() => validate(restored), /position migration/); doc.destroy(); restored.destroy();
  }
  for (const nested of [new Y.Map(), new Y.Text(), new Uint8Array([1, 2]), new Y.Doc()]) {
    const doc = create(); doc.getMap(root).set('1:0', nested); doc.getMap(root).delete('1:0');
    const restored = roundtrip(doc);
    assert.throws(() => validate(restored), /position migration/); doc.destroy(); restored.destroy();
  }
});

test('live and retained migration byte, record, transfer and causal-vector budgets fail closed', () => {
  const cases = [
    doc => { for (let i = 0; i < 257; i++) put(doc); },
    doc => { textNode(doc, 'intro').insert(0, 'a'.repeat(300001)); put(doc, v => { v.transfers = [v.transfers[0], structuredClone(v.transfers[0])]; for (const t of v.transfers) t.source.length = 300001; }); },
    doc => { put(doc, v => { v.seen = Array.from({ length: 129 }, (_, i) => [i, 0]); }); },
    doc => { put(doc, v => { v.transfers = Array.from({ length: 4097 }, () => v.transfers[0]); }); },
    doc => { const e = put(doc, v => { v.transfers = Array.from({ length: 1000 }, () => v.transfers[0]); }); doc.getMap(root).delete(e.key); },
  ];
  for (const mutate of cases) {
    const doc = create(); mutate(doc); const restored = roundtrip(doc);
    assert.throws(() => validate(restored), /position migration.*limit/); doc.destroy(); restored.destroy();
  }
});

test('snapshots emitted by the previous 3.0.7 binding remain readable without a format rewrite', async () => {
  // prosemirrorToYDoc from @tiptap/y-tiptap@3.0.7, pinned Yjs13.6.33;
  // a stable paragraph with no migration root, valid in stored schemas1/2/3.
  const old = 'AQSF78WwBAAHAQdkZWZhdWx0AwlwYXJhZ3JhcGgHAIXvxbAEAAYEAIXvxbAEARVTdG9yZWQgYmVmb3JlIHVwZ3JhZGUoAIXvxbAEAAJpZAF3BmxlZ2FjeQA=';
  for (const schema of [1, 2, 3]) {
    const doc = load(old);
    try {
      assert.equal(validate(doc, schema).content[0].attrs.id, 'legacy');
      assert.equal((await outline(doc, schema))[0].text, 'Stored before upgrade');
      assert.equal(snapshot(doc), old);
      assert.deepEqual([...doc.share.keys()], ['default']);
    } finally { doc.destroy(); }
  }
});

test('position transfers cannot invent retained identities or reference their own metadata Items', () => {
  for (const kind of ['source', 'target', 'type', 'seen', 'metadata']) {
    const doc = create();
    const e = entry(doc), missing = (doc.clientID + 1) >>> 0;
    if (kind === 'source') e.value.transfers[0].source.client = missing;
    if (kind === 'target') e.value.transfers[0].target.client = missing;
    if (kind === 'type') e.value.transfers[0].target.type = { client: missing, clock: 0 };
    if (kind === 'seen') e.value.seen.push([missing, 1]);
    if (kind === 'metadata') e.value.transfers[0].source = { client: e.value.client, clock: e.value.clock, length: 1 };
    doc.getMap(root).set(e.key, e.value);
    const restored = roundtrip(doc);
    assert.throws(() => validate(restored), /position migration/);
    doc.destroy(); restored.destroy();
  }
});


test('historical GC intervals and compacted migration snapshots retain valid numeric identities', () => {
  const doc = create(); const e = entry(doc); e.value.transfers[0].target = null;
  const paragraph = find(doc, 'intro'), fragment = doc.getXmlFragment('default');
  doc.gc = true; fragment.delete(fragment.toArray().indexOf(paragraph), 1);
  doc.getMap(root).set(e.key, e.value);
  const restored = roundtrip(doc), compacted = roundtrip(restored);
  try {
    assert.doesNotThrow(() => validate(restored));
    assert.doesNotThrow(() => validate(compacted));
    assert.deepEqual(project(compacted), project(restored));
  } finally { doc.destroy(); restored.destroy(); compacted.destroy(); }
});

test('out-of-order migration and text updates validate once all referenced history is available', () => {
  const original = create(), initial = snapshot(original), vector = Y.encodeStateVector(original);
  textNode(original, 'intro').insert(0, 'New identity ');
  const textUpdate = Y.encodeStateAsUpdate(original, vector), next = Y.encodeStateVector(original);
  put(original);
  const metadataUpdate = Y.encodeStateAsUpdate(original, next), received = load(initial);
  Y.applyUpdate(received, metadataUpdate);
  assert.throws(() => validate(received), /unresolved Yjs dependencies/);
  Y.applyUpdate(received, textUpdate);
  const compacted = roundtrip(received);
  try {
    assert.doesNotThrow(() => validate(received));
    assert.doesNotThrow(() => validate(compacted));
    assert.deepEqual(project(compacted), project(original));
  } finally { original.destroy(); received.destroy(); compacted.destroy(); }
});
