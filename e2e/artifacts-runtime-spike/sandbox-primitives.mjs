// Run only through crewship-heavy, with an external timeout (README.md).
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';

const root = fileURLToPath(new URL('../../', import.meta.url));
const options = new Map();
for (let i = 2; i < process.argv.length; i += 2) {
  const name = process.argv[i], value = process.argv[i + 1];
  if (!['--modules', '--firefox-executable', '--webkit-executable'].includes(name) || !value || options.has(name)) {
    throw new Error('Expected --modules PATH and optional --firefox-executable PATH / --webkit-executable PATH');
  }
  options.set(name, value);
}
const require = createRequire(resolve(options.get('--modules') ?? resolve(root, 'node_modules'), '.spike-loader.cjs'));
const playwright = require('playwright');
const ts = require('typescript');
const sourcePaths = ['internal/pagebuild/bootstrap.js', 'lib/pages/preview-runtime.ts', 'lib/pages/runtime-support.ts'];
const sources = Object.fromEntries(await Promise.all(sourcePaths.map(async path => [path, await readFile(resolve(root, path), 'utf8')])));
// Transpile the production module without bundling or changing its channel.
// The theme import is unused by these channel-only tests; no snapshot normalizer is invoked.
const channelJS = ts.transpileModule(sources['lib/pages/preview-runtime.ts'], {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
}).outputText.replace(/^import .*from ["'].*["'];?\s*$/gm, '').replace(/\bexport /g, '') + '\nwindow.SpikeChannel = PreviewSnapshotChannel;';
const report = {
  format: 'crewship-artifacts-runtime-primitives-spike/v1',
  sourceCommit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(),
  sourceSHA256: Object.fromEntries(Object.entries(sources).map(([path, text]) => [path, createHash('sha256').update(text).digest('hex')])),
  environment: { node: process.version, platform: process.platform, arch: process.arch, playwright: require('playwright/package.json').version },
  scope: 'Local opaque-sandbox and MessagePort primitives only; not production App or runtime 1.1 acceptance',
  browsers: [],
};

async function bootstrapChecks(page, bootstrap) {
  return page.evaluate(async bootstrap => {
    const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
    const check = (value, message) => { if (!value) throw new Error(message); };
    const events = [];
    window.spikeObservations = {};
    window.spikeChecks = [];
    window.addEventListener('message', event => { if (event.data?.type === 'spike.self') events.push(event.data); });
    // A returned constructor is not proof that worker code ran. Require an
    // actual message/error outcome; silence at the deadline is a failure.
    const workerProbe = `async function observeWorker(label) {
      const proof = {constructor:'not-attempted', errorName:null, errorEvent:false, errorMessage:null, errorFilename:null, message:null, timedOut:false, cspDirective:null};
      const violation = event => { if (event.effectiveDirective === 'worker-src') proof.cspDirective = event.effectiveDirective; };
      document.addEventListener('securitypolicyviolation', violation);
      let worker, url, timer;
      await new Promise(async resolve => {
        try {
          url = URL.createObjectURL(new Blob(['postMessage("spike.worker.executed")'], {type:'text/javascript'}));
          await window.spikeRegisterLocalBlob(url, label);
          worker = new Worker(url); proof.constructor = 'returned';
          worker.onmessage = event => { proof.message = event.data; resolve(); };
          worker.onerror = event => { event.preventDefault(); proof.errorEvent = true; proof.errorMessage = event.message ?? null; proof.errorFilename = event.filename ?? null; resolve(); };
          timer = setTimeout(() => { proof.timedOut = true; resolve(); }, 1000);
        } catch (error) { proof.constructor = 'threw'; proof.errorName = error.name; resolve(); }
      });
      clearTimeout(timer);
      // CSP violation reporting can follow the worker error event.
      await new Promise(resolve => setTimeout(resolve, 100));
      worker?.terminate(); if (url) URL.revokeObjectURL(url);
      document.removeEventListener('securitypolicyviolation', violation);
      return proof;
    }`;
    window.spikeWorkerProbe = workerProbe;
    const artifact = {
      format: 'crewship-page-preview/v1', css: '',
      javascript: `${workerProbe}; (async () => {
        let parentDenied = false, storageDenied = false;
        try { parent.document.body; } catch { parentDenied = true; }
        try { localStorage.getItem('spike'); } catch { storageDenied = true; }
        fetch('data:text/plain,blocked').then(() => window.__crewshipPagesPort.postMessage({type:'spike.network', blocked:false}), () => window.__crewshipPagesPort.postMessage({type:'spike.network', blocked:true}));
        const worker = await observeWorker('negative');
        window.__crewshipPagesPort.postMessage({type:'spike.booted', parentDenied, storageDenied, worker});
      })();`,
    };
    function frame(expectedParent, selfAttempt = false, workerSource = "'none'") {
      const element = document.createElement('iframe');
      element.sandbox = 'allow-scripts';
      // No origins, hosts, servers, auth state or outgoing requests are used.
      const selfScript = selfAttempt ? `const selfChannel=new MessageChannel();selfChannel.port1.onmessage=()=>parent.postMessage({type:'spike.self'},'*');window.postMessage({type:'crewship.pages.boot/v1',artifact:${JSON.stringify(artifact)}},'*',[selfChannel.port2]);` : '';
      element.srcdoc = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'nonce-spike'; style-src 'unsafe-inline'; connect-src 'none'; worker-src ${workerSource}; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><div id="root"></div><script nonce="spike">const expectedParent=${JSON.stringify(expectedParent)};const bootNonce='spike';${bootstrap}\n${selfScript}</script>`;
      return new Promise(resolve => {
        element.onload = () => resolve(element);
        document.body.append(element);
      });
    }
    function send(element, value, portCount = 1) {
      const channels = Array.from({ length: portCount }, () => new MessageChannel());
      const received = [];
      for (const channel of channels) { channel.port1.onmessage = event => received.push(event.data); channel.port1.start(); }
      element.contentWindow.postMessage(value, '*', channels.map(channel => channel.port2));
      return { channels, received };
    }
    const child = await frame('null', true);
    const boot = { type: 'crewship.pages.boot/v1', artifact };
    const wrongType = send(child, { ...boot, type: 'invalid' });
    const wrongCount = send(child, boot, 2);
    const wrongFormat = send(child, { ...boot, artifact: { ...artifact, format: 'invalid' } });
    const wrongOrigin = await frame('mismatching-origin');
    const rejectedOrigin = send(wrongOrigin, boot);
    await delay(150);
    check(events.length === 0, 'bootstrap admitted a message from its own window instead of its parent');
    for (const rejected of [wrongType, wrongCount, wrongFormat, rejectedOrigin]) check(rejected.received.length === 0, 'bootstrap admitted invalid handshake');
    window.spikeChecks.push('wrong-source-origin-shape-denied');
    const admitted = send(child, boot);
    const deadline = Date.now() + 3000;
    while (admitted.received.length < 2 && Date.now() < deadline) await delay(10);
    const proof = admitted.received.find(value => value.type === 'spike.booted');
    window.spikeObservations.sandbox = proof ?? null;
    check(proof, 'bootstrap did not return sandbox observations');
    check(proof.parentDenied, 'opaque sandbox permitted parent DOM read');
    window.spikeChecks.push('opaque-parent-dom-denied');
    check(proof.storageDenied, 'opaque sandbox permitted localStorage read');
    window.spikeChecks.push('opaque-storage-denied');
    check(proof.worker.message === null, 'worker code executed despite worker-src none');
    check(!proof.worker.timedOut, 'worker denial unknown: deadline without an error or execution outcome');
    check((proof.worker.constructor === 'threw' && proof.worker.errorName === 'SecurityError') ||
      (proof.worker.errorEvent && proof.worker.cspDirective === 'worker-src'), 'worker rejection lacks SecurityError or CSP error evidence');
    window.spikeChecks.push('csp-worker-denied');
    check(admitted.received.some(value => value.type === 'spike.network' && value.blocked), 'CSP permitted fetch');
    window.spikeChecks.push('csp-fetch-denied');
    // Observe engine-specific opaque-origin behavior separately. WebKit can
    // reject this worker even when CSP permits blob:, which is not a bypass.
    const controlFrame = await frame('null', false, 'blob:');
    const control = send(controlFrame, { ...boot, artifact: { ...artifact,
      javascript: `${workerProbe}; observeWorker('opaque-control').then(worker => window.__crewshipPagesPort.postMessage({type:'spike.worker-control', worker}));`,
    } });
    const controlDeadline = Date.now() + 3000;
    while (!control.received.length && Date.now() < controlDeadline) await delay(10);
    const controlProof = control.received.find(value => value.type === 'spike.worker-control')?.worker;
    window.spikeObservations.opaqueWorkerControl = controlProof ?? null;
    check(controlProof && !controlProof.timedOut && (controlProof.message === 'spike.worker.executed' || controlProof.errorEvent || controlProof.constructor === 'threw'),
      'opaque worker observation has no explicit outcome');
    const second = send(child, boot);
    await delay(100);
    check(second.received.length === 0, 'bootstrap admitted a second port');
    window.spikeChecks.push('single-port-admission');
    for (const handshake of [wrongType, wrongCount, wrongFormat, rejectedOrigin, admitted, second, control]) {
      for (const channel of handshake.channels) channel.port1.close();
    }
    child.remove(); wrongOrigin.remove(); controlFrame.remove();
    return window.spikeChecks;
  }, bootstrap);
}

async function sameOriginWorkerControl(context, workerProbe) {
  const page = await context.newPage();
  // This exact synthetic loopback navigation is fulfilled without opening a
  // server or sending an HTTP request. Every other request remains aborted.
  const url = 'http://127.0.0.1:65530/artifacts-runtime-spike-control';
  try {
    await page.route(url, route => route.fulfill({
      status: 200, contentType: 'text/html',
      headers: { 'Content-Security-Policy': "default-src 'none'; script-src 'nonce-spike'; connect-src 'none'; worker-src blob:; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'" },
      body: `<!doctype html><script nonce="spike">${workerProbe}; observeWorker('same-origin-control').then(proof => window.workerControlProof = proof);</script>`,
    }));
    await page.goto(url, { timeout: 5000 });
    await page.waitForFunction(() => window.workerControlProof, undefined, { timeout: 3000 });
    return await page.evaluate(() => window.workerControlProof);
  } finally { await page.close(); }
}

async function channelChecks(page) {
  await page.addScriptTag({ content: channelJS });
  return page.evaluate(async () => {
    const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
    const check = (value, message) => { if (!value) throw new Error(message); };
    const pair = new MessageChannel();
    const received = []; pair.port2.onmessage = event => received.push(event.data); pair.port2.start();
    let calls = 0, aborted = false, finish;
    const pending = new Promise(resolve => finish = resolve);
    const old = new window.SpikeChannel(pair.port1, async (_request, signal) => {
      calls++; signal.addEventListener('abort', () => aborted = true); return pending;
    });
    const request = { type: 'crewship.pages.request/v1', id: 1, method: 'runAction', params: {} };
    pair.port2.postMessage({ ...request, method: 'arbitraryTool' });
    await delay(30);
    check(calls === 0 && received.some(value => value.error), 'unknown capability entered host handler');
    window.spikeChecks.push('unknown-rpc-denied');
    received.length = 0;
    pair.port2.postMessage(request);
    const deadline = Date.now() + 3000;
    while (!calls && Date.now() < deadline) await delay(10);
    check(calls === 1, 'known request never reached handler');
    old.close(); check(aborted, 'channel close did not abort pending request');
    window.spikeChecks.push('close-aborts-pending-handler');
    const freshPair = new MessageChannel(); let freshCalls = 0;
    const fresh = new window.SpikeChannel(freshPair.port1, async () => { freshCalls++; return 'fresh'; });
    finish('late-old-response'); pair.port2.postMessage({ ...request, id: 2 });
    await delay(100);
    check(calls === 1 && freshCalls === 0, 'closed port request affected current channel');
    window.spikeChecks.push('closed-port-request-ignored');
    check(received.length === 0, 'late response reached the closed connection');
    window.spikeChecks.push('late-old-response-suppressed');
    fresh.close(); pair.port2.close(); freshPair.port2.close();
    return ['unknown-rpc-denied', 'close-aborts-pending-handler', 'closed-port-request-ignored', 'late-old-response-suppressed'];
  });
}

let failed = false;
for (const name of ['firefox', 'webkit']) {
  const result = { browser: name, executable: options.get(`--${name}-executable`) ?? playwright[name].executablePath(), checks: [], status: 'not-run' };
  report.browsers.push(result);
  let browser, page;
  const requests = [];
  const localBlobs = new Map(), fulfilledLocalWorkers = [];
  try {
    browser = await playwright[name].launch({ headless: true, ...(options.has(`--${name}-executable`) ? { executablePath: options.get(`--${name}-executable`) } : {}) });
    result.browserVersion = browser.version();
    const context = await browser.newContext({ storageState: { cookies: [], origins: [] }, serviceWorkers: 'block' });
    await context.exposeBinding('spikeRegisterLocalBlob', ({ frame }, url, label) => {
      assert.ok(['negative', 'opaque-control', 'same-origin-control'].includes(label), 'unknown worker fixture');
      assert.equal([...localBlobs.values()].some(entry => entry.label === label), false, 'duplicate worker fixture');
      const sameOrigin = label === 'same-origin-control';
      assert.equal(frame.url(), sameOrigin ? 'http://127.0.0.1:65530/artifacts-runtime-spike-control' : 'about:srcdoc', 'unexpected worker fixture frame');
      const prefix = sameOrigin ? 'blob:http://127.0.0.1:65530/' : 'blob:null/';
      assert.ok(typeof url === 'string' && url.startsWith(prefix) && /^[0-9a-f-]{36}$/i.test(url.slice(prefix.length)), 'unexpected local Blob URL');
      assert.equal(localBlobs.has(url), false, 'duplicate Blob URL');
      localBlobs.set(url, { label, frameURL: frame.url() });
    });
    await context.route('**/*', route => {
      const url = route.request().url(), registered = localBlobs.get(url);
      if (registered) {
        assert.equal(fulfilledLocalWorkers.some(entry => entry.url === url), false, 'repeated Blob request');
        fulfilledLocalWorkers.push({ url, ...registered });
        return route.fulfill({ status: 200, contentType: 'text/javascript', body: 'postMessage("spike.worker.executed")' });
      }
      requests.push(url); return route.abort();
    });
    page = await context.newPage(); page.setDefaultTimeout(5000);
    result.userAgent = await page.evaluate(() => navigator.userAgent);
    await page.setContent('<!doctype html><title>Isolated runtime primitive spike</title><body></body>');
    result.checks.push(...await bootstrapChecks(page, sources['internal/pagebuild/bootstrap.js']));
    const workerControl = await sameOriginWorkerControl(context, await page.evaluate(() => window.spikeWorkerProbe));
    result.observations = { sameOriginWorkerControl: workerControl };
    result.checks.push(...await channelChecks(page));
    assert.equal(workerControl.message, 'spike.worker.executed', 'same-origin worker positive control must execute');
    assert.equal(workerControl.timedOut, false, 'worker control timeout is not acceptance');
    assert.equal(workerControl.errorEvent, false, 'worker control errors cannot qualify this environment');
    assert.equal(localBlobs.size, 3, 'all three worker fixtures must register before construction');
    assert.equal(fulfilledLocalWorkers.some(entry => entry.label === 'negative'), false, 'CSP-denied worker reached request routing');
    assert.equal(requests.length, 0, 'fixture attempted a network request');
    result.status = 'pass';
  } catch (error) {
    result.status = browser ? 'fail' : 'environment-blocked';
    result.error = String(error.message).split('\nCall log:')[0];
    failed = true;
  } finally {
    result.blockedRequests = requests;
    result.registeredLocalBlobs = [...localBlobs].map(([url, entry]) => ({ url, ...entry }));
    result.fulfilledLocalWorkers = fulfilledLocalWorkers;
    if (page) {
      result.observations = { ...await page.evaluate(() => window.spikeObservations ?? {}).catch(() => ({})), ...result.observations };
      result.checks = [...new Set([...await page.evaluate(() => window.spikeChecks ?? []).catch(() => []), ...result.checks])];
    }
    await browser?.close();
  }
}
console.log(JSON.stringify(report, null, 2));
process.exitCode = failed ? 1 : 0;
