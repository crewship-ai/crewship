// Local-only Chromium contract for the embedded editor origin (ADR R1-E).
// Two origins on 127.0.0.1 ports: same site, like a sibling subdomain. The Go
// harness serves a sandboxed bundle; this file plays the Studio host page.
// No live instance or credentials are used. Run it through
// scripts/test-editor-origin-browser.sh.
//
// Environment:
//   PLAYWRIGHT_MODULE        absolute path of playwright's index.mjs when this
//                            checkout has no node_modules (e.g. a worktree)
//   PLAYWRIGHT_BROWSERS_PATH preferred browser cache; used only if it holds the
//                            exact Chromium headless revision this Playwright
//                            expects, otherwise known caches are searched
//   HOST_COEP=1              Studio sends COEP credentialless (production)
//   HOST_SANDBOX=1           host iframe also has sandbox="allow-scripts"
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
  const candidates = [process.env.PLAYWRIGHT_BROWSERS_PATH, join(homedir(), '.cache/ms-playwright'), '/mnt/dev-workspace/cache/ms-playwright'].filter(Boolean)
  const cache = candidates.find(dir => existsSync(join(dir, `chromium_headless_shell-${revision}`)))
  if (!cache) throw new Error(`Chromium headless shell ${revision} not found in ${candidates.join(', ')}; run "npx playwright install chromium-headless-shell"`)
  if (cache !== process.env.PLAYWRIGHT_BROWSERS_PATH) console.log(`using Chromium headless shell ${revision} from ${cache}`)
  process.env.PLAYWRIGHT_BROWSERS_PATH = cache
}
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : 'playwright')

