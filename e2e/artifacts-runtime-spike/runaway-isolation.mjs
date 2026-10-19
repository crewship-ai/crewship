// Run with an external process deadline. No application/authenticated sessions.
// TLS tunnel derives from ddaebc0ba:e2e/artifact-runtime/fixture.ts.
import assert from 'node:assert/strict';
import https from 'node:https';
import http from 'node:http';
import net from 'node:net';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { firefox, webkit } from 'playwright';
import { createRequire } from 'node:module';

const studio = 'https://studio.crewship.test', runtime = 'https://runtime.artifacts.test';
const runtimePath = '/api/v1/pages/runtime/bootstrap';
assert.equal(process.argv.length, 3, 'Pass the compiled stop-fixture binary');
const response = JSON.parse(execFileSync(process.argv[2], [], { encoding: 'utf8', timeout: 3000, maxBuffer: 256 * 1024 }));
assert.equal(response.status, 200);
assert.ok(response.headers['Content-Security-Policy'].join(';').includes("worker-src 'none'"));
const sources = ['internal/pagebuild/runtime.go', 'internal/pagebuild/bootstrap.js', 'lib/pages/runtime-support.ts'];
const require = createRequire(import.meta.url);
const report = {
  format: 'crewship-artifacts-runtime-stop-spike/v1',
  sourceCommit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
  sourceSHA256: Object.fromEntries(sources.map(path => [path, createHash('sha256').update(readFileSync(path)).digest('hex')])),
  environment: { node: process.version, platform: process.platform, playwright: require('playwright/package.json').version },
  scope: 'Browser-context lifecycle only; not OS-process/CPU isolation or R1 acceptance',
  runtimeAcceptance: false, productionSupportChanged: false,
  bootstrapSHA256: createHash('sha256').update(response.body).digest('hex'),
  origins: { studio, runtime, certificateValidation: 'test-only self-signed; no trust-store changes' },
  browsers: [],
};
const bounded = (promise, milliseconds, label) => {
  let timer;
  return Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${label} deadline`)), milliseconds); })]).finally(() => clearTimeout(timer));
};
const ownBrowsers = new Set();
async function interrupted() {
  try { await bounded(Promise.allSettled([...ownBrowsers].map(server => server.kill())), 3000, 'interrupted own-browser cleanup'); }
  finally { process.exit(124); }
}
process.once('SIGTERM', interrupted); process.once('SIGINT', interrupted);

function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'artifacts-stop-'));
  const sockets = new Set(), requests = [], blocked = [], tunnels = [];
  const track = socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); };
  let server, proxy;
  const close = async () => {
    for (const socket of sockets) socket.destroy();
    await Promise.all([server, proxy].filter(Boolean).map(value => new Promise(resolve => value.close(resolve))));
    rmSync(directory, { recursive: true, force: true });
  };
  return (async () => {
    try {
      execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', join(directory, 'key'), '-out', join(directory, 'cert'), '-days', '1', '-subj', '/CN=runtime.artifacts.test'], { stdio: 'ignore', timeout: 10000 });
      server = https.createServer({ key: readFileSync(join(directory, 'key')), cert: readFileSync(join(directory, 'cert')) }, (request, reply) => {
        const target = `https://${request.headers.host}${request.url}`;
        requests.push(target);
        if (target === `${studio}/`) {
          reply.writeHead(200, { 'Content-Type': 'text/html', 'Content-Security-Policy': `default-src 'none'; script-src 'unsafe-inline'; worker-src blob:; frame-src ${runtime}; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'`, 'Cross-Origin-Embedder-Policy': 'credentialless' });
          reply.end('<!doctype html><h1>Trusted host fixture</h1><button id="stop">Stop</button><button id="restart">Restart</button><button id="worker-stop">Terminate host control</button><script>window.ticks=0;setInterval(()=>window.ticks++,20)</script>');
        } else if (target === runtime + runtimePath) {
          reply.writeHead(response.status, response.headers); reply.end(response.body);
        } else { blocked.push(target); reply.writeHead(403); reply.end(); }
      });
      server.on('connection', track);
      await bounded(new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); }), 3000, 'TLS fixture start');
      proxy = http.createServer((_request, reply) => { blocked.push('non-CONNECT proxy request'); reply.writeHead(403); reply.end(); });
      proxy.on('connection', track);
      proxy.on('connect', (request, socket, head) => {
        tunnels.push(request.url);
        if (!['studio.crewship.test:443', 'runtime.artifacts.test:443'].includes(request.url)) { blocked.push(request.url); socket.destroy(); return; }
        const upstream = net.connect(server.address().port, '127.0.0.1', () => {
          socket.write('HTTP/1.1 200 Connection Established\r\n\r\n'); upstream.write(head); socket.pipe(upstream); upstream.pipe(socket);
        });
        track(upstream); upstream.on('error', () => socket.destroy()); socket.on('error', () => upstream.destroy());
      });
      await bounded(new Promise((resolve, reject) => { proxy.once('error', reject); proxy.listen(0, '127.0.0.1', resolve); }), 3000, 'proxy fixture start');
      return { proxy: `http://127.0.0.1:${proxy.address().port}`, requests, blocked, tunnels, close };
    } catch (error) { await close(); throw error; }
  })();
}

