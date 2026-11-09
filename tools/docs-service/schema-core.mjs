import { validatePositionMigrations } from './position-migrations.mjs';

// Shared implementation instantiated with the caller's pinned Tiptap/Yjs types.
// Browser and managed Node use identical closed validators without mixing
// separate node_modules copies of Yjs in instanceof/relative-position checks.
export function createDocumentSchema({ Extension, Node, Mark, getSchema, Table, TableRow, TableCell, TableHeader, Plugin, Y, prosemirrorToYDoc, yDocToProsemirrorJSON }) {
const VERSION = 2;
const legacyTypes = ['heading', 'paragraph', 'table', 'tableRow', 'tableCell', 'tableHeader', 'bulletList', 'orderedList', 'listItem'];
const RICH_VERSION = 3;
// Schema 4 is additive over 3: bold/italic/strike marks, code block languages,
// block quotes and task lists. Schemas 1-3 keep their exact closed contracts.
const FORMATTED_VERSION = 4;
// Schema 5 is additive over 4: underline, and a text colour and a highlight
// whose value is a palette NAME. The reader's theme resolves the name; stored
// content never carries CSS, so a colour cannot become markup or a payload.
const LATEST_VERSION = 5;
const PALETTE = Object.freeze(['gray', 'red', 'orange', 'yellow', 'green', 'blue', 'purple', 'pink']);
const validColor = value => typeof value === 'string' && PALETTE.includes(value);
// Schema 5 inline atoms. A date is a calendar day, never a time or free text.
// A mention stores only an opaque user id; the name is resolved when rendered,
// for that reader, so no display text or address is ever part of the content.
const ATOMS = Object.freeze(['date', 'mention']);
const ATOM_KEY = Object.freeze({ date: 'value', mention: 'userId' });
const DATE = /^(\d{4})-(\d{2})-(\d{2})$/;
const validDate = value => {
  const match = typeof value === 'string' && DATE.exec(value);
  if (!match) return false;
  // setUTCFullYear keeps years 1-99 literal (Date.UTC maps them to 1900-1999).
  const [year, month, day] = match.slice(1).map(Number), time = new Date(0); time.setUTCFullYear(year, month - 1, day);
  return year >= 1 && time.getUTCFullYear() === year && time.getUTCMonth() === month - 1 && time.getUTCDate() === day;
};
const validUserId = value => typeof value === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const validAtomValue = (type, value) => type === 'date' ? validDate(value) : type === 'mention' ? validUserId(value) : false;
const UNKNOWN_MEMBER = 'Unknown member';
const richTypes = [...legacyTypes, 'codeBlock'];
const formattedTypes = [...richTypes, 'blockquote', 'taskList', 'taskItem'];
const typesFor = version => { if (![1,2,3,4,5].includes(version)) throw new Error('unsupported content schema'); return version >= FORMATTED_VERSION ? formattedTypes : version === 3 ? richTypes : legacyTypes; };
const isRich = version => version >= RICH_VERSION;
const isFormatted = version => version >= FORMATTED_VERSION;
const atomsFor = version => version >= 5 ? ATOMS : [];
const isAtom = (type, version) => atomsFor(version).includes(type);
// A code block language is a short lowercase identifier (python, c++, c#, objective-c), never markup.
const LANGUAGE = /^[a-z0-9][a-z0-9+#-]{0,31}$/;
const validLanguage = value => typeof value === 'string' && LANGUAGE.test(value);
const COLOR_MARKS = ['textColor', 'highlight'];
const formatKeys = version => version >= 5 ? ['link', 'code', 'bold', 'italic', 'strike', 'underline', ...COLOR_MARKS] : isFormatted(version) ? ['link', 'code', 'bold', 'italic', 'strike'] : isRich(version) ? ['link', 'code'] : [];
const maxMarks = version => version >= 5 ? 7 : isFormatted(version) ? 4 : isRich(version) ? 1 : 0;
const idsFor = types => Extension.create({
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
function extensionsFor(version = VERSION) {
 const types = typesFor(version);
 return [
  Node.create({ name: 'doc', topNode: true, content: 'block+' }),
  Node.create({ name: 'text', group: 'inline' }),
  Node.create({ name: 'paragraph', group: 'block', content: version >= 5 ? 'inline*' : 'text*', parseHTML: () => [{ tag: 'p' }], renderHTML: ({ HTMLAttributes }) => ['p', HTMLAttributes, 0] }),
  Node.create({ name: 'heading', group: 'block', content: version >= 5 ? 'inline*' : 'text*', addAttributes: () => ({ level: { default: 1 } }), parseHTML: () => [1,2,3].map(level=>({tag:`h${level}`,attrs:{level}})), renderHTML: ({ node,HTMLAttributes }) => [`h${node.attrs.level}`, HTMLAttributes, 0] }),
  Node.create({name:'bulletList',group:'block',content:'listItem+',parseHTML:()=>[{tag:'ul'}],renderHTML:({HTMLAttributes})=>['ul',HTMLAttributes,0]}),
  Node.create({name:'orderedList',group:'block',content:'listItem+',addAttributes:()=>({start:{default:1},type:{default:null}}),parseHTML:()=>[{tag:'ol'}],renderHTML:({HTMLAttributes})=>['ol',HTMLAttributes,0]}),
  Node.create({name:'listItem',content:'paragraph block*',parseHTML:()=>[{tag:'li'}],renderHTML:({HTMLAttributes})=>['li',HTMLAttributes,0]}),
  Table.configure({ resizable: false }), TableRow, TableCell, TableHeader, idsFor(types),
  ...(version === RICH_VERSION ? [
    Mark.create({ name: 'link', excludes: 'link code', addAttributes: () => ({ href: { default: null } }), parseHTML: () => [{ tag: 'a[href]', getAttrs: element => validLinkHref(element.getAttribute('href')) ? { href: element.getAttribute('href') } : false }], renderHTML: ({ mark }) => ['a', { href: mark.attrs.href, rel: 'noopener noreferrer', target: '_blank' }, 0] }),
    Mark.create({ name: 'code', code: true, excludes: 'link code', parseHTML: () => [{ tag: 'code' }], renderHTML: () => ['code', {}, 0] }),
    Node.create({ name: 'codeBlock', group: 'block', content: 'text*', marks: '', code: true, defining: true, parseHTML: () => [{ tag: 'pre', preserveWhitespace: 'full' }], renderHTML: ({ HTMLAttributes }) => ['pre', HTMLAttributes, ['code', {}, 0]] }),
  ] : []),
  ...(isFormatted(version) ? [
    Mark.create({ name: 'link', excludes: 'link code', addAttributes: () => ({ href: { default: null } }), parseHTML: () => [{ tag: 'a[href]', getAttrs: element => validLinkHref(element.getAttribute('href')) ? { href: element.getAttribute('href') } : false }], renderHTML: ({ mark }) => ['a', { href: mark.attrs.href, rel: 'noopener noreferrer', target: '_blank' }, 0] }),
    Mark.create({ name: 'bold', parseHTML: () => [{ tag: 'strong' }, { tag: 'b' }], renderHTML: () => ['strong', {}, 0] }),
    Mark.create({ name: 'italic', parseHTML: () => [{ tag: 'em' }, { tag: 'i' }], renderHTML: () => ['em', {}, 0] }),
    Mark.create({ name: 'strike', parseHTML: () => [{ tag: 's' }, { tag: 'del' }, { tag: 'strike' }], renderHTML: () => ['s', {}, 0] }),
    // Schema 5 marks rank after strike and before code, so schema 4 mark order
    // (and with it every schema 4 projection and hash) is unchanged.
    ...(version >= 5 ? [
      Mark.create({ name: 'underline', parseHTML: () => [{ tag: 'u' }], renderHTML: () => ['u', {}, 0] }),
      ...COLOR_MARKS.map(name => {
        const tag = name === 'highlight' ? 'mark' : 'span', data = name === 'highlight' ? 'data-highlight' : 'data-text-color';
        const parsed = element => validColor(element.getAttribute(data)) ? element.getAttribute(data) : null;
        return Mark.create({ name,
          addAttributes: () => ({ color: { default: null, parseHTML: parsed, renderHTML: () => ({}) } }),
          parseHTML: () => [{ tag: `${tag}[${data}]`, getAttrs: element => parsed(element) ? { color: parsed(element) } : false }],
          renderHTML: ({ mark }) => [tag, validColor(mark.attrs.color) ? { [data]: mark.attrs.color } : {}, 0] });
      }),
      Node.create({ name: 'date', group: 'inline', inline: true, atom: true, selectable: true, marks: '',
        addAttributes: () => ({ value: { default: null, parseHTML: element => validDate(element.getAttribute('data-date')) ? element.getAttribute('data-date') : null, renderHTML: () => ({}) } }),
        parseHTML: () => [{ tag: 'time[data-date]', getAttrs: element => validDate(element.getAttribute('data-date')) ? { value: element.getAttribute('data-date') } : false }],
        renderHTML: ({ node }) => validDate(node.attrs.value) ? ['time', { 'data-date': node.attrs.value, datetime: node.attrs.value }, node.attrs.value] : ['time', {}, ''] }),
      // The rendered label is neutral; a reader replaces it with the name it may see.
      Node.create({ name: 'mention', group: 'inline', inline: true, atom: true, selectable: true, marks: '',
        addAttributes: () => ({ userId: { default: null, parseHTML: element => validUserId(element.getAttribute('data-mention')) ? element.getAttribute('data-mention') : null, renderHTML: () => ({}) } }),
        parseHTML: () => [{ tag: 'span[data-mention]', getAttrs: element => validUserId(element.getAttribute('data-mention')) ? { userId: element.getAttribute('data-mention') } : false }],
        renderHTML: ({ node }) => ['span', validUserId(node.attrs.userId) ? { 'data-mention': node.attrs.userId } : {}, '@' + UNKNOWN_MEMBER] }),
    ] : []),
    // Inline code stands alone in schema 4: it excludes every other mark.
    Mark.create({ name: 'code', code: true, excludes: '_', parseHTML: () => [{ tag: 'code' }], renderHTML: () => ['code', {}, 0] }),
    Node.create({ name: 'codeBlock', group: 'block', content: 'text*', marks: '', code: true, defining: true,
      addAttributes: () => ({ language: { default: null,
        parseHTML: element => { const value = element.getAttribute('data-language') ?? /(?:^|\s)language-(\S+)/.exec(element.firstElementChild?.getAttribute('class') ?? '')?.[1] ?? null; return validLanguage(value) ? value : null; },
        renderHTML: attrs => validLanguage(attrs.language) ? { 'data-language': attrs.language } : {} } }),
      parseHTML: () => [{ tag: 'pre', preserveWhitespace: 'full' }], renderHTML: ({ HTMLAttributes }) => ['pre', HTMLAttributes, ['code', {}, 0]] }),
    Node.create({ name: 'blockquote', group: 'block', content: 'block+', defining: true, parseHTML: () => [{ tag: 'blockquote' }], renderHTML: ({ HTMLAttributes }) => ['blockquote', HTMLAttributes, 0] }),
    Node.create({ name: 'taskList', group: 'block', content: 'taskItem+', parseHTML: () => [{ tag: 'ul[data-type="taskList"]', priority: 51 }], renderHTML: ({ HTMLAttributes }) => ['ul', { ...HTMLAttributes, 'data-type': 'taskList' }, 0] }),
    Node.create({ name: 'taskItem', content: 'paragraph block*', defining: true,
      addAttributes: () => ({ checked: { default: false, keepOnSplit: false, parseHTML: element => element.getAttribute('data-checked') === 'true', renderHTML: attrs => ({ 'data-checked': attrs.checked === true ? 'true' : 'false' }) } }),
      parseHTML: () => [{ tag: 'li[data-type="taskItem"]', priority: 51 }],
      renderHTML: ({ node, HTMLAttributes }) => ['li', { ...HTMLAttributes, 'data-type': 'taskItem' }, ['label', { contenteditable: 'false' }, ['input', { type: 'checkbox', disabled: 'disabled', ...(node.attrs.checked === true ? { checked: 'checked' } : {}) }]], ['div', 0]] }),
  ] : []),
 ];
}
const extensions = extensionsFor(VERSION);
const schema = getSchema(extensions);
const richSchema = getSchema(extensionsFor(RICH_VERSION));
const formattedSchema = getSchema(extensionsFor(FORMATTED_VERSION));
const latestSchema = getSchema(extensionsFor(LATEST_VERSION));
const schemaFor = (version = VERSION) => { typesFor(version); return version === LATEST_VERSION ? latestSchema : version === FORMATTED_VERSION ? formattedSchema : version === RICH_VERSION ? richSchema : schema; };
function validLinkHref(value) {
  if (typeof value !== 'string' || new TextEncoder().encode(value).length > 2048 || /[\u0000-\u0020\u007f\p{Cs}]/u.test(value)) return false;
  try { const url = new URL(value); return ['http:', 'https:'].includes(url.protocol) && !!url.hostname && !url.username && !url.password && url.href === value; } catch { return false; }
}
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
function validFormat(key, value, version = RICH_VERSION) {
  if (!formatKeys(version).includes(key)) return false;
  if (value === null) return true;
  if (!object(value)) return false;
  if (COLOR_MARKS.includes(key)) return Object.keys(value).length === 1 && validColor(value.color);
  return key === 'link' ? Object.keys(value).length === 1 && validLinkHref(value.href) : Object.keys(value).length === 0;
}
// Schema 4 combines bold, italic, strike and link; inline code stays alone.
const validCombination = types => new Set(types).size === types.length && (!types.includes('code') || types.length === 1);
function validateMarks(marks, version) {
  if (marks === undefined) return;
  if (!Array.isArray(marks) || marks.length > maxMarks(version)) throw new Error('unsupported marks');
  for (const mark of marks) if (!object(mark) || Object.keys(mark).some(key => !['type','attrs'].includes(key)) || !validFormat(mark.type, mark.attrs ?? {}, version) || mark.attrs === null) throw new Error('unsupported mark');
  if (!validCombination(marks.map(mark => mark.type))) throw new Error('unsupported mark combination');
}
const encode = bytes => {
  if (typeof Buffer !== 'undefined') return Buffer.from(bytes).toString('base64');
  let str = ''; for (const byte of bytes) str += String.fromCharCode(byte); return btoa(str);
};
const decode = text => {
  if (typeof Buffer !== 'undefined') return new Uint8Array(Buffer.from(text, 'base64'));
  return Uint8Array.from(atob(text), x => x.charCodeAt(0));
};
function canonical(value) {
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
  if (value && typeof value === 'object') return '{' + Object.keys(value).sort().map(k => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}';
  return JSON.stringify(value);
}
async function hash(value) {
  const bytes = new TextEncoder().encode('docs-d0-c14n-v1\n' + canonical(value));
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))].map(b => b.toString(16).padStart(2, '0')).join('');
}
function load(snapshot) {
  // gc:false is an explicit D0 retention policy, not a scalability claim.
  const doc = new Y.Doc({ gc: false });
  if (snapshot) Y.applyUpdate(doc, decode(snapshot));
  return doc;
}
const snapshot = doc => encode(Y.encodeStateAsUpdate(doc));
// y-tiptap 3.0.11 stopped emitting empty mark attrs. Keep the projection that
// block hashes and stored receipts were computed from: every mark has attrs.
// Yjs reports formatting in insertion order; schema 4 text can carry several
// marks, so the projection orders them as ProseMirror does. One mark (all of
// schema 3) is unchanged, so earlier hashes and receipts stay valid.
const markRank = mark => latestSchema.marks[mark?.type]?.rank ?? Number.MAX_SAFE_INTEGER;
const withMarkAttrs = node => {
  if (Array.isArray(node.marks)) node.marks = node.marks.map(mark => mark && typeof mark === 'object' && !('attrs' in mark) ? { ...mark, attrs: {} } : mark);
  if (Array.isArray(node.marks) && node.marks.length > 1) node.marks = [...node.marks].sort((a, b) => markRank(a) - markRank(b));
  if (Array.isArray(node.content)) node.content.forEach(withMarkAttrs);
  return node;
};
const project = doc => withMarkAttrs(yDocToProsemirrorJSON(doc, 'default'));
function validateJSON(json, version = VERSION) {
  const types = typesFor(version);
  const seen = new Set(); let count = 0;
  const visit = (node, depth = 0) => {
    if (depth > 40 || !node || typeof node !== 'object') throw new Error('invalid bounded node');
    if (++count > 10000) throw new Error('node limit');
    if (isRich(version) && node.type === 'doc' && (depth !== 0 || Object.keys(node).some(k => !['type','content'].includes(k)))) throw new Error('closed rich root required');
    if (Object.keys(node).some(k => !['type', 'attrs', 'content', 'text', 'marks'].includes(k))) throw new Error('unsupported node properties');
    if (node.type !== 'doc' && node.type !== 'text' && !types.includes(node.type) && !isAtom(node.type, version)) throw new Error('unsupported node');
    validateMarks(node.marks, version);
    if (node.type !== 'text' && node.marks?.length) throw new Error('unsupported block marks');
    if (isAtom(node.type, version)) {
      // Exactly one attribute, no content: an atom can carry nothing else.
      const key = ATOM_KEY[node.type];
      if (!object(node.attrs) || Object.keys(node.attrs).length !== 1 || !validAtomValue(node.type, node.attrs[key]) || (node.content !== undefined && (!Array.isArray(node.content) || node.content.length)) || node.text !== undefined) throw new Error('invalid inline atom');
      return;
    }
    if (node.type === 'text' && (typeof node.text !== 'string' || node.attrs || node.content)) throw new Error('invalid text node');
    if (node.type !== 'text' && node.text !== undefined) throw new Error('unexpected text property');
    if (types.includes(node.type)) {
      if (typeof node.attrs?.id !== 'string' || !/^[a-zA-Z0-9_-]{1,80}$/.test(node.attrs.id) || seen.has(node.attrs.id)) throw new Error('invalid or duplicate block ID');
      seen.add(node.attrs.id);
    }
    const allowed = node.type === 'tableCell' || node.type === 'tableHeader' ? ['id', 'colspan', 'rowspan', 'colwidth', 'align'] : node.type === 'heading' ? ['id', 'level'] : node.type === 'orderedList' ? ['id','start','type'] :
      isFormatted(version) && node.type === 'codeBlock' ? ['id', 'language'] : node.type === 'taskItem' ? ['id', 'checked'] : ['id'];
    if (Object.keys(node.attrs || {}).some(k => !allowed.includes(k))) throw new Error('unsupported attributes');
    if (node.type === 'codeBlock' && node.attrs.language != null && !validLanguage(node.attrs.language)) throw new Error('unsupported code block language');
    if (node.type === 'taskItem' && node.attrs.checked !== undefined && typeof node.attrs.checked !== 'boolean') throw new Error('invalid task item state');
    if (node.type === 'heading' && ![1,2,3].includes(node.attrs.level)) throw new Error('unsupported heading level');
    if (node.type === 'orderedList' && node.attrs.type != null) throw new Error('unsupported ordered list type');
    if (node.type === 'orderedList' && (!Number.isInteger(node.attrs.start??1) || (node.attrs.start??1)<1 || (node.attrs.start??1)>10000)) throw new Error('invalid ordered list start');
    if (['tableCell', 'tableHeader'].includes(node.type) && ((node.attrs.colspan ?? 1) !== 1 || (node.attrs.rowspan ?? 1) !== 1 || node.attrs.colwidth != null || node.attrs.align != null)) throw new Error('D0 simple table only');
    for (const child of node.content || []) visit(child, depth + 1);
  };
  visit(json); const pm = schemaFor(version).nodeFromJSON(json); pm.check();
  if (JSON.stringify(json).length > 500000) throw new Error('document limit');
  return json;
}
// Yjs 13.6.33's retained Item/content classes are part of this experiment's
// binary contract. Upgrades must rerun the retained-content regressions.
function validateRetained(doc, version, migrations) {
  const types = typesFor(version);
  const root = doc.getXmlFragment('default');
  const atom = type => type instanceof Y.XmlElement && isAtom(type.nodeName, version);
  const supportedType = type => type === root || type instanceof Y.XmlText ||
    (type instanceof Y.XmlElement && (types.includes(type.nodeName) || atom(type)));
  for (const structs of doc.store.clients.values()) for (const item of structs) {
    // GC carries no recoverable payload. Deleted Items still do.
    if (item instanceof Y.GC) continue;
    if (migrations && item.parent === migrations) continue; // Fully checked, including retained payloads, before this walk.
    if (!(item instanceof Y.Item) || !supportedType(item.parent)) throw new Error('unsupported retained Yjs parent');
    // Atoms are leaves: nothing was ever allowed inside one, even if deleted.
    if (item.parentSub === null && atom(item.parent)) throw new Error('unsupported retained atom content');
    const content = item.content;
    if (item.parentSub !== null) {
      const parent = item.parent;
      if (!(parent instanceof Y.XmlElement)) throw new Error('unsupported retained text attributes');
      const key = item.parentSub;
      const cell = ['tableCell', 'tableHeader'].includes(parent.nodeName);
      const allowed = atom(parent) ? key === ATOM_KEY[parent.nodeName] : key === 'id' || (key === 'level' && parent.nodeName === 'heading') || (['start','type'].includes(key) && parent.nodeName === 'orderedList') ||
        (cell && ['colspan', 'rowspan', 'colwidth', 'align'].includes(key)) ||
        (isFormatted(version) && ((key === 'language' && parent.nodeName === 'codeBlock') || (key === 'checked' && parent.nodeName === 'taskItem')));
      if (!allowed) throw new Error('unsupported retained attributes');
      if (content instanceof Y.ContentDeleted) continue;
      // Yjs coalesces adjacent deleted versions of the same scalar attribute.
      // Active attributes remain scalar; every retained value still needs validation.
      if (!(content instanceof Y.ContentAny) || content.arr.length === 0 || (!item.deleted && content.arr.length !== 1)) throw new Error('unsupported retained attribute content');
      for (const value of content.arr) {
        const valid = atom(parent) ? validAtomValue(parent.nodeName, value) : key === 'id' ? value === null || (typeof value === 'string' && /^[a-zA-Z0-9_-]{1,80}$/.test(value)) :
          key === 'level' ? [1,2,3].includes(value) : key === 'start' ? Number.isInteger(value) && value >=1 && value<=10000 : ['colspan','rowspan'].includes(key) ? value===1 :
          key === 'language' ? value === null || validLanguage(value) : key === 'checked' ? typeof value === 'boolean' : value===null;
        if (!valid) throw new Error('unsupported retained attribute value');
      }
    } else if (content instanceof Y.ContentType) {
      if (item.parent instanceof Y.XmlText || !supportedType(content.type) || content.type === root) throw new Error('unsupported retained Yjs type');
    } else if (content instanceof Y.ContentString) {
      if (!(item.parent instanceof Y.XmlText)) throw new Error('unsupported retained string parent');
    } else if (content instanceof Y.ContentFormat && isRich(version)) {
      if (!(item.parent instanceof Y.XmlText) || item.parent.parent?.nodeName === 'codeBlock' || !validFormat(content.key, content.value, version)) throw new Error('unsupported retained format');
    } else if (!(content instanceof Y.ContentDeleted)) {
      // Includes formatting (even removed marks), embeds, binary and subdocs.
      throw new Error('unsupported retained Yjs content');
    }
  }
}
function validate(doc, version = VERSION) {
  typesFor(version);
  if(doc.store.pendingStructs || doc.store.pendingDs)throw new Error("unresolved Yjs dependencies");
  // Projection serializers omit some raw Yjs metadata. Validate the binary
  // tree as well so omitted attributes cannot bypass the content contract.
  const inspect = (node, depth = 0) => {
    if (depth > 40) throw new Error('node depth limit');
    if (node instanceof Y.XmlText) {
      if (Object.keys(node.getAttributes()).length) throw new Error('unsupported text attributes');
      if (node.toDelta().some(part => typeof part.insert !== 'string' || Object.keys(part.attributes || {}).length > maxMarks(version) || !validCombination(Object.keys(part.attributes || {})) || Object.entries(part.attributes || {}).some(([key,value]) => !validFormat(key,value,version) || value === null))) throw new Error('unsupported text delta');
    } else if (node instanceof Y.XmlElement && isAtom(node.nodeName, version)) {
      const attrs = node.getAttributes(), key = ATOM_KEY[node.nodeName];
      if (node.length !== 0 || Object.keys(attrs).length !== 1 || !validAtomValue(node.nodeName, attrs[key])) throw new Error('invalid inline atom');
    } else if (node instanceof Y.XmlElement || node instanceof Y.XmlFragment) {
      for (const child of node.toArray()) inspect(child, depth + 1);
    } else throw new Error('unsupported Yjs content');
  };
  inspect(doc.getXmlFragment('default'));
  const json = validateJSON(project(doc), version);
  if (Y.encodeStateAsUpdate(doc).length > 1 << 20) throw new Error('document limit');
  // Prevent opaque client-controlled Yjs roots from becoming a covert payload.
  const migrations = validatePositionMigrations(doc, Y);
  validateRetained(doc, version, migrations);
  return json;
}
async function outline(doc, version = VERSION) {
  const json = validate(doc, version); const blocks = [];
  const visit = async (node, parent = null) => {
    if (node.attrs?.id) blocks.push({ id: node.attrs.id, type: node.type, parent, hash: await hash(node), text: textOf(node) });
    for (const child of node.content || []) await visit(child, node.attrs?.id || parent);
  };
  await visit(json); return blocks;
}
function textOf(node) { return node.text || (node.content || []).map(textOf).join(''); }
/** @returns {import("yjs").XmlElement} */
function find(doc, id) {
  let found;
  const visit = node => {
    if (node instanceof Y.XmlElement && node.getAttribute('id') === id) found = node;
    if (node instanceof Y.XmlElement || node instanceof Y.XmlFragment) for (const child of node.toArray()) visit(child);
  };
  visit(doc.getXmlFragment('default')); if (!found) throw new Error('missing block'); return found;
}
function textNode(doc, id) {
  const block = find(doc, id);
  if (!['paragraph', 'heading'].includes(block.nodeName)) throw new Error('text target required');
  let text = block.toArray().find(n => n instanceof Y.XmlText);
  if (!text) { text = new Y.XmlText(); block.insert(0, [text]); }
  return text;
}
// Text offsets are aggregate UTF-16 positions across every live text child.
// Reading anchors never coalesces children or changes retained CRDT identities.
function textChildren(doc, id, version = VERSION) {
  const block = find(doc, id);
  if (!(isRich(version) ? ['paragraph', 'heading', 'codeBlock'] : ['paragraph', 'heading']).includes(block.nodeName)) throw new Error('text target required');
  return { block, texts: block.toArray().filter(node => node instanceof Y.XmlText) };
}
const textBoundary = (text, index) => index === 0 || index === text.length ||
  !(text.charCodeAt(index - 1) >= 0xd800 && text.charCodeAt(index - 1) <= 0xdbff &&
    text.charCodeAt(index) >= 0xdc00 && text.charCodeAt(index) <= 0xdfff);
function textPosition(texts, offset, assoc) {
  let base = 0;
  for (const text of texts) {
    if (offset < base + text.length || (assoc < 0 && offset === base + text.length && text.length > 0)) return { text, index: offset - base };
    base += text.length;
  }
  const text = texts.at(-1);
  if (!text || offset !== base) throw new Error('invalid text position');
  return { text, index: text.length };
}
const plainXmlText = text => text.toDelta().map(part => { if (typeof part.insert !== 'string') throw new Error('unsupported text content'); return part.insert; }).join('');
function aggregatePosition(texts, position) {
  let base = 0;
  for (const text of texts) {
    if (text === position.type) return position.index <= text.length ? base + position.index : null;
    base += text.length;
  }
  return null;
}
function patch(doc, op, version = VERSION) {
  if (op.op !== 'replaceText' || typeof op.text !== 'string' || op.text.length > 100000) throw new Error('D0 requires bounded replaceText');
  const { block, texts } = textChildren(doc, op.id, version);
  const original = texts.map(plainXmlText).join('');
  const range = op.range ?? { from: 0, to: original.length };
  if (!Number.isInteger(range.from) || !Number.isInteger(range.to) || range.from < 0 || range.to < range.from || range.to > original.length ||
    !textBoundary(original, range.from) || !textBoundary(original, range.to)) throw new Error('invalid semantic edit range');
  const prefix = original.slice(0, range.from), suffixText = original.slice(range.to);
  if (op.text.length < prefix.length + suffixText.length || !op.text.startsWith(prefix) || !op.text.endsWith(suffixText)) throw new Error('semantic edit outside selected range');
  const before = original.slice(range.from, range.to), after = op.text.slice(range.from, op.text.length - suffixText.length);
  let localFrom = 0, suffix = 0;
  while (localFrom < before.length && localFrom < after.length && before[localFrom] === after[localFrom]) localFrom++;
  while (!textBoundary(before, localFrom) || !textBoundary(after, localFrom)) localFrom--;
  while (suffix < before.length - localFrom && suffix < after.length - localFrom && before[before.length - suffix - 1] === after[after.length - suffix - 1]) suffix++;
  while (!textBoundary(before, before.length - suffix) || !textBoundary(after, after.length - suffix)) suffix--;
  const from = range.from + localFrom, to = range.to - suffix, replacement = after.slice(localFrom, after.length - suffix);
  // Dates and mentions have no width in text offsets, so text inserted exactly
  // beside one could land on either side, and a range across one would leave it
  // stranded. Refuse both instead of guessing (#5155).
  let atomBase = 0;
  for (const child of block.toArray()) {
    if (child instanceof Y.XmlText) { atomBase += child.length; continue; }
    if (!(child instanceof Y.XmlElement) || !ATOMS.includes(child.nodeName)) continue;
    if ((replacement && from === atomBase) || (from < atomBase && atomBase < to)) throw Object.assign(new Error('semantic edit touches a date or mention; edit that text in the editor'), { code: 'ambiguous_inline_atom' });
  }
  if (from !== to || replacement) doc.transact(() => {
    if (texts.length === 0) { const text = new Y.XmlText(); block.insert(0, [text]); texts.push(text); }
    const insertion = textPosition(texts, from, 0);
    let base = 0;
    for (const text of texts) {
      const length = text.length, start = Math.max(0, from - base), end = Math.min(length, to - base);
      if (end > start) text.delete(start, end - start);
      base += length;
    }
    if (replacement) insertion.text.insert(insertion.index, replacement, isRich(version) ? {} : undefined);
  }, 'semantic-agent');
  validate(doc, version);
  return { from, to, insert: replacement };
}
function anchor(doc, id, from, to, threadID, rev, version = VERSION) {
  const { texts } = textChildren(doc, id, version), text = texts.map(plainXmlText).join('');
  if (!Number.isInteger(from) || !Number.isInteger(to) || from < 0 || to <= from || to > text.length ||
    !textBoundary(text, from) || !textBoundary(text, to)) throw new Error('invalid range');
  const relative = (text, index, assoc) => encode(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(text, index, assoc)));
  const spans = []; let base = 0;
  for (const part of texts) {
    const start = Math.max(0, from - base), end = Math.min(part.length, to - base);
    if (end > start) spans.push({ start: relative(part, start, 0), end: relative(part, end, -1) });
    base += part.length;
  }
  const result = { id: threadID, block: id, quote: text.slice(from, to), checkpoint: rev,
    start: spans[0].start, end: spans.at(-1).end, spans };
  // Match the durable Go anchor limit. Many tiny children must not create an
  // unbounded anchor even when the selected quote itself is short.
  if (spans.length > 64 || new TextEncoder().encode(JSON.stringify(result)).length > 8192) throw new Error('comment anchor limit');
  return result;
}
function resolve(doc, thread, version = VERSION) {
  let block, texts;
  try { ({ block, texts } = textChildren(doc, thread.block, version)); } catch { return { ...thread, status: 'detached' }; }
  const absolute = encoded => Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(decode(encoded)), doc);
  const start = absolute(thread.start), end = absolute(thread.end);
  if (!start || !end || start.type.parent !== block || end.type.parent !== block) return { ...thread, status: 'detached' };
  const from = aggregatePosition(texts, start), to = aggregatePosition(texts, end);
  if (from === null || to === null) return { ...thread, status: 'detached' };
  if (thread.spans === undefined) {
    // Existing anchors were confined to one text child. Do not reinterpret an
    // unsupported cross-type legacy pair as a broad paragraph interval.
    if (start.type !== end.type || from > to) return { ...thread, status: 'detached' };
    return { ...thread, status: from === to ? 'collapsed' : 'attached', from, to };
  }
  if (!Array.isArray(thread.spans) || thread.spans.length === 0 || thread.spans.length > 64 ||
    thread.spans[0].start !== thread.start || thread.spans.at(-1).end !== thread.end) return { ...thread, status: 'detached' };
  const surviving = [];
  for (const span of thread.spans) {
    const first = absolute(span.start), last = absolute(span.end);
    if (!first || !last || first.type !== last.type || first.type.parent !== block || last.type.parent !== block) continue;
    const left = aggregatePosition(texts, first), right = aggregatePosition(texts, last);
    if (left !== null && right !== null && left < right) {
      if (surviving.length && left < surviving.at(-1).to) return { ...thread, status: 'detached' };
      surviving.push({ from: left, to: right });
    }
  }
  // Separate collapsed children can have newly inserted text between them.
  // That text never belonged to this comment's selected CRDT identities.
  if (!surviving.length) return { ...thread, status: 'collapsed', from: Math.min(from, to), to: Math.min(from, to) };
  return { ...thread, status: 'attached', from: surviving[0].from, to: surviving.at(-1).to };
}
function initialJSON() {
  const p = (id, text) => ({ type: 'paragraph', attrs: { id }, content: text ? [{ type: 'text', text }] : [] });
  const cell = (id, text) => ({ type: 'tableCell', attrs: { id }, content: [p(id + '-p', text)] });
  return { type: 'doc', content: [
    { type: 'heading', attrs: { id: 'title', level: 1 }, content: [{ type: 'text', text: 'D0 dokument' }] },
    p('intro', 'Společný text pro dva lidi a agenta.'),
    { type: 'table', attrs: { id: 'table' }, content: [{ type: 'tableRow', attrs: { id: 'row' }, content: [cell('cell-a', 'A'), cell('cell-b', 'B')] }] },
  ] };
}
/** @param {unknown} json */
function create(json = initialJSON(), version = VERSION) {
  // Validate before ProseMirror can normalize away unknown future content.
  validateJSON(json, version);
  const pm = schemaFor(version).nodeFromJSON(json); pm.check();
  const doc = prosemirrorToYDoc(pm, 'default'); doc.gc = false; validate(doc, version); return doc;
}

