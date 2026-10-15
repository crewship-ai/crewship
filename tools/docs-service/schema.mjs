import { Extension, Node, getSchema } from '@tiptap/core';
import { Table, TableRow, TableCell, TableHeader } from '@tiptap/extension-table';
import { Plugin } from '@tiptap/pm/state';
import * as Y from 'yjs';
import { prosemirrorToYDoc, yDocToProsemirrorJSON } from '@tiptap/y-tiptap';

export const VERSION = 2;
const types = ['heading', 'paragraph', 'table', 'tableRow', 'tableCell', 'tableHeader', 'bulletList', 'orderedList', 'listItem'];
const ids = Extension.create({
  name: 'stableIDs',
  addGlobalAttributes: () => [{ types, attributes: { id: {
    default: null,
    parseHTML: element => element.getAttribute('data-block-id'),
    renderHTML: attrs => ({ 'data-block-id': attrs.id }),
  } } }],
  addProseMirrorPlugins() {
    return [new Plugin({ appendTransaction(transactions, old, state) {
      if (!transactions.some(t => t.docChanged)) return null;
      const seen = new Set(); const tr = state.tr;
      state.doc.descendants((node, pos) => {
        if (!types.includes(node.type.name)) return;
        let id = node.attrs.id;
        if (!id || seen.has(id)) {
          id = crypto.randomUUID(); tr.setNodeMarkup(pos, undefined, { ...node.attrs, id });
        }
        seen.add(id);
      });
      return tr.docChanged ? tr : null;
    } })];
  },
});
export const extensions = [
  Node.create({ name: 'doc', topNode: true, content: 'block+' }),
  Node.create({ name: 'text', group: 'inline' }),
  Node.create({ name: 'paragraph', group: 'block', content: 'text*', parseHTML: () => [{ tag: 'p' }], renderHTML: ({ HTMLAttributes }) => ['p', HTMLAttributes, 0] }),
  Node.create({ name: 'heading', group: 'block', content: 'text*', addAttributes: () => ({ level: { default: 1 } }), parseHTML: () => [1,2,3].map(level=>({tag:`h${level}`,attrs:{level}})), renderHTML: ({ node,HTMLAttributes }) => [`h${node.attrs.level}`, HTMLAttributes, 0] }),
  Node.create({name:'bulletList',group:'block',content:'listItem+',parseHTML:()=>[{tag:'ul'}],renderHTML:({HTMLAttributes})=>['ul',HTMLAttributes,0]}),
  Node.create({name:'orderedList',group:'block',content:'listItem+',addAttributes:()=>({start:{default:1},type:{default:null}}),parseHTML:()=>[{tag:'ol'}],renderHTML:({HTMLAttributes})=>['ol',HTMLAttributes,0]}),
  Node.create({name:'listItem',content:'paragraph block*',parseHTML:()=>[{tag:'li'}],renderHTML:({HTMLAttributes})=>['li',HTMLAttributes,0]}),
  Table.configure({ resizable: false }), TableRow, TableCell, TableHeader, ids,
];
export const schema = getSchema(extensions);
export const encode = bytes => {
  if (typeof Buffer !== 'undefined') return Buffer.from(bytes).toString('base64');
  let str = ''; for (const byte of bytes) str += String.fromCharCode(byte); return btoa(str);
};
export const decode = text => {
  if (typeof Buffer !== 'undefined') return new Uint8Array(Buffer.from(text, 'base64'));
  return Uint8Array.from(atob(text), x => x.charCodeAt(0));
};
export function canonical(value) {
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
  if (value && typeof value === 'object') return '{' + Object.keys(value).sort().map(k => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}';
  return JSON.stringify(value);
}
export async function hash(value) {
  const bytes = new TextEncoder().encode('docs-d0-c14n-v1\n' + canonical(value));
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))].map(b => b.toString(16).padStart(2, '0')).join('');
}
export function load(snapshot) {
  // gc:false is an explicit D0 retention policy, not a scalability claim.
  const doc = new Y.Doc({ gc: false });
  if (snapshot) Y.applyUpdate(doc, decode(snapshot));
  return doc;
}
export const snapshot = doc => encode(Y.encodeStateAsUpdate(doc));
export const project = doc => yDocToProsemirrorJSON(doc, 'default');
export function validateJSON(json) {
  const seen = new Set(); let count = 0;
  const visit = (node, depth = 0) => {
    if (depth > 40 || !node || typeof node !== 'object') throw new Error('invalid bounded node');
    if (++count > 10000) throw new Error('node limit');
    if (Object.keys(node).some(k => !['type', 'attrs', 'content', 'text', 'marks'].includes(k))) throw new Error('unsupported node properties');
    if (node.type !== 'doc' && node.type !== 'text' && !types.includes(node.type)) throw new Error('unsupported node');
    if (node.marks?.length) throw new Error('unsupported marks');
    if (node.type === 'text' && (typeof node.text !== 'string' || node.attrs || node.content)) throw new Error('invalid text node');
    if (node.type !== 'text' && node.text !== undefined) throw new Error('unexpected text property');
    if (types.includes(node.type)) {
      if (typeof node.attrs?.id !== 'string' || !/^[a-zA-Z0-9_-]{1,80}$/.test(node.attrs.id) || seen.has(node.attrs.id)) throw new Error('invalid or duplicate block ID');
      seen.add(node.attrs.id);
    }
    const allowed = node.type === 'tableCell' || node.type === 'tableHeader' ? ['id', 'colspan', 'rowspan', 'colwidth', 'align'] : node.type === 'heading' ? ['id', 'level'] : node.type === 'orderedList' ? ['id','start','type'] : ['id'];
    if (Object.keys(node.attrs || {}).some(k => !allowed.includes(k))) throw new Error('unsupported attributes');
    if (node.type === 'heading' && ![1,2,3].includes(node.attrs.level)) throw new Error('unsupported heading level');
    if (node.type === 'orderedList' && node.attrs.type != null) throw new Error('unsupported ordered list type');
    if (node.type === 'orderedList' && (!Number.isInteger(node.attrs.start??1) || (node.attrs.start??1)<1 || (node.attrs.start??1)>10000)) throw new Error('invalid ordered list start');
    if (['tableCell', 'tableHeader'].includes(node.type) && ((node.attrs.colspan ?? 1) !== 1 || (node.attrs.rowspan ?? 1) !== 1 || node.attrs.colwidth != null || node.attrs.align != null)) throw new Error('D0 simple table only');
    for (const child of node.content || []) visit(child, depth + 1);
  };
  visit(json); const pm = schema.nodeFromJSON(json); pm.check();
  if (JSON.stringify(json).length > 500000) throw new Error('document limit');
  return json;
}
// Yjs 13.6.33's retained Item/content classes are part of this experiment's
// binary contract. Upgrades must rerun the retained-content regressions.
function validateRetained(doc) {
  const root = doc.getXmlFragment('default');
  const supportedType = type => type === root || type instanceof Y.XmlText ||
    (type instanceof Y.XmlElement && types.includes(type.nodeName));
  for (const structs of doc.store.clients.values()) for (const item of structs) {
    // GC carries no recoverable payload. Deleted Items still do.
    if (item instanceof Y.GC) continue;
    if (!(item instanceof Y.Item) || !supportedType(item.parent)) throw new Error('unsupported retained Yjs parent');
    const content = item.content;
    if (item.parentSub !== null) {
      const parent = item.parent;
      if (!(parent instanceof Y.XmlElement)) throw new Error('unsupported retained text attributes');
      const key = item.parentSub;
      const cell = ['tableCell', 'tableHeader'].includes(parent.nodeName);
      const allowed = key === 'id' || (key === 'level' && parent.nodeName === 'heading') || (['start','type'].includes(key) && parent.nodeName === 'orderedList') ||
        (cell && ['colspan', 'rowspan', 'colwidth', 'align'].includes(key));
      if (!allowed) throw new Error('unsupported retained attributes');
      if (content instanceof Y.ContentDeleted) continue;
      // Yjs coalesces adjacent deleted versions of the same scalar attribute.
      // Active attributes remain scalar; every retained value still needs validation.
      if (!(content instanceof Y.ContentAny) || content.arr.length === 0 || (!item.deleted && content.arr.length !== 1)) throw new Error('unsupported retained attribute content');
      for (const value of content.arr) {
        const valid = key === 'id' ? value === null || (typeof value === 'string' && /^[a-zA-Z0-9_-]{1,80}$/.test(value)) :
          key === 'level' ? [1,2,3].includes(value) : key === 'start' ? Number.isInteger(value) && value >=1 && value<=10000 : ['colspan','rowspan'].includes(key) ? value===1 : value===null;
        if (!valid) throw new Error('unsupported retained attribute value');
      }
    } else if (content instanceof Y.ContentType) {
      if (item.parent instanceof Y.XmlText || !supportedType(content.type) || content.type === root) throw new Error('unsupported retained Yjs type');
    } else if (content instanceof Y.ContentString) {
      if (!(item.parent instanceof Y.XmlText)) throw new Error('unsupported retained string parent');
    } else if (!(content instanceof Y.ContentDeleted)) {
      // Includes formatting (even removed marks), embeds, binary and subdocs.
      throw new Error('unsupported retained Yjs content');
    }
  }
}
export function validate(doc) {
  if(doc.store.pendingStructs || doc.store.pendingDs)throw new Error("unresolved Yjs dependencies");
  // Projection serializers omit some raw Yjs metadata. Validate the binary
  // tree as well so omitted attributes cannot bypass the content contract.
  const inspect = (node, depth = 0) => {
    if (depth > 40) throw new Error('node depth limit');
    if (node instanceof Y.XmlText) {
      if (Object.keys(node.getAttributes()).length) throw new Error('unsupported text attributes');
      if (node.toDelta().some(part => typeof part.insert !== 'string' || Object.keys(part.attributes || {}).length)) throw new Error('unsupported text delta');
    } else if (node instanceof Y.XmlElement || node instanceof Y.XmlFragment) {
      for (const child of node.toArray()) inspect(child, depth + 1);
    } else throw new Error('unsupported Yjs content');
  };
  inspect(doc.getXmlFragment('default'));
  const json = validateJSON(project(doc));
  if (Y.encodeStateAsUpdate(doc).length > 1 << 20) throw new Error('document limit');
  // Prevent opaque client-controlled Yjs roots from becoming a covert payload.
  if ([...doc.share.keys()].some(k => k !== 'default')) throw new Error('unsupported Yjs root');
  validateRetained(doc);
  return json;
}
export async function outline(doc) {
  const json = validate(doc); const blocks = [];
  const visit = async (node, parent = null) => {
    if (node.attrs?.id) blocks.push({ id: node.attrs.id, type: node.type, parent, hash: await hash(node), text: textOf(node) });
    for (const child of node.content || []) await visit(child, node.attrs?.id || parent);
  };
  await visit(json); return blocks;
}
export function textOf(node) { return node.text || (node.content || []).map(textOf).join(''); }
export function find(doc, id) {
  let found;
  const visit = node => {
    if (node instanceof Y.XmlElement && node.getAttribute('id') === id) found = node;
    if (node instanceof Y.XmlElement || node instanceof Y.XmlFragment) for (const child of node.toArray()) visit(child);
  };
  visit(doc.getXmlFragment('default')); if (!found) throw new Error('missing block'); return found;
}
export function textNode(doc, id) {
  const block = find(doc, id);
  if (!['paragraph', 'heading'].includes(block.nodeName)) throw new Error('text target required');
  let text = block.toArray().find(n => n instanceof Y.XmlText);
  if (!text) { text = new Y.XmlText(); block.insert(0, [text]); }
  return text;
}
export function patch(doc, op) {
  if (op.op !== 'replaceText' || typeof op.text !== 'string' || op.text.length > 100000) throw new Error('D0 requires bounded replaceText');
  const text = textNode(doc, op.id);
  doc.transact(() => { text.delete(0, text.length); text.insert(0, op.text); }, 'semantic-agent');
  validate(doc);
}
export function anchor(doc, id, from, to, threadID, rev) {
  const text = textNode(doc, id);
  if (!Number.isInteger(from) || !Number.isInteger(to) || from < 0 || to <= from || to > text.length) throw new Error('invalid range');
  return { id: threadID, block: id, quote: text.toString().slice(from, to), checkpoint: rev,
    start: encode(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(text, from, 0))),
    end: encode(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(text, to, -1))) };
}
export function resolve(doc, thread) {
  const start = Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(decode(thread.start)), doc);
  const end = Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(decode(thread.end)), doc);
  if (!start || !end || start.type !== end.type || !start.type.parent || start.type.parent.getAttribute('id') !== thread.block || start.type.parent._item?.deleted) return { ...thread, status: 'detached' };
  return { ...thread, status: start.index >= end.index ? 'collapsed' : 'attached', from: start.index, to: end.index };
}
export function initialJSON() {
  const p = (id, text) => ({ type: 'paragraph', attrs: { id }, content: text ? [{ type: 'text', text }] : [] });
  const cell = (id, text) => ({ type: 'tableCell', attrs: { id }, content: [p(id + '-p', text)] });
  return { type: 'doc', content: [
    { type: 'heading', attrs: { id: 'title', level: 1 }, content: [{ type: 'text', text: 'D0 dokument' }] },
    p('intro', 'Společný text pro dva lidi a agenta.'),
    { type: 'table', attrs: { id: 'table' }, content: [{ type: 'tableRow', attrs: { id: 'row' }, content: [cell('cell-a', 'A'), cell('cell-b', 'B')] }] },
  ] };
}
export function create(json = initialJSON()) {
  // Validate before ProseMirror can normalize away unknown future content.
  validateJSON(json);
  const pm = schema.nodeFromJSON(json); pm.check();
  const doc = prosemirrorToYDoc(pm, 'default'); doc.gc = false; validate(doc); return doc;
}

