// Derived from structure.mjs at 22b081c8e34bc5b7509ad2b708d9386fee102c40.
// UNREGISTERED computation only: a candidate/proof grants no read or write right.
// The caller must supply a trusted original scope/revision and later obtain live
// human/run admission and independent durable-writer verification. No service
// calls this module. Move is intentionally unsupported: cloning a moved Yjs type
// would replace its character identities and needs separate anchor safeguards.
import { create, validate, outline, hash, snapshot, load } from './schema.mjs';

const idPattern = /^[a-zA-Z0-9_-]{1,80}$/;
const requestPattern = /^[a-zA-Z0-9_-]{1,64}$/;
const hashPattern = /^[a-f0-9]{64}$/;
const fail = message => { throw new Error(message); };
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const closed = (value, keys, required = keys) => object(value) &&
  Object.keys(value).every(key => keys.includes(key)) && required.every(key => Object.hasOwn(value, key));
const bytes = value => Buffer.byteLength(JSON.stringify(value), 'utf8');
const pair = block => block ? { id: block.id, hash: block.hash } : null;
const validPair = value => closed(value, ['id', 'hash']) && typeof value.id === 'string' &&
  typeof value.hash === 'string' && idPattern.test(value.id) && hashPattern.test(value.hash);
const rev = value => Number.isSafeInteger(value) && value >= 0;

// Captures content dependencies only, never authorization. In production this
// must run for the original explicit human selection, not a fresh agent read.
export async function captureStructuralScope(doc, selection, schema) {
  const input = structuredClone(selection);
  if (schema !== 3 || !closed(input, ['block_ids', 'allow_insert_after']) ||
      input.allow_insert_after !== true || !Array.isArray(input.block_ids) ||
      input.block_ids.length < 1 || input.block_ids.length > 256 ||
      input.block_ids.some(id => typeof id !== 'string' || !idPattern.test(id)) ||
      new Set(input.block_ids).size !== input.block_ids.length) fail('explicit bounded schema3 selection required');
  const json = validate(doc, 3);
  const roots = (await outline(doc, 3)).filter(block => block.parent === null);
  const start = roots.findIndex(block => block.id === input.block_ids[0]);
  if (json.content.length !== roots.length || start < 0 ||
      input.block_ids.some((id, index) => roots[start + index]?.id !== id)) fail('contiguous root blocks required');
  if (json.content.slice(start, start + input.block_ids.length).some(node =>
    !['paragraph', 'heading'].includes(node.type))) fail('only paragraph/heading structural targets supported');
  return {
    version: 1,
    blocks: roots.slice(start, start + input.block_ids.length).map(pair),
    before: pair(roots[start - 1]), after: pair(roots[start + input.block_ids.length]),
    allow_insert_after: true,
  };
}

function validScope(scope) {
  return closed(scope, ['version', 'blocks', 'before', 'after', 'allow_insert_after']) &&
    scope.version === 1 && scope.allow_insert_after === true && Array.isArray(scope.blocks) &&
    scope.blocks.length >= 1 && scope.blocks.length <= 256 && scope.blocks.every(validPair) &&
    (scope.before === null || validPair(scope.before)) && (scope.after === null || validPair(scope.after));
}

function insertionNode(value, id) {
  if (!closed(value, ['type', 'attrs', 'content'], ['type']) ||
      !['paragraph', 'heading'].includes(value.type)) fail('plain paragraph/heading insertion required');
  const attrs = value.attrs === undefined ? {} : value.attrs;
  if (!closed(attrs, value.type === 'heading' ? ['level'] : [], value.type === 'heading' ? ['level'] : []) ||
      (value.type === 'heading' && ![1, 2, 3].includes(attrs.level)) ||
      (value.content !== undefined && (!Array.isArray(value.content) || value.content.some(child =>
        !closed(child, ['type', 'text']) || child.type !== 'text' || typeof child.text !== 'string')))) {
    fail('closed unmarked insertion required');
  }
  return { ...value, attrs: { ...attrs, id } };
}

