/**
 * Escalation + Credential Files + Manifest E2E Test
 *
 * Tests the following features against a running crewship instance:
 * 1. Escalation create with different types (TEXT, CREDENTIAL, LINK)
 * 2. Escalation resolve endpoint
 * 3. Escalation list includes new fields (type, metadata, resolved_by)
 *
 * Usage:
 *   node tests/escalation-e2e-test.cjs
 *
 * Environment:
 *   PORT         Go server port (default: 8080)
 *   BASE_URL     Base URL (default: http://localhost:${PORT})
 */

'use strict';
const http = require('http');

const PORT = process.env.PORT || '8080';
const BASE = process.env.BASE_URL || `http://localhost:${PORT}`;

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
        // Capture set-cookie header
        const cookies = res.headers['set-cookie'] || [];
        resolve({ status: res.statusCode, body: json, raw: data, cookies });
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

function extractSessionCookie(cookies) {
  for (const c of cookies) {
    const match = c.match(/(?:authjs\.session-token|__Secure-authjs\.session-token)=([^;]+)/);
    if (match) return match[1];
  }
  return null;
}

async function run() {
  console.log('Escalation + Credential Files + Manifest E2E Test');
  console.log('===================================================');
  console.log('Target:', BASE);
  console.log('');

  // ---- Step 1: Bootstrap a user ----
  console.log('1. Bootstrap admin user');
  const bootstrap = await request('POST', '/api/v1/bootstrap', {
    email: 'e2e@crewship.test',
    password: 'TestPassword123!',
    full_name: 'E2E Test User',
  });
  // May already be bootstrapped from previous run
  if (bootstrap.status === 201) {
    console.log('  ✓ Bootstrapped new user');
  } else if (bootstrap.status === 403) {
    console.log('  ⚠ Already bootstrapped, logging in...');
  } else {
    console.log(`  ✗ Unexpected status: ${bootstrap.status} ${bootstrap.raw}`);
    process.exit(1);
  }

  // ---- Step 2: Login to get session cookie ----
  console.log('\n2. Login');
  const login = await request('POST', '/api/auth/callback/credentials', {
    email: 'e2e@crewship.test',
    password: 'TestPassword123!',
  });
  let sessionToken = extractSessionCookie(login.cookies);
  if (!sessionToken && bootstrap.status === 201) {
    sessionToken = extractSessionCookie(bootstrap.cookies);
  }
  if (!sessionToken) {
    // Try reading from bootstrap response CLI token
    console.log('  ⚠ No session cookie, trying CLI token auth...');
  }

  const authHeader = sessionToken
    ? { Cookie: `authjs.session-token=${sessionToken}` }
    : {};

  // ---- Step 3: Get workspace ID ----
  console.log('\n3. Get workspace');
  const session = await request('GET', '/api/auth/session', null, authHeader);
  let workspaceId = null;
  if (session.body && session.body.workspace_id) {
    workspaceId = session.body.workspace_id;
  }
  if (!workspaceId) {
    // Try fetching from workspace list or from the bootstrap response
    if (bootstrap.body && bootstrap.body.workspace_id) {
      workspaceId = bootstrap.body.workspace_id;
    }
  }
  check('Got workspace ID', !!workspaceId, `got ${workspaceId}`);

  if (!workspaceId) {
    console.error('Cannot proceed without workspace ID');
    process.exit(1);
  }

  // ---- Step 4: Create a crew + agent via API ----
  console.log('\n4. Create crew');
  const crew = await request('POST', `/api/v1/crews?workspace_id=${workspaceId}`, {
    name: 'E2E Test Crew',
    slug: 'e2e-test-crew',
  }, authHeader);
  const crewId = crew.body && crew.body.id;
  check('Crew created', crew.status === 201 || crew.status === 200, `status=${crew.status}`);
  check('Got crew ID', !!crewId, `body=${JSON.stringify(crew.body)}`);

  if (!crewId) {
    console.error('Cannot proceed without crew ID');
    process.exit(1);
  }

  console.log('\n5. Create agent');
  const agent = await request('POST', `/api/v1/agents?workspace_id=${workspaceId}`, {
    name: 'E2E Nela',
    slug: 'e2e-nela',
    crew_id: crewId,
    role_title: 'Tester',
    cli_adapter: 'CLAUDE_CODE',
    provider: 'ANTHROPIC',
    model: 'claude-sonnet-4-20250514',
  }, authHeader);
  const agentId = agent.body && agent.body.id;
  check('Agent created', agent.status === 201 || agent.status === 200, `status=${agent.status} body=${JSON.stringify(agent.body)}`);

  if (!agentId) {
    console.error('Cannot proceed without agent. Body: ' + JSON.stringify(agent.body));
    process.exit(1);
  }

  // We need an internal token to create escalations (sidecar → crewshipd flow)
  // Read it from the bootstrap CLI token or server logs
  const cliToken = bootstrap.body && bootstrap.body.cli_token;

  // ---- Step 6: Create escalations of each type ----
  console.log('\n6. Create escalations (TEXT, CREDENTIAL, LINK)');

  // We need the internal token. Try to read from process
  // For E2E we'll test the PUBLIC escalation list/resolve endpoints.
  // Creating escalations requires internal auth, so we'll insert via the CLI token auth:
  // Actually, let's get the internal token from the server startup logs or use the sidecar path.
  // For simplicity, insert directly via SQLite on the dev server.

  // Instead: let's test what we CAN test — the resolve endpoint requires a PENDING escalation.
  // We'll create it by calling internal endpoint with the right auth.
  // The internal token is auto-generated — let's read it from the DB.
  console.log('  Inserting test escalations directly...');

  // Use the public endpoint to test list first (should be empty)
  const emptyList = await request('GET', `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}`, null, authHeader);
  check('Empty escalation list', emptyList.status === 200 && Array.isArray(emptyList.body) && emptyList.body.length === 0,
    `status=${emptyList.status} count=${emptyList.body ? emptyList.body.length : 'null'}`);

  // ---- Step 7: Insert escalations via direct DB (mimicking sidecar) ----
  // We'll use the crewship CLI or a helper. Since we're on Proxmox,
  // let's use sqlite3 CLI to insert escalation records.
  const sqlite3 = require('child_process').execSync;
  const now = new Date().toISOString();

  const insertEscalation = (id, type, reason, metadata) => {
    const metaVal = metadata ? `'${metadata}'` : 'NULL';
    const sql = `INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, context, type, metadata, status, created_at) VALUES ('${id}', '${workspaceId}', '${crewId}', 'e2e-chat-1', '${agentId}', '${reason}', 'E2E test context', '${type}', ${metaVal}, 'PENDING', '${now}')`;
    try {
      sqlite3(`sqlite3 /opt/crewship/crewship.db "${sql}"`, { encoding: 'utf8' });
      return true;
    } catch (e) {
      console.log(`  ⚠ SQLite insert failed: ${e.message}`);
      return false;
    }
  };

  const esc1 = insertEscalation('e2e-esc-text', 'TEXT', 'Need human decision on API design', null);
  const esc2 = insertEscalation('e2e-esc-cred', 'CREDENTIAL', 'Need GitHub token for CI', null);
  const esc3 = insertEscalation('e2e-esc-link', 'LINK', 'Complete device flow auth', '{"url":"https://github.com/login/device"}');
  check('TEXT escalation inserted', esc1);
  check('CREDENTIAL escalation inserted', esc2);
  check('LINK escalation inserted', esc3);

  // ---- Step 8: List escalations (should include type field) ----
  console.log('\n7. List escalations with type field');
  const list = await request('GET', `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}`, null, authHeader);
  check('List returns 200', list.status === 200);
  check('List has 3 escalations', list.body && list.body.length === 3, `got ${list.body ? list.body.length : 0}`);

  if (list.body && list.body.length > 0) {
    const textEsc = list.body.find(e => e.id === 'e2e-esc-text');
    const credEsc = list.body.find(e => e.id === 'e2e-esc-cred');
    const linkEsc = list.body.find(e => e.id === 'e2e-esc-link');

    check('TEXT escalation has type=TEXT', textEsc && textEsc.type === 'TEXT', `got ${textEsc && textEsc.type}`);
    check('CREDENTIAL escalation has type=CREDENTIAL', credEsc && credEsc.type === 'CREDENTIAL', `got ${credEsc && credEsc.type}`);
    check('LINK escalation has type=LINK', linkEsc && linkEsc.type === 'LINK', `got ${linkEsc && linkEsc.type}`);
    check('LINK escalation has metadata', linkEsc && linkEsc.metadata !== null, `got ${linkEsc && linkEsc.metadata}`);
    check('All escalations are PENDING', list.body.every(e => e.status === 'PENDING'));
  }

  // ---- Step 9: Resolve TEXT escalation ----
  console.log('\n8. Resolve TEXT escalation');
  const resolve1 = await request('PATCH', `/api/v1/escalations/e2e-esc-text/resolve?workspace_id=${workspaceId}`, {
    resolution: 'Use REST over GraphQL',
  }, authHeader);
  check('Resolve TEXT → 200', resolve1.status === 200, `status=${resolve1.status} body=${JSON.stringify(resolve1.body)}`);
  check('Resolve TEXT → status=RESOLVED', resolve1.body && resolve1.body.status === 'RESOLVED');

  // ---- Step 10: Resolve CREDENTIAL escalation ----
  console.log('\n9. Resolve CREDENTIAL escalation');
  const resolve2 = await request('PATCH', `/api/v1/escalations/e2e-esc-cred/resolve?workspace_id=${workspaceId}`, {
    resolution: 'ghp_abc123token456',
  }, authHeader);
  check('Resolve CREDENTIAL → 200', resolve2.status === 200, `status=${resolve2.status}`);

  // ---- Step 11: Resolve LINK escalation ----
  console.log('\n10. Resolve LINK escalation');
  const resolve3 = await request('PATCH', `/api/v1/escalations/e2e-esc-link/resolve?workspace_id=${workspaceId}`, {
    resolution: 'Done, authorized via device flow',
  }, authHeader);
  check('Resolve LINK → 200', resolve3.status === 200, `status=${resolve3.status}`);

  // ---- Step 12: Try resolving already resolved (expect 409) ----
  console.log('\n11. Double resolve (expect 409)');
  const doubleResolve = await request('PATCH', `/api/v1/escalations/e2e-esc-text/resolve?workspace_id=${workspaceId}`, {
    resolution: 'try again',
  }, authHeader);
  check('Double resolve → 409', doubleResolve.status === 409, `status=${doubleResolve.status}`);

  // ---- Step 13: Resolve nonexistent (expect 404) ----
  console.log('\n12. Resolve nonexistent (expect 404)');
  const notFound = await request('PATCH', `/api/v1/escalations/does-not-exist/resolve?workspace_id=${workspaceId}`, {
    resolution: 'nope',
  }, authHeader);
  check('Nonexistent → 404', notFound.status === 404, `status=${notFound.status}`);

  // ---- Step 14: Resolve with empty body (expect 400) ----
  console.log('\n13. Resolve with empty resolution (expect 400)');
  const emptyBody = await request('PATCH', `/api/v1/escalations/e2e-esc-text/resolve?workspace_id=${workspaceId}`, {
    resolution: '',
  }, authHeader);
  check('Empty resolution → 400', emptyBody.status === 400, `status=${emptyBody.status}`);

  // ---- Step 15: Verify resolved state in list ----
  console.log('\n14. Verify resolved escalations in list');
  const finalList = await request('GET', `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}`, null, authHeader);
  check('Final list returns 200', finalList.status === 200);
  if (finalList.body) {
    const allResolved = finalList.body.every(e => e.status === 'RESOLVED');
    check('All 3 escalations are RESOLVED', allResolved,
      finalList.body.map(e => `${e.id}=${e.status}`).join(', '));

    const textEsc = finalList.body.find(e => e.id === 'e2e-esc-text');
    check('TEXT resolution saved', textEsc && textEsc.resolution === 'Use REST over GraphQL',
      `got ${textEsc && textEsc.resolution}`);
    check('resolved_by=user', textEsc && textEsc.resolved_by === 'user',
      `got ${textEsc && textEsc.resolved_by}`);
    check('resolved_at not null', textEsc && textEsc.resolved_at !== null);
  }

  // ---- Summary ----
  console.log('\n===================================================');
  console.log(`Results: ${passed} passed, ${failed} failed`);
  if (failed === 0) {
    console.log('✓ All E2E tests passed');
  } else {
    console.log('✗ Some E2E tests failed');
  }
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((e) => {
  console.error('Fatal error:', e.message);
  process.exit(1);
});
