import test from 'node:test';
import assert from 'node:assert/strict';
import { setTimeout as delay } from 'node:timers/promises';
import { Server } from '@hocuspocus/server';
import { HocuspocusProvider, HocuspocusProviderWebsocket } from '@hocuspocus/provider';
import WebSocket from 'ws';
import * as Y from 'yjs';
const latch = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
async function until(predicate, label) { const deadline = Date.now() + 5000; while (!predicate()) { if (Date.now() > deadline) throw new Error(`timeout: ${label}`); await delay(10); } }
// Counterexample to latest-store-context attribution, not a proof about every public hook strategy.
test('revoked broadcast survives refused persistence and persists through authorized peer', { timeout: 15000 }, async t => {
  const entered = latch(), release = latch(), refused = latch();
  const revoked = new Set(), attempts = []; let persisted = null, first = true;
  const server = new Server({ port: 0, address: '127.0.0.1', quiet: true, stopOnSignals: false, debounce: 30, maxDebounce: 100,
    async onAuthenticate({ token }) { if (revoked.has(token)) throw new Error('revoked'); return { actor: token }; },
    async onStoreDocument({ document, lastContext }) {
      const actor = lastContext.actor; attempts.push(actor);
      if (first) { first = false; entered.resolve(); await release.promise; }
      if (revoked.has(actor)) { refused.resolve(); return; }
      persisted = Y.encodeStateAsUpdate(document);
    },
  });
  const clients = [];
  t.after(async () => { release.resolve(); for (const c of clients) { c.provider.destroy(); c.socket.destroy(); c.doc.destroy(); } await server.destroy(); });
  await server.listen(); const url = `ws://127.0.0.1:${server.httpServer.address().port}`;
  async function connect(actor) {
    const doc = new Y.Doc(), socket = new HocuspocusProviderWebsocket({ url, WebSocketPolyfill: WebSocket });
    const provider = new HocuspocusProvider({ websocketProvider: socket, name: 'counterexample', document: doc, token: actor });
    provider.attach();
    const client = { doc, socket, provider }; clients.push(client); await until(() => provider.synced, `${actor} sync`); return client;
  }
  const a = await connect('A'), b = await connect('B');
  a.doc.getText('body').insert(0, 'A pending.');
  await until(() => b.doc.getText('body').toString() === 'A pending.', 'A broadcast'); await entered.promise;
  revoked.add('A'); a.provider.destroy(); a.socket.destroy();
  for (const c of server.hocuspocus.documents.get('counterexample').getConnections()) if (c.context.actor === 'A') c.close();
  release.resolve(); await refused.promise;
  assert.equal(persisted, null, 'A batch refused'); assert.equal(b.doc.getText('body').toString(), 'A pending.');
  b.doc.getText('body').insert(b.doc.getText('body').length, ' B authorized.'); await until(() => persisted !== null, 'B persistence');
  const stored = new Y.Doc(); t.after(() => stored.destroy()); Y.applyUpdate(stored, persisted);
  assert.equal(stored.getText('body').toString(), 'A pending. B authorized.'); assert.deepEqual(attempts.slice(0, 2), ['A', 'B']);
});
