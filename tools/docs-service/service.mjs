import http from 'node:http';
import { creationDocument } from './create-document.mjs';
import { readFile, chmod, lstat, stat, unlink } from 'node:fs/promises';
import net from 'node:net';
import { createHash, randomUUID, timingSafeEqual } from 'node:crypto';
import { basename, dirname, isAbsolute, normalize } from 'node:path';
import { pathToFileURL } from 'node:url';
import * as Y from 'yjs';
import { VERSION, RICH_VERSION, FORMATTED_VERSION, LATEST_VERSION, create, load, snapshot, outline, validate, patch, hash, decode, anchor, resolve, markdown, validUserId } from './schema.mjs';
import { readServiceSecret } from './secret.mjs';

export const PROTOCOL = 3;
const fault = (status, message,code) => Object.assign(new Error(message), { status,...(code?{code}:{}) });
const id = value => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,128}$/.test(value);
// Offsets are ProseMirror/JavaScript UTF-16 positions. Do not admit a range
// which splits a surrogate pair, even when its submitted quote matches it.
const textBoundary = (text, index) => index === 0 || index === text.length ||
  !(text.charCodeAt(index - 1) >= 0xd800 && text.charCodeAt(index - 1) <= 0xdbff && text.charCodeAt(index) >= 0xdc00 && text.charCodeAt(index) <= 0xdfff);
