import test from 'node:test';
import assert from 'node:assert/strict';
import { create, validate, snapshot, load, anchor, resolve, hash } from './schema.mjs';
import { captureStructuralScope, buildStructuralCandidate } from './structural-candidate.mjs';

const p = (id, text = id) => ({ type: 'paragraph', attrs: { id }, content: [{ type: 'text', text }] });
const insert = after_id => ({ op: 'insert_after', after_id, node: { type: 'heading', attrs: { level: 2 }, content: [{ type: 'text', text: 'New' }] } });
async function fixture(json = { type: 'doc', content: ['left', 'a', 'b', 'right'].map(id => p(id)) }) {
  const doc = create(json, 3);
  const scope = await captureStructuralScope(doc, { block_ids: ['a', 'b'], allow_insert_after: true }, 3);
  return { doc, input: { snapshot: snapshot(doc), schema: 3, baseRev: 7, currentRev: 7, requestID: 'request', scope } };
}
async function rejected(input, operations) {
  const before = structuredClone({ input, operations });
  await assert.rejects(() => buildStructuralCandidate(input, operations));
  assert.deepEqual({ input, operations }, before, 'rejection must not mutate inputs');
}

test('candidate inserts/deletes once, preserves source and untouched character anchors through reload', async () => {
  const { doc, input } = await fixture();
  try {
    const comment = anchor(doc, 'right', 0, 5, 'comment', 7, 3);
    const operations = [insert('a'), { op: 'delete', id: 'b' }];
    const original = structuredClone({ input, operations });
    const result = await buildStructuralCandidate(input, operations);
    assert.deepEqual({ input, operations }, original);
    assert.equal(snapshot(doc), input.snapshot);
    const candidate = load(result.snapshot);
    try {
      assert.deepEqual(validate(candidate, 3).content.map(node => node.attrs.id), ['left', 'a', 'request-new-0', 'right']);
      assert.deepEqual(resolve(candidate, comment, 3), { ...comment, status: 'attached', from: 0, to: 5 });
      assert.deepEqual(result.proof.before, validate(doc, 3));
      assert.deepEqual(result.proof.after, validate(candidate, 3));
      assert.equal(result.proof.version, 1);
      assert.equal(result.proof.base_rev, 7);
      assert.equal(result.proof.request_id, 'request');
      assert.equal(result.proof.operations[0].anchor_hash, await hash(p('a')));
      assert.equal(result.proof.operations[1].if_hash, await hash(p('b')));
    } finally { candidate.destroy(); }
    result.proof.before.content[0].attrs.id = 'changed';
    assert.deepEqual({ input, operations }, original, 'result must not alias inputs');
  } finally { doc.destroy(); }
});

test('valid then invalid batch has no externally visible partial candidate', async () => {
  const { doc, input } = await fixture();
  try {
    await rejected(input, [insert('a'), { op: 'delete', id: 'right' }]);
    assert.equal(snapshot(doc), input.snapshot);
    const result = await buildStructuralCandidate(input, [insert('a')]);
    assert.equal(result.proof.operations[0].node.attrs.id, 'request-new-0');
  } finally { doc.destroy(); }
});

test('capture requires explicit contiguous root blocks and closed schema3 selection', async () => {
  const { doc } = await fixture();
  try {
    for (const input of [
      { block_ids: ['a', 'right', 'b'], allow_insert_after: true },
      { block_ids: ['a', 'a'], allow_insert_after: true },
      { block_ids: ['a'], allow_insert_after: false },
      { block_ids: ['a'], allow_insert_after: true, extra: true },
    ]) await assert.rejects(() => captureStructuralScope(doc, input, 3));
    await assert.rejects(() => captureStructuralScope(doc, { block_ids: ['a'], allow_insert_after: true }, 2));
  } finally { doc.destroy(); }
});

test('original revision, selected hashes, root order and both boundaries are immutable preconditions', async () => {
  const { doc, input } = await fixture();
  try {
    const cases = [
      { ...input, currentRev: 8 }, { ...input, baseRev: -1 }, { ...input, baseRev: NaN },
      { ...input, schema: 4 }, { ...input, requestID: '' }, { ...input, extra: true },
    ];
    for (const change of [
      scope => { scope.blocks[0].hash = '0'.repeat(64); },
      scope => { scope.blocks.reverse(); },
      scope => { scope.before = null; },
      scope => { scope.after.hash = '0'.repeat(64); },
      scope => { scope.version = 2; },
      scope => { scope.extra = true; },
      scope => { scope.blocks[0].extra = true; },
    ]) { const copy = structuredClone(input); change(copy.scope); cases.push(copy); }
    for (const value of cases) await rejected(value, [insert('a')]);
  } finally { doc.destroy(); }
});