// Clone the complete CRDT snapshot, not its JSON projection: retained character
// identities and untouched comment anchors survive. A valid prefix followed by
// an invalid operation never mutates the caller's document or returns a result.
export async function buildStructuralCandidate(trustedInput, operationInput) {
  // Copy before the first await, so caller mutation cannot widen a running batch.
  const input = structuredClone(trustedInput), operations = structuredClone(operationInput);
  if (!closed(input, ['snapshot', 'schema', 'baseRev', 'currentRev', 'requestID', 'scope']) ||
      input.schema !== 3 || typeof input.snapshot !== 'string' || input.snapshot.length > 1400000 ||
      !rev(input.baseRev) || input.baseRev !== input.currentRev ||
      typeof input.requestID !== 'string' || !requestPattern.test(input.requestID) || !validScope(input.scope)) {
    fail('original bounded schema3 revision and scope required');
  }
  if (!Array.isArray(operations) || operations.length < 1 || operations.length > 256 ||
      bytes(operations) > 256 * 1024) fail('bounded structural batch required');
  const doc = load(input.snapshot);
  try {
    const before = validate(doc, 3);
    if (bytes(before) > 500000) fail('structural proof limit');
    const captured = await captureStructuralScope(doc, {
      block_ids: input.scope.blocks.map(block => block.id), allow_insert_after: input.scope.allow_insert_after,
    }, 3);
    if (await hash(captured) !== await hash(input.scope)) fail('original structural scope changed');
    const allowed = new Set(input.scope.blocks.map(block => block.id));
    // Keep IDs reserved for this entire batch even after deletion.
    const usedIDs = new Set((await outline(doc, 3)).map(block => block.id));
    const root = doc.getXmlFragment('default'), applied = [];
    for (let index = 0; index < operations.length; index++) {
      const op = operations[index];
      const at = id => root.toArray().findIndex(node => node.getAttribute('id') === id);
      if (op?.op === 'insert_after') {
        if (!closed(op, ['op', 'after_id', 'node']) || !allowed.has(op.after_id) || at(op.after_id) < 0) {
          fail('insert after a selected block required');
        }
        // Identity is from the trusted tool mutation ID and operation ordinal;
        // model-provided node IDs/hashes are rejected by insertionNode.
        const id = `${input.requestID}-new-${index}`;
        if (!idPattern.test(id) || usedIDs.has(id)) fail('inserted ID collision');
        const node = insertionNode(op.node, id);
        const scratch = create({ type: 'doc', content: [node] }, 3);
        let clone;
        try { validate(scratch, 3); clone = scratch.getXmlFragment('default').get(0).clone(); }
        finally { scratch.destroy(); }
        const anchorHash = await hash(validate(doc, 3).content[at(op.after_id)]);
        root.insert(at(op.after_id) + 1, [clone]);
        usedIDs.add(id); allowed.add(id);
        applied.push({ op: 'insert_after', after_id: op.after_id, anchor_hash: anchorHash, node });
      } else if (op?.op === 'delete') {
        if (!closed(op, ['op', 'id']) || !allowed.has(op.id) || at(op.id) < 0) fail('delete a selected block required');
        const target = at(op.id), ifHash = await hash(validate(doc, 3).content[target]);
        root.delete(target, 1);
        applied.push({ op: 'delete', id: op.id, if_hash: ifHash });
      } else fail('unsupported structural operation');
      // Preserve the closed nonempty document contract at every intermediate step.
      validate(doc, 3);
    }
    const after = validate(doc, 3);
    if (bytes(after) > 500000) fail('structural proof limit');
    return {
      snapshot: snapshot(doc),
      proof: { version: 1, request_id: input.requestID, schema: 3, base_rev: input.baseRev, operations: applied, before, after },
    };
  } finally { doc.destroy(); }
}