const contentSchema = version => version === 1 ? VERSION : version;
const PROPOSAL_VERSION = FORMATTED_VERSION;
const supportedSchema = version => [1, VERSION, RICH_VERSION, FORMATTED_VERSION, LATEST_VERSION].includes(version);
// Schemas 3, 4 and 5 are bound to their original Page and carry rich semantic proofs.
const richSchema = version => version === RICH_VERSION || version === FORMATTED_VERSION || version === LATEST_VERSION;
async function verifyTargets(doc, targets, version = VERSION) {
  if (!Array.isArray(targets) || targets.length < 1 || targets.length > 16) throw fault(400, 'one to sixteen selected text targets required');
  const blocks = new Map((await outline(doc,version)).map(block => [block.id, block]));
  const seen = new Set(), verified = []; let quoted = 0;
  for (const target of targets) {
    if (!target || typeof target !== 'object' || Array.isArray(target) || Object.keys(target).some(key => !['id','hash','from','to','quote'].includes(key)) ||
      !id(target.id) || seen.has(target.id) || typeof target.hash !== 'string' || !/^[a-f0-9]{64}$/.test(target.hash) || typeof target.quote !== 'string') throw fault(400, 'invalid selected text target');
    seen.add(target.id);
    const block = blocks.get(target.id);
    if (!block) throw fault(409, 'selected block changed');
    if (!['paragraph','heading'].includes(block.type)) throw fault(400, 'select text inside a paragraph or heading');
    if (!Number.isSafeInteger(target.from) || !Number.isSafeInteger(target.to) || target.from < 0 || target.to <= target.from || target.to > block.text.length ||
      !textBoundary(block.text, target.from) || !textBoundary(block.text, target.to)) throw fault(400, 'invalid selected text range');
    quoted += target.quote.length;
    if (quoted > 4096) throw fault(400, 'selected text exceeds request limit');
    if (target.hash !== block.hash || target.quote !== block.text.slice(target.from, target.to)) throw fault(409, 'selected text changed');
    const ancestors = []; let parent = block.parent;
    while (parent) { ancestors.push(parent); parent = blocks.get(parent)?.parent; }
    verified.push({ id: block.id, hash: block.hash, from: target.from, to: target.to, quote: block.text.slice(target.from, target.to), text: block.text, ancestors, ...(richSchema(version) ? {schema:version} : {}) });
  }
  return verified;
}
// Startup custody of one socket is serialized by a Linux abstract-namespace
// listener named from the socket's parent directory identity (device and
// inode, so path aliases share it) and basename. The kernel releases it when
// its process exits, including SIGKILL, and an engine holds it while it lives.
async function holdStartupLock(socket) {
  if (process.platform !== 'linux') return undefined;
  const parent = await stat(dirname(socket), { bigint: true });
  const lock = net.createServer();
  const name = '\0crewship-docs-' + createHash('sha256').update(`${parent.dev}:${parent.ino}:${basename(socket)}`).digest('hex').slice(0, 40);
  await new Promise((resolve, reject) => {
    lock.once('error', error => reject(error.code === 'EADDRINUSE' ? new Error('service socket already exists') : error));
    lock.listen(name, resolve);
  });
  lock.unref();
  return lock;
}
// A killed engine leaves its socket file behind; its supervised replacement
// must still start at the configured path. Only while holding the startup lock,
// and only a socket with no listener, is removed. A live (or paused) engine
// accepts the probe and keeps custody; any other file, or any other probe
// error, is refused unchanged. Without the lock nothing is removed.
async function reclaimCrashedSocket(socket, locked) {
  let found;
  try { found = await lstat(socket); } catch (error) { if (error.code === 'ENOENT') return; throw error; }
  if (!locked || !found.isSocket()) throw new Error('service socket already exists');
  const abandoned = await new Promise(resolve => {
    const probe = net.connect(socket);
    probe.once('connect', () => { probe.destroy(); resolve(false); });
    probe.once('error', error => resolve(error.code === 'ECONNREFUSED'));
  });
  const current = abandoned && await lstat(socket).catch(() => undefined);
  if (!current || current.ino !== found.ino || current.dev !== found.dev) throw new Error('service socket already exists');
  await unlink(socket);
}
async function body(req) {
  let size = 0; const chunks = [];
  for await (const chunk of req) { size += chunk.length; if (size > 2 * 1024 * 1024) throw fault(413, 'request limit'); chunks.push(chunk); }
  try { return JSON.parse(Buffer.concat(chunks).toString()); } catch { throw fault(400, 'invalid JSON'); }
}
export async function startService({ socket, authoritySocket, tokenFile, idleMs = 30000, timeoutMs = 10000, drainTimeoutMs = 30000 }) {
  if (!socket || !authoritySocket || !tokenFile || socket === authoritySocket || tokenFile === socket || tokenFile === authoritySocket) throw new Error('distinct service and authority Unix sockets and token file required');
  for (const path of [socket, authoritySocket, tokenFile]) if (!isAbsolute(path) || normalize(path) !== path || Buffer.byteLength(path) > 100) throw new Error('clean absolute Unix socket path required (maximum 100 bytes)');
  const secret = await readServiceSecret(tokenFile);
  const build = createHash('sha256');
  for (const file of ['pnpm-lock.yaml', 'schema.mjs', 'schema-core.mjs', 'position-migrations.mjs', 'service.mjs', 'create-document.mjs', 'secret.mjs']) build.update(await readFile(new URL(file, import.meta.url)));
  const identity = { protocol: PROTOCOL, schema: LATEST_VERSION, build: build.digest('hex') };
  const leases = new Map();
  const owner = randomUUID();const syncEpoch=state=>`${state.incarnation??"legacy"}:${owner}:${state.generation??state.epoch}`; const documents = new Map(); let queued = 0; let draining = false;let ready=false;let admitted=0;const decoding=new Set();
  async function go(page, operation, context, data = {}) {
    let deadline;
    return new Promise((resolve, reject) => {
      const bytes = JSON.stringify(data);
      const req = http.request({ socketPath: authoritySocket, path: operation === 'health' ? '/health/ready' : `/internal/docs/v1/${encodeURIComponent(page)}/${operation}`, method: operation === 'health' ? 'GET' : 'POST', headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(bytes), 'X-Crewship-Docs-Service': secret, 'X-Workspace-ID': context.workspace, ...(context.authorization ? { Authorization: context.authorization } : {}) } }, res => {
        const chunks = []; let size = 0;
        res.on('data', chunk => { size += chunk.length; if (size > 4 * 1024 * 1024) { res.destroy(); reject(fault(502, 'authority response limit')); } else chunks.push(chunk); });
        res.on('end', () => { try { const result = JSON.parse(Buffer.concat(chunks).toString()); if (res.statusCode >= 400) reject(fault(res.statusCode, result.error || 'authority rejected request',result.code)); else resolve(result); } catch { reject(fault(502, 'invalid authority response')); } });
        res.on('error', () => reject(fault(502, 'authority unavailable')));
      });
      // Bound the whole authority exchange, including response bytes. Socket
      // inactivity timeouts alone can be extended forever by a slow response.
      deadline = setTimeout(() => { reject(fault(503, 'authority unavailable')); req.destroy(); }, timeoutMs);
      req.on('error', () => reject(fault(503, 'authority unavailable'))); req.end(bytes);
    }).finally(() => clearTimeout(deadline));
  }
  function enqueue(key, fn) {
    let entry = documents.get(key);
    if (draining) throw fault(503, 'service draining');
    if (!entry) {
      if (documents.size >= 32) {
        // These are scheduler tails, not cached document contents. Never remove
        // a busy tail: doing so would allow concurrent writes for the same key.
        let oldest;
        for (const [candidate, value] of documents) if (value.queued === 0 && (!oldest || value.touched < oldest[1].touched)) oldest = [candidate, value];
        if (!oldest) throw fault(429, 'open document limit');
        documents.delete(oldest[0]);
      }
      entry = { tail: Promise.resolve(), queued: 0, touched: Date.now() }; documents.set(key, entry);
    }
    if (queued >= 128 || entry.queued >= 32) throw fault(429, 'document queue full');
    queued++; entry.queued++; entry.touched = Date.now();
    const result = entry.tail.then(fn);
    entry.tail = result.catch(() => {}).finally(() => { queued--; entry.queued--; entry.touched = Date.now(); });
    return result;
  }
  // Dispatch uses only the closed URL operation, never fields in the body.
  // Reads have no path into fingerprint/receipt, writer lease or commit logic.
  async function execute(page, operation, context, request) {
    switch (operation) {
      case 'prepare-create': {
        if (!request || typeof request !== 'object' || Object.keys(request).length !== 1 || !Object.hasOwn(request,'blocks')) throw fault(400,'closed authoring blocks required');
        const doc=create(creationDocument(request.blocks),LATEST_VERSION);
        try { return {schema:LATEST_VERSION,snapshot:snapshot(doc),blocks:(await outline(doc,LATEST_VERSION)).map(({id,hash})=>({id,hash}))}; }
        finally { doc.destroy(); }
      }
      case 'read': return executeRead(page, 'read', context, request);
      case 'verify-targets': return executeRead(page, 'verify-targets', context, request);
      case 'comments': return executeRead(page, 'comments', context, request);
      case 'checkpoints': return executeRead(page, 'checkpoints', context, request);
      case 'checkpoint-read': return executeRead(page, 'checkpoint-read', context, request);
      case 'export': return executeRead(page, 'export', context, request);
      case 'proposals': return executeRead(page, 'proposals', context, request);
      case 'proposal': return executeRead(page, 'proposal', context, request);
      case 'initialize': case 'upgrade': case 'patch': case 'checkpoint': case 'restore':
      case 'comment': case 'resolve': case 'propose':
        return executeMutation(page, operation, context, request);
      case 'proposal-accept': case 'proposal-reject':
        return executeProposalDecision(page, operation, context, request);
      default: throw fault(404, 'unknown operation');
    }
  }
  // Names the caller resolved for this reader (Go decides who may be named).
  // The engine never looks names up and refuses anything but a bounded map.
  function exportPeople(people) {
    if (people === undefined || people === null) return undefined;
    if (typeof people !== 'object' || Array.isArray(people) || Object.keys(people).length > 1000) throw fault(400, 'invalid export people');
    for (const [id, name] of Object.entries(people)) if (!validUserId(id) || typeof name !== 'string' || !name || [...name].length > 200 || /[\u0000-\u001f\u007f]/.test(name)) throw fault(400, 'invalid export people');
    return people;
  }
  async function executeRead(page, operation, context, request) {
    if (operation === 'verify-targets' && context.authorization.startsWith('Bearer agtv2.')) throw fault(403, 'human request admission required');
    if ((operation === 'proposals' || operation === 'proposal') && context.authorization.startsWith('Bearer agtv2.')) throw fault(403, 'human proposal review required');
    if (operation === 'proposal' && (!request || typeof request !== 'object' || Object.keys(request).length !== 1 || !id(request.proposal))) throw fault(400, 'one proposal ID required');
    const state = await go(page, 'state', context);
    if (!supportedSchema(state.schema)) throw fault(409, 'unsupported schema; content preserved');
    const version = contentSchema(state.schema);
    const doc = load(state.snapshot);
    try {
      for (const update of state.updates || []) Y.applyUpdate(doc, decode(typeof update === 'string' ? update : update.update));
      if (operation === 'read') {
        const { rev, generation, incarnation, snapshot, snapshotRev, updates, blocks } = state;
        return { schema: state.schema, rev, generation, incarnation, snapshot, snapshotRev, updates, blocks, epoch: syncEpoch(state), content: validate(doc,version), outline: await outline(doc,version) };
      }
      if (operation === 'verify-targets') {
        if (!Number.isSafeInteger(request.base_rev) || request.base_rev !== state.rev) throw fault(409, 'document request revision changed');
        return { page_id: page, rev: state.rev, ...(richSchema(version) ? {schema:version} : {}), targets: await verifyTargets(doc, request.targets,version) };
      }
      if(operation==='export') { const people = exportPeople(request.people); try { return {markdown:markdown(validate(doc,version),version,{people})}; } catch (error) { if (error.code === 'unsupported_markdown') throw fault(422,error.message,error.code); throw error; } }
      if(operation==='checkpoints') return {checkpoints:await go(page,'checkpoints',context)};
      if(operation==='checkpoint-read') { const c=await go(page,'checkpoint-read',context,{id:request.checkpoint});const pinned=load(c.snapshot);try{return {...c,content:validate(pinned,contentSchema(c.schema??VERSION)),outline:await outline(pinned,contentSchema(c.schema??VERSION))}}finally{pinned.destroy()} };
      if (operation === 'proposals' || operation === 'proposal') {
        // An open proposal whose target changed since it was made is stale: it
        // can no longer be accepted as it was reviewed. Go keeps the stored status.
        const blocks = new Map((await outline(doc,version)).map(block => [block.id, block]));
        const live = p => ({ ...p, status: p.status === 'open' && (state.generation ?? 0) === p.generation && blocks.get(p.target)?.hash === p.base_hash ? 'open' : p.status === 'open' ? 'stale' : p.status });
        if (operation === 'proposal') return live(await go(page, 'proposal', context, { id: request.proposal }));
        return { proposals: (await go(page, 'proposals', context)).map(live) };
      }
      if (operation === 'comments') {
        const comments = await go(page, 'comments', context);
        return { comments: comments.map(comment => {
          // A reply carries no anchor of its own; it follows its root thread.
          if (comment.parent) { const { anchor: _none, ...reply } = comment; return reply; }
          const { id, block, quote, checkpoint, start, end, status, from, to } = resolve(doc, comment.anchor,version);
          const publicAnchor = { id, block, quote, checkpoint, start, end };
          return { ...comment, anchor: publicAnchor, ...publicAnchor, status: comment.resolved ? 'resolved' : status, from, to };
        }) };
      }
      throw fault(404, 'unknown read operation');
    } finally { doc.destroy(); }
  }
  async function executeMutation(page, operation, context, request) {
    if ((richSchema(request.schema) || request.page_id !== undefined) && request.page_id !== page) throw fault(409,'original Page identity changed');
    if (operation === 'initialize' && richSchema(request.schema) && context.authorization.startsWith('Bearer agtv2.')) throw fault(403,'human rich document initialization required');
    if (operation === 'initialize') {
      if (![VERSION,RICH_VERSION,FORMATTED_VERSION,LATEST_VERSION].includes(request.schema) || !id(request.id) || !request.body) throw fault(400, 'schema and operation ID required');
      const fingerprint = await hash({ operation, request });
      try { const receipt = await go(page, 'receipt', context, { id: request.id, fingerprint }); if (receipt.found) return { ...receipt, duplicate: true }; } catch (error) { if (error.status !== 404) throw error; }
      const doc = create(request.body,request.schema);
      try { return await go(page, 'initialize', context, { id: request.id, fingerprint, schema: request.schema, snapshot: snapshot(doc), blocks: (await outline(doc,request.schema)).map(({ id, hash }) => ({ id, hash })) }); }
      finally { doc.destroy(); }
    }
    const state = await go(page, 'state', context);
    if (!supportedSchema(state.schema)) throw fault(409, 'unsupported schema; content preserved');
    const version = contentSchema(state.schema);
    if (!supportedSchema(request.schema)) throw fault(409,'unsupported client schema');
    if (operation === 'upgrade' && context.authorization.startsWith('Bearer agtv2.')) throw fault(403,'human schema upgrade required');
    if (!id(request.id)) throw fault(400, 'operation ID required');
    const fingerprint = await hash({ operation, request });
    const receipt = await go(page, 'receipt', context, { id: request.id, fingerprint });
    if (receipt.found) {
      let schema = operation === 'upgrade' ? request.target_schema : request.schema;
      if (operation === 'restore') { const pinned = await go(page,'checkpoint-read',context,{id:request.checkpoint}); schema = pinned.schema ?? VERSION; }
      return { ...receipt, schema, epoch:syncEpoch(state), duplicate: true };
    }
    if (request.schema !== state.schema && !(state.schema === 1 && request.schema === VERSION)) throw fault(409,'unsupported client schema; reopen the confirmed document');
    if (operation === 'upgrade') {
      if (context.authorization.startsWith('Bearer agtv2.')) throw fault(403,'human schema upgrade required');
      if (request.page_id !== page || request.target_schema !== LATEST_VERSION || ![1,VERSION,RICH_VERSION,FORMATTED_VERSION].includes(state.schema) || request.ifRev !== state.rev || Object.keys(request).some(key=>!['id','schema','target_schema','page_id','epoch','ifRev'].includes(key))) throw fault(409,'original Page identity, revision and supported upgrade required');
    }
    let doc = load(state.snapshot); let lease;
    try {
      for (const update of state.updates || []) Y.applyUpdate(doc, decode(typeof update === 'string' ? update : update.update));
      validate(doc,version);
      const baseVector = Y.encodeStateVector(doc);
      if (request.epoch !== syncEpoch(state)) throw fault(409, 'old epoch; reload document');
      if(operation==='comment'||operation==='resolve') {
        if (context.authorization.startsWith('Bearer agtv2.')) throw fault(403,'human comment required');
        const reply = operation === 'comment' && request.parent !== undefined;
        const fields = ['id','schema','page_id','epoch','ifRev', ...(operation === 'resolve' ? ['comment','version','resolved'] : reply ? ['parent','text'] : ['block','from','to','text'])];
        if (Object.keys(request).some(key => !fields.includes(key)) || fields.some(key => key !== 'page_id' && request[key] === undefined)) throw fault(400,'closed comment request required');
        if (operation === 'comment' && (typeof request.text !== 'string' || !request.text.trim() || request.text.length > 10000)) throw fault(400,'comment text of 1 to 10000 characters required');
        if (reply && !id(request.parent)) throw fault(400,'reply parent required');
        if (operation === 'resolve' && (!id(request.comment) || !Number.isSafeInteger(request.version) || request.version < 1 || typeof request.resolved !== 'boolean')) throw fault(400,'comment version and resolved state required');
        if(request.ifRev!==state.rev)throw fault(409,'comment revision changed');
        if(operation==='resolve') return {...await go(page,'resolve',context,{id:request.id,fingerprint,comment:request.comment,version:request.version,resolved:request.resolved,ifRev:state.rev,generation:state.generation??0}),epoch:syncEpoch(state)};
        let selected;
        if (!reply) { try { selected = anchor(doc,request.block,request.from,request.to,request.id,state.rev,version); } catch (error) { if (error.message === 'comment anchor limit') throw fault(413,error.message); throw fault(400,'select text inside a paragraph, heading or code block'); } }
        const result=await go(page,'comment',context,{id:request.id,fingerprint,body:request.text,...(reply ? {parent:request.parent} : {anchor:selected}),ifRev:state.rev,generation:state.generation??0});return {...result,epoch:syncEpoch(state)};
      }
      if (operation === 'propose') {
        // A proposal is stored beside the document and never changes content,
        // revision, epoch or generation. Only a live agent run proposes, only
        // on schema 4, and only as a closed semantic replacement Go re-proves.
        if (!context.authorization.startsWith('Bearer agtv2.')) throw fault(403, 'agent run proposal required');
        const fields = ['id','schema','page_id','epoch','ifRev','target','ifHash','text'];
        if (Object.keys(request).some(key => !fields.includes(key)) || fields.some(key => request[key] === undefined)) throw fault(400, 'closed proposal request required');
        // Proposals stay on schema 4 (FORMATTED_VERSION, Go's documents.ProposalSchema); schema 5 is refused cleanly.
        if (version !== PROPOSAL_VERSION || request.schema !== PROPOSAL_VERSION) throw fault(409, 'proposals require a schema 4 document');
        if (typeof request.text !== 'string' || request.text.length > 100000) throw fault(400, 'bounded proposal text required');
        const target = (await outline(doc,version)).find(block => block.id === request.target);
        if (request.ifRev !== state.rev || !target || target.hash !== request.ifHash) throw fault(409, 'semantic precondition conflict');
        if (!['paragraph','heading'].includes(target.type)) throw fault(400, 'propose text inside a paragraph or heading');
        const richProof = () => { const value=validate(doc,version); if(Buffer.byteLength(JSON.stringify(value),'utf8')>500000) throw fault(413,'rich semantic proof exceeds 500000 bytes; content unchanged'); return value; };
        const before = richProof();
        const edit = patch(doc, { op: 'replaceText', id: request.target, text: request.text }, version);
        if (doc.store.pendingStructs || doc.store.pendingDs) throw fault(400, 'unresolved Yjs dependencies');
        const after = richProof();
        const blocks = (await outline(doc,version)).map(({ id, hash }) => ({ id, hash }));
        return {...await go(page, 'propose', context, { id: request.id, fingerprint, target: request.target, ifHashes: { [request.target]: request.ifHash }, semantic: { target: request.target, text: request.text, edit, rich: { before, after } }, blocks, ifRev: state.rev, generation: state.generation ?? 0 }), epoch: syncEpoch(state)};
      }
      const key=`${context.workspace}/${page}`;
      lease=await writerLease(page,context,state);
      if (operation === 'upgrade') {
        validate(doc,LATEST_VERSION);
        const result = await go(page,'upgrade',context,{id:request.id,fingerprint,schema:state.schema,targetSchema:LATEST_VERSION,ifRev:state.rev,generation:state.generation??0,epoch:lease.epoch,leaseToken:lease.token,leaseOwner:owner});
        leases.delete(key);
        return {...result,schema:LATEST_VERSION,epoch:syncEpoch({...state,generation:(state.generation??0)+1})};
      }
      const hashes = {}; let semantic; let resultVersion = version; let replaced;
      switch (operation) {
        case 'patch':
          if (request.kind === 'human') {
            if (context.authorization.startsWith('Bearer agtv2.')) throw fault(403, 'agent raw update forbidden');
            if (typeof request.update !== 'string' || request.update.length > 1400000) throw fault(400, 'bounded update required');
            Y.applyUpdate(doc, decode(request.update));
          } else {
            const target = (await outline(doc,version)).find(block => block.id === request.target);
            if (request.ifRev !== state.rev || !target || target.hash !== request.ifHash) throw fault(409, 'semantic precondition conflict');
            hashes[request.target] = request.ifHash;
            const scope = state.requestScope;
            if (scope && (scope.baseRev !== state.rev || request.ifRev !== scope.baseRev || request.target !== scope.target.id || request.ifHash !== scope.target.hash || target.text !== scope.target.text)) throw fault(409, 'original request scope changed');
            if (scope && typeof request.text === 'string') {
              const prefix = scope.target.text.slice(0, scope.target.from), suffix = scope.target.text.slice(scope.target.to);
              if (request.text.length < prefix.length + suffix.length || !request.text.startsWith(prefix) || !request.text.endsWith(suffix)) throw fault(409, 'selected text range conflict');
            }
            const richProof = () => { const value=validate(doc,version); if(Buffer.byteLength(JSON.stringify(value),'utf8')>500000) throw fault(413,'rich semantic proof exceeds 500000 bytes; content unchanged'); return value; };
            const before = richSchema(version) ? richProof() : undefined;
            const edit = patch(doc, { op: 'replaceText', id: request.target, text: request.text, ...(scope ? { range: { from: scope.target.from, to: scope.target.to } } : {}) },version);
            semantic = { target: request.target, text: request.text, edit, ...(before ? {rich:{before,after:richProof()}} : {}) };
          }
          break;
        case 'restore': {
          if (request.ifRev !== state.rev) throw fault(409, 'restore precondition conflict');
          const checkpoint = await go(page, 'checkpoint-read', context, { id: request.checkpoint });
          if (checkpoint.schema !== undefined && !supportedSchema(checkpoint.schema)) throw fault(409, 'unsupported checkpoint schema');
          resultVersion = contentSchema(checkpoint.schema??VERSION); replaced = snapshot(doc); doc.destroy(); doc = load(checkpoint.snapshot); break;
        }
        case 'checkpoint':
          if (request.ifRev !== state.rev) throw fault(409, 'checkpoint precondition conflict');
          break;
        default: throw fault(404, 'unknown operation');
      }
      if (doc.store.pendingStructs || doc.store.pendingDs) throw fault(400, 'unresolved Yjs dependencies');
      validate(doc,resultVersion);
      const blocks = (await outline(doc,resultVersion)).map(({ id, hash }) => ({ id, hash }));
      const renewal=await go(page, 'renew', context, { owner, epoch: lease.epoch, token: lease.token });
      if(renewal.expiresAt)lease.expiresAt=renewal.expiresAt;
      if (operation === 'restore') return {...await go(page, 'restore', context, { id: request.id, fingerprint, checkpoint: request.checkpoint, snapshot: replaced, ifRev: state.rev,generation:state.generation??0, epoch: lease.epoch, leaseToken: lease.token, leaseOwner: owner }),epoch:syncEpoch({...state,generation:(state.generation??0)+1})};
      if (operation === 'checkpoint') return {...await go(page, 'checkpoint', context, { id: request.id, fingerprint, epoch: lease.epoch, leaseToken: lease.token, leaseOwner: owner, ifRev: state.rev,generation:state.generation??0, snapshot: snapshot(doc) }),epoch:syncEpoch(state)};
      return {...await go(page, 'commit', context, { id: request.id, fingerprint, kind: operation === 'patch' ? (context.authorization.startsWith('Bearer agtv2.') ? 'agent' : 'human') : operation, ...(operation === 'patch' && context.authorization.startsWith('Bearer agtv2.') ? { semantic } : {}), epoch: lease.epoch, leaseToken: lease.token, leaseOwner: owner, update: Buffer.from(Y.encodeStateAsUpdate(doc, baseVector)).toString('base64'), ifRev: state.rev, generation:state.generation??0, schema: version, snapshot: snapshot(doc), blocks, ifHashes: hashes, checkpoint: request.checkpoint }),epoch:syncEpoch(state)};
    } finally {
      doc.destroy();
      // The engine retains its fenced lease across editor operations. Each write renews with the original live principal; no unconfirmed document is cached.
    }
  }
  // Accept and reject act on a stored proposal by its ID. Accept re-proves the
  // target is exactly as proposed against, applies the same replacement and
  // commits it as the deciding human; Go marks the proposal in that commit.
  async function executeProposalDecision(page, operation, context, request) {
    if (context.authorization.startsWith('Bearer agtv2.')) throw fault(403, 'human proposal decision required');
    const fields = ['id','proposal','ifRev'];
    if (!request || typeof request !== 'object' || Array.isArray(request) || Object.keys(request).some(key => !fields.includes(key)) || fields.some(key => request[key] === undefined)) throw fault(400, 'closed proposal decision required');
    if (!id(request.id) || !id(request.proposal) || !Number.isSafeInteger(request.ifRev) || request.ifRev < 0) throw fault(400, 'decision ID, proposal ID and expected revision required');
    const state = await go(page, 'state', context);
    const fingerprint = await hash({ operation, request });
    const receipt = await go(page, 'receipt', context, { id: request.id, fingerprint });
    if (receipt.found) return { ...receipt, proposal: request.proposal, epoch: syncEpoch(state), duplicate: true };
    if (state.schema !== PROPOSAL_VERSION) throw fault(409, 'proposals require a schema 4 document');
    if (request.ifRev !== state.rev) throw fault(409, 'document changed since the proposal was reviewed; reload');
    const proposal = await go(page, 'proposal', context, { id: request.proposal });
    // A proposal from an earlier generation (engine restart, restore) or with a
    // changed target can never be accepted, but it can always be rejected, so
    // nothing is left open that nobody can close.
    if (proposal.status !== 'open' || (operation !== 'proposal-reject' && proposal.generation !== (state.generation ?? 0))) throw fault(409, 'proposal is no longer open');
    if (operation === 'proposal-reject') return { ...await go(page, 'proposal-reject', context, { id: request.id, fingerprint, proposal: request.proposal, ifRev: state.rev, generation: state.generation ?? 0 }), proposal: request.proposal, epoch: syncEpoch(state) };
    let doc = load(state.snapshot);
    try {
      for (const update of state.updates || []) Y.applyUpdate(doc, decode(typeof update === 'string' ? update : update.update));
      validate(doc, PROPOSAL_VERSION);
      const baseVector = Y.encodeStateVector(doc);
      const target = (await outline(doc, PROPOSAL_VERSION)).find(block => block.id === proposal.target);
      if (!target || target.hash !== proposal.base_hash) throw fault(409, 'proposal target changed; it can no longer be accepted as reviewed');
      patch(doc, { op: 'replaceText', id: proposal.target, text: proposal.text }, PROPOSAL_VERSION);
      if (doc.store.pendingStructs || doc.store.pendingDs) throw fault(400, 'unresolved Yjs dependencies');
      validate(doc, PROPOSAL_VERSION);
      const blocks = (await outline(doc, PROPOSAL_VERSION)).map(({ id, hash }) => ({ id, hash }));
      const lease = await writerLease(page, context, state);
      const renewal = await go(page, 'renew', context, { owner, epoch: lease.epoch, token: lease.token });
      if (renewal.expiresAt) lease.expiresAt = renewal.expiresAt;
      return { ...await go(page, 'commit', context, { id: request.id, fingerprint, kind: 'human', proposal: request.proposal, epoch: lease.epoch, leaseToken: lease.token, leaseOwner: owner, update: Buffer.from(Y.encodeStateAsUpdate(doc, baseVector)).toString('base64'), ifRev: state.rev, generation: state.generation ?? 0, schema: PROPOSAL_VERSION, snapshot: snapshot(doc), blocks, ifHashes: { [proposal.target]: proposal.base_hash } }), proposal: request.proposal, epoch: syncEpoch(state) };
    } finally { doc.destroy(); }
  }
  async function writerLease(page, context, state) {
    const key=`${context.workspace}/${page}`;const cached=leases.get(key);let lease;
    if(cached && cached.lease.epoch===state.epoch && (!cached.lease.expiresAt || Date.parse(cached.lease.expiresAt)>Date.now()+1000)) lease=cached.lease;
    if(!lease) { lease=await go(page,'lease',context,{owner,epoch:state.epoch});leases.set(key,{lease,context,page,touched:Date.now()}); } else {cached.touched=Date.now();cached.context=context;}
    if(lease.generation!==undefined && lease.generation!==state.generation) throw fault(409,'document generation changed; reload');
    if (!Number.isSafeInteger(lease.epoch) || lease.epoch < state.epoch || typeof lease.token !== 'string') throw fault(409, 'writer lease changed; reload document');
    return lease;
  }
  const timer = setInterval(() => {
    for(const [key,held] of leases) if(!documents.get(key)?.queued && Date.now()-held.touched>=Math.min(idleMs,15000)) {
      leases.delete(key);void go(held.page,'release',held.context,{owner,epoch:held.lease.epoch,token:held.lease.token}).catch(()=>{});
    }
    for(const [key,entry] of documents) if(!entry.queued && Date.now()-entry.touched>=idleMs)documents.delete(key);
  },Math.min(idleMs,1000));timer.unref();
  async function drain() {
    draining=true;for(const request of decoding)request.destroy();let timeout;
    const finish=async()=>{await Promise.all([...documents.values()].map(entry=>entry.tail));await Promise.all([...leases.values()].map(held=>go(held.page,'release',held.context,{owner,epoch:held.lease.epoch,token:held.lease.token}).catch(()=>{})));leases.clear();documents.clear()};
    try{await Promise.race([finish(),new Promise((_,reject)=>{timeout=setTimeout(()=>reject(fault(503,'drain timeout')),drainTimeoutMs)})])}finally{clearTimeout(timeout)}
  }
  const server = http.createServer(async (req, res) => {
    let counted=false;
    res.setHeader('Cache-Control', 'no-store');
    const reply = (status, value) => { if (!res.destroyed) { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); } };
    try {
      const supplied = Buffer.from(String(req.headers['x-crewship-docs-service'] || '')); const expected = Buffer.from(secret);
      if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected)) throw fault(403, 'service authentication required');
      if (req.method === 'GET' && req.url === '/health/live') return reply(200, { live: true, ...identity });
      if (req.method === 'GET' && req.url === '/health/ready') {
        if (draining || !ready) return reply(503, { ready: false, ...identity });
        const authority = await go('', 'health', { workspace: '' });
        if (authority.protocol !== PROTOCOL || authority.schema !== LATEST_VERSION) throw fault(503, 'authority protocol or schema unsupported');
        return reply(200, { ready: true, ...identity });
      }
      if (req.method === 'POST' && req.url === '/drain') { await drain(); return reply(200, { drained: true }); }
      if(draining){res.setHeader('Connection','close');res.once('finish',()=>req.socket.destroy());throw fault(503,'service draining');}
      if(!ready){res.setHeader('Connection','close');res.once('finish',()=>req.socket.destroy());throw fault(503,'engine recovering');}
      const match = /^\/v1\/documents\/([a-zA-Z0-9_-]{1,128})\/(prepare-create|initialize|upgrade|read|verify-targets|patch|checkpoint|restore|comments|comment|resolve|checkpoints|checkpoint-read|export|propose|proposals|proposal|proposal-accept|proposal-reject)$/.exec(req.url);
      if (req.method !== 'POST' || !match) throw fault(404, 'unknown operation');
      const context = { workspace: req.headers['x-workspace-id'], authorization: req.headers.authorization };
      if (!id(context.workspace) || typeof context.authorization !== 'string' || !/^Bearer \S+$/.test(context.authorization)) throw fault(401, 'workspace and bearer required');
      if(admitted>=128)throw fault(429,'request admission limit');admitted++;counted=true;
      let request;decoding.add(req);try{request=await body(req)}finally{decoding.delete(req)}
      reply(200, await enqueue(`${context.workspace}/${match[1]}`, () => execute(match[1], match[2], context, request)));
    } catch (error) { reply(error.status || 400, { error: error.status ? error.message : 'invalid document operation',...(error.status && error.code?{code:error.code}:{}) }); } finally {if(counted)admitted--;}
  });
  server.requestTimeout = timeoutMs * 8; server.headersTimeout = timeoutMs; server.keepAliveTimeout = 1000;
  // Prove listener custody before fencing the previous engine. No document
  // request is admitted until Go confirms the idempotent recovery barrier.
  let bound=false,startupLock;
  try {
    startupLock=await holdStartupLock(socket);
    await reclaimCrashedSocket(socket,Boolean(startupLock));
    await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(socket,resolve)});bound=true;
    await chmod(socket,0o600);
    const recovered=await go('engine','recover',{workspace:''},{boot:owner,protocol:PROTOCOL,schema:LATEST_VERSION});
    if(!recovered.recovered || recovered.protocol!==PROTOCOL || recovered.schema!==LATEST_VERSION)throw new Error('authority did not confirm compatible engine recovery');ready=true;
  }catch(error){clearInterval(timer);if(bound){server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}startupLock?.close();throw error}
  return { identity, socket, drain, async close() { await drain(); clearInterval(timer); await new Promise(resolve => server.close(resolve)); startupLock?.close(); } };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const service = await startService({ socket: process.env.CREWSHIP_DOCS_SOCKET, authoritySocket: process.env.CREWSHIP_DOCS_AUTHORITY_SOCKET, tokenFile: process.env.CREWSHIP_DOCS_SERVICE_TOKEN_FILE });
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => service.close().then(() => process.exit(0)));
}
