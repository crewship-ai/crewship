/**
 * Escalation + Credential Files + Manifest E2E Test
 *
 * Tests CRE-9, CRE-21, CRE-41 against a running crewship instance.
 * Requires a fresh DB (script nukes and re-bootstraps).
 *
 * Usage:
 *   node tests/escalation-e2e-test.cjs
 *
 * Environment:
 *   PORT  Go server port (default: 8080)
 */

'use strict';
const http = require('http');
const { execSync } = require('child_process');

const PORT = process.env.PORT || '8080';
const BASE = `http://localhost:${PORT}`;

let passed = 0;
let failed = 0;

async function req(method, path, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, BASE);
    const opts = {
      method,
      hostname: url.hostname,
      port: url.port,
      path: url.pathname + url.search,
      headers: { 'Content-Type': 'application/json', ...headers },
    };
    const r = http.request(opts, (res) => {
      let data = '';
      res.on('data', (c) => (data += c));
      res.on('end', () => {
        let json = null;
        try { json = JSON.parse(data); } catch {}
        resolve({ status: res.statusCode, body: json, raw: data });
      });
    });
    r.on('error', reject);
    if (body) r.write(JSON.stringify(body));
    r.end();
  });
}

function ok(name, condition, detail = '') {
  if (condition) { console.log(`  ✓ ${name}`); passed++; }
  else { console.log(`  ✗ ${name}${detail ? ': ' + detail : ''}`); failed++; }
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function run() {
  console.log('Escalation E2E Test');
  console.log('===================');
  console.log('Target:', BASE);
  console.log('');

  // -- 1. Bootstrap --
  console.log('1. Bootstrap');
  const boot = await req('POST', '/api/v1/bootstrap', {
    email: 'e2e@crewship.test',
    password: 'TestPass123!',
    full_name: 'E2E Tester',
  });
  if (boot.status !== 201) {
    console.error(`  Bootstrap failed (status=${boot.status}). DB must be fresh. Run: rm crewship.db* && ./dev.sh restart`);
    process.exit(1);
  }
  const cliToken = boot.body.cli_token;
  const workspaceId = boot.body.workspace_id;
  ok('Bootstrapped', !!cliToken && !!workspaceId);
  const auth = { Authorization: `Bearer ${cliToken}` };

  // -- 2. Create crew --
  console.log('\n2. Create crew');
  const crew = await req('POST', `/api/v1/crews?workspace_id=${workspaceId}`, {
    name: 'E2E Crew', slug: 'e2e-crew',
  }, auth);
  ok('Crew created', crew.status === 201, `status=${crew.status} body=${crew.raw}`);
  const crewId = crew.body && crew.body.id;

  // -- 3. Create agent --
  console.log('\n3. Create agent');
  const agent = await req('POST', `/api/v1/agents?workspace_id=${workspaceId}`, {
    name: 'Nela', slug: 'nela', crew_id: crewId,
    role_title: 'Tester', cli_adapter: 'CLAUDE_CODE',
    provider: 'ANTHROPIC', model: 'claude-sonnet-4-20250514',
  }, auth);
  ok('Agent created', agent.status === 201, `status=${agent.status} body=${agent.raw}`);
  const agentId = agent.body && agent.body.id;

  if (!crewId || !agentId) {
    console.error('Cannot continue without crew+agent');
    process.exit(1);
  }

  // -- 4. Empty escalation list --
  console.log('\n4. Escalation list (empty)');
  const empty = await req('GET', `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}`, null, auth);
  ok('Empty list → 200', empty.status === 200);
  ok('Zero items', Array.isArray(empty.body) && empty.body.length === 0);

  // -- 5. Insert escalations via SQLite (simulates sidecar) --
  console.log('\n5. Insert test escalations (TEXT, CREDENTIAL, LINK)');
  const now = new Date().toISOString();
  const DB = '/opt/crewship/crewship.db';
  const sql = (s) => execSync(`sqlite3 "${DB}" "${s}"`, { encoding: 'utf8' }).trim();

  sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,context,type,status,created_at) VALUES ('esc-text','${workspaceId}','${crewId}','chat1','${agentId}','Need API decision','Context here','TEXT','PENDING','${now}')`);
  sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,context,type,status,created_at) VALUES ('esc-cred','${workspaceId}','${crewId}','chat1','${agentId}','Need GitHub token','For CI','CREDENTIAL','PENDING','${now}')`);
  sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,context,type,metadata,status,created_at) VALUES ('esc-link','${workspaceId}','${crewId}','chat1','${agentId}','Complete device flow','Auth needed','LINK','{"url":"https://github.com/login/device"}','PENDING','${now}')`);
  ok('3 escalations inserted', true);

  // -- 6. List with type field --
  console.log('\n6. List escalations (type field)');
  const list = await req('GET', `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}`, null, auth);
  ok('List → 200', list.status === 200);
  ok('3 items', list.body && list.body.length === 3, `got ${list.body && list.body.length}`);

  if (list.body && list.body.length >= 3) {
    const text = list.body.find(e => e.id === 'esc-text');
    const cred = list.body.find(e => e.id === 'esc-cred');
    const link = list.body.find(e => e.id === 'esc-link');
    ok('TEXT type', text && text.type === 'TEXT');
    ok('CREDENTIAL type', cred && cred.type === 'CREDENTIAL');
    ok('LINK type', link && link.type === 'LINK');
    ok('LINK metadata present', link && link.metadata != null);
    ok('All PENDING', list.body.every(e => e.status === 'PENDING'));
  }

  // -- 7. Resolve TEXT --
  console.log('\n7. Resolve TEXT escalation');
  const r1 = await req('PATCH', `/api/v1/escalations/esc-text/resolve?workspace_id=${workspaceId}`, {
    resolution: 'Use REST over GraphQL',
  }, auth);
  ok('Resolve TEXT → 200', r1.status === 200, `status=${r1.status} body=${r1.raw}`);
  ok('Status RESOLVED', r1.body && r1.body.status === 'RESOLVED');

  // -- 8. Resolve CREDENTIAL --
  console.log('\n8. Resolve CREDENTIAL escalation');
  const r2 = await req('PATCH', `/api/v1/escalations/esc-cred/resolve?workspace_id=${workspaceId}`, {
    resolution: 'ghp_abc123',
  }, auth);
  ok('Resolve CREDENTIAL → 200', r2.status === 200, `status=${r2.status}`);

  // -- 9. Resolve LINK --
  console.log('\n9. Resolve LINK escalation');
  const r3 = await req('PATCH', `/api/v1/escalations/esc-link/resolve?workspace_id=${workspaceId}`, {
    resolution: 'Done, authorized',
  }, auth);
  ok('Resolve LINK → 200', r3.status === 200, `status=${r3.status}`);

  // -- 10. Double resolve → 409 --
  console.log('\n10. Double resolve → 409');
  const r4 = await req('PATCH', `/api/v1/escalations/esc-text/resolve?workspace_id=${workspaceId}`, {
    resolution: 'again',
  }, auth);
  ok('Double → 409', r4.status === 409, `status=${r4.status}`);

  // -- 11. Nonexistent → 404 --
  console.log('\n11. Nonexistent → 404');
  const r5 = await req('PATCH', `/api/v1/escalations/nope/resolve?workspace_id=${workspaceId}`, {
    resolution: 'x',
  }, auth);
  ok('Nonexistent → 404', r5.status === 404, `status=${r5.status}`);

  // -- 12. Empty resolution → 400 --
  console.log('\n12. Empty resolution → 400');
  const r6 = await req('PATCH', `/api/v1/escalations/esc-text/resolve?workspace_id=${workspaceId}`, {
    resolution: '',
  }, auth);
  ok('Empty → 400', r6.status === 400, `status=${r6.status}`);

  // -- 13. Final list — all resolved --
  console.log('\n13. Final list (all resolved)');
  const fin = await req('GET', `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}`, null, auth);
  ok('Final list → 200', fin.status === 200);
  if (fin.body) {
    ok('All RESOLVED', fin.body.every(e => e.status === 'RESOLVED'));
    const t = fin.body.find(e => e.id === 'esc-text');
    ok('Resolution text saved', t && t.resolution === 'Use REST over GraphQL');
    ok('resolved_by=user', t && t.resolved_by === 'user');
    ok('resolved_at not null', t && t.resolved_at != null);
  }

  // -- 14. Resolve with action field --
  console.log('\n14. Resolve with action field (CRE-21)');
  sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at) VALUES ('esc-act1','${workspaceId}','${crewId}','chat1','${agentId}','Need approval for deploy','TEXT','PENDING','${now}')`);
  sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at) VALUES ('esc-act2','${workspaceId}','${crewId}','chat1','${agentId}','Wrong agent for this task','TEXT','PENDING','${now}')`);
  sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at) VALUES ('esc-act3','${workspaceId}','${crewId}','chat1','${agentId}','Bad approach','TEXT','PENDING','${now}')`);

  const rApprove = await req('PATCH', `/api/v1/escalations/esc-act1/resolve?workspace_id=${workspaceId}`, {
    resolution: 'Go ahead with deploy', action: 'approve',
  }, auth);
  ok('Resolve with approve → 200', rApprove.status === 200);
  ok('Response has action=approve', rApprove.body && rApprove.body.action === 'approve');

  const rReject = await req('PATCH', `/api/v1/escalations/esc-act3/resolve?workspace_id=${workspaceId}`, {
    resolution: 'Try different approach', action: 'reject',
  }, auth);
  ok('Resolve with reject → 200', rReject.status === 200);

  const rRedirect = await req('PATCH', `/api/v1/escalations/esc-act2/resolve?workspace_id=${workspaceId}`, {
    resolution: 'Nela should handle this', action: 'redirect', redirect_to: 'nela',
  }, auth);
  ok('Resolve with redirect → 200', rRedirect.status === 200);

  // -- 15. Verify action in list --
  console.log('\n15. List includes action field');
  const listAct = await req('GET', `/api/v1/crews/${crewId}/escalations?workspace_id=${workspaceId}`, null, auth);
  ok('List → 200', listAct.status === 200);
  if (listAct.body) {
    const act1 = listAct.body.find(e => e.id === 'esc-act1');
    const act2 = listAct.body.find(e => e.id === 'esc-act2');
    const act3 = listAct.body.find(e => e.id === 'esc-act3');
    ok('act1 action=approve', act1 && act1.action === 'approve');
    ok('act2 action=redirect', act2 && act2.action === 'redirect');
    ok('act2 redirect_to=nela', act2 && act2.redirect_to === 'nela');
    ok('act3 action=reject', act3 && act3.action === 'reject');
  }

  // -- 16. Invalid action → 400 --
  console.log('\n16. Invalid action → 400');
  sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at) VALUES ('esc-inv','${workspaceId}','${crewId}','chat1','${agentId}','test','TEXT','PENDING','${now}')`);
  const rInvalid = await req('PATCH', `/api/v1/escalations/esc-inv/resolve?workspace_id=${workspaceId}`, {
    resolution: 'test', action: 'invalid_action',
  }, auth);
  ok('Invalid action → 400', rInvalid.status === 400, `status=${rInvalid.status}`);

  // -- 17. Redirect without redirect_to → 400 --
  console.log('\n17. Redirect without redirect_to → 400');
  const rNoTarget = await req('PATCH', `/api/v1/escalations/esc-inv/resolve?workspace_id=${workspaceId}`, {
    resolution: 'redirect please', action: 'redirect',
  }, auth);
  ok('Redirect no target → 400', rNoTarget.status === 400, `status=${rNoTarget.status}`);

  // -- 18. Escalation type validation --
  console.log('\n18. Escalation type validation (DB constraint)');
  try {
    sql(`INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at) VALUES ('esc-bad','${workspaceId}','${crewId}','chat1','${agentId}','test','INVALID','PENDING','${now}')`);
    ok('Invalid type rejected by CHECK constraint', false, 'insert succeeded');
  } catch {
    ok('Invalid type rejected by CHECK constraint', true);
  }

  // -- Summary --
  console.log('\n===================');
  console.log(`Results: ${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((e) => { console.error('Fatal:', e.message); process.exit(1); });