async function iframeChecks(page, result) {
  result.iframePhases = [];
  const phase = name => { result.iframePhase = name; result.iframePhases.push({ name, nodeTime: Date.now() }); };
  phase('host-navigation');
  await page.goto(studio, { timeout: 5000 });
  phase('mount-real-bootstrap');
  await page.evaluate(({ runtimeURL }) => {
    window.events = []; window.framesCreated = 0; window.oldPortClosed = false;
    const fresh = `window.__crewshipPagesPort.addEventListener('message',e=>{if(e.data?.type==='probe')window.__crewshipPagesPort.postMessage({type:'fresh-probe',id:e.data.id})});window.__crewshipPagesPort.postMessage({type:'fresh-executed'});`;
    window.mount = javascript => {
      const frame = document.createElement('iframe'); frame.sandbox = 'allow-scripts'; frame.src = runtimeURL;
      window.framesCreated++;
      frame.onload = () => {
        const pair = new MessageChannel(); window.activePort = pair.port1;
        pair.port1.onmessage = event => window.events.push(event.data); pair.port1.start();
        frame.contentWindow.postMessage({ type: 'crewship.pages.boot/v1', artifact: { format: 'crewship-page-preview/v1', css: '', javascript } }, '*', [pair.port2]);
      };
      document.body.append(frame);
    };
    document.querySelector('#stop').onclick = () => {
      window.oldPort = window.activePort; window.oldPort.close(); window.oldPortClosed = true;
      document.querySelector('iframe').remove();
    };
    document.querySelector('#restart').onclick = () => window.mount(fresh);
    // Finite author code proves bootstrap/nonce/parent/port admission before
    // runaway is requested. No alternate bootstrap or capability is installed.
    window.mount(`const port=window.__crewshipPagesPort;
      port.addEventListener('message',event=>{
        if(event.data?.type==='phase-probe'&&event.data.id==='stop-spike-ready')port.postMessage({type:'phase-ack',id:event.data.id});
        if(event.data?.type==='start-loop'&&event.data.id==='stop-spike-loop'){port.postMessage({type:'iframe-loop-started'});while(true){}}
      });
      port.postMessage({type:'finite-canary-ready'});`);
  }, { runtimeURL: runtime + runtimePath });
  phase('await-finite-canary');
  await bounded(page.waitForFunction(() => window.events.some(event => event.type === 'finite-canary-ready'), undefined, { timeout: 2000 }), 2500, 'finite canary');
  result.finiteCanary = { admitted: true };
  phase('await-independent-port-ack');
  await page.evaluate(() => window.activePort.postMessage({ type: 'phase-probe', id: 'stop-spike-ready' }));
  await bounded(page.waitForFunction(() => window.events.some(event => event.type === 'phase-ack' && event.id === 'stop-spike-ready'), undefined, { timeout: 2000 }), 2500, 'port phase ACK');
  result.finiteCanary.portAcknowledged = true;
  phase('issue-separate-start-loop');
  // The loop still starts synchronously in the child message handler. Do not
  // replace the loop with a timer or count its scheduling as actual execution.
  await bounded(page.evaluate(() => window.activePort.postMessage({ type: 'start-loop', id: 'stop-spike-loop' })), 1500, 'host loop dispatch');
  phase('await-actual-loop-marker');
  await page.waitForFunction(() => window.events.some(event => event.type === 'iframe-loop-started'), undefined, { timeout: 3000 });
  result.checks.push('iframe-loop-actually-started');
  phase('host-response-during-loop');
  const ticks = await page.evaluate(() => window.ticks);
  await page.waitForFunction(before => window.ticks > before + 5, ticks, { timeout: 2000 });
  result.checks.push('host-responsive-during-iframe-loop');
  const child = page.frames().find(frame => frame.parentFrame());
  assert.ok(child, 'Expected actual runtime frame');
  let detached = false; page.on('framedetached', frame => { if (frame === child) detached = true; });
  phase('click-stop');
  await page.locator('#stop').click({ timeout: 2000 });
  assert.equal(detached, true, 'Browser did not report context detachment');
  assert.equal(child.isDetached(), true);
  assert.equal(await page.locator('iframe').count(), 0);
  assert.equal(await page.evaluate(() => window.oldPortClosed), true);
  result.checks.push('stop-detaches-context-and-closes-old-port');
  phase('restart-and-probe');
  await page.locator('#restart').click({ timeout: 2000 });
  await page.waitForFunction(() => window.events.some(event => event.type === 'fresh-executed'), undefined, { timeout: 3000 });
  await page.evaluate(() => {
    window.oldPort.postMessage({ type: 'probe', id: 'stale' });
    window.activePort.postMessage({ type: 'probe', id: 'fresh' });
  });
  await page.waitForFunction(() => window.events.some(event => event.type === 'fresh-probe' && event.id === 'fresh'), undefined, { timeout: 3000 });
  await new Promise(resolve => setTimeout(resolve, 150));
  assert.equal(await page.evaluate(() => window.events.some(event => event.id === 'stale')), false);
  assert.equal(await page.evaluate(() => window.framesCreated), 2);
  result.checks.push('restart-executes-fresh-context-and-stale-port-has-no-response');
  const after = await page.evaluate(() => window.ticks);
  await page.waitForFunction(before => window.ticks > before + 5, after, { timeout: 2000 });
  result.checks.push('host-responsive-after-stop-and-restart');
  result.iframeObservations = await page.evaluate(() => ({ events: window.events, oldPortClosed: window.oldPortClosed, framesCreated: window.framesCreated }));
  await page.evaluate(() => { window.activePort.close(); document.querySelector('iframe').remove(); });
  phase('iframe-lifecycle-complete');
}

