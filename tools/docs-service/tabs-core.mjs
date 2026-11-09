// Docs tabs (design: CANVAS-V1-DESIGN.md §4, planned content schema 7).
//
// The first tab is the existing `default` fragment, so every older document is
// a one-tab document without migration. Further tabs are XmlFragments named
// `tab:<id>`. Their names, nesting and order live in one Y.Map, `tabs`:
//
//   tabs  key = tab ID -> { name, parent: tab ID | null, order: fractional key }
//
// Renaming, reordering and nesting change only this metadata. Tab content is
// never moved (Yjs has no move: moving content would give it new identities
// and detach its comments). No content schema admits these roots yet.
export function createTabs({ Y }) {
  const TABS = 'tabs';
  const DEFAULT = 'default';
  const PREFIX = 'tab:';
  const ID_PATTERN = /^[a-zA-Z0-9_-]{1,80}$/;
  const RESERVED = new Set(['__proto__', 'constructor', 'prototype']);
  const validID = id => typeof id === 'string' && ID_PATTERN.test(id) && !RESERVED.has(id);
  // Fractional keys never end in '0', so a key below any stored key always exists.
  const ORDER = /^[0-9A-Za-z]{0,63}[1-9A-Za-z]$/;
  // Implicit orders: the first tab without metadata sorts first, orphan fragments last.
  const IMPLICIT = { [DEFAULT]: '1', orphan: 'zzzz' };
  const LIMITS = Object.freeze({ tabs: 200, name: 120, depth: 8, retainedBytes: 256 * 1024 });
  const ORIGIN = 'tabs';
  const fail = message => { throw new Error(message); };
  const plain = v => v !== null && typeof v === 'object' && !Array.isArray(v) && Object.getPrototypeOf(v) === Object.prototype;
  const validName = name => typeof name === 'string' && name.length <= LIMITS.name && name.trim().length > 0 &&
    !/[\u0000-\u001f\u007f]|\p{Cs}/u.test(name);
  function validMeta(key, v) {
    if (!plain(v) || Object.keys(v).some(k => !['name', 'parent', 'order'].includes(k) || v[k] === undefined) || !['name', 'parent', 'order'].every(k => Object.hasOwn(v, k))) return false;
    return validName(v.name) && (v.parent === null || (validID(v.parent) && v.parent !== key)) && typeof v.order === 'string' && ORDER.test(v.order);
  }
  const fragmentName = id => id === DEFAULT ? DEFAULT : PREFIX + id;
  const bytes = v => new TextEncoder().encode(JSON.stringify(v)).length;

  /**
   * Validate the tab metadata, live and retained, and every `tab:` root's
   * shape. Returns { meta: Map(id -> value), fragments: Set(id) }. The content
   * inside each fragment is the document schema's job, as `default` is.
   */
  function validateTabs(doc) {
    const meta = new Map(), fragments = new Set([DEFAULT]);
    for (const [name, type] of doc.share) {
      if (!name.startsWith(PREFIX)) continue;
      const id = name.slice(PREFIX.length);
      if (!validID(id) || id === DEFAULT) fail('invalid tab root');
      // A fragment carries only list content, never map entries.
      if (!(type instanceof Y.XmlFragment || type.constructor === Y.AbstractType) || type._map.size !== 0) fail('invalid tab root');
      fragments.add(id);
    }
    if (fragments.size > LIMITS.tabs) fail('tab limit');
    const root = doc.share.get(TABS);
    if (root === undefined) return { meta, fragments };
    if (!(root instanceof Y.Map || root.constructor === Y.AbstractType) || root._start !== null) fail('invalid tab metadata');
    let retained = 0;
    for (const structs of doc.store.clients.values()) for (const item of structs) {
      if (!(item instanceof Y.Item)) continue;
      if (item.parent !== root) {
        let type = item.parent, depth = 0;
        while (type?._item && depth++ < 64) type = type._item.parent;
        if (type === root) fail('invalid tab metadata');
        continue;
      }
      if (item.parentSub === null || !validID(item.parentSub)) fail('invalid tab ID');
      retained += item.parentSub.length;
      if (item.content instanceof Y.ContentDeleted) continue;
      if (!(item.content instanceof Y.ContentAny) || item.content.arr.length < 1 || (!item.deleted && item.content.arr.length !== 1)) fail('invalid tab metadata');
      for (const value of item.content.arr) {
        if (!validMeta(item.parentSub, value)) fail('invalid tab metadata');
        retained += bytes(value);
      }
      if (retained > LIMITS.retainedBytes) fail('tab history limit');
      if (!item.deleted) meta.set(item.parentSub, item.content.arr[0]);
    }
    if (meta.size > LIMITS.tabs) fail('tab limit');
    return { meta, fragments };
  }

  /**
   * The tab tree as it renders: a deterministic forest every replica agrees on.
   * - A fragment without metadata (its metadata was removed while someone
   *   wrote in it) is shown as "Untitled tab" at the root: content is never hidden.
   * - Metadata without a fragment is shown as an empty tab: an empty tab never disappears.
   * - A missing parent makes the tab a root. Concurrent nesting can form a
   *   cycle; the cycle's smallest ID becomes a root.
   * Siblings sort by order, then ID; `default` without metadata sorts first.
   */
  function tree(doc) {
    const { meta, fragments } = validateTabs(doc);
    // A Yjs root cannot be removed: a deleted tab leaves an empty fragment
    // without metadata, which stays hidden. Live content keeps it visible.
    const live = id => doc.share.get(fragmentName(id))?._length > 0;
    const ids = new Set([...[...fragments].filter(id => id === DEFAULT || meta.has(id) || live(id)), ...meta.keys()]);
    const info = id => meta.get(id) ?? { name: id === DEFAULT ? 'Tab 1' : 'Untitled tab', parent: null, order: id === DEFAULT ? IMPLICIT[DEFAULT] : IMPLICIT.orphan };
    const parent = new Map([...ids].map(id => [id, ids.has(info(id).parent) ? info(id).parent : null]));
    for (const id of [...ids].sort()) {
      const path = []; let cursor = id;
      while (cursor !== null && !path.includes(cursor)) { path.push(cursor); cursor = parent.get(cursor); }
      if (cursor !== null) { const cycle = path.slice(path.indexOf(cursor)); parent.set([...cycle].sort()[0], null); }
    }
    const nodes = new Map([...ids].map(id => [id, { id, name: info(id).name, order: info(id).order, children: [] }]));
    const roots = [];
    for (const id of ids) (parent.get(id) === null ? roots : nodes.get(parent.get(id)).children).push(nodes.get(id));
    const sort = list => { list.sort((a, b) => a.order < b.order ? -1 : a.order > b.order ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0); list.forEach(n => sort(n.children)); return list; };
    const strip = n => ({ id: n.id, name: n.name, children: n.children.map(strip) });
    return sort(roots).map(strip);
  }

  // Fractional keys over [0-9A-Za-z]: a key strictly between a and b ('' = open end).
  const DIGITS = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
  function between(a = '', b = '') {
    if ((b && a >= b) || (a && !ORDER.test(a)) || (b && !ORDER.test(b))) fail('invalid tab order');
    let key = '';
    for (let i = 0; ; i++) {
      const lo = i < a.length ? DIGITS.indexOf(a[i]) : 0;
      const hi = b && i < b.length ? DIGITS.indexOf(b[i]) : DIGITS.length;
      if (hi - lo > 1) { key += DIGITS[Math.floor((lo + hi) / 2)]; return key; }
      key += DIGITS[lo];
      if (key.length >= 63) fail('tab order exhausted');
      // Once the prefix is strictly above a, any suffix stays below b only while equal to b's prefix.
      if (b && key !== b.slice(0, key.length)) b = '';
    }
  }

  const tabsMap = doc => doc.getMap(TABS);
  function flatWithParents(forest, parentId = null, depth = 0, out = []) {
    for (const n of forest) { out.push({ id: n.id, parent: parentId, depth }); flatWithParents(n.children, n.id, depth + 1, out); }
    return out;
  }
  function orderOf(doc, id) { return tabsMap(doc).get(id)?.order ?? (id === DEFAULT ? IMPLICIT[DEFAULT] : IMPLICIT.orphan); }
  function slot(doc, parentId, beforeId, self) {
    const list = flatWithParents(tree(doc)).filter(n => n.parent === parentId && n.id !== self).map(n => n.id);
    const index = beforeId == null ? list.length : list.indexOf(beforeId);
    if (index < 0) fail('sibling tab required');
    const left = index > 0 ? orderOf(doc, list[index - 1]) : '', right = index < list.length ? orderOf(doc, list[index]) : '';
    return between(left, right);
  }
  function depthOf(doc, id) { return flatWithParents(tree(doc)).find(n => n.id === id)?.depth ?? 0; }
  function subtreeHeight(doc, id) {
    const node = (function find(list) { for (const n of list) { if (n.id === id) return n; const f = find(n.children); if (f) return f; } return null; })(tree(doc));
    const height = n => 1 + Math.max(0, ...n.children.map(height));
    return node ? height(node) : 1;
  }
  const exists = (doc, id) => flatWithParents(tree(doc)).some(n => n.id === id);
  function descendants(doc, id) {
    const out = new Set(); const node = (function find(list) { for (const n of list) { if (n.id === id) return n; const f = find(n.children); if (f) return f; } return null; })(tree(doc));
    (function add(n) { for (const c of n?.children ?? []) { out.add(c.id); add(c); } })(node);
    return out;
  }

  /** Create a tab with one empty paragraph, as the last child of `parent` or before `before`. */
  function createTab(doc, { id, name, parent = null, before = null, blockId }, origin = ORIGIN) {
    validateTabs(doc);
    if (!validID(id) || id === DEFAULT || exists(doc, id)) fail('new tab ID required');
    if (!validName(name)) fail('invalid tab name');
    if (parent !== null && !exists(doc, parent)) fail('parent tab required');
    if (parent !== null && depthOf(doc, parent) + 1 >= LIMITS.depth) fail('tab nesting limit');
    if (!validID(blockId)) fail('block ID required');
    const value = { name, parent, order: slot(doc, parent, before, id) };
    doc.transact(() => {
      const paragraph = new Y.XmlElement('paragraph'); paragraph.setAttribute('id', blockId);
      doc.getXmlFragment(fragmentName(id)).insert(0, [paragraph]);
      tabsMap(doc).set(id, value);
    }, origin);
    return value;
  }

  function renameTab(doc, id, name, origin = ORIGIN) {
    validateTabs(doc);
    if (!exists(doc, id)) fail('tab required');
    if (!validName(name)) fail('invalid tab name');
    const current = tabsMap(doc).get(id) ?? { parent: null, order: orderOf(doc, id) };
    doc.transact(() => tabsMap(doc).set(id, { name, parent: current.parent, order: current.order }), origin);
  }

  /** Reorder or nest: metadata only; the tab's content and its comments are untouched. */
  function moveTab(doc, id, { parent = null, before = null }, origin = ORIGIN) {
    validateTabs(doc);
    if (!exists(doc, id)) fail('tab required');
    if (parent !== null && (!exists(doc, parent) || parent === id || descendants(doc, id).has(parent))) fail('a tab cannot move into itself');
    if (parent !== null && depthOf(doc, parent) + subtreeHeight(doc, id) >= LIMITS.depth) fail('tab nesting limit');
    const name = tabsMap(doc).get(id)?.name ?? (id === DEFAULT ? 'Tab 1' : 'Untitled tab');
    const order = slot(doc, parent, before, id);
    doc.transact(() => tabsMap(doc).set(id, { name, parent, order }), origin);
  }

  /**
   * Delete a tab: its content and metadata in one transaction; its child tabs
   * move up to its parent. The first tab (`default`) cannot be deleted.
   * Restoring is the document's version restore (no separate trash in v1).
   */
  function deleteTab(doc, id, origin = ORIGIN) {
    validateTabs(doc);
    if (id === DEFAULT) fail('the first tab cannot be deleted');
    if (!exists(doc, id)) fail('tab required');
    const parentId = flatWithParents(tree(doc)).find(n => n.id === id).parent;
    const children = flatWithParents(tree(doc)).filter(n => n.parent === id).map(n => n.id);
    doc.transact(() => {
      for (const child of children) {
        const current = tabsMap(doc).get(child) ?? { name: 'Untitled tab', order: IMPLICIT.orphan };
        tabsMap(doc).set(child, { name: current.name, parent: parentId, order: current.order });
      }
      const fragment = doc.getXmlFragment(fragmentName(id));
      if (fragment.length) fragment.delete(0, fragment.length);
      if (tabsMap(doc).has(id)) tabsMap(doc).delete(id);
    }, origin);
  }

  return { TABS, DEFAULT, PREFIX, LIMITS, ORIGIN, fragmentName, validateTabs, tree, between, createTab, renameTab, moveTab, deleteTab };
}
