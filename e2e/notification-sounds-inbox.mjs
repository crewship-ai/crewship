import { readPrivateJson, createPrivateArtifacts } from './helpers/private-artifacts.mjs';
// Isolated browser-only Inbox sound fixture on Dev2. No fake inbox rows are
// written to the server. Real password login, WebSocket handshake and native
// Web Audio stay intact; only authorized unread-list responses/events are varied.
// TEAM_CHAT_STATE=/private/accounts.json node e2e/notification-sounds-inbox.mjs
import { chromium, expect } from '@playwright/test';
import fs from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';
const statePath = process.env.TEAM_CHAT_STATE;
if (!statePath) throw new Error('TEAM_CHAT_STATE must point to private demo credentials');
const state = await readPrivateJson(statePath);
const base = 'https://crewship-dev2.unifylab.cz';
const report = { server: base, fixture: 'browser-only-authorized-inbox', checks: [], page_errors: [], audio_evidence: 'Original native OscillatorNode.start calls, not physical speaker output. Inbox rows and invalidation frames are simulated only in this browser.' };
const artifacts = await createPrivateArtifacts('notification-sounds-inbox');
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
async function request(url, options = {}) {
  for (let attempt = 0; attempt < 4; attempt++) {
    const response = await context.request.fetch(url, options);
    if (response.status() !== 429) return response;
    const seconds = Number(response.headers()['retry-after']);
    let delay = Number.isFinite(seconds) && seconds > 0 ? seconds * 1000 : 60000;
    assert.ok(delay <= 300000);
    while (delay > 0) { console.log('WAIT respecting authentication Retry-After'); const part = Math.min(delay, 20000); await wait(part); delay -= part; }
  }
  throw new Error('Authentication rate limit persisted; no bypass attempted');
}
const passed = label => { report.checks.push(label); console.log('PASS', label); };
let page;
try {
  const csrf = await request(base + '/api/auth/csrf');
  assert.equal(csrf.status(), 200);
  const login = await request(base + '/api/auth/callback/credentials', { method: 'POST', data: { email: state.accounts.emma.email, password: state.accounts.emma.password, csrfToken: (await csrf.json()).csrfToken, redirect: 'false', json: 'true' } });
  assert.equal(login.status(), 200);
  assert.ok(!(await login.json()).error, 'Login failed; details suppressed');
  await context.addInitScript(() => {
    window.__inboxSoundStarts = [];
    window.__inboxSoundSockets = [];
    const originalStart = OscillatorNode.prototype.start;
    OscillatorNode.prototype.start = function (...args) {
      const result = Reflect.apply(originalStart, this, args);
      window.__inboxSoundStarts.push({ at: Date.now(), state: this.context.state });
      return result;
    };
    window.WebSocket = new Proxy(window.WebSocket, { construct(Target, args, NewTarget) {
      const socket = Reflect.construct(Target, args, NewTarget);
      window.__inboxSoundSockets.push(socket);
      return socket;
    } });
  });
  let rows = [];
  let reads = 0;
  await context.route('**/api/v1/inbox?*', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('state') !== 'unread') return route.continue();
    // Authenticate and authorize the real request before replacing its data.
    const response = await route.fetch();
    if (response.status() !== 200) return route.fulfill({ response });
    const actual = await response.json();
    reads++;
    await route.fulfill({ response, json: { ...actual, rows, unread_count: rows.filter(row => row.state === 'unread').length } });
  });
  page = await context.newPage();
  page.on('pageerror', error => report.page_errors.push(error.message));
  await page.goto(base + `/chat?conversation=${encodeURIComponent(state.accounts.emma.direct_conversation_id)}&workspace_id=${encodeURIComponent(state.workspace_id)}`);
  await page.getByRole('button', { name: 'User menu', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Notification sounds', exact: true }).click();
  const dialog = page.getByRole('region', { name: 'Notification sounds' });
  await expect(dialog).toBeVisible();
  const enable = dialog.getByRole('button', { name: 'Enable sounds', exact: true });
  if (await enable.count()) await enable.click();
  else { const activate = dialog.getByRole('button', { name: 'Activate audio', exact: true }); if (await activate.count()) await activate.click(); }
  await expect(page.getByRole('status').filter({ hasText: 'Audio is ready' })).toBeVisible();
  await dialog.getByRole('combobox', { name: 'Inbox sound' }).selectOption('chime');
  await page.goBack();
  await expect(dialog).not.toBeVisible();
  await expect.poll(() => page.evaluate(() => window.__inboxSoundSockets.some(socket => socket.readyState === WebSocket.OPEN))).toBe(true);
  const starts = () => page.evaluate(() => window.__inboxSoundStarts.length);
  const emit = () => page.evaluate(workspace => {
    const socket = window.__inboxSoundSockets.find(item => item.readyState === WebSocket.OPEN);
    if (!socket) throw new Error('Real WebSocket is not connected');
    socket.dispatchEvent(new MessageEvent('message', { data: JSON.stringify({ type: 'inbox.updated', channel: `workspace:${workspace}`, payload: { workspace_id: workspace, source: 'isolated_browser_fixture' } }) }));
  }, state.workspace_id);
  const stamp = Date.now();
  const item = (id, overrides = {}) => ({ id: `sound-browser-fixture-${stamp}-${id}`, workspace_id: state.workspace_id, source_id: `fixture-${id}`, kind: 'waitpoint', state: 'unread', priority: 'high', blocking: true, title: 'Browser-only sound fixture', created_at: new Date().toISOString(), updated_at: new Date().toISOString(), ...overrides });
  assert.equal(await starts(), 0);
  await wait(100);
  rows = [item('old', { created_at: '2020-01-01T00:00:00Z' }), item('read', { state: 'read' }), item('routine', { kind: 'schedule_missed', priority: 'medium', blocking: false }), item('message', { kind: 'message' })];
  const initialReads = reads;
  await emit();
  await expect.poll(() => reads).toBeGreaterThan(initialReads);
  await wait(800);
  assert.equal(await starts(), 0);
  passed('Real authenticated inbox glue leaves old, read, ordinary routine and chat-projection rows silent');
  rows = [item('important-1')];
  await emit();
  await expect.poll(starts, { timeout: 10000 }).toBe(3);
  passed('Fresh high-priority unread waitpoint starts exactly three native Chime nodes');
  await emit(); await emit(); await emit();
  await wait(2300);
  assert.equal(await starts(), 3);
  passed('Duplicate simulated invalidations do not replay the same inbox item');
  rows = [item('important-2')];
  await emit();
  await expect.poll(starts, { timeout: 10000 }).toBe(6);
  passed('A distinct important item plays another Chime after the shared cooldown');
  await page.getByRole('button', { name: 'User menu', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Notification sounds', exact: true }).click();
  await dialog.getByRole('checkbox', { name: 'Do not disturb' }).check();
  await page.goBack();
  await wait(2300);
  rows = [item('dnd')];
  await emit(); await wait(800);
  assert.equal(await starts(), 6);
  passed('Do not disturb suppresses fresh important Inbox alerts');
  await page.getByRole('button', { name: 'User menu', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Notification sounds', exact: true }).click();
  await dialog.getByRole('checkbox', { name: 'Do not disturb' }).uncheck();
  await page.goBack();
  await emit(); await wait(800);
  assert.equal(await starts(), 6);
  passed('Leaving DND does not replay its historical alert');
  assert.ok((await page.evaluate(() => window.__inboxSoundStarts)).every(entry => entry.state === 'running'));
  assert.deepEqual(report.page_errors, []);
  await page.getByRole('button', { name: 'User menu', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Notification sounds', exact: true }).click();
  await page.screenshot({ path: path.join(artifacts, 'settings-inbox-chime.png'), fullPage: true });
  report.status = 'passed';
} catch (error) {
  report.status = 'failed'; report.error = error.message;
  console.error('Isolated Inbox browser test failed:', error.message);
  if (page) await page.screenshot({ path: path.join(artifacts, 'notification-sounds-inbox-failure.png'), fullPage: true });
  process.exitCode = 1;
} finally {
  await fs.writeFile(path.join(artifacts, 'notification-sounds-inbox-report.json'), JSON.stringify(report, null, 2));
  await context.close(); await browser.close();
}