const hits = { navigated: 0, probe: 0, selfnav: [] }
let editor
const server = createServer((req, res) => {
  const url = new URL(req.url, 'http://x')
  if (url.pathname === '/navigated') hits.navigated++
  if (url.pathname === '/probe') hits.probe++
  if (url.pathname === '/selfnav') hits.selfnav.push(req.headers.cookie ?? '')
  res.setHeader('Content-Type', 'text/html')
  res.setHeader('Set-Cookie', 'sid=parent-session; Path=/; SameSite=Lax')
  if (url.pathname === '/runtime') {
    // A Studio-origin document that Studio may frame, like the Page runtime
    // bootstrap on dev2 where the runtime origin equals Studio. It reports
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
  if (process.env.HOST_COEP === '1') res.setHeader('Cross-Origin-Embedder-Policy', 'credentialless')
  // ?frameself=1 is the dev2 setup: the Page runtime origin equals Studio, so
  // Studio's frame-src includes its own origin.
  const frameSrc = [url.searchParams.get('frameself') === '1' ? parentOrigin : '', editor?.origin].filter(Boolean).join(' ')
  res.setHeader('Content-Security-Policy', `default-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-src ${frameSrc}; frame-ancestors 'none'`)
  res.end('<!doctype html><html><body><h1>STUDIO-SECRET</h1></body></html>')
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const parentOrigin = `http://127.0.0.1:${server.address().port}`
const directory = await mkdtemp(join(tmpdir(), 'editor-origin-browser-'))
const file = join(directory, 'editor.json')
const command = process.env.CREWSHIP_TEST_EDITOR_BINARY ?? 'go'
const args = process.env.CREWSHIP_TEST_EDITOR_BINARY ? ['-test.run=^TestEditorOriginBrowserHarness$'] : ['test', './internal/editororigin', '-run', '^TestEditorOriginBrowserHarness$', '-count=1']
const harness = spawn(command, args, { env: { ...process.env, CREWSHIP_TEST_EDITOR_FILE: file, CREWSHIP_TEST_STUDIO_ORIGIN: parentOrigin }, stdio: ['ignore', 'pipe', 'pipe'] })
let log = ''
harness.stdout.on('data', d => { log += d })
harness.stderr.on('data', d => { log += d })
const exited = new Promise(resolve => harness.on('exit', resolve))
let browser
const check = (ok, what) => { if (!ok) throw new Error(what) }

// The host bridge contract: accept origin "null" only from the exact frame
// window, with the expected protocol and bundle digest; hand the MessagePort
// to that window (targetOrigin "*" is forced by the opaque origin and safe
// because the recipient is the verified window object).
async function mount(page, { sandbox, credentialless }) {
  const src = `${editor.origin}/b/${editor.digest}/index.html?host=authority&parent_origin=${encodeURIComponent(parentOrigin)}&v=1`
  return page.evaluate(({ src, digest, sandbox, credentialless }) => new Promise((resolve, reject) => {
    const frame = document.createElement('iframe')
    if (sandbox !== null) frame.setAttribute('sandbox', sandbox)
    if (credentialless) frame.credentialless = true
    frame.referrerPolicy = 'no-referrer'
    frame.src = src
    const timer = setTimeout(() => reject(new Error('no handshake')), 10000)
    addEventListener('message', event => {
      if (event.source !== frame.contentWindow || event.origin !== 'null') return
      if (event.data?.type !== 'gridcraft:hello' || event.data.protocol !== 1 || event.data.bundle !== digest) return
      const channel = new MessageChannel()
      channel.port1.onmessage = m => { clearTimeout(timer); window.editorPort = channel.port1; resolve(m.data) }
      frame.contentWindow.postMessage({ type: 'gridcraft:connect' }, '*', [channel.port2])
      channel.port1.postMessage({ type: 'ping', n: 1 })
    })
    document.body.append(frame)
  }), { src, digest: editor.digest, sandbox, credentialless })
}

try {
  for (let i = 0; i < 300 && !editor; i++) {
    try { editor = JSON.parse(await readFile(file, 'utf8')) } catch { await new Promise(r => setTimeout(r, 100)) }
  }
  check(editor, `editor harness did not start: ${log}`)
  browser = await chromium.launch({ headless: true })
  const hostSandbox = process.env.HOST_SANDBOX === '1' ? 'allow-scripts' : null
  for (const credentialless of process.env.HOST_COEP === '1' ? [true] : [false, true]) {
    const context = await browser.newContext()
    const page = await context.newPage()
    const consoleLines = []
    page.on('console', m => consoleLines.push(m.text()))
    await page.goto(parentOrigin + '/')
    const result = await mount(page, { sandbox: hostSandbox, credentialless })
    const r = result.results
    console.log(`credentialless=${credentialless}`, JSON.stringify(r))
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
    await page.waitForTimeout(500)
    check(page.url() === parentOrigin + '/', `top window navigated to ${page.url()}`)
    check(hits.navigated === 0 && hits.probe === 0, `forbidden requests reached the parent: ${JSON.stringify(hits)}`)
    const cookies = await context.cookies(parentOrigin)
    check(cookies.length === 1 && cookies[0].name === 'sid' && cookies[0].value === 'parent-session', `parent cookie jar changed: ${JSON.stringify(cookies)}`)
    // With frame-src limited to the editor, the frame cannot navigate itself
    // to Studio at all.
    const before = hits.selfnav.length
    await page.evaluate(() => window.editorPort.postMessage({ navigate: '/selfnav' }))
    await page.waitForTimeout(1000)
    check(hits.selfnav.length === before, `frame navigated itself to Studio: ${JSON.stringify(hits.selfnav)}`)
    check(!consoleLines.some(l => /Refused to (execute|load|apply)/.test(l)), `CSP refused a bundle resource: ${consoleLines.join('\n')}`)
    await context.close()
  }
  console.log('PASS: sandboxed editor runs inline bootstrap by hash, loads wasm, CSS, image and classic script, talks over MessagePort; no cookie read/write, storage, parent fetch, top or Studio navigation')

  // Dev2 setup: Studio's frame-src includes its own origin, so the frame may
  // navigate itself to a framable Studio-origin document. The server's CSP
  // sandbox does not carry over to the new document; only the iframe
  // attribute does. The mandatory host attributes keep it opaque; adding
  // allow-same-origin (the negative control) lets it read Studio.
  if (process.env.HOST_COEP !== '1') {
    for (const [sandbox, contained] of [['allow-scripts', true], ['allow-scripts allow-same-origin', false]]) {
      const context = await browser.newContext()
      const page = await context.newPage()
      await page.goto(parentOrigin + '/?frameself=1')
      await mount(page, { sandbox, credentialless: true })
      const probe = page.evaluate(() => new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('runtime probe did not report')), 5000)
        addEventListener('message', e => { if (e.data?.type === 'runtime-probe') { clearTimeout(timer); resolve(e.data) } })
      }))
      await page.evaluate(() => window.editorPort.postMessage({ navigate: '/runtime' }))
      const p = await probe
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
} finally {
  await browser?.close()
  if (editor) await fetch(editor.origin + '/__stop').catch(() => {})
  else harness.kill('SIGTERM')
  await exited
  await new Promise(resolve => server.close(resolve))
  await rm(directory, { recursive: true, force: true })
}
