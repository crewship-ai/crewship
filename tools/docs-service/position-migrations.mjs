// The only extra CRDT root admitted by Docs is y-tiptap's position history for
// our `default` fragment. Numeric identities are metadata, never authorization.
// Keep these bounds over retained Items too: deleting a map entry does not erase
// its payload from a gc:false snapshot. Existing document byte limits also apply.
export function validatePositionMigrations(doc, Y) {
  const name = '__tiptap_position_migrations:root:default';
  for (const key of doc.share.keys()) if (key !== 'default' && key !== name) throw new Error('unsupported Yjs root');
  if (!doc.share.has(name)) return null;
  const fail = () => { throw new Error('unsupported position migration metadata'); };
  const limit = () => { throw new Error('position migration metadata limit'); };
  let root;
  try { root = doc.getMap(name); } catch { fail(); }
  // A decoded root has not necessarily been specialized yet. Its list payload
  // must still be empty: only map entries belong to the migration contract.
  if (!(root instanceof Y.Map) || root._start !== null) fail();
  const object = (v, required, optional = []) => v !== null && typeof v === 'object' && !Array.isArray(v) &&
    required.every(k => Object.hasOwn(v, k)) && Object.keys(v).every(k => required.includes(k) || optional.includes(k));
  const uint = n => Number.isSafeInteger(n) && n >= 0;
  const client = n => uint(n) && n <= 0xffffffff;
  const identity = v => object(v, ['client', 'clock']) && client(v.client) && uint(v.clock);
  const boundary = v => v === undefined || v === 'start' || v === 'end';
  const documentRoot = doc.getXmlFragment('default');
  const states = Y.decodeStateVector(Y.encodeStateVector(doc));
  // Walk stored intervals, not individual characters. GC intervals retain
  // identity ranges without payload and remain legitimate historical sources.
  const retained = (id, length = 1, typeOnly = false) => {
    const structs = doc.store.clients.get(id.client);
    if (!structs) fail();
    let lo = 0, hi = structs.length;
    while (lo < hi) { const mid = (lo + hi) >>> 1; if (structs[mid].id.clock + structs[mid].length <= id.clock) lo = mid + 1; else hi = mid; }
    let cursor = id.clock;
    while (cursor < id.clock + length) {
      const item = structs[lo++];
      if (!item || item.id.clock > cursor) fail();
      if (!(item instanceof Y.GC)) {
        if (!(item instanceof Y.Item)) fail();
        let parent = item.parent, depth = 0;
        while (parent?._item && depth++ < 40) parent = parent._item.parent;
        if (parent !== documentRoot) fail();
        const content = item.content;
        if (typeOnly && !(content instanceof Y.ContentDeleted) &&
          (!(content instanceof Y.ContentType) || item.id.clock !== id.clock)) fail();
        if (!(content instanceof Y.ContentString || content instanceof Y.ContentDeleted ||
          (content instanceof Y.ContentType && (content.type instanceof Y.XmlText || content.type instanceof Y.XmlElement)))) fail();
      }
      cursor = Math.min(id.clock + length, item.id.clock + item.length);
    }
  };
  let records = 0, transfers = 0, bytes = 0, span = 0;
  const record = (key, v) => {
    if (++records > 256) limit();
    if (!object(v, ['version', 'client', 'clock', 'seen', 'transfers']) || v.version !== 1 ||
      !client(v.client) || !uint(v.clock) || key !== `${v.client}:${v.clock}` || !Array.isArray(v.seen) || !Array.isArray(v.transfers)) fail();
    if (v.seen.length > 128 || (transfers += v.transfers.length) > 4096) limit();
    const seen = new Set();
    for (const pair of v.seen) {
      if (!Array.isArray(pair) || pair.length !== 2 || !client(pair[0]) || !uint(pair[1]) || seen.has(pair[0])) fail();
      if (pair[1] > (states.get(pair[0]) ?? 0)) fail();
      seen.add(pair[0]);
    }
    for (const transfer of v.transfers) {
      if (!object(transfer, ['source', 'target'])) fail();
      const { source, target } = transfer;
      if (!object(source, ['client', 'clock', 'length'], ['boundary']) || !client(source.client) || !uint(source.clock) ||
        !Number.isSafeInteger(source.length) || source.length < 1 || source.length > 500000 ||
        !Number.isSafeInteger(source.clock + source.length) || !boundary(source.boundary)) fail();
      if ((span += source.length) > 500000) limit();
      retained(source, source.length);
      if (target === null) continue;
      if (!object(target, ['client', 'clock', 'type'], ['tname', 'assoc', 'boundary']) || !client(target.client) || !uint(target.clock) ||
        !Number.isSafeInteger(target.clock + source.length) || !(target.type === null || identity(target.type)) ||
        !(target.tname === undefined || target.tname === 'default') ||
        !(target.assoc === undefined || [-1, 0, 1].includes(target.assoc)) || !boundary(target.boundary)) fail();
      retained(target, source.length);
      if (target.type !== null) retained(target.type, 1, true);
    }
    bytes += new TextEncoder().encode(JSON.stringify({ [key]: v })).length;
    if (bytes > 64 * 1024) limit();
  };
  for (const structs of doc.store.clients.values()) for (const item of structs) {
    if (!(item instanceof Y.Item) || item.parent !== root) continue;
    if (item.parentSub === null || !/^(0|[1-9]\d{0,9}):(0|[1-9]\d{0,15})$/.test(item.parentSub)) fail();
    if (item.content instanceof Y.ContentDeleted) continue;
    if (!(item.content instanceof Y.ContentAny) || item.content.arr.length < 1 || (!item.deleted && item.content.arr.length !== 1)) fail();
    for (const value of item.content.arr) record(item.parentSub, value);
  }
  return root;
}
