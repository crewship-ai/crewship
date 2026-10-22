// Local-only Chromium contract for the embedded editor origin (ADR R1-E, H1).
// Two origins on 127.0.0.1 ports: same site, like a sibling subdomain. The Go
// harness serves a sandboxed bundle; this file plays the Studio host page.
// No live instance or credentials are used.
import { createServer } from 'node:http'
import { readFile, mkdtemp, rm } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ?? 'playwright')
const hits = { navigated: 0, probe: 0, selfnav: [] }
let editor
const server = createServer((req, res) => {
  if (req.url.startsWith('/navigated')) hits.navigated++
  if (req.url.startsWith('/probe')) hits.probe++
  if (req.url.startsWith('/selfnav')) hits.selfnav.push(req.headers.cookie ?? '')
  res.setHeader('Content-Type', 'text/html')
  res.setHeader('Set-Cookie', 'sid=parent-session; Path=/; SameSite=Lax')
  res.setHeader('Content-Security-Policy', `default-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-src ${editor?.origin}; frame-ancestors 'none'`)
  if (process.env.HOST_COEP === '1') res.setHeader('Cross-Origin-Embedder-Policy', 'credentialless')
  res.end('<!doctype html><html><body><h1>Studio host</h1></body></html>')
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
try {
  for (let i = 0; i < 300 && !editor; i++) {
    try { editor = JSON.parse(await readFile(file, 'utf8')) } catch { await new Promise(r => setTimeout(r, 100)) }
  }
  check(editor, `editor harness did not start: ${log}`)
  browser = await chromium.launch({ headless: true })
  for (const credentialless of process.env.HOST_COEP === '1' ? [true] : [false, true]) {
    const context = await browser.newContext()
    const page = await context.newPage()
    const consoleLines = []
    page.on('console', m => consoleLines.push(m.text()))
    await page.goto(parentOrigin + '/')
    const src = `${editor.origin}/b/${editor.digest}/index.html?host=authority&parent_origin=${encodeURIComponent(parentOrigin)}&v=1`
    // The host bridge contract: accept origin "null" only from the exact
    // frame window, with the expected protocol and bundle digest; hand the
    // MessagePort to that window (targetOrigin "*" is forced by the opaque
    // origin and safe because the recipient is the verified window object).
    const hostSandbox = process.env.HOST_SANDBOX === '1'
    const result = await page.evaluate(({ src, digest, credentialless, hostSandbox }) => new Promise((resolve, reject) => {
      const frame = document.createElement('iframe')
      // No sandbox attribute by default: the server-set CSP sandbox must hold
      // on its own (a host bug, another browser or a direct load).
      if (hostSandbox) frame.sandbox = 'allow-scripts'
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
    }), { src, digest: editor.digest, credentialless, hostSandbox })
    const r = result.results
    console.log(`credentialless=${credentialless}`, JSON.stringify(r))
    check(result.type === 'gridcraft:echo' && result.got?.n === 1, 'MessagePort round trip failed')
    check(r.origin === 'null', 'editor document is not in an opaque origin')
    check(r.cookieRead.startsWith('blocked:SecurityError'), 'editor could read cookies')
    check(r.cookieWrite.startsWith('blocked:SecurityError'), 'editor could set cookies')
    check(r.storage.startsWith('blocked:'), 'editor could use storage')
    check(r.wasm === 'ok', `wasm did not load: ${r.wasm}`)
    check(r.fetchParent === 'blocked', 'editor reached the parent origin with fetch')
    await page.waitForTimeout(500)
    check(page.url() === parentOrigin + '/', `top window navigated to ${page.url()}`)
    check(hits.navigated === 0 && hits.probe === 0, `forbidden requests reached the parent: ${JSON.stringify(hits)}`)
    const cookies = await context.cookies(parentOrigin)
    check(cookies.length === 1 && cookies[0].name === 'sid' && cookies[0].value === 'parent-session', `parent cookie jar changed: ${JSON.stringify(cookies)}`)
    // Informational (review H1 vector 2): a frame may still navigate itself;
    // record whether that same-site request carries the parent's cookies.
    const before = hits.selfnav.length
    await page.evaluate(() => window.editorPort.postMessage('navigate-self'))
    for (let i = 0; i < 50 && hits.selfnav.length === before; i++) await page.waitForTimeout(100)
    console.log(`credentialless=${credentialless} self-navigation cookie header: ${JSON.stringify(hits.selfnav.at(-1) ?? '(no request)')}`)
    check(!consoleLines.some(l => /Refused to (execute|load) the script/.test(l)), `CSP refused a bundle script: ${consoleLines.join('\n')}`)
    await context.close()
  }
  console.log('PASS: sandboxed editor runs inline bootstrap by hash, loads wasm, talks over MessagePort; no cookie read/write, storage, parent fetch or top navigation')
} finally {
  await browser?.close()
  if (editor) await fetch(editor.origin + '/__stop').catch(() => {})
  else harness.kill('SIGTERM')
  await exited
  await new Promise(resolve => server.close(resolve))
  await rm(directory, { recursive: true, force: true })
}