async function workerControl(page, context, result) {
  // This trusted-host control deliberately permits a worker; author CSP is unchanged.
  const registered = new Set(); result.fulfilledHostWorkerURLs = [];
  await context.exposeBinding('registerHostWorker', ({ frame }, url) => {
    assert.equal(frame.url(), `${studio}/`);
    assert.ok(typeof url === 'string' && url.startsWith(`blob:${studio}/`) && /^[a-f0-9-]{36}$/i.test(url.slice(`blob:${studio}/`.length)));
    assert.equal(registered.size, 0); registered.add(url);
  });
  await context.route('blob:**', route => {
    const url = route.request().url();
    if (!registered.has(url) || result.fulfilledHostWorkerURLs.length) { result.unexpected.push(url); return route.abort(); }
    result.fulfilledHostWorkerURLs.push(url);
    return route.fulfill({ status: 200, contentType: 'text/javascript', body: 'postMessage("host-worker-loop-started");while(true){}' });
  });
  const created = page.waitForEvent('worker', { timeout: 3000 });
  await page.evaluate(async () => {
    const url = URL.createObjectURL(new Blob(['postMessage("host-worker-loop-started");while(true){}'], { type: 'text/javascript' }));
    await window.registerHostWorker(url); window.hostWorkerURL = url;
    window.hostWorker = new Worker(url); window.hostWorkerMessage = null;
    window.hostWorker.onmessage = event => { window.hostWorkerMessage = event.data; };
    document.querySelector('#worker-stop').onclick = () => { window.hostWorker.terminate(); URL.revokeObjectURL(window.hostWorkerURL); };
  });
  const worker = await created;
  await page.waitForFunction(() => window.hostWorkerMessage === 'host-worker-loop-started', undefined, { timeout: 3000 });
  result.checks.push('host-owned-worker-loop-actually-started');
  const ticks = await page.evaluate(() => window.ticks);
  await page.waitForFunction(before => window.ticks > before + 5, ticks, { timeout: 2000 });
  const closed = bounded(new Promise(resolve => worker.once('close', resolve)), 3000, 'Worker close event');
  await page.locator('#worker-stop').click({ timeout: 2000 });
  await closed;
  result.hostOwnedWorker = { startMarker: 'host-worker-loop-started', browserCloseEvent: true,
    delivery: result.fulfilledHostWorkerURLs.length ? 'synthetic-local-route' : 'native-local-blob' };
  result.checks.push('host-owned-worker-terminate-closes-browser-worker-context');
}

