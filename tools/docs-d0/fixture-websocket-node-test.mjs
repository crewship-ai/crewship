import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import WebSocket from 'ws';
import { fixtureWebSocketURL } from './fixture-boundaries.mjs';

test('fixture WebSocket recipient is fixed numeric loopback and cannot follow redirects', async t => {
  assert.equal(fixtureWebSocketURL('http://127.0.0.1:80'), 'ws://127.0.0.1:80/ws');
  assert.equal(fixtureWebSocketURL('http://127.0.0.1:65535'), 'ws://127.0.0.1:65535/ws');
  assert.equal(fixtureWebSocketURL('http://127.0.0.1:1234'), 'ws://127.0.0.1:1234/ws');
  for (const origin of ['http://example.test:1234', 'http://localhost:1234', 'http://127.0.0.1:1234/foreign', 'ws://127.0.0.1:1234']) {
    assert.throws(() => fixtureWebSocketURL(origin));
  }
  let redirectedRequests = 0;
  const target = http.createServer((_req, res) => { redirectedRequests++; res.end('unexpected'); });
  await new Promise(resolve => target.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => target.close(resolve)));
  const redirect = http.createServer();
  redirect.on('upgrade', (_req, socket) => socket.end(`HTTP/1.1 302 Found\r\nLocation: ws://127.0.0.1:${target.address().port}/capture\r\nConnection: close\r\n\r\n`));
  await new Promise(resolve => redirect.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => redirect.close(resolve)));
  const ws = new WebSocket(fixtureWebSocketURL(`http://127.0.0.1:${redirect.address().port}`), { followRedirects: false, headers: { Authorization: 'Bearer synthetic-fixture-token' } });
  t.after(() => ws.terminate());
  const closed = new Promise(resolve => ws.once('close', resolve));
  const [error] = await once(ws, 'error');
  assert.match(error.message, /302/);
  await closed;
  assert.equal(redirectedRequests, 0);
});