test('closed operations reject scope escape, move, replacement, injected IDs, marks and unsafe content', async () => {
  const { doc, input } = await fixture();
  try {
    for (const operation of [
      { op: 'delete', id: 'left' }, insert('right'),
      { op: 'move_before', id: 'a', before_id: 'b' },
      { op: 'replaceText', id: 'a', text: 'other' },
      { op: 'delete', id: 'a', if_hash: input.scope.blocks[0].hash },
      { ...insert('a'), id: 'injected' },
      { op: 'insert_after', after_id: 'a', node: p('injected') },
      { op: 'insert_after', after_id: 'a', node: { type: 'codeBlock' } },
      { op: 'insert_after', after_id: 'a', node: { type: 'paragraph', content: [{ type: 'text', text: 'link', marks: [{ type: 'link', attrs: { href: 'javascript:alert(1)' } }] }] } },
      { op: 'insert_after', after_id: 'a', node: { type: 'heading', attrs: { level: 9 } } },
    ]) await rejected(input, [operation]);
  } finally { doc.destroy(); }
});

test('deterministic IDs cannot collide, and a deleted original ID cannot be reused', async () => {
  const { doc, input } = await fixture({ type: 'doc', content: ['left', 'a', 'b', 'request-new-0', 'right'].map(id => p(id)) });
  try { await rejected(input, [insert('a')]); } finally { doc.destroy(); }
  const next = await fixture({ type: 'doc', content: ['left', 'a', 'b', 'request-new-1', 'right'].map(id => p(id)) });
  try {
    next.input.scope = await captureStructuralScope(next.doc, { block_ids: ['a', 'b', 'request-new-1'], allow_insert_after: true }, 3);
    await rejected(next.input, [{ op: 'delete', id: 'request-new-1' }, insert('a')]);
  } finally { next.doc.destroy(); }
});

test('running computation snapshots inputs before asynchronous hashing', async () => {
  const { doc, input } = await fixture();
  try {
    const operations = [insert('a')];
    const pending = buildStructuralCandidate(input, operations);
    operations[0].after_id = 'right';
    input.requestID = 'changed';
    input.scope.blocks[0].hash = '0'.repeat(64);
    const result = await pending;
    assert.equal(result.proof.request_id, 'request');
    assert.equal(result.proof.operations[0].after_id, 'a');
  } finally { doc.destroy(); }
});

test('empty intermediate document is rejected, while inserted blocks can be subsequent explicit targets', async () => {
  const doc = create({ type: 'doc', content: [p('a')] }, 3);
  try {
    const scope = await captureStructuralScope(doc, { block_ids: ['a'], allow_insert_after: true }, 3);
    const input = { snapshot: snapshot(doc), schema: 3, baseRev: 0, currentRev: 0, requestID: 'r', scope };
    await rejected(input, [{ op: 'delete', id: 'a' }]);
    const result = await buildStructuralCandidate(input, [insert('a'), { op: 'delete', id: 'a' }, insert('r-new-0')]);
    assert.deepEqual(result.proof.after.content.map(node => node.attrs.id), ['r-new-0', 'r-new-2']);
  } finally { doc.destroy(); }
});

test('bounded batches and full selection never silently truncate', async () => {
  const doc = create({ type: 'doc', content: Array.from({ length: 257 }, (_, i) => p(`p${i}`)) }, 3);
  try {
    const ids = validate(doc, 3).content.map(node => node.attrs.id);
    const scope = await captureStructuralScope(doc, { block_ids: ids.slice(0, 256), allow_insert_after: true }, 3);
    assert.equal(scope.blocks.length, 256);
    await assert.rejects(() => captureStructuralScope(doc, { block_ids: ids, allow_insert_after: true }, 3));
    const input = { snapshot: snapshot(doc), schema: 3, baseRev: 0, currentRev: 0, requestID: 'r', scope };
    await rejected(input, []);
    await rejected(input, Array.from({ length: 257 }, () => insert('p0')));
    const large = insert('p0'); large.node.content[0].text = 'x'.repeat(256 * 1024);
    await rejected(input, [large]);
  } finally { doc.destroy(); }
});