let failed = false;
for (const [name, engine] of [['firefox', firefox], ['webkit', webkit]]) {
  const result = { browser: name, checks: [], unexpected: [], status: 'not-run' }; report.browsers.push(result);
  let lab, server, browser, watchdog, page;
  try {
    lab = await fixture();
    server = await engine.launchServer({ headless: true, timeout: 10000 });
    ownBrowsers.add(server);
    watchdog = setTimeout(() => { result.watchdogKilled = true; void server.kill(); }, 25000);
    browser = await engine.connect(server.wsEndpoint(), { timeout: 5000 });
    result.browserVersion = browser.version();
    const context = await browser.newContext({ proxy: { server: lab.proxy }, ignoreHTTPSErrors: true, storageState: { cookies: [], origins: [] }, serviceWorkers: 'block' });
    await context.route('**/*', route => {
      if ([`${studio}/`, runtime + runtimePath].includes(route.request().url())) return route.continue();
      result.unexpected.push(route.request().url()); return route.abort();
    });
    page = await context.newPage(); page.setDefaultTimeout(3000);
    result.pageErrors = [];
    page.on('pageerror', error => { if (result.pageErrors.length < 8) result.pageErrors.push(String(error.message).slice(0, 512)); });
    result.userAgent = await page.evaluate(() => navigator.userAgent);
    await bounded(iframeChecks(page, result), 14000, 'iframe lifecycle');
    await bounded(workerControl(page, context, result), 7000, 'host worker control');
    assert.deepEqual(result.unexpected, []); assert.deepEqual(lab.blocked, []);
    assert.equal(result.watchdogKilled, undefined);
    result.status = 'pass';
  } catch (error) {
    failed = true; result.status = browser ? 'fail' : 'environment-blocked'; result.error = String(error.message);
    if (page) {
      // A Node-owned timer remains independent of the page event loop. Failure
      // to answer is a channel observation, not proof of OS/process CPU state.
      try {
        const observation = await bounded(page.evaluate(() => ({ ticks: window.ticks, events: window.events ?? [], framesCreated: window.framesCreated })), 1000, 'post-failure host probe');
        result.failureHostProbe = { status: 'responsive', observation };
      } catch (probeError) {
        result.failureHostProbe = { status: 'unresponsive-or-disconnected', error: String(probeError.message) };
      }
    }
  }
  finally {
    clearTimeout(watchdog);
    result.assertionCount = result.checks.length;
    if (lab) { result.requests = lab.requests; result.blocked = lab.blocked; result.tunnels = lab.tunnels; }
    try { if (server) { await bounded(server.kill(), 3000, 'own browser cleanup'); ownBrowsers.delete(server); } } catch (error) { failed = true; result.status = 'fail'; result.cleanupError = error.message; }
    try { if (lab) await bounded(lab.close(), 3000, 'own TLS cleanup'); } catch (error) { failed = true; result.status = 'fail'; result.cleanupError = error.message; }
  }
}
console.log(JSON.stringify(report, null, 2));
process.exitCode = failed ? 1 : 0;
