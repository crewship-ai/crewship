// Docs tabs (design: CANVAS-V1-DESIGN.md §4, planned content schema 7).
//
// The first tab is the existing `default` fragment, so every older document is
// a one-tab document without migration. Further tabs are XmlFragments named
// `tab:<id>`. Where a tab sits and what it is called are two separate maps, so
// a concurrent rename and move both survive (each is last-writer-wins alone):
//
//   tabs       key = tab ID -> { parent: tab ID | null, order: fractional key }
//   tab-names  key = tab ID -> name
//
// Renaming, reordering and nesting change only these maps. Tab content is
// never moved (Yjs has no move: moving content would give it new identities
// and detach its comments). No content schema admits these roots yet.
export function createTabs({ Y }) {
  const TABS = 'tabs';
  const NAMES = 'tab-names';
  const DEFAULT = 'default';
  const PREFIX = 'tab:';
  const ID_PATTERN = /^[a-zA-Z0-9_-]{1,80}$/;
  const RESERVED = new Set(['__proto__', 'constructor', 'prototype']);
  const validID = id => typeof id === 'string' && ID_PATTERN.test(id) && !RESERVED.has(id);
  // Fractional keys never end in '0', so a key below any stored key always exists.
  const ORDER = /^[0-9A-Za-z]{0,63}[1-9A-Za-z]$/;
  // Implicit orders: the first tab without metadata sorts first, orphan fragments last.
  const IMPLICIT = { [DEFAULT]: '1', orphan: 'zzzz' };
  // `tabs` is the hard limit the validator (and so every server commit) enforces;
  // mutators stop at `create`, leaving room for tabs created concurrently elsewhere.
  const LIMITS = Object.freeze({ tabs: 400, create: 200, name: 120, depth: 8, retainedBytes: 256 * 1024 });
  const ORIGIN = 'tabs';
  const fail = message => { throw new Error(message); };
  const plain = v => v !== null && typeof v === 'object' && !Array.isArray(v) && Object.getPrototypeOf(v) === Object.prototype;
  const validName = name => typeof name === 'string' && name.length <= LIMITS.name && name.trim().length > 0 &&
    !/[\u0000-\u001f\u007f]|\p{Cs}/u.test(name);
  function validPlace(key, v) {
    if (!plain(v) || Object.keys(v).length !== 2 || !Object.hasOwn(v, 'parent') || !Object.hasOwn(v, 'order')) return false;
    return (v.parent === null || (validID(v.parent) && v.parent !== key)) && typeof v.order === 'string' && ORDER.test(v.order);
  }
  const fragmentName = id => id === DEFAULT ? DEFAULT : PREFIX + id;
  const encoder = new TextEncoder();
  const utf8 = s => encoder.encode(s).length;
  // Upper bound of one map item in an encoded update (info, two IDs, root and key
  // strings, content length, a delete-set range) plus lib0 writeAny of its values.
  const any = v => v === null || typeof v === 'boolean' ? 1 : typeof v === 'number' ? 9 : typeof v === 'string' ? 6 + utf8(v)
    : 6 + Object.entries(v).reduce((sum, [k, x]) => sum + 5 + utf8(k) + any(x), 0);
  const encoded = (root, key, values) => 1 + 32 + 1 + 5 + utf8(root) + 5 + utf8(key) + 8 + 16 + values.reduce((sum, v) => sum + any(v), 0);
  const WRITER = 34;
  // Live entries of a map root without creating or converting it: a read must
  // never write, and a freshly loaded root is still an untyped AbstractType.
  const liveOf = (doc, name) => {
    const type = doc.share.get(name), out = new Map();
    if (type) for (const [key, item] of type._map) if (!item.deleted) out.set(key, item.content.getContent().at(-1));
    return out;
  };

  /**
   * Validate the tab maps, live and retained, and every `tab:` root's shape.
   * Returns { place, names, fragments, retained }. The content inside each
   * fragment is the document schema's job, as `default` is.
   */
  function tabRoots(doc) {
    const fragments = new Set([DEFAULT]);
    for (const [name, type] of doc.share) {
      if (!name.startsWith(PREFIX)) continue;
      const id = name.slice(PREFIX.length);
      if (!validID(id) || id === DEFAULT) fail('invalid tab root');
      // A fragment carries only list content, never map entries.
      if (!(type instanceof Y.XmlFragment || type.constructor === Y.AbstractType) || type._map.size !== 0) fail('invalid tab root');
      fragments.add(id);
    }
    return fragments;
  }
  function validateTabs(doc) {
    const fragments = tabRoots(doc);
    const roots = new Map(), place = new Map(), names = new Map();
    for (const [name, target] of [[TABS, place], [NAMES, names]]) {
      const root = doc.share.get(name);
      if (root === undefined) continue;
      if (!(root instanceof Y.Map || root.constructor === Y.AbstractType) || root._start !== null) fail('invalid tab metadata');
      roots.set(root, { name, target });
    }
    let retained = 0;
    const writers = new Set();
    for (const structs of doc.store.clients.values()) for (const item of structs) {
      if (!(item instanceof Y.Item)) continue;
      const root = roots.get(item.parent);
      if (root === undefined) {
        let type = item.parent, depth = 0;
        while (type?._item && depth++ < 64) type = type._item.parent;
        if (roots.has(type)) fail('invalid tab metadata');
        continue;
      }
      if (item.parentSub === null || !validID(item.parentSub)) fail('invalid tab ID');
      if (!writers.has(item.id.client)) { writers.add(item.id.client); retained += WRITER; }
      if (item.content instanceof Y.ContentDeleted) { retained += encoded(root.name, item.parentSub, []); }
      else {
        if (!(item.content instanceof Y.ContentAny) || item.content.arr.length < 1 || (!item.deleted && item.content.arr.length !== 1)) fail('invalid tab metadata');
        for (const value of item.content.arr) {
          if (root.name === TABS ? !validPlace(item.parentSub, value) : !validName(value)) fail('invalid tab metadata');
        }
        retained += encoded(root.name, item.parentSub, item.content.arr);
        if (!item.deleted) root.target.set(item.parentSub, item.content.arr[0]);
      }
      if (retained > LIMITS.retainedBytes) fail('tab history limit');
    }
    if (visible(doc, { place, names, fragments }).size > LIMITS.tabs) fail('tab limit');
    return { place, names, fragments, retained, writers };
  }

  // Shown tabs: `default`, any tab with metadata, and a fragment whose metadata was
  // removed while someone wrote in it (live content is never hidden). A deleted
  // tab leaves an empty root behind (a Yjs root cannot be removed); it is neither
  // shown nor counted.
  function visible(doc, { place, names, fragments }) {
    const live = id => doc.share.get(fragmentName(id))?._length > 0;
    return new Set([...[...fragments].filter(id => id === DEFAULT || place.has(id) || names.has(id) || live(id)), ...place.keys(), ...names.keys()]);
  }

  /**
   * The tab tree as it renders: a deterministic forest every replica agrees on.
   * - A fragment without metadata is "Untitled tab" at the root; metadata
   *   without a fragment is an empty tab.
   * - A missing parent makes the tab a root. Concurrent nesting can form a
   *   cycle; the cycle's smallest ID becomes a root.
   * - Concurrent valid moves can nest deeper than the limit; a tab below the
   *   deepest level is shown under its ancestor at the level above it.
   * Siblings sort by order, then ID; `default` without metadata sorts first.
   */
  function tree(doc) { return treeOf(doc, validateTabs(doc)); }
  function treeOf(doc, state) {
    const ids = visible(doc, state);
    const name = id => state.names.get(id) ?? (id === DEFAULT ? 'Tab 1' : 'Untitled tab');
    const where = id => state.place.get(id) ?? { parent: null, order: id === DEFAULT ? IMPLICIT[DEFAULT] : IMPLICIT.orphan };
    const parent = new Map([...ids].map(id => [id, ids.has(where(id).parent) ? where(id).parent : null]));
    for (const id of [...ids].sort()) {
      const path = []; let cursor = id;
      while (cursor !== null && !path.includes(cursor)) { path.push(cursor); cursor = parent.get(cursor); }
      if (cursor !== null) { const cycle = path.slice(path.indexOf(cursor)); parent.set([...cycle].sort()[0], null); }
    }
    // Depth clamp: depends only on each tab's ancestor chain, so it is the same on every replica.
    const chain = id => { const out = []; for (let c = parent.get(id); c !== null; c = parent.get(c)) out.unshift(c); return out; };
    const clamped = new Map([...ids].map(id => { const up = chain(id); return [id, up.length < LIMITS.depth ? parent.get(id) : up[LIMITS.depth - 2]]; }));
    const nodes = new Map([...ids].map(id => [id, { id, name: name(id), order: where(id).order, children: [] }]));
    const roots = [];
    for (const id of ids) (clamped.get(id) === null ? roots : nodes.get(clamped.get(id)).children).push(nodes.get(id));
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
  // n evenly spaced two-character keys, none ending in '0' (n <= LIMITS.tabs).
  function spread(n) {
    const out = [];
    for (let i = 0; i < n; i++) {
      let index = Math.floor((i + 1) * (62 * 62 - 1) / (n + 1));
      if (index % 62 === 0) index++;
      out.push(DIGITS[Math.floor(index / 62)] + DIGITS[index % 62]);
    }
    return out;
  }

  function flat(forest, parentId = null, depth = 0, out = []) {
    for (const n of forest) { out.push({ id: n.id, parent: parentId, depth, node: n }); flat(n.children, n.id, depth + 1, out); }
    return out;
  }
  // Where a tab goes among `parentId`'s children, before `beforeId` (or last).
  // Equal neighbours (two replicas that each appended) or an exhausted key
  // rebalance the siblings' orders in the same write; returns the writes to make.
  function slot(doc, rows, state, parentId, beforeId, self) {
    const list = rows.filter(n => n.parent === parentId && n.id !== self).map(n => n.id);
    const index = beforeId == null ? list.length : list.indexOf(beforeId);
    if (index < 0) fail('sibling tab required');
    const order = id => state.place.get(id)?.order ?? (id === DEFAULT ? IMPLICIT[DEFAULT] : IMPLICIT.orphan);
    const left = index > 0 ? order(list[index - 1]) : '', right = index < list.length ? order(list[index]) : '';
    if (!right || left < right) { try { return { order: between(left, right), moves: [] }; } catch (error) { if (error.message !== 'tab order exhausted') throw error; } }
    const keys = spread(list.length + 1), moves = [];
    list.slice(0, index).forEach((id, i) => moves.push([id, keys[i]]));
    list.slice(index).forEach((id, i) => moves.push([id, keys[index + 1 + i]]));
    return { order: keys[index], moves };
  }

  // Mutators read the tree once, check every limit first and write in one
  // transaction. The full validation walk runs once per foreign change (the
  // state vector differs from after this replica's last own write); own writes
  // add their counted cost, so typing a name is not O(history) per keystroke.
  const budgets = new WeakMap();
  const vector = doc => { let out = ''; for (const b of Y.encodeStateVector(doc)) out += String.fromCharCode(b); return out; };
  // Mutators stop at three quarters of the history budget: room for replicas that write concurrently.
  const SOFT = Math.floor(LIMITS.retainedBytes * 3 / 4);
  function prepare(doc) {
    const cached = budgets.get(doc);
    let state;
    if (cached && cached.vector === vector(doc)) {
      state = { place: liveOf(doc, TABS), names: liveOf(doc, NAMES), fragments: tabRoots(doc), retained: cached.retained, writers: cached.writers, estimated: true };
    } else state = validateTabs(doc);
    const rows = flat(treeOf(doc, state));
    return { state, rows, find: id => rows.find(n => n.id === id) };
  }
  // writes: [root, key, value]. The same formula as the validator's walk, which may
  // count less (overwritten values of one writer merge into one item); an estimate
  // that reaches the soft cap is replaced by a fresh walk before anything is refused.
  function commit(doc, state, writes, action, origin) {
    const cost = (state.writers.has(doc.clientID) ? 0 : WRITER) + writes.reduce((sum, [root, key, value]) => sum + encoded(root, key, [value]), 0);
    if (state.retained + cost > SOFT && state.estimated) { const fresh = validateTabs(doc); state.retained = fresh.retained; state.writers = fresh.writers; }
    if (state.retained + cost > SOFT) fail('tab history limit');
    doc.transact(action, origin);
    budgets.set(doc, { vector: vector(doc), retained: state.retained + cost, writers: new Set([...state.writers, doc.clientID]) });
  }
  const height = n => 1 + Math.max(0, ...n.children.map(height));
  function blockIds(doc) {
    const ids = new Set();
    const visit = node => {
      if (node instanceof Y.XmlElement && typeof node.getAttribute('id') === 'string') ids.add(node.getAttribute('id'));
      if (node instanceof Y.XmlElement || node instanceof Y.XmlFragment) for (const child of node.toArray()) visit(child);
    };
    // Called by a mutator: typing a loaded root as a fragment emits no update, even if the write is then refused.
    for (const name of [...doc.share.keys()]) if (name === DEFAULT || name.startsWith(PREFIX)) visit(doc.getXmlFragment(name));
    return ids;
  }

  /** Create a tab with one empty paragraph, as the last child of `parent` or before `before`. */
  function createTab(doc, { id, name, parent = null, before = null, blockId }, origin = ORIGIN) {
    const { state, rows, find } = prepare(doc);
    if (!validID(id) || id === DEFAULT || find(id) || doc.share.has(fragmentName(id))) fail('new tab ID required');
    if (!validName(name)) fail('invalid tab name');
    if (rows.length >= LIMITS.create) fail('tab limit');
    if (parent !== null && !find(parent)) fail('parent tab required');
    if (parent !== null && find(parent).depth + 1 >= LIMITS.depth) fail('tab nesting limit');
    // Unique in this replica; uniqueness across concurrent replicas is part of schema 7 admission.
    if (!validID(blockId) || blockIds(doc).has(blockId)) fail('new block ID required');
    const { order, moves } = slot(doc, rows, state, parent, before, id);
    const value = { parent, order };
    commit(doc, state, [[TABS, id, value], [NAMES, id, name], ...moves.map(([sibling, key]) => [TABS, sibling, { parent: state.place.get(sibling)?.parent ?? null, order: key }])], () => {
      const paragraph = new Y.XmlElement('paragraph'); paragraph.setAttribute('id', blockId);
      doc.getXmlFragment(fragmentName(id)).insert(0, [paragraph]);
      for (const [sibling, key] of moves) doc.getMap(TABS).set(sibling, { parent: state.place.get(sibling)?.parent ?? null, order: key });
      doc.getMap(TABS).set(id, value);
      doc.getMap(NAMES).set(id, name);
    }, origin);
    return { name, ...value };
  }

  /** Rename: the name only, so a concurrent move of the same tab survives. */
  function renameTab(doc, id, name, origin = ORIGIN) {
    const { state, find } = prepare(doc);
    if (!find(id)) fail('tab required');
    if (!validName(name)) fail('invalid tab name');
    if (state.names.get(id) === name) return;
    commit(doc, state, [[NAMES, id, name]], () => doc.getMap(NAMES).set(id, name), origin);
  }

  /** Reorder or nest: placement only; the tab's content, name and comments are untouched. */
  function moveTab(doc, id, { parent = null, before = null }, origin = ORIGIN) {
    const { state, rows, find } = prepare(doc);
    const self = find(id);
    if (!self) fail('tab required');
    if (parent !== null) {
      const target = find(parent);
      if (!target || parent === id || flat(self.node.children).some(n => n.id === parent)) fail('a tab cannot move into itself');
      if (target.depth + height(self.node) >= LIMITS.depth) fail('tab nesting limit');
    }
    const { order, moves } = slot(doc, rows, state, parent, before, id);
    const value = { parent, order };
    commit(doc, state, [[TABS, id, value], ...moves.map(([sibling, key]) => [TABS, sibling, { parent: state.place.get(sibling)?.parent ?? null, order: key }])], () => {
      for (const [sibling, key] of moves) doc.getMap(TABS).set(sibling, { parent: state.place.get(sibling)?.parent ?? null, order: key });
      doc.getMap(TABS).set(id, value);
    }, origin);
  }

  /**
   * Delete a tab: its content and metadata in one transaction; its child tabs
   * move up to its parent. The first tab (`default`) cannot be deleted.
   * Restoring is the document's version restore (no separate trash in v1).
   */
  function deleteTab(doc, id, origin = ORIGIN) {
    const { state, rows, find } = prepare(doc);
    if (id === DEFAULT) fail('the first tab cannot be deleted');
    const self = find(id);
    if (!self) fail('tab required');
    // Children by the stored parent (the depth clamp may show a child elsewhere); they move to
    // the parent the tree shows, which already reflects cycle breaking. A child that is that
    // parent or one of its ancestors (a broken cycle) becomes a root instead of its own parent.
    const up = self.parent, above = new Set();
    for (let cursor = up; cursor !== null; cursor = find(cursor).parent) above.add(cursor);
    const writes = [...state.place].filter(([, v]) => v.parent === id)
      .map(([child, v]) => [TABS, child, { parent: above.has(child) ? null : up, order: v.order }]);
    commit(doc, state, writes, () => {
      for (const [, child, value] of writes) doc.getMap(TABS).set(child, value);
      const fragment = doc.getXmlFragment(fragmentName(id));
      if (fragment.length) fragment.delete(0, fragment.length);
      if (state.place.has(id)) doc.getMap(TABS).delete(id);
      if (state.names.has(id)) doc.getMap(NAMES).delete(id);
    }, origin);
  }

  return { TABS, NAMES, DEFAULT, PREFIX, LIMITS, ORIGIN, fragmentName, validateTabs, tree, between, spread, createTab, renameTab, moveTab, deleteTab };
}
