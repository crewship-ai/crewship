// Local-only browser contract using the real Go bootstrap and Docker artifact.
// No live Studio instance or credentials are used.
import { chromium, firefox, webkit } from 'playwright'
import { createServer } from 'node:http'
import { readFile, mkdtemp, rm } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const artifact = JSON.parse(await readFile(process.argv[2] ?? process.env.CREWSHIP_TEST_PAGE_ARTIFACT_OUT ?? '/tmp/pages-p2-artifact.json', 'utf8'))
let forbiddenRequests = 0, runtimeURL = ''
const server = createServer((req, res) => {
  if (req.url === '/probe') forbiddenRequests++
  res.setHeader('Content-Type', 'text/html')
  res.setHeader('Content-Security-Policy', `default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src ${new URL(runtimeURL).origin}; frame-ancestors 'none'; base-uri 'self'; form-action 'self'`)
  res.setHeader('Cross-Origin-Embedder-Policy', 'credentialless')
  res.end('<!doctype html><html><body><h1>Studio host</h1></body></html>')
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = `http://${process.env.CREWSHIP_TEST_STUDIO_HOST ?? "127.0.0.1"}:${server.address().port}`
const directory = await mkdtemp(join(tmpdir(), 'pages-runtime-browser-'))
const runtimeFile = join(directory, 'runtime-url')
const harnessCommand = process.env.CREWSHIP_TEST_RUNTIME_BINARY ?? 'go'
const harnessArgs = process.env.CREWSHIP_TEST_RUNTIME_BINARY ? ['-test.run=^TestServeRuntimeBrowserHarness$'] : ['test', './internal/pagebuild', '-run', '^TestServeRuntimeBrowserHarness$', '-count=1']
const harness = spawn(harnessCommand, harnessArgs, { env: { ...process.env, GOMAXPROCS: '2', CREWSHIP_TEST_RUNTIME_FILE: runtimeFile, CREWSHIP_TEST_STUDIO_ORIGIN: origin }, stdio: ['ignore', 'pipe', 'pipe'] })
let harnessLog = ''
harness.stdout.on('data', data => { harnessLog += data })
harness.stderr.on('data', data => { harnessLog += data })
const harnessExit = new Promise(resolve => harness.on('exit', code => resolve(code)))
let browser
try {
  for (let i = 0; i < 150; i++) {
    try {
      runtimeURL = await readFile(runtimeFile, 'utf8')
      if (process.env.CREWSHIP_TEST_RUNTIME_HOST) { const url = new URL(runtimeURL); url.hostname = process.env.CREWSHIP_TEST_RUNTIME_HOST; runtimeURL = url.href }
      break
    } catch { await new Promise(resolve => setTimeout(resolve, 100)) }
  }
  if (!runtimeURL) throw new Error(`Bootstrap harness did not start: ${harnessLog}`)
  const engine = { chromium, firefox, webkit }[process.env.CREWSHIP_TEST_BROWSER ?? 'chromium']
  if (!engine) throw new Error('Unsupported test browser')
  browser = await engine.launch({ headless: true })
  const page = await browser.newPage()
  await page.goto(origin)
  async function mount(artifact) {
    await page.evaluate(({ artifact, runtimeURL }) => {
      const frame = window.document.createElement('iframe')
      frame.sandbox = 'allow-scripts'; frame.title = 'Application preview'; frame.src = runtimeURL
      frame.onload = () => {
        const channel = new MessageChannel()
        frame.contentWindow.postMessage({ type: 'crewship.pages.boot/v1', artifact }, '*', [channel.port2])
        window.previewPort = channel.port1
        window.pageRequests = []
        channel.port1.onmessage = event => {
          if (event.data?.type === 'crewship.pages.request/v1') window.pageRequests.push(event.data)
        }
        channel.port1.start()
        channel.port1.postMessage({ type: 'crewship.pages.snapshot/v1', snapshot: { slug: 'mysql', name: 'MySQL operations', panels: [{ id: 'health', title: 'Database', state: 'fresh', data: { status: 'online' }, producedAt: '2026-09-08T00:00:00Z' }] } })
      }
      window.document.body.append(frame)
    }, { artifact, runtimeURL })
  }
  await mount(artifact)
  const frame = page.frameLocator('iframe')
  await frame.getByRole('heading', { name: 'MySQL operations' }).waitFor({ timeout: 5000 })
  const child = page.frames().find(frame => frame.parentFrame())
  const boundary = await child.evaluate(async origin => {
    let parentBlocked = false, storageBlocked = false, fetchBlocked = false
    try { void parent.document.body } catch { parentBlocked = true }
    try { localStorage.setItem('escape', '1') } catch { storageBlocked = true }
    try { await fetch(origin + '/probe') } catch { fetchBlocked = true }
    return { parentBlocked, storageBlocked, fetchBlocked }
  }, origin)
  if (!Object.values(boundary).every(Boolean) || forbiddenRequests) throw new Error(`Sandbox boundary failed: ${JSON.stringify(boundary)}, requests=${forbiddenRequests}`)
  if (process.env.CREWSHIP_TEST_PAGE_ACTIONS === '1') {
    await frame.getByRole('button', { name: 'Run check', exact: true }).click()
    await page.waitForFunction(() => window.pageRequests.length === 1)
    const request = await page.evaluate(() => window.pageRequests[0])
    if (request.method !== 'runAction' || request.params.panelId !== 'health' || request.params.actionId !== 'refresh' || !request.params.idempotencyKey) throw new Error('SDK sent an invalid action contract')
    await page.evaluate(id => window.previewPort.postMessage({ type: 'crewship.pages.response/v1', id, result: { pending_id: 'my-pending' } }), request.id)
    await frame.getByRole('status').filter({ hasText: 'Queued: my-pending' }).waitFor()
    await frame.getByRole('button', { name: 'Check my run' }).click()
    await page.waitForFunction(() => window.pageRequests.length === 2)
    const status = await page.evaluate(() => window.pageRequests[1])
    if (status.method !== 'getActionStatus' || status.params.pendingId !== 'my-pending') throw new Error('SDK lost the action receipt')
    await page.evaluate(id => window.previewPort.postMessage({ type: 'crewship.pages.response/v1', id, result: { pending_id: 'my-pending', pending_status: 'fired', run_id: 'my-run', run_status: 'completed' } }), status.id)
    await frame.getByRole('status').filter({ hasText: 'my-run completed' }).waitFor()
    await frame.getByRole('button', { name: 'Load recent snapshots' }).click()
    await page.waitForFunction(() => window.pageRequests.length === 3)
    const history = await page.evaluate(() => window.pageRequests[2])
    if (history.method !== 'getPanelHistory' || history.params.panelId !== 'health' || history.params.limit !== 10) throw new Error('SDK sent invalid history bounds')
    await page.evaluate(id => window.previewPort.postMessage({ type: 'crewship.pages.response/v1', id, result: { items: [{ sequence: 1, data: {}, producedAt: '2026-09-09', state: 'ok' }], next_before: 0, publication: 1 } }), history.id)
    await frame.getByRole('status').filter({ hasText: 'History: 1 snapshots available.' }).waitFor()
    console.log('PASS: compiled SDK round-trips action, status and bounded history messages without treating a queue receipt as completion')
  }
  await page.evaluate(() => window.previewPort.postMessage({ type: 'crewship.pages.snapshot/v1', snapshot: { slug: 'mysql', name: 'Updated snapshot', panels: [] } }))
  await frame.getByRole('heading', { name: 'Updated snapshot' }).waitFor()
  await page.locator('iframe').evaluate(frame => frame.remove())
  await page.evaluate(() => {
    const stop = document.createElement('button'); stop.textContent = 'Stop broken preview'
    stop.onclick = () => document.querySelector('iframe')?.remove()
    document.body.append(stop)
  })
  const loopStarted = new Promise(resolve => page.on('console', message => { if (message.text() === 'preview-loop-started') resolve() }))
  await mount({ format: 'crewship-page-preview/v1', javascript: "console.log('preview-loop-started');while(true){}", css: '', toolchain: 'test' })
  await Promise.race([loopStarted, new Promise((_, reject) => setTimeout(() => reject(new Error('Loop never started')), 3000))])
  await page.getByRole('button', { name: 'Stop broken preview' }).click({ timeout: 2000 })
  if (!(await page.getByRole('heading', { name: 'Studio host' }).isVisible())) throw new Error('Host disappeared')
  console.log('PASS: React renders; SDK updates; DOM, storage and fetch denied; Studio removes an infinite-loop frame on a separate site')
} finally {
  await browser?.close()
  if (runtimeURL) await fetch(new URL('/__stop', runtimeURL)).catch(() => {})
  else harness.kill('SIGTERM')
  const code = await harnessExit
  await new Promise(resolve => server.close(resolve))
  await rm(directory, { recursive: true, force: true })
  if (code !== 0) throw new Error(`Bootstrap harness failed: ${harnessLog}`)
}
