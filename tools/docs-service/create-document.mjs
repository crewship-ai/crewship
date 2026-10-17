import { randomUUID } from 'node:crypto';

export const CREATION_MAX_BLOCKS = 128;
export const CREATION_MAX_TEXT_BYTES = 256 * 1024;
const MAX_ITEM_BYTES = 64 * 1024;
const MAX_GENERATED_BLOCKS = 512;

// A deliberately small authoring surface. The agent supplies readable text;
// this trusted adapter supplies stable identities before schema3 validation.
export function creationDocument(blocks, makeID = randomUUID) {
  if (!Array.isArray(blocks) || blocks.length === 0 || blocks.length > CREATION_MAX_BLOCKS) throw new Error('one to 128 authoring blocks required');
  let bytes = 0, generated = 0, readable = false;
  const attrs = extra => {
    if (++generated > MAX_GENERATED_BLOCKS) throw new Error('too many generated document blocks');
    const id = makeID();
    if (typeof id !== 'string' || !/^[A-Za-z0-9_-]{1,80}$/.test(id)) throw new Error('invalid generated document identity');
    return {id, ...extra};
  };
  const text = value => {
    if (typeof value !== 'string' || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f\p{Cs}]/u.test(value)) throw new Error('bounded valid text required');
    const size = Buffer.byteLength(value, 'utf8');
    if (size > MAX_ITEM_BYTES || size > CREATION_MAX_TEXT_BYTES - bytes) throw new Error('document authoring text exceeds limit');
    bytes += size;
    // Explicit same whitespace/BOM set as Go; JS trim omits U+0085.
    if (/[^\t\r\n \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff]/u.test(value)) readable = true;
    return value ? [{type:'text', text:value}] : [];
  };
  const content = blocks.map(block => {
    if (!block || typeof block !== 'object' || Array.isArray(block)) throw new Error('authoring block must be an object');
    const allowed = block.type === 'heading' ? ['type','text','level'] : ['bulletList','orderedList'].includes(block.type) ? ['type','items'] : ['type','text'];
    if (Object.keys(block).some(key => !allowed.includes(key))) throw new Error('unsupported authoring block field');
    if (['paragraph','codeBlock'].includes(block.type)) return {type:block.type, attrs:attrs(), content:text(block.text)};
    if (block.type === 'heading') {
      if (!Number.isInteger(block.level) || block.level < 1 || block.level > 3) throw new Error('heading level must be 1 to 3');
      return {type:'heading', attrs:attrs({level:block.level}), content:text(block.text)};
    }
    if (['bulletList','orderedList'].includes(block.type)) {
      if (!Array.isArray(block.items) || block.items.length === 0 || block.items.length > 128) throw new Error('one to 128 list items required');
      return {type:block.type, attrs:attrs(), content:block.items.map(value => ({type:'listItem', attrs:attrs(), content:[{type:'paragraph', attrs:attrs(), content:text(value)}]}))};
    }
    throw new Error('unsupported authoring block type');
  });
  if (!readable) throw new Error('document must contain readable content');
  return {type:'doc', content};
}