export function markdown(json) {
  const escape = text => text.replace(/([\\`*_{}\[\]<>#!|])/g, '\\$1');
  const visit = (node, depth=0) => {
    if (node.type==='text') return escape(node.text||'');
    if (node.type==='heading') return '#'.repeat(node.attrs.level)+' '+(node.content||[]).map(n=>visit(n,depth)).join('')+'\n\n';
    if (node.type==='paragraph') return (node.content||[]).map(n=>visit(n,depth)).join('')+'\n\n';
    if (node.type==='bulletList'||node.type==='orderedList') return (node.content||[]).map((n,i)=>'  '.repeat(depth)+(node.type==='bulletList'?'- ':`${(node.attrs.start||1)+i}. `)+visit(n,depth+1).trim().replace(/\n/g,'\n'+'  '.repeat(depth+1))).join('\n')+'\n\n';
    if (node.type==='table') {
      const rows=(node.content||[]).map(r=>r.content.map(c=>escape((c.content||[]).map(textOf).join(' ')).replace(/\n/g,' ')));
      return rows.map((r,i)=>'| '+r.join(' | ')+' |'+(i===0?'\n| '+r.map(()=>'---').join(' | ')+' |':'')).join('\n')+'\n\n';
    }
    return (node.content||[]).map(n=>visit(n,depth)).join('');
  };
  return visit(json).trimEnd()+'\n';
}
