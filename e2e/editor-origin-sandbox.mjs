// Local-only Chromium contract for the embedded workbook editor (ADR R1-E).
// No live instance or credentials are used. Run it through
// scripts/test-editor-origin-browser.sh.
//
// Two modes:
//   dedicated (default)  two origins on 127.0.0.1 ports, same site like a
//                        sibling subdomain. The Go harness serves the bundle,
//                        this file plays the Studio host page.
//   EDITOR_MODE=same-host the zero-config default. The Go harness is the
//                        front server of the one Studio origin: its Gate sends
//                        /_editor/ to the editor handler and proxies all other
//                        paths to the Studio stand-in in this file, with the
//                        frame-src source Crewship computes for that request.
//                        No Studio origin is configured. Attacks from a
//                        hostile bundle and from a weakened host page are
//                        negative tests.
//
// Environment:
//   PLAYWRIGHT_MODULE        absolute path of playwright's index.mjs when this
//                            checkout has no node_modules (e.g. a worktree)
//   PLAYWRIGHT_BROWSERS_PATH preferred browser cache; used only if it holds the
//                            exact Chromium headless revision this Playwright
//                            expects, otherwise known caches are searched
//   HOST_COEP=1              Studio sends COEP credentialless (production)
//   HOST_SANDBOX=1           host iframe also has sandbox="allow-scripts"
//   EDITOR_MODE=same-host    zero-config mode (always with Studio COEP), including
//                            reopening behind a proxy that sets its own cookie
//   EDITOR_PROXY=1           (same-host) the browser reaches Crewship through a
//                            reverse proxy on another host:port (localhost:<p>
//                            in front of 127.0.0.1:<q>) that forwards the Host;
//                            nothing tells Crewship that public origin
//   EDITOR_PINNED=1          (same-host, with EDITOR_PROXY) Crewship is configured
//                            with its own address as the public Studio origin:
//                            the editor must refuse the proxy's origin
import { createServer } from 'node:http'
import { readFile, mkdtemp, rm } from 'node:fs/promises'
import { existsSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { spawn } from 'node:child_process'
import { homedir, tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { pathToFileURL } from 'node:url'

// Shared hosts keep several browser caches with different revisions, and
// crewship-heavy presets PLAYWRIGHT_BROWSERS_PATH to a shared cache that may
// hold another revision. Use the first cache, the configured one first, that
// holds the exact Chromium headless revision this Playwright was released
// with, so a run never fails on a missing executable or uses another Chromium.
{
  const moduleBase = process.env.PLAYWRIGHT_MODULE ?? join(process.cwd(), 'package.json')
  const require = createRequire(createRequire(moduleBase).resolve('playwright'))
  const core = dirname(require.resolve('playwright-core/package.json'))
  const { browsers } = JSON.parse(readFileSync(join(core, 'browsers.json'), 'utf8'))
  const revision = browsers.find(b => b.name === 'chromium-headless-shell').revision
  const candidates = [process.env.PLAYWRIGHT_BROWSERS_PATH, join(homedir(), '.cache/ms-playwright')].filter(Boolean)
  const cache = candidates.find(dir => existsSync(join(dir, `chromium_headless_shell-${revision}`)))
  if (!cache) throw new Error(`Chromium headless shell ${revision} not found in ${candidates.join(', ')}; run "npx playwright install chromium-headless-shell"`)
  if (cache !== process.env.PLAYWRIGHT_BROWSERS_PATH) console.log(`using Chromium headless shell ${revision} from ${cache}`)
  process.env.PLAYWRIGHT_BROWSERS_PATH = cache
}
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : 'playwright')

const sameHost = process.env.EDITOR_MODE === 'same-host'
const viaProxy = sameHost && process.env.EDITOR_PROXY === '1'
const pinned = viaProxy && process.env.EDITOR_PINNED === '1'
const hits = { navigated: 0, probe: 0, selfnav: [], runtime: 0, api: 0, ws: 0, beacon: 0, opened: 0 }
let editor
// The origin the browser sees for Studio: this server (dedicated mode) or the
// Go front server (same-host mode).
let parentOrigin
const server = createServer((req, res) => {
  const url = new URL(req.url, 'http://x')
  if (url.pathname === '/navigated') hits.navigated++
  if (url.pathname === '/probe') hits.probe++
  if (url.pathname === '/selfnav') hits.selfnav.push(req.headers.cookie ?? '')
  const isRuntime = /runtime$/i.test(url.pathname)
  if (isRuntime) hits.runtime++
  if (url.pathname.startsWith('/api/')) hits.api++
  if (url.pathname === '/beacon') hits.beacon++
  if (url.pathname === '/opened') hits.opened++
  if (sameHost && url.pathname.toLowerCase().includes('editor')) {
    // The Go front server must never hand an editor path to Studio.
    res.statusCode = 500
    res.setHeader('Content-Type', 'text/plain; charset=utf-8')
    res.end('editor path reached Studio')
    throw new Error('editor path reached Studio: ' + req.url)
  }
  res.setHeader('Content-Type', 'text/html')
  // Studio's own session cookie, by its real name (plain HTTP spelling).
  res.setHeader('Set-Cookie', 'authjs.session-token=parent-session; Path=/; SameSite=Lax')
  if (isRuntime) {
    // A Studio-origin document that Studio may frame, like the Page runtime
    // bootstrap in a development setup where the runtime origin equals Studio. It reports
    // which origin it ended up in and what it could reach.
    res.setHeader('Content-Security-Policy', `script-src 'unsafe-inline'; frame-ancestors ${parentOrigin}`)
    res.end(`<!doctype html><script>
      const r = { type: 'runtime-probe', origin: self.origin }
      try { r.parentText = parent.document.querySelector('h1').textContent } catch (e) { r.parentText = 'blocked:' + e.name }
      try { r.cookie = document.cookie } catch (e) { r.cookie = 'blocked:' + e.name }
      parent.postMessage(r, '*')
    </script>`)
    return
  }
  if (url.pathname.startsWith('/api/')) {
    res.setHeader('Content-Type', 'application/json')
    res.end('{"user":"secret"}')
    return
  }
  if (sameHost ? url.searchParams.get('coep') !== '0' : process.env.HOST_COEP === '1') res.setHeader('Cross-Origin-Embedder-Policy', 'credentialless')
  // ?frameself=1 is a same-origin Page runtime (development): the runtime origin equals Studio, so
  // Studio's frame-src includes its own origin. Same-host configuration
  // refuses that combination; here it is the negative control.
  // Same host: the source Crewship's security headers add for this very
  // request, passed on by the Go front server.
  const editorSrc = sameHost ? req.headers['x-test-editor-frame-src'] : editor?.frameSrc
  const frameSrc = [url.searchParams.get('frameself') === '1' ? parentOrigin : '', editorSrc].filter(Boolean).join(' ') || "'none'"
  res.setHeader('Content-Security-Policy', `default-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-src ${frameSrc}; frame-ancestors 'none'`)
  res.end('<!doctype html><html><body><h1>STUDIO-SECRET</h1></body></html>')
})
server.on('upgrade', (req, socket) => { hits.ws++; socket.destroy() })
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const studioUpstream = `http://127.0.0.1:${server.address().port}`
parentOrigin = studioUpstream
const directory = await mkdtemp(join(tmpdir(), 'editor-origin-browser-'))
const file = join(directory, 'editor.json')
const command = process.env.CREWSHIP_TEST_EDITOR_BINARY ?? 'go'
const args = process.env.CREWSHIP_TEST_EDITOR_BINARY ? ['-test.run=^TestEditorOriginBrowserHarness$'] : ['test', './internal/editororigin', '-run', '^TestEditorOriginBrowserHarness$', '-count=1']
const harnessEnv = { ...process.env, CREWSHIP_TEST_EDITOR_FILE: file, CREWSHIP_TEST_STUDIO_ORIGIN: studioUpstream }
if (sameHost) Object.assign(harnessEnv, { CREWSHIP_TEST_EDITOR_MODE: 'same-host', CREWSHIP_TEST_STUDIO_UPSTREAM: studioUpstream })
if (pinned) harnessEnv.CREWSHIP_TEST_EDITOR_PINNED = '1'
// EDITOR_PROXY: a reverse proxy on another host name and port that passes
// every request through unchanged, Host included (Caddy, Traefik, nginx with
// proxy_set_header Host $host). Crewship is never told this origin.
let proxy
async function startProxy(target) {
  const { request } = await import('node:http')
  proxy = createServer((req, res) => {
    const up = request({ host: '127.0.0.1', port: target, method: req.method, path: req.url, headers: req.headers }, r => {
      res.writeHead(r.statusCode, r.rawHeaders)
      r.pipe(res)
    })
    up.on('error', () => { res.statusCode = 502; res.end() })
    req.pipe(up)
  })
  await new Promise(resolve => proxy.listen(0, '127.0.0.1', resolve))
  return `http://localhost:${proxy.address().port}`
}
const harness = spawn(command, args, { env: harnessEnv, stdio: ['ignore', 'pipe', 'pipe'] })
let log = ''
harness.stdout.on('data', d => { log += d })
harness.stderr.on('data', d => { log += d })
const exited = new Promise(resolve => harness.on('exit', resolve))
let browser
const check = (ok, what) => { if (!ok) throw new Error(what) }
// Metadata is private harness DATA, never an arbitrary HTTP destination. URLs
// are reconstructed from a checked loopback port and a closed bundle digest.
function parseEditorMetadata(raw) {
  if (raw.length > 4096) throw new Error('editor metadata exceeds its fixed schema budget')
  const value = JSON.parse(raw)
  const keys = ['digest', 'frameSrc', 'index', 'origin']
  if (!value || Array.isArray(value) || Object.keys(value).sort().join(',') !== keys.join(',') || keys.some(key => typeof value[key] !== 'string') || JSON.stringify(value) !== raw) throw new Error('invalid editor metadata schema')
  const match = /^http:\/\/127[.]0[.]0[.]1:([1-9][0-9]{0,4})$/.exec(value.origin)
  if (!match) throw new Error('invalid editor metadata origin')
  const port = Number(match[1])
  if (!Number.isInteger(port) || port < 1 || port > 65535 || String(port) !== match[1]) throw new Error('invalid editor metadata port')
  const origin = `http://127.0.0.1:${port}`
  const digest = value.digest
  if (value.origin !== origin || digest.length !== 64 || !/^[a-f0-9]{64}$/.test(digest)) throw new Error('invalid editor metadata identity')
  const index = sameHost ? `/_editor/b/${digest}/index.html` : `${origin}/b/${digest}/index.html`
  const frameSrc = sameHost ? '' : origin
  if (value.index !== index || value.frameSrc !== frameSrc) throw new Error('invalid editor metadata paths')
  return Object.freeze({ origin, port, digest, index, frameSrc })
}

// The editor document URL as the host page frames it: same host resolves
// the server's path against the page's own origin (F2 editorFrameSrc).
const editorIndex = () => new URL(editor.index, parentOrigin).href
const zeroHits = () => hits.navigated === 0 && hits.probe === 0 && hits.runtime === 0 && hits.api === 0 && hits.ws === 0 && hits.beacon === 0 && hits.opened === 0

// The host bridge contract: accept origin "null" only from the exact frame
// window, with the expected protocol and bundle digest; hand the MessagePort
// to that window (targetOrigin "*" is forced by the opaque origin and safe
// because the recipient is the verified window object).
async function mount(page, { sandbox, credentialless, timeout = 10000 }) {
  const src = `${editorIndex()}?host=authority&parent_origin=${encodeURIComponent(parentOrigin)}&v=1`
  return page.evaluate(({ src, digest, sandbox, credentialless, timeout }) => new Promise((resolve, reject) => {
    const frame = document.createElement('iframe')
    if (sandbox !== null) frame.setAttribute('sandbox', sandbox)
    if (credentialless) frame.credentialless = true
    frame.referrerPolicy = 'no-referrer'
    frame.src = src
    const timer = setTimeout(() => reject(new Error('no handshake')), timeout)
    addEventListener('message', event => {
      if (event.source !== frame.contentWindow || event.origin !== 'null') return
      if (event.data?.type !== 'gridcraft:hello' || event.data.protocol !== 1 || event.data.bundle !== digest) return
      const channel = new MessageChannel()
      channel.port1.onmessage = m => { clearTimeout(timer); window.editorPort = channel.port1; resolve(m.data) }
      frame.contentWindow.postMessage({ type: 'gridcraft:connect' }, '*', [channel.port2])
      channel.port1.postMessage({ type: 'ping', n: 1 })
    })
    document.body.append(frame)
  }), { src, digest: editor.digest, sandbox, credentialless, timeout })
}

// Mounts the production frame and checks everything a hostile bundle tries
// from inside it.
async function checkContained(page, context, consoleLines, opts, label) {
  const result = await mount(page, opts)
  const r = result.results
  console.log(label, JSON.stringify(r))
  check(result.type === 'gridcraft:echo' && result.got?.n === 1, 'MessagePort round trip failed')
  check(r.origin === 'null', 'editor document is not in an opaque origin')
  check(r.cookieRead.startsWith('blocked:SecurityError'), 'editor could read cookies')
  check(r.cookieWrite.startsWith('blocked:SecurityError'), 'editor could set cookies')
  check(r.storage.startsWith('blocked:'), 'editor could use storage')
  check(r.wasm === 'ok', `wasm did not load: ${r.wasm}`)
  check(r.cssApplied, 'stylesheet <link> did not load in the sandbox')
  check(r.imgLoaded, '<img> did not load in the sandbox')
  check(r.classicRan, 'classic <script src> did not run in the sandbox')
  check(r.fetchParent === 'blocked', 'editor reached the parent origin with fetch')
  check(r.fetchApi === 'blocked', 'editor reached the Studio API with credentials')
  check(r.serviceWorker.startsWith('blocked:'), `editor registered a service worker: ${r.serviceWorker}`)
  check(r.popup.startsWith('blocked'), 'editor opened a popup')
  await page.waitForTimeout(500)
  check(page.url().startsWith(parentOrigin + '/') && !page.url().includes('navigated'), `top window navigated to ${page.url()}`)
  check(zeroHits(), `forbidden requests reached Studio: ${JSON.stringify(hits)}`)
  const cookies = await context.cookies(parentOrigin)
  check(cookies.length === 1 && cookies[0].name === 'authjs.session-token' && cookies[0].value === 'parent-session', `parent cookie jar changed: ${JSON.stringify(cookies)}`)
  check(!consoleLines.some(l => /Refused to (execute|load|apply)/.test(l)), `CSP refused a bundle resource: ${consoleLines.join('\n')}`)
}

async function freshPage(path = '/') {
  const context = await browser.newContext()
  const page = await context.newPage()
  const consoleLines = []
  page.on('console', m => consoleLines.push(m.text()))
  await page.goto(parentOrigin + path)
  return { context, page, consoleLines }
}

// Waits for a framed Studio document to report, or for nothing to happen.
function runtimeReport(page, ms, expectedOrigin) {
  return page.evaluate(({ ms, expectedOrigin }) => new Promise(resolve => {
    const frame = document.querySelector('iframe')
    if (!frame?.contentWindow) throw new Error('runtime probe requires the controlled frame')
    const source = frame.contentWindow
    const finish = value => { clearTimeout(timer); removeEventListener('message', onMessage); resolve(value) }
    const onMessage = event => {
      if (event.source !== source || event.origin !== expectedOrigin || event.data?.type !== 'runtime-probe') return
      finish(event.data)
    }
    const timer = setTimeout(() => finish(null), ms)
    addEventListener('message', onMessage)
  }), { ms, expectedOrigin })
}

try {
  for (let i = 0; i < 300 && !editor; i++) {
    try { editor = parseEditorMetadata(await readFile(file, 'utf8')) } catch (error) {
      if (error.code !== 'ENOENT' && !(error instanceof SyntaxError)) throw error
      await new Promise(r => setTimeout(r, 100))
    }
  }
  check(editor, `editor harness did not start: ${log}`)
  if (sameHost) parentOrigin = viaProxy ? await startProxy(editor.port) : editor.origin
  // Same host: the server names the editor by path only; the host page
  // resolves it against its own origin (what the browser shows), whatever
  // Host or scheme a proxy presents to Crewship.
  check(sameHost ? editor.index === `/_editor/b/${editor.digest}/index.html` : editor.index.startsWith(`${editor.origin}/b/`), `unexpected editor URL ${editor.index}`)
  if (sameHost) {
    const studioPolicy = (await fetch(parentOrigin + '/')).headers.get('content-security-policy') ?? ''
    const frameSrc = studioPolicy.split(';').map(d => d.trim()).find(d => d.startsWith('frame-src'))
    console.log(`Studio at ${parentOrigin}${viaProxy ? ` (proxy in front of ${editor.origin})` : ''}: ${frameSrc}`)
    check(frameSrc === (pinned ? "frame-src 'none'" : `frame-src ${parentOrigin}/_editor/b/`), `Studio frame-src does not follow the request origin: ${frameSrc}`)
  } else {
    check(editor.frameSrc === editor.origin, `unexpected frame-src source ${editor.frameSrc}`)
  }
  browser = await chromium.launch({ headless: true })

  if (pinned) {
    // Fail closed: Crewship is configured with another public origin than
    // the one the browser uses. The editor must not load, and its server
    // answers 421 rather than a policy for a host Studio is not on.
    const { context, page } = await freshPage()
    const statuses = []
    page.on('response', r => { if (r.url().startsWith(editorIndex())) statuses.push(r.status()) })
    const outcome = await mount(page, { sandbox: 'allow-scripts', credentialless: true, timeout: 3000 }).then(() => 'handshake', e => (e.message.includes('no handshake') ? 'no handshake' : e.message))
    // Studio's frame-src names no editor here, so the browser does not even
    // request the frame; the server refuses the editor path on its own too.
    const direct = await fetch(editorIndex(), { headers: { 'Sec-Fetch-Dest': 'iframe', 'Sec-Fetch-Site': 'same-origin' } })
    console.log('configured origin, browser on another origin:', JSON.stringify({ outcome, statuses, direct: direct.status }))
    check(outcome === 'no handshake' && direct.status === 421, `editor loaded on an origin other than the configured one: ${outcome} ${direct.status}`)
    await context.close()
    console.log('PASS: with a configured Studio origin the editor refuses (421) a browser that reaches Crewship on another origin')
  }

  if (!sameHost) {
    const hostSandbox = process.env.HOST_SANDBOX === '1' ? 'allow-scripts' : null
    for (const credentialless of process.env.HOST_COEP === '1' ? [true] : [false, true]) {
      const { context, page, consoleLines } = await freshPage()
      await checkContained(page, context, consoleLines, { sandbox: hostSandbox, credentialless }, `credentialless=${credentialless}`)
      // With frame-src limited to the editor, the frame cannot navigate itself
      // to Studio at all.
      const before = hits.selfnav.length
      await page.evaluate(() => window.editorPort.postMessage({ navigate: '/selfnav' }))
      await page.waitForTimeout(1000)
      check(hits.selfnav.length === before, `frame navigated itself to Studio: ${JSON.stringify(hits.selfnav)}`)
      await context.close()
    }
    console.log('PASS: sandboxed editor runs inline bootstrap by hash, loads wasm, CSS, image and classic script, talks over MessagePort; no cookie read/write, storage, service worker, popup, parent or API fetch, top or Studio navigation')
  }

  // Dedicated mode with a same-origin Page runtime: Studio's frame-src includes its own origin, so the
  // frame may navigate itself to a framable Studio-origin document. The
  // server's CSP sandbox does not carry over to the new document; only the
  // iframe attribute does. The mandatory host attributes keep it opaque;
  // adding allow-same-origin (the negative control) lets it read Studio.
  if (!sameHost && process.env.HOST_COEP !== '1') {
    for (const [sandbox, contained] of [['allow-scripts', true], ['allow-scripts allow-same-origin', false]]) {
      const { context, page } = await freshPage('/?frameself=1')
      await mount(page, { sandbox, credentialless: true })
      const probe = runtimeReport(page, 5000, contained ? 'null' : parentOrigin)
      await page.evaluate(() => window.editorPort.postMessage({ navigate: '/runtime' }))
      const p = await probe
      check(p, 'runtime probe did not report')
      console.log(`frame-src includes Studio, sandbox="${sandbox}" credentialless:`, JSON.stringify(p))
      if (contained) {
        check(p.origin === 'null', `self-navigated frame took the Studio origin: ${p.origin}`)
        check(p.parentText.startsWith('blocked:SecurityError'), 'self-navigated frame read the Studio DOM')
        check(p.cookie.startsWith('blocked:SecurityError'), 'self-navigated frame reached cookies')
      } else {
        // Negative control: proves this test detects a host that adds
        // allow-same-origin, which ADR §4.2 forbids.
        check(p.origin === parentOrigin && p.parentText === 'STUDIO-SECRET', `negative control did not escape (${JSON.stringify(p)}); the test no longer guards the iframe attribute`)
      }
      await context.close()
    }
    console.log('PASS: with Studio in frame-src, sandbox="allow-scripts" + credentialless keeps a self-navigated frame opaque; allow-same-origin would let it read Studio')
  }

  if (sameHost && !pinned) {
    // 1. Production frame on the Studio origin under Studio's COEP.
    {
      const { context, page, consoleLines } = await freshPage()
      await checkContained(page, context, consoleLines, { sandbox: 'allow-scripts', credentialless: true }, 'same-host production frame')
      // Studio must never run the foreign code in its own origin, even with
      // the bundle warm in the browser cache: the server refuses same-origin
      // (credentialed) asset requests and varies on those headers.
      const studioLoad = await page.evaluate(async base => {
        const out = {}
        out.classic = await new Promise(resolve => {
          const s = document.createElement('script')
          s.src = base + 'classic.js'
          s.onload = () => resolve('ran:' + (window.classicRan === true))
          s.onerror = () => resolve('refused')
          document.head.append(s)
        })
        try { await import(base + 'app.js'); out.module = 'imported' } catch (e) { out.module = 'refused' }
        out.fetch = (await fetch(base + 'app.js')).status
        return out
      }, editorIndex().replace(/index\.html$/, ''))
      console.log('Studio-context load of editor code:', JSON.stringify(studioLoad))
      check(studioLoad.classic === 'refused' && studioLoad.module === 'refused' && studioLoad.fetch === 403, `Studio loaded the editor bundle into its own origin: ${JSON.stringify(studioLoad)}`)
      // Path confusion seen from Studio itself (credentialed, same-origin):
      // every spelling under the prefix ends in the editor handler, never in
      // Studio's API, and nothing redirects.
      const confusion = await page.evaluate(async origin => {
        const out = {}
        for (const p of ['/_editor/b/..%2f..%2fapi/v1/auth/session', '/_editor/b/..%5c..%5capi/v1/auth/session', '/_editor%2fb%2f..%2f..%2fapi/v1/auth/session', '/%5Feditor/b/x/index.html', '/_EDITOR/b/x/index.html', '/_editor', '/_editor//api/v1/auth/session']) {
          const r = await fetch(origin + p, { redirect: 'manual' })
          out[p] = r.status + ' ' + (r.headers.get('content-security-policy') ?? '').endsWith('sandbox allow-scripts')
        }
        return out
      }, parentOrigin)
      console.log('path confusion:', JSON.stringify(confusion))
      check(Object.values(confusion).every(v => v === '404 true'), `prefix spelling escaped the editor handler: ${JSON.stringify(confusion)}`)
      check(hits.api === 0, `path confusion reached the Studio API: ${hits.api}`)
      await context.close()
    }
    console.log('PASS: same-host editor is opaque and fully functional; no cookie, storage, service worker, popup, top navigation, Studio API fetch, WebSocket or beacon; Studio cannot load its code; prefix spellings stay in the editor handler')

    // 2. Self-navigation to Studio documents. Studio's frame-src names only
    // the bundle path, so this holds even when the host page drops the
    // sandbox attribute or adds allow-same-origin: the frame can only ever
    // hold documents the editor handler sandboxes itself.
    const targets = ['/runtime', '/_editor/b/../../runtime', '/_editor/b/%2e%2e/%2e%2e/runtime', '/_editor/b/..%2f..%2fruntime', '/_editor/b/..%5c..%5cruntime', '/_EDITOR/../runtime', '//runtime']
    for (const sandbox of ['allow-scripts', null, 'allow-scripts allow-same-origin']) {
      for (const target of targets) {
        const { context, page } = await freshPage()
        await mount(page, { sandbox, credentialless: true })
        const probe = runtimeReport(page, 1500, sandbox === 'allow-scripts' ? 'null' : parentOrigin)
        await page.evaluate(t => window.editorPort.postMessage({ navigate: t }), target)
        const p = await probe
        check(p === null && hits.runtime === 0, `sandbox=${JSON.stringify(sandbox)} self-navigation to ${target} reached a Studio document: ${JSON.stringify(p)}`)
        await context.close()
      }
    }
    console.log(`PASS: self-navigation to Studio (${targets.length} spellings) blocked with sandbox="allow-scripts", with the attribute stripped and with allow-same-origin added`)

    // 3. Negative control: if Studio listed its own whole origin in frame-src
    // (a same-origin Page runtime, which same-host configuration refuses),
    // a host page with allow-same-origin would let the frame read Studio.
    {
      const { context, page } = await freshPage('/?frameself=1')
      await mount(page, { sandbox: 'allow-scripts allow-same-origin', credentialless: true })
      const probe = runtimeReport(page, 5000, parentOrigin)
      await page.evaluate(() => window.editorPort.postMessage({ navigate: '/runtime' }))
      const p = await probe
      console.log('control, frame-src = whole Studio origin + allow-same-origin:', JSON.stringify(p))
      check(p?.origin === parentOrigin && p.parentText === 'STUDIO-SECRET', `negative control did not escape (${JSON.stringify(p)}); the test no longer proves why frame-src is path-scoped`)
      hits.runtime = 0
      await context.close()
    }
    console.log('PASS: negative control escapes only when frame-src names the whole Studio origin')

    // 4. Stripped credentialless attribute: the frame would carry Studio's
    // cookies. Chromium refuses it under Studio's COEP, and the server refuses
    // it on its own (Cookie present) where COEP is missing, so the editor
    // never runs with Studio's credentials.
    for (const coep of ['1', '0']) {
      const { context, page } = await freshPage(`/?coep=${coep}`)
      const statuses = []
      page.on('response', r => { if (r.url().startsWith(editorIndex())) statuses.push(r.status()) })
      const outcome = await mount(page, { sandbox: 'allow-scripts', credentialless: false, timeout: 3000 }).then(() => 'handshake', e => (e.message.includes('no handshake') ? 'no handshake' : e.message))
      console.log(`credentialless stripped, Studio COEP=${coep}:`, outcome, JSON.stringify(statuses))
      check(outcome === 'no handshake', `editor ran without credentialless (COEP=${coep})`)
      if (coep === '0') check(statuses.length > 0 && statuses.every(s => s === 403), `server served the editor to a credentialed frame: ${JSON.stringify(statuses)}`)
      await context.close()
    }
    console.log('PASS: without the credentialless attribute the editor does not load (browser COEP and server cookie gate, each on its own)')

    // 5. S4c: a load balancer or CDN in front sets its own SameSite=None
    // cookie on editor responses (ALB stickiness, bot management). The
    // credentialless frame's ephemeral jar belongs to the whole Studio
    // document, so reopening the editor in the same tab sends it back. Only
    // Crewship's own cookies may refuse the frame; this one must not.
    await fetch(parentOrigin + '/__proxycookie?on=1')
    try {
      const { context, page } = await freshPage()
      await mount(page, { sandbox: 'allow-scripts', credentialless: true })
      await fetch(parentOrigin + '/__editorcookies')
      // Reopen with a new iframe element (another workbook Page, "open again").
      await page.evaluate(() => document.querySelectorAll('iframe').forEach(f => f.remove()))
      const reopened = await mount(page, { sandbox: 'allow-scripts', credentialless: true, timeout: 5000 }).then(r => (r.type === 'gridcraft:echo' ? 'handshake' : JSON.stringify(r)), e => e.message)
      // Reload the same element in place.
      const reloaded = await page.evaluate(timeout => new Promise(resolve => {
        const frame = document.querySelector('iframe')
        const timer = setTimeout(() => resolve('no handshake'), timeout)
        addEventListener('message', e => { if (e.source === frame.contentWindow && e.origin === 'null' && e.data?.type === 'gridcraft:hello') { clearTimeout(timer); resolve('handshake') } })
        frame.src = frame.src + '&r=2'
      }), 5000)
      const sent = await (await fetch(parentOrigin + '/__editorcookies')).json()
      console.log('S4c reopen in the same tab with a proxy cookie:', JSON.stringify({ reopened, reloaded, sent }))
      check(sent.some(c => c.includes('LBSTICKY=abc')), `the reopened frame did not send the proxy cookie; S4c is not exercised: ${JSON.stringify(sent)}`)
      check(!sent.some(c => /authjs|sidebar_state/.test(c)), `a credentialless frame sent Studio's cookies: ${JSON.stringify(sent)}`)
      check(reopened === 'handshake', `editor did not reopen in the same tab behind a cookie-setting proxy: ${reopened}`)
      check(reloaded === 'handshake', `editor did not reload in place behind a cookie-setting proxy: ${reloaded}`)
      await context.close()
    } finally {
      await fetch(parentOrigin + '/__proxycookie?on=0')
    }
    console.log('PASS: S4c a proxy cookie in the shared credentialless jar does not stop the editor from reopening or reloading in the same tab')
  }
} finally {
  await browser?.close()
  if (proxy) await new Promise(resolve => proxy.close(resolve))
  if (editor) await fetch(editor.origin + '/__stop').catch(() => {})
  else harness.kill('SIGTERM')
  await exited
  await new Promise(resolve => server.close(resolve))
  await rm(directory, { recursive: true, force: true })
}
