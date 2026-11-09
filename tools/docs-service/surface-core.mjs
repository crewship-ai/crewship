// Docs Canvas surface (R71 rev 2 §0.4): a Page block lives in the `default`
// fragment; the canvas only places it. Two extra Y.Map roots of the same Y.Doc:
//
//   surface:placements  key = top-level Page block ID -> { x, y, w, h, z }
//   surface:items       key = surface item ID         -> note | shape | connector
//
// A placement is keyed by its block ID, so v1 has at most one placement per
// block by construction. Values are plain JSON (ContentAny): moving replaces a
// value, never the block, so block content, IDs and hashes are untouched.
//
// No content schema admits these roots yet. Schema 5 belongs to formatting
// marks; the surface becomes schema 6 after it (see CANVAS_SCHEMA). Until then
// the document validator keeps refusing every surface root.
//
// Shared implementation instantiated with the caller's pinned Yjs, as
// schema-core.mjs is, so browser and service never mix Yjs copies.
export function createSurface({ Y }) {
  const CANVAS_SCHEMA = 6;
  const PLACEMENTS = 'surface:placements';
  const ITEMS = 'surface:items';
  const TEXT = 'surface:text';
  const ROOTS = [PLACEMENTS, ITEMS, TEXT];
  // Block ID grammar, minus names that become prototype keys in Y.Map#toJSON.
  const ID_PATTERN = /^[a-zA-Z0-9_-]{1,80}$/;
  const RESERVED = new Set(['__proto__', 'constructor', 'prototype']);
  const validID = id => typeof id === 'string' && ID_PATTERN.test(id) && !RESERVED.has(id);
  // `retainedBytes` bounds every value ever written to the surface, including
  // deleted ones (gc:false keeps them). It is half of the whole-document binary
  // limit: the Canvas can never take the room the Page needs. Every commit is
  // validated on the merged state, so copies that each stayed inside their own
  // budget are refused together once merged past it (server-side quota).
  const LIMITS = Object.freeze({ entries: 2000, coordinate: 1_000_000, minW: 40, maxW: 4000, minH: 24, maxH: 4000, z: 1_000_000,
    text: 4000, textBytes: 12288, label: 200, valueBytes: 4096, retainedBytes: 512 * 1024 });
  const TONES = Object.freeze(['default', 'yellow', 'green', 'blue', 'red', 'purple']);
  const SHAPES = Object.freeze(['rect', 'ellipse', 'diamond']);
  const ORIGIN = 'surface';

  const fail = message => { throw new Error(message); };
  const object = v => v !== null && typeof v === 'object' && !Array.isArray(v) && Object.getPrototypeOf(v) === Object.prototype;
  // Present keys never hold undefined: JSON and Yjs would drop them differently.
  const exactly = (v, required, optional = []) => object(v) && required.every(k => Object.hasOwn(v, k)) &&
    Object.keys(v).every(k => (required.includes(k) || optional.includes(k)) && v[k] !== undefined);
  const number = n => typeof n === 'number' && Number.isFinite(n) && !Object.is(n, -0);
  const finite = (n, bound) => number(n) && Math.abs(n) <= bound;
  const sized = (n, min, max) => number(n) && n >= min && n <= max;
  const integer = n => Number.isSafeInteger(n) && !Object.is(n, -0) && Math.abs(n) <= LIMITS.z;
  const text = (s, max) => typeof s === 'string' && s.length <= max && !/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]|\p{Cs}/u.test(s);
  // Only called on values whose shape already passed: plain JSON scalars.
  const bytes = v => new TextEncoder().encode(JSON.stringify(v)).length;

  const GEOMETRY = ['x', 'y', 'w', 'h', 'z'];
  function geometry(v) {
    return finite(v.x, LIMITS.coordinate) && finite(v.y, LIMITS.coordinate) && sized(v.w, LIMITS.minW, LIMITS.maxW) &&
      sized(v.h, LIMITS.minH, LIMITS.maxH) && integer(v.z);
  }
  function validPlacement(v) {
    return exactly(v, GEOMETRY) && geometry(v) && bytes(v) <= LIMITS.valueBytes;
  }
  const endpoint = v => exactly(v, ['ref']) && validID(v.ref);
  function validItem(key, v) {
    if (!object(v)) return false;
    let ok = false;
    if (v.kind === 'note') ok = exactly(v, ['kind', ...GEOMETRY, 'tone']) && geometry(v) && TONES.includes(v.tone);
    else if (v.kind === 'shape') ok = exactly(v, ['kind', ...GEOMETRY, 'shape', 'tone']) && geometry(v) && SHAPES.includes(v.shape) && TONES.includes(v.tone);
    // A connector has no geometry of its own: it is drawn between its
    // endpoints' current geometry, so concurrent moves keep it attached.
    else if (v.kind === 'connector') ok = exactly(v, ['kind', 'from', 'to', 'z'], ['label']) && endpoint(v.from) && endpoint(v.to) &&
      v.from.ref !== v.to.ref && v.from.ref !== key && v.to.ref !== key && integer(v.z) && (v.label === undefined || text(v.label, LIMITS.label));
    return ok && bytes(v) <= LIMITS.valueBytes;
  }
  // 4000 UTF-16 units are at most 12 KiB of UTF-8; text values are bounded by both.
  const validText = v => text(v, LIMITS.text) && bytes(v) <= LIMITS.textBytes;
  const validValue = { [PLACEMENTS]: (key, v) => validPlacement(v), [ITEMS]: validItem, [TEXT]: (key, v) => validText(v) };
  const failure = { [PLACEMENTS]: 'invalid placement', [ITEMS]: 'invalid surface item', [TEXT]: 'invalid surface text' };

  // The root a struct belongs to, following nested parents. Surface roots hold
  // only scalar map entries, so anything nested under them is refused.
  function rootOf(type) {
    let depth = 0;
    while (type?._item && depth++ < 64) type = type._item.parent;
    return type;
  }

  /**
   * Validate the surface roots of `doc`, live and retained (gc:false keeps
   * deleted values; they must not become a covert payload). Returns the live
   * values per root. Read-only: decoded roots are inspected, not specialized.
   * Cross-references (a placement whose block is gone, an item ID that a
   * concurrent writer also gave a block) are valid data here; project()
   * reports them instead of failing the whole room.
   */
  function validateSurface(doc) {
    const live = { placements: new Map(), items: new Map(), text: new Map() };
    const target = { [PLACEMENTS]: live.placements, [ITEMS]: live.items, [TEXT]: live.text };
    const roots = new Map();
    for (const name of ROOTS) {
      const root = doc.share.get(name);
      if (root === undefined) continue;
      // Undecided (AbstractType) or Y.Map only; list content must stay empty.
      if (!(root instanceof Y.Map || root.constructor === Y.AbstractType) || root._start !== null) fail('unsupported surface root');
      roots.set(root, name);
    }
    if (roots.size === 0) return Object.defineProperty(live, 'retained', { value: 0 });
    const surfaceRoots = new Set(roots.keys());
    let retained = 0;
    for (const structs of doc.store.clients.values()) for (const item of structs) {
      if (!(item instanceof Y.Item)) continue;
      const name = roots.get(item.parent);
      if (name === undefined) {
        if (surfaceRoots.has(rootOf(item.parent))) fail('unsupported surface content');
        continue;
      }
      if (item.parentSub === null || !validID(item.parentSub)) fail('invalid surface key');
      retained += item.parentSub.length;
      if (item.content instanceof Y.ContentDeleted) { if (retained > LIMITS.retainedBytes) fail('surface history limit'); continue; }
      if (!(item.content instanceof Y.ContentAny) || item.content.arr.length < 1 || (!item.deleted && item.content.arr.length !== 1)) fail('unsupported surface content');
      for (const value of item.content.arr) {
        if (!validValue[name](item.parentSub, value)) fail(failure[name]);
        if ((retained += bytes(value)) > LIMITS.retainedBytes) fail('surface history limit');
      }
      if (!item.deleted) target[name].set(item.parentSub, item.content.arr[0]);
    }
    if (live.placements.size > LIMITS.entries || live.items.size > LIMITS.entries || live.text.size > LIMITS.entries) fail('surface entry limit');
    Object.defineProperty(live, 'retained', { value: retained });
    return live;
  }

  /** Top-level Page block IDs, in Page order. Only these can be placed in v1. */
  function topLevel(doc) {
    return doc.getXmlFragment('default').toArray().flatMap(node => node instanceof Y.XmlElement && typeof node.getAttribute('id') === 'string' ? [node.getAttribute('id')] : []);
  }
  function allBlockIds(doc) {
    const ids = new Set();
    const visit = node => {
      if (node instanceof Y.XmlElement && typeof node.getAttribute('id') === 'string') ids.add(node.getAttribute('id'));
      if (node instanceof Y.XmlElement || node instanceof Y.XmlFragment) for (const child of node.toArray()) visit(child);
    };
    visit(doc.getXmlFragment('default'));
    return ids;
  }

  /**
   * The canvas as it renders. `placements`: placed live top-level blocks.
   * `items`: notes and shapes (with their text) and connectors. Not rendered
   * and reported instead: `orphans` (placements whose block is gone or no
   * longer top-level), `conflicts` (items whose ID a block also has, or that
   * share a key with a placement), `strays` (text with no note or shape)
   * and `detached` (connectors with an endpoint
   * that resolves to nothing rendered). Sorted by z, then ID.
   */
  function project(doc) {
    const live = validateSurface(doc), blocks = allBlockIds(doc), top = new Set(topLevel(doc));
    const order = (a, b) => a.z - b.z || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
    const placements = [], orphans = [], items = [], conflicts = [], strays = [];
    for (const [id, value] of live.placements) (top.has(id) ? placements : orphans).push({ id, ...value });
    for (const [id, value] of live.items) {
      if (blocks.has(id) || live.placements.has(id)) { conflicts.push(id); continue; }
      items.push(value.kind === 'connector' ? { id, ...value } : { id, ...value, text: live.text.get(id) ?? '' });
    }
    // Text whose note or shape is gone (a removal concurrent with an edit) is never shown.
    for (const id of live.text.keys()) { const owner = live.items.get(id); if (!owner || owner.kind === 'connector') strays.push(id); }
    const targets = new Set([...placements.map(p => p.id), ...items.filter(i => i.kind !== 'connector').map(i => i.id)]);
    const detached = items.filter(i => i.kind === 'connector' && (!targets.has(i.from.ref) || !targets.has(i.to.ref))).map(i => i.id).sort();
    return { placements: placements.sort(order), items: items.sort(order), orphans: orphans.map(o => o.id).sort(), conflicts: conflicts.sort(), strays: strays.sort(), detached };
  }

  const clean = (keys, v) => Object.fromEntries(keys.filter(k => v[k] !== undefined).map(k => [k, v[k]]));
  const mapOf = (doc, name) => doc.getMap(name);
  // Mutators never write into an invalid surface and never cross the history
  // cap. The full walk runs once per foreign change (state vector differs);
  // own writes add their cost to the cached total, so a gesture costs O(1)
  // validation work instead of O(history). The cached total is an upper bound.
  const budgets = new WeakMap();
  const vector = doc => encode(Y.encodeStateVector(doc));
  const encode = bytes => { let out = ''; for (const b of bytes) out += String.fromCharCode(b); return out; };
  function checked(doc) {
    const cached = budgets.get(doc), now = vector(doc);
    if (cached && cached.vector === now) return cached.retained;
    const { retained } = validateSurface(doc);
    budgets.set(doc, { vector: now, retained });
    return retained;
  }
  // entries: [key, value] pairs this write sets (deletions add no payload).
  function commit(doc, origin, entries, action) {
    const cost = entries.reduce((sum, [key, value]) => sum + key.length + bytes(value), 0);
    const retained = checked(doc);
    if (retained + cost > LIMITS.retainedBytes) fail('surface history limit');
    doc.transact(action, origin);
    budgets.set(doc, { vector: vector(doc), retained: retained + cost });
  }
  const taken = (doc, id) => allBlockIds(doc).has(id) || mapOf(doc, PLACEMENTS).has(id) || mapOf(doc, ITEMS).has(id);

  /** Place a top-level Page block. A block has at most one placement (v1). */
  function place(doc, id, value, origin = ORIGIN) {
    checked(doc);
    if (!topLevel(doc).includes(id)) fail('top-level Page block required');
    if (mapOf(doc, PLACEMENTS).has(id)) fail('block already placed');
    if (mapOf(doc, ITEMS).has(id)) fail('a surface item uses this ID');
    const next = clean(GEOMETRY, value);
    if (!validPlacement(next)) fail('invalid placement');
    commit(doc, origin, [[id, next]], () => mapOf(doc, PLACEMENTS).set(id, next));
    return next;
  }

  /**
   * Move or resize: geometry only, never the block, the item's text or its
   * kind. A move concurrent with a removal keeps the entry (Yjs map
   * semantics): the removal is undone visibly, nothing is lost.
   */
  function move(doc, id, geometryPatch, origin = ORIGIN) {
    checked(doc);
    const placements = mapOf(doc, PLACEMENTS), items = mapOf(doc, ITEMS);
    const map = placements.has(id) ? placements : items.has(id) ? items : null;
    if (!map) fail('nothing placed with this ID');
    const current = map.get(id);
    if (current.kind === 'connector') fail('a connector follows its endpoints');
    const next = { ...current, ...clean(GEOMETRY, geometryPatch) };
    if (map === placements ? !validPlacement(next) : !validItem(id, next)) fail('invalid geometry');
    commit(doc, origin, [[id, next]], () => map.set(id, next));
    return next;
  }

  /** "Remove from canvas": the placement only; the block stays in the Page. */
  function unplace(doc, id, origin = ORIGIN) {
    checked(doc);
    if (!mapOf(doc, PLACEMENTS).has(id)) fail('block is not placed');
    commit(doc, origin, [], () => mapOf(doc, PLACEMENTS).delete(id));
  }

  /** Add a canvas-only note, shape or connector, with optional text. Never a Page block. */
  function putItem(doc, id, value, origin = ORIGIN) {
    checked(doc);
    if (!validID(id)) fail('invalid surface item ID');
    if (taken(doc, id)) fail('this ID is already used by a block, placement or item');
    const { text: body, ...rest } = object(value) ? value : {};
    if (!validItem(id, rest)) fail('invalid surface item');
    if (body !== undefined && (rest.kind === 'connector' || !validText(body))) fail('invalid surface text');
    const copy = structuredClone(rest);
    // A stray text left under this ID by a concurrent edit never carries over.
    commit(doc, origin, body ? [[id, copy], [id, body]] : [[id, copy]], () => {
      mapOf(doc, ITEMS).set(id, copy);
      if (body) mapOf(doc, TEXT).set(id, body); else if (mapOf(doc, TEXT).has(id)) mapOf(doc, TEXT).delete(id);
    });
    return copy;
  }

  /** Replace a note's or shape's text; geometry is a separate value and stays. */
  function setText(doc, id, body, origin = ORIGIN) {
    checked(doc);
    const item = mapOf(doc, ITEMS).get(id);
    if (!item || item.kind === 'connector') fail('note or shape required');
    if (!validText(body)) fail('invalid surface text');
    commit(doc, origin, body ? [[id, body]] : [], () => { if (body) mapOf(doc, TEXT).set(id, body); else if (mapOf(doc, TEXT).has(id)) mapOf(doc, TEXT).delete(id); });
  }

  function removeItem(doc, id, origin = ORIGIN) {
    checked(doc);
    if (!mapOf(doc, ITEMS).has(id)) fail('no surface item with this ID');
    commit(doc, origin, [], () => { mapOf(doc, ITEMS).delete(id); if (mapOf(doc, TEXT).has(id)) mapOf(doc, TEXT).delete(id); });
  }

  /**
   * What deleting a block affects, for the confirmation the UI must show
   * before deleteBlock (§0.4/2): whether it is placed and which connectors
   * end on it. Comment threads are counted by the caller from the comment
   * store; they become detached and keep their quote.
   */
  function blockImpact(doc, id) {
    if (!allBlockIds(doc).has(id)) fail('missing block');
    const connectors = [...mapOf(doc, ITEMS)].filter(([, v]) => v.kind === 'connector' && (v.from.ref === id || v.to.ref === id)).map(([key]) => key).sort();
    return { page: true, placed: mapOf(doc, PLACEMENTS).has(id), connectors };
  }

  /** Delete a top-level block from the Page and its placement in one transaction. */
  function deleteBlock(doc, id, origin = ORIGIN) {
    checked(doc);
    const fragment = doc.getXmlFragment('default');
    const position = fragment.toArray().findIndex(node => node instanceof Y.XmlElement && node.getAttribute('id') === id);
    if (position < 0) fail('top-level Page block required');
    if (fragment.length === 1) fail('a document keeps at least one block');
    commit(doc, origin, [], () => {
      fragment.delete(position, 1);
      if (mapOf(doc, PLACEMENTS).has(id)) mapOf(doc, PLACEMENTS).delete(id);
    });
  }

  return { CANVAS_SCHEMA, PLACEMENTS, ITEMS, TEXT, ROOTS, LIMITS, TONES, SHAPES, ORIGIN, validID, validPlacement, validItem, validateSurface, topLevel, project, place, move, unplace, putItem, setText, removeItem, blockImpact, deleteBlock };
}
