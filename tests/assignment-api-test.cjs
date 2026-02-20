/**
 * Assignment API Smoke Test
 *
 * Validates the new Phase 2A assignment endpoints are registered and
 * respond correctly to basic requests.
 *
 * Usage:
 *   node tests/assignment-api-test.cjs
 *
 * Environment:
 *   PORT              Go server port. Defaults to 8081
 *   INTERNAL_TOKEN    Optional. Read from crewship config if not set.
 */

'use strict';
const http = require('http');

const PORT = process.env.PORT || '8081';
const BASE = `http://localhost:${PORT}`;

let passed = 0;
let failed = 0;

async function request(method, path, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, BASE);
    const opts = {
      method,
      hostname: url.hostname,
      port: url.port,
      path: url.pathname + url.search,
      headers: {
        'Content-Type': 'application/json',
        ...headers,
      },
    };
    const req = http.request(opts, (res) => {
      let data = '';
      res.on('data', (c) => (data += c));
      res.on('end', () => {
        let json = null;
        try { json = JSON.parse(data); } catch {}
        resolve({ status: res.statusCode, body: json, raw: data });
      });
    });
    req.on('error', reject);
    if (body) req.write(JSON.stringify(body));
    req.end();
  });
}

function check(name, condition, detail = '') {
  if (condition) {
    console.log(`  ✓ ${name}`);
    passed++;
  } else {
    console.log(`  ✗ ${name}${detail ? ': ' + detail : ''}`);
    failed++;
  }
}

async function getInternalToken() {
  // Try to read from running server config via health endpoint detail
  // Fall back to checking the server state
  const res = await request('GET', '/api/health');
  if (res.status !== 200) {
    console.error('Server not running at', BASE);
    process.exit(1);
  }
  return null; // token not exposed via health
}

async function run() {
  console.log('Assignment API Smoke Test');
  console.log('=========================');
  console.log('Target:', BASE);
  console.log('');

  // 1. Health check
  console.log('1. Health check');
  const health = await request('GET', '/api/health');
  check('GET /api/health → 200', health.status === 200);

  // 2. GET assignment without token → 403 Forbidden (internal auth)
  console.log('\n2. Assignment GET without token (expect 403)');
  const noAuth = await request('GET', '/api/v1/internal/assignments/nonexistent');
  check('GET /internal/assignments/x without token → 403', noAuth.status === 403,
    `got ${noAuth.status}`);

  // 3. POST assignment without token → 403
  console.log('\n3. Assignment POST without token (expect 403)');
  const noAuthPost = await request('POST', '/api/v1/internal/assignments', {
    target_slug: 'viktor',
    task: 'test',
    crew_id: 'crew1',
    workspace_id: 'ws1',
    chat_id: 'chat1',
  });
  check('POST /internal/assignments without token → 403', noAuthPost.status === 403,
    `got ${noAuthPost.status}`);

  // 4. GET assignment with wrong token → 403
  console.log('\n4. Assignment GET with wrong token (expect 403)');
  const wrongToken = await request('GET', '/api/v1/internal/assignments/nonexistent', null, {
    'X-Internal-Token': 'wrong-token',
  });
  check('GET /internal/assignments/x with wrong token → 403', wrongToken.status === 403,
    `got ${wrongToken.status}`);

  // 5. GET assignment with valid token but nonexistent ID → 404
  const DEV_TOKEN = process.env.INTERNAL_TOKEN || 'crewship-dev-internal-token-for-testing';
  console.log('\n5. Assignment GET with valid token, nonexistent ID (expect 404)');
  const validNotFound = await request('GET', '/api/v1/internal/assignments/does-not-exist', null, {
    'X-Internal-Token': DEV_TOKEN,
  });
  check('GET /internal/assignments/does-not-exist with valid token → 404', validNotFound.status === 404,
    `got ${validNotFound.status} body=${JSON.stringify(validNotFound.body)}`);

  // 6. POST assignment with valid token but missing fields → 400
  console.log('\n6. Assignment POST with valid token, missing fields (expect 400)');
  const missingFields = await request('POST', '/api/v1/internal/assignments',
    { target_slug: 'viktor', task: 'test' },
    { 'X-Internal-Token': DEV_TOKEN }
  );
  check('POST /internal/assignments with missing fields → 400', missingFields.status === 400,
    `got ${missingFields.status} body=${JSON.stringify(missingFields.body)}`);

  // 7. POST assignment with valid token and nonexistent chat → 404
  console.log('\n7. Assignment POST with valid token, nonexistent chat (expect 404)');
  const chatNotFound = await request('POST', '/api/v1/internal/assignments',
    { target_slug: 'viktor', task: 'test', crew_id: 'crew1', workspace_id: 'ws1', chat_id: 'nonexistent-chat' },
    { 'X-Internal-Token': DEV_TOKEN }
  );
  check('POST /internal/assignments with nonexistent chat → 404', chatNotFound.status === 404,
    `got ${chatNotFound.status} body=${JSON.stringify(chatNotFound.body)}`);

  // Summary
  console.log('\n=========================');
  console.log(`Results: ${passed} passed, ${failed} failed`);
  if (failed === 0) {
    console.log('✓ All smoke tests passed');
  } else {
    console.log('✗ Some smoke tests failed');
  }
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((e) => {
  console.error('Error:', e.message);
  process.exit(1);
});
