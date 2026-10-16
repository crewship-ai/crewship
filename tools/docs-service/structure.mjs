// Schema3 structural authoring. The immutable scope comes ONLY from private Go
// state; public operation input cannot supply a wider scope or fresh baseline.
import { create, validate, outline, hash } from './schema.mjs';

const segment = /^[a-zA-Z0-9_-]{1,64}$/;
const fail = message => { throw new Error(message); };
const exact = (value, keys) => value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => keys.includes(key));

export async function captureStructure(doc, input, version) {
  if (version !== 3 || !exact(input, ['block_ids','allow_insert_after']) || input.allow_insert_after !== true || !Array.isArray(input.block_ids) || input.block_ids.length < 1 || input.block_ids.length > 256 || new Set(input.block_ids).size !== input.block_ids.length) fail('explicit schema3 whole-block selection required');
  const json = validate(doc, 3), roots = (await outline(doc, 3)).filter(block => block.parent === null);
  const start = roots.findIndex(block => block.id === input.block_ids[0]);
  if (start < 0 || input.block_ids.some((id, index) => roots[start + index]?.id !== id)) fail('contiguous root blocks required');
  const pair = block => block ? { id: block.id, hash: block.hash } : null;
  // validate() enforces a nonempty closed root and unique IDs throughout it.
  if (json.content.length !== roots.length) fail('root outline mismatch');
  return { version: 1, blocks: roots.slice(start, start + input.block_ids.length).map(pair), before: pair(roots[start - 1]), after: pair(roots[start + input.block_ids.length]), allow_insert_after: true };
}

export async function structuralPatch(doc, operations, requestID, scope, version) {
  if (version !== 3 || !segment.test(requestID) || !Array.isArray(operations) || operations.length < 1 || operations.length > 256 || Buffer.byteLength(JSON.stringify(operations)) > 256 * 1024) fail('bounded schema3 structural batch required');
  const before = validate(doc, 3);
  if (Buffer.byteLength(JSON.stringify(before)) > 500000) fail('structural proof limit');
  if (!scope || scope.version !== 1 || !Array.isArray(scope.blocks)) fail('immutable structural request required');
  const captured = await captureStructure(doc, { block_ids: scope.blocks.map(block => block.id), allow_insert_after: scope.allow_insert_after }, version);
  if ((await hash(captured)) !== (await hash(scope))) fail('original structural scope changed');
  const allowed = new Set(scope.blocks.map(block => block.id)), applied = [];
  const root = doc.getXmlFragment('default');
  for (let index = 0; index < operations.length; index++) {
    const op = operations[index];
    if (!exact(op, ['op','id','after_id','before_id','node'])) fail('closed structural operation required');
    const nodes = root.toArray(), at = id => nodes.findIndex(node => node.getAttribute('id') === id);
    const target = op.id === undefined ? -1 : at(op.id);
    if (op.op === 'insert_after') {
      if (Object.keys(op).length !== 3 || !allowed.has(op.after_id) || at(op.after_id) < 0 || !exact(op.node, ['type','attrs','content']) || !['paragraph','heading','codeBlock'].includes(op.node.type)) fail('insert after a granted block required');
      const inserted = structuredClone(op.node), id = `${requestID}-new-${index}`;
      if (!exact(inserted.attrs ?? {}, op.node.type === 'heading' ? ['level'] : []) || (inserted.content ?? []).some(child => !exact(child, ['type','text']) || child.type !== 'text' || typeof child.text !== 'string')) fail('plain typed insertion required');
      inserted.attrs = { ...(inserted.attrs ?? {}), id };
      const scratch = create({ type: 'doc', content: [inserted] }, 3);
      let clone;
      try { clone = scratch.getXmlFragment('default').get(0).clone(); } finally { scratch.destroy(); }
      const anchorHash = await hash(validate(doc, 3).content[at(op.after_id)]);
      root.insert(at(op.after_id) + 1, [clone]); allowed.add(id);
      applied.push({ op: 'insert_after', after_id: op.after_id, anchor_hash: anchorHash, node: inserted });
    } else if (op.op === 'delete') {
      if (Object.keys(op).length !== 2 || !allowed.has(op.id) || target < 0) fail('delete a granted block required');
      const ifHash = await hash(validate(doc, 3).content[target]);
      root.delete(target, 1); applied.push({ op: 'delete', id: op.id, if_hash: ifHash });
    } else if (op.op === 'move_before') {
      if (Object.keys(op).length !== 3 || !allowed.has(op.id) || !allowed.has(op.before_id) || target < 0 || at(op.before_id) < 0 || op.id === op.before_id) fail('move within granted blocks required');
      const json = validate(doc, 3), ifHash = await hash(json.content[target]), anchorHash = await hash(json.content[at(op.before_id)]);
      // Integrated Yjs types cannot be inserted again. Semantic IDs are stable;
      // the durable Go guard refuses moves with comment anchors, without commit.
      const clone = nodes[target].clone(); root.delete(target, 1);
      const destination = root.toArray().findIndex(node => node.getAttribute('id') === op.before_id);
      root.insert(destination, [clone]);
      applied.push({ op: 'move_before', id: op.id, before_id: op.before_id, if_hash: ifHash, anchor_hash: anchorHash });
    } else fail('unsupported structural operation');
    // No intermediate empty document: insert an empty paragraph before deleting
    // the final block. This keeps the closed schema valid through every step.
    validate(doc, 3);
  }
  const after = validate(doc, 3);
  if (Buffer.byteLength(JSON.stringify(after)) > 500000) fail('structural proof limit');
  return { request_id: requestID, operations: applied, before, after };
}