// `people` maps a mention's user id to the name this reader may see. A mention
// without one is written as the neutral label, so an export never confirms
// whether an id belongs to anyone.
function markdown(json, version = RICH_VERSION, { people } = {}) {
  const personName = id => (people && Object.hasOwn(people, id) && typeof people[id] === 'string' && people[id]) ? people[id] : UNKNOWN_MEMBER;
  // Schema 4 can produce strikethrough, so a literal tilde is escaped there only:
  // earlier documents keep their exact export.
  const escape = isFormatted(version) ? text => text.replace(/([\\`*_{}\[\]<>#!|&~])/g, '\\$1') : text => text.replace(/([\\`*_{}\[\]<>#!|&])/g, '\\$1');
  // Emphasis delimiters must touch non-space text: outer spaces stay outside.
  // Schema 5 underline, colour and highlight have no Markdown syntax; they are
  // written as closed inline HTML whose only attribute value is a palette name,
  // around the emphasis. Text itself stays escaped, so it never becomes markup.
  const emphasis = (value, marks) => {
    const open = [['bold','**'],['italic','*'],['strike','~~']].filter(([type]) => marks.some(mark => mark.type === type)).map(([, d]) => d);
    const has = type => marks.find(mark => mark.type === type);
    const tags = [];
    if (has('textColor') && validColor(has('textColor').attrs?.color)) tags.push([`<span data-text-color="${has('textColor').attrs.color}">`, '</span>']);
    if (has('highlight') && validColor(has('highlight').attrs?.color)) tags.push([`<mark data-highlight="${has('highlight').attrs.color}">`, '</mark>']);
    if (has('underline')) tags.push(['<u>', '</u>']);
    if (!open.length && !tags.length) return value;
    const [, lead, core, trail] = /^(\s*)([\s\S]*?)(\s*)$/.exec(value);
    // Tags wrap the emphasis, so a delimiter always touches text and stays
    // flanking inside a word (abc<u>**def**</u>ghi).
    const inner = open.join('') + core + [...open].reverse().join('');
    return core ? lead + tags.map(([o]) => o).join('') + inner + tags.map(([, c]) => c).reverse().join('') + trail : value;
  };
  // GFM strips one escape before a pipe before parsing inline code. An odd
  // original backslash run cannot survive that preprocessing without turning
  // the pipe into a cell separator. Refuse rather than change the code text.
  const tableCode = value => {
    let result = '', backslashes = 0;
    for (const char of value) {
      if (char === '\\') { result += char; backslashes++; continue; }
      if (char === '|') {
        if (backslashes % 2) throw Object.assign(new Error('Markdown export cannot preserve inline code with an odd backslash run before a table pipe; move this code outside the table.'), {code:'unsupported_markdown'});
        result += '\\|';
      } else result += char;
      backslashes = 0;
    }
    return result;
  };
  // URI percent encoding avoids both CommonMark destination escapes and GFM
  // table delimiters. Preserve existing percent escapes and decoded URL data.
  const destination = href => [...href].map(char => char === '\\' ? '%5C' : char === '|' ? '%7C' : char === '&' ? '&amp;' : char).join('');
  const visit = (node, depth=0, table = false) => {
    if (node.type==='text') {
      const value = node.text || '', mark = node.marks?.[0];
      if (mark?.type === 'code') { if (/[\r\n]/.test(value)) throw Object.assign(new Error('Markdown export cannot preserve line breaks in inline code; use a code block.'), {code:'unsupported_markdown'}); const fence = '`'.repeat(Math.max(0, ...(value.match(/`+/g) || []).map(run => run.length)) + 1); const pad = !/^ +$/.test(value) && /^[ `]|[ `]$/.test(value) ? ' ' : ''; return fence + pad + (table ? tableCode(value) : value) + pad + fence; }
      const marks = node.marks || [], link = marks.find(mark => mark.type === 'link');
      const styled = emphasis(escape(value), marks);
      if (link) return '[' + styled + '](<' + destination(link.attrs.href) + '>)';
      return styled;
    }
    if (node.type==='date' && isAtom(node.type, version)) return escape(node.attrs.value);
    if (node.type==='mention' && isAtom(node.type, version)) return '@' + escape(personName(node.attrs.userId).replace(/[\r\n]+/g, ' '));
    if (node.type==='codeBlock') { if (table) throw Object.assign(new Error('Markdown export cannot represent a code block inside a simple table; move it outside the table.'), {code:'unsupported_markdown'}); const value = textOf(node), fence = '`'.repeat(Math.max(2, ...(value.match(/`+/g) || []).map(run => run.length)) + 1); return fence + (validLanguage(node.attrs?.language) ? node.attrs.language : '') + '\n' + value + '\n' + fence + '\n\n'; }
    if (node.type==='blockquote') {
      if (table) throw Object.assign(new Error('Markdown export cannot represent a quote inside a simple table; move it outside the table.'), {code:'unsupported_markdown'});
      return (node.content||[]).map(n=>visit(n,0,false)).join('').trimEnd().split('\n').map(line => line ? '> ' + line : '>').join('\n') + '\n\n';
    }
    if (node.type==='taskList') {
      if (table) throw Object.assign(new Error('Markdown export cannot represent a checklist inside a simple table; move it outside the table.'), {code:'unsupported_markdown'});
      return (node.content||[]).map(n=>'  '.repeat(depth)+(n.attrs?.checked === true ? '- [x] ' : '- [ ] ')+visit(n,depth+1,table).trim().replace(/\n/g,'\n'+'  '.repeat(depth+1))).join('\n')+'\n\n';
    }
    if (node.type==='heading') return '#'.repeat(node.attrs.level)+' '+(node.content||[]).map(n=>visit(n,depth,table)).join('')+'\n\n';
    if (node.type==='paragraph') return (node.content||[]).map(n=>visit(n,depth,table)).join('')+'\n\n';
    if (node.type==='bulletList'||node.type==='orderedList') return (node.content||[]).map((n,i)=>'  '.repeat(depth)+(node.type==='bulletList'?'- ':`${(node.attrs.start||1)+i}. `)+visit(n,depth+1,table).trim().replace(/\n/g,'\n'+'  '.repeat(depth+1))).join('\n')+'\n\n';
    if (node.type==='table') {
      const rich = node => !!node.marks?.length || ['codeBlock','blockquote','taskList','date','mention'].includes(node.type) || (node.content || []).some(rich);
      const rows=(node.content||[]).map(r=>r.content.map(c=>(rich(c) ? (c.content||[]).map(n=>visit(n,depth,true).trimEnd()).join(' ') : escape((c.content||[]).map(textOf).join(' '))).replace(/\n/g,' ')));
      return rows.map((r,i)=>'| '+r.join(' | ')+' |'+(i===0?'\n| '+r.map(()=>'---').join(' | ')+' |':'')).join('\n')+'\n\n';
    }
    return (node.content||[]).map(n=>visit(n,depth,table)).join('');
  };
  return visit(json).trimEnd()+'\n';
}

return { VERSION, RICH_VERSION, FORMATTED_VERSION, LATEST_VERSION, PALETTE, validColor, ATOMS, validDate, validUserId, UNKNOWN_MEMBER, validLanguage, extensionsFor, extensions, schema, schemaFor, validLinkHref, encode, decode, canonical, hash, load, snapshot, project, validateJSON, validate, outline, textOf, find, textNode, patch, anchor, resolve, initialJSON, create, markdown };
}
