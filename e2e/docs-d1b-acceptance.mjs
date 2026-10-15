#!/usr/bin/env node
// Real embedded application + managed document service acceptance. No D0,
// intercepted API responses, preloaded sessions or model completion fixtures.
// Run through crewship-heavy; the approved helper owns the HTTP server.
import { chromium, expect } from '@playwright/test'
import { spawn } from 'node:child_process'
import { randomBytes, randomUUID, createHash } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { request as unixRequest } from 'node:http'
import { mkdtemp, writeFile, readFile, rm, mkdir, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve, join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const script = fileURLToPath(import.meta.url)
const repo = resolve(fileURLToPath(new URL('..', import.meta.url)))
if (!process.argv.includes('--inner')) {
  const binary = resolve(process.env.D1B_ACCEPTANCE_BINARY ?? '/tmp/crewship-d1b')
  if (!(await stat(join(dirname(binary), 'crewship-sidecar')).catch(() => null))?.isFile()) throw new Error('Prepared release directory must contain crewship and adjacent crewship-sidecar; set D1B_ACCEPTANCE_BINARY to its crewship binary')
  const directory = await mkdtemp(join(tmpdir(), 'd1b-'))
  const secret = join(directory, 'token')
  await writeFile(secret, randomBytes(32).toString('hex'), { mode: 0o600 })
  const options = { authority: join(directory, 'authority.sock'), service: join(directory, 'service.sock'), token: secret }
  const report = resolve(process.env.D1B_ACCEPTANCE_OUTPUT ?? join(tmpdir(), `docs-d1b-acceptance-${Date.now()}`))
  await mkdir(report, { recursive: true, mode: 0o700 })
  const args = ['scripts/throwaway-server.sh', 'run', `d1b-${Date.now()}`, '--binary', binary,
    '--env', 'CREWSHIP_DOCUMENTS_ENABLED=true', '--env', `CREWSHIP_DOCUMENTS_AUTHORITY_SOCKET=${options.authority}`,
    '--env', `CREWSHIP_DOCUMENTS_SERVICE_SOCKET=${options.service}`, '--env', `CREWSHIP_DOCUMENTS_SERVICE_TOKEN_FILE=${options.token}`,
    '--', process.execPath, script, '--inner', JSON.stringify(options), report]
  const child = spawn('bash', args, { cwd: repo, stdio: 'inherit' })
  for (const signal of ['SIGTERM', 'SIGINT']) process.once(signal, () => child.kill(signal))
  const code = await new Promise(resolve => child.once('exit', resolve))
  await rm(directory, { recursive: true, force: true })
  process.exit(code ?? 1)
}

const options = JSON.parse(process.argv[process.argv.indexOf('--inner') + 1])
const output = process.argv[process.argv.indexOf('--inner') + 2]
const baseURL = process.env.CREWSHIP_SERVER
if (!baseURL || !/^http:\/\/127\.0\.0\.1:81\d\d$/.test(baseURL)) throw new Error('Approved throwaway helper URL required')
const binaryHash = createHash('sha256')
for await (const chunk of createReadStream(process.env.D1B_ACCEPTANCE_BINARY ?? '/tmp/crewship-d1b')) binaryHash.update(chunk)
const report = { binarySHA256: binaryHash.digest('hex'), qualification: 'Working tree binary; final release SHA requires its own rebuild and rerun', baseURL, checks: [], modelRun: { performed: false, reason: 'Not attempted: fresh own fixture has no configured model credential; runtime availability is recorded separately.' } }
const record = name => { report.checks.push(name); console.log(`PASS ${name}`) }
let serviceLog = ''
const service = spawn(process.execPath, ['tools/docs-service/service.mjs'], { cwd: repo, env: { ...process.env,
  CREWSHIP_DOCS_SOCKET: options.service, CREWSHIP_DOCS_AUTHORITY_SOCKET: options.authority, CREWSHIP_DOCS_SERVICE_TOKEN_FILE: options.token }, stdio: ['ignore', 'pipe', 'pipe'] })
service.stdout.on('data', data => { serviceLog += data })
service.stderr.on('data', data => { serviceLog += data })
const serviceExit = new Promise(resolve => service.once('exit', (code, signal) => resolve({ code, signal })))
let browser, failurePage
const routeTasks = new Set(), routeFailures = [], routeReleases = []
const diagnosticTasks = new Set()
report.failedPatches = []
const patchPattern = '**/api/v1/pages/d1b-acceptance/document/patch?*'
async function interceptPatch(page, handler) {
  await page.route(patchPattern, route => {
    const task = handler(route).catch(error => { routeFailures.push(error.message) })
    routeTasks.add(task); task.finally(() => routeTasks.delete(task))
    return task
  })
}
async function finishRoutes() { for (const release of routeReleases.splice(0)) release(); await Promise.all([...routeTasks]) }
async function moveToEnd(editor) {
  await editor.focus()
  await editor.press('Control+End', { delay: 50 })
  await expect.poll(() => editor.evaluate(element => {
    const selection = window.getSelection()
    if (!selection?.isCollapsed || !selection.rangeCount || !element.contains(selection.anchorNode)) return false
    const remaining = document.createRange()
    remaining.selectNodeContents(element)
    remaining.setStart(selection.anchorNode, selection.anchorOffset)
    return remaining.toString().length === 0
  })).toBe(true)
}
function assertRoutes() { if (routeFailures.length) throw new Error(`Transport fixture failure: ${routeFailures.join('; ')}`) }
try {
  await expect.poll(async () => { try { return (await stat(options.service)).isSocket() } catch { return false } }, { timeout: 30000 }).toBe(true)
  const serviceSecret = (await readFile(options.token, 'utf8')).trim()
  await expect.poll(() => new Promise(resolve => {
    const req = unixRequest({ socketPath: options.service, path: '/health/ready', headers: { 'X-Crewship-Docs-Service': serviceSecret } }, response => {
      let body = ''; response.on('data', chunk => { body += chunk }); response.once('end', () => { if (response.statusCode === 200) { try { report.nodeIdentity = JSON.parse(body) } catch { resolve(502); return } }; resolve(response.statusCode) })
    })
    req.once('error', () => resolve(0)); req.setTimeout(3000, () => req.destroy()); req.end()
  }), { timeout: 30000 }).toBe(200)
  record('Authenticated real Node readiness after durable Go boot recovery')
  browser = await chromium.launch({ headless: true })
  const owner = await browser.newContext({ baseURL, acceptDownloads: true })
  const colleague = await browser.newContext({ baseURL, acceptDownloads: true })
  for (const [actor, context] of [['owner', owner], ['colleague', colleague]]) context.on('response', response => {
    if (response.request().method() !== 'POST' || !new URL(response.url()).pathname.endsWith('/document/patch') || response.ok()) return
    const task = (async () => {
      const request = response.request()
      let payload
      try { payload = request.postDataJSON() } catch { payload = { malformedBody: true } }
      // This payload contains only our throwaway document's Yjs update/CAS
      // fields. Never capture request headers, cookies or browser storage.
      const diagnostic = { actor, status: response.status(), payload, body: await response.text() }
      report.failedPatches.push(diagnostic)
      console.error(`FAILED DOCUMENT PATCH ${JSON.stringify(diagnostic)}`)
    })().catch(error => { report.failedPatches.push({ actor, diagnosticError: error.message }) })
    diagnosticTasks.add(task); task.finally(() => diagnosticTasks.delete(task))
  })
  async function api(context, method, path, body, workspace) {
    const url = path + (workspace ? `${path.includes('?') ? '&' : '?'}workspace_id=${workspace}` : '')
    const response = await context.request.fetch(url, { method, data: body, headers: { Origin: baseURL, ...(workspace ? { 'X-Workspace-ID': workspace } : {}) } })
    const raw = await response.text()
    if (!response.ok()) throw new Error(`${method} ${path}: ${response.status()} ${raw}`)
    return raw ? JSON.parse(raw) : null
  }
  const password = `D1b-${randomBytes(16).toString('hex')}!`
  const boot = await api(owner, 'POST', '/api/v1/bootstrap', { full_name: 'Document Owner', email: 'owner@d1b.example.com', password })
  const workspace = boot.workspace_id
  const member = await api(owner, 'POST', `/api/v1/workspaces/${workspace}/members/provision`, { full_name: 'Document Colleague', email: 'colleague@d1b.example.com', role: 'MEMBER', access_mode: 'trusted', create_only: true }, workspace)
  const setup = new URL(member.setup_url, baseURL).searchParams.get('token')
  if (!setup) throw new Error('Own colleague setup link missing')
  await api(colleague, 'POST', '/api/v1/auth/reset', { token: setup, new_password: password })
  const csrf = await api(colleague, 'GET', '/api/auth/csrf')
  const login = await api(colleague, 'POST', '/api/auth/callback/credentials', { email: 'colleague@d1b.example.com', password, csrfToken: csrf.csrfToken, redirect: 'false' })
  if (login?.error) throw new Error('Own colleague login failed')
  report.runtime = await api(owner, 'GET', '/api/v1/system/runtime')
  const credentialMetadata = await api(owner, 'GET', '/api/v1/credentials', undefined, workspace)
  report.ownWorkspaceCredentialCount = (Array.isArray(credentialMetadata) ? credentialMetadata : credentialMetadata.credentials ?? credentialMetadata.items ?? []).length
  if (report.ownWorkspaceCredentialCount > 0) report.modelRun.reason = 'Not attempted: own credential metadata exists but no supported model runtime configuration was supplied to this browser harness.'
  record('Own production bootstrap, provision/reset/login and two independent people')
  const alice = await owner.newPage(), bob = await colleague.newPage()
  failurePage = alice
  const errors = []
  for (const page of [alice, bob]) page.on('pageerror', error => errors.push(error.message))
  await alice.goto('/pages')
  await alice.getByRole('button', { name: 'New Doc or artifact', exact: true }).first().click()
  await alice.getByLabel('Content', { exact: true }).selectOption('document')
  await alice.getByLabel('Name', { exact: true }).fill('D1b acceptance document')
  await alice.getByLabel('Page address', { exact: true }).fill('d1b-acceptance')
  await alice.getByRole('button', { name: 'Create Doc page', exact: true }).click()
  const doc = alice.getByRole('region', { name: 'Doc page', exact: true })
  await expect(doc).toBeVisible({ timeout: 30000 })
  const editor = alice.locator('[contenteditable="true"][aria-label="Document editor"]')
  await expect(editor).toBeVisible()
  await editor.fill('Shared starting paragraph')
  const saved = page => expect(page.getByRole('region', { name: 'Doc page', exact: true }).locator('[role="status"][aria-live="polite"]')).toHaveText('Saved', { timeout: 30000 })
  await saved(alice)
  const path = '/api/v1/pages/d1b-acceptance/document'
  const state = () => api(owner, 'GET', path, undefined, workspace)
  const identity = await api(owner, 'GET', '/api/v1/pages/d1b-acceptance', undefined, workspace)
  if (!identity.id) throw new Error('Production Page ID missing')
  const jobs = await api(owner, 'GET', path + '/requests', undefined, workspace)
  report.ownEligibleAgentChats = jobs.eligible_chats.length
  if (jobs.requests.length !== 0) throw new Error('Fresh own document unexpectedly contains agent jobs')
  await api(owner, 'PUT', '/api/v1/pages/d1b-acceptance/grants', { subject_type: 'user', subject: member.user_id, level: 'write' }, workspace)
  await bob.goto('/pages/d1b-acceptance')
  const editorB = bob.locator('[contenteditable="true"][aria-label="Document editor"]')
  await expect(editorB).toContainText('Shared starting paragraph', { timeout: 30000 })
  record('Create Doc from production Pages UI, human ownership, second person write grant')

  // Hold each actual update request until both independent editors have edited
  // the same saved block. Responses are never fabricated or altered.
  const barriers = []
  for (const page of [alice, bob]) await interceptPatch(page, async route => {
    if (route.request().method() === 'POST') await new Promise(resolve => { barriers.push(resolve); routeReleases.push(resolve) })
    await route.continue()
  })
  await moveToEnd(editor); await alice.keyboard.insertText(' ALPHA')
  await moveToEnd(editorB); await bob.keyboard.insertText(' BETA')
  await expect.poll(() => barriers.length, { timeout: 15000 }).toBe(2)
  await expect(doc.getByRole('status')).toHaveText('Saving…')
  await expect(bob.getByRole('region', { name: 'Doc page', exact: true }).getByRole('status')).toHaveText('Saving…')
  await finishRoutes(); assertRoutes()
  await alice.unroute(patchPattern)
  await bob.unroute(patchPattern)
  await saved(alice); await saved(bob)
  await expect(editor).toContainText('ALPHA', { timeout: 15000 }); await expect(editor).toContainText('BETA', { timeout: 15000 })
  await expect(editorB).toContainText('ALPHA', { timeout: 15000 }); await expect(editorB).toContainText('BETA', { timeout: 15000 })
  record('Two people concurrent same-block edits merge and receive durable Saved acknowledgements')

  // Commit the first real request, then drop only its reply. The retry must
  // recover the exact durable operation instead of inventing a second edit.
  let lostAttempt, retryAttempt
  const beforeLostReply = await state()
  await interceptPatch(alice, async route => {
    if (!lostAttempt) { lostAttempt = route.request().postData(); await route.fetch(); await route.abort('failed') }
    else { retryAttempt = route.request().postData(); await route.continue() }
  })
  await moveToEnd(editor); await alice.keyboard.insertText(' LOSTACK')
  await expect(doc.getByRole('status')).toHaveText('Disconnected · changes are unsaved', { timeout: 20000 })
  await saved(alice)
  await finishRoutes(); assertRoutes()
  await alice.unroute(patchPattern)
  if (!retryAttempt || retryAttempt !== lostAttempt) throw new Error('Lost-reply retry changed operation identity or payload')
  const afterLostReply = await state()
  if (afterLostReply.rev !== beforeLostReply.rev + 1) throw new Error('Lost-reply retry duplicated a durable edit')
  record('Lost acknowledgement preserves unsaved status and exact retry recovers one durable edit')

  await editor.click(); await editor.press('Control+Home', { delay: 50 }); await editor.press('Home', { delay: 50 }); await editor.press('Shift+ArrowRight', { delay: 50 }); await editor.press('Shift+ArrowRight', { delay: 50 }); await editor.press('Shift+ArrowRight', { delay: 50 })
  await doc.getByRole('button', { name: 'Comment on selected text', exact: true }).click()
  await doc.getByLabel('Comment text').fill('Please preserve this shared introduction.')
  await doc.getByRole('button', { name: 'Add comment', exact: true }).click()
  await expect(doc.getByText('Please preserve this shared introduction.', { exact: true })).toBeVisible()
  await expect(doc.getByRole('region', { name: 'Document comments' }).getByText('attached', { exact: true })).toBeVisible()
  record('Selection-anchored comment saved through production authority')

  await moveToEnd(editor); await editor.press('Enter')
  await doc.getByRole('button', { name: 'Heading 2', exact: true }).click(); await expect(editor).toBeFocused(); await alice.keyboard.insertText('Acceptance heading')
  await editor.press('Enter'); await doc.getByRole('button', { name: 'Bullet list', exact: true }).click(); await expect(editor).toBeFocused(); await alice.keyboard.insertText('Acceptance list item')
  await editor.press('Enter'); await editor.press('Enter'); await doc.getByRole('button', { name: 'Table', exact: true }).click()
  await saved(alice)
  await expect(editor).toContainText('Shared starting paragraph')
  await expect(editor).toContainText('ALPHA'); await expect(editor).toContainText('BETA'); await expect(editor).toContainText('LOSTACK')
  await expect(editor.locator('h2')).toContainText('Acceptance heading')
  await expect(editor.locator('ul')).toContainText('Acceptance list item')
  await expect(editor.locator('table')).toBeVisible()
  await doc.getByRole('button', { name: 'Save checkpoint', exact: true }).click()
  await expect(doc.getByRole('region', { name: 'Document history' })).toBeVisible()
  const checkpoints = await api(owner, 'GET', path + '/checkpoints', undefined, workspace)
  const checkpoint = (checkpoints.checkpoints ?? checkpoints)[0]
  if (!checkpoint?.id) throw new Error('Confirmed checkpoint missing')
  record('Production heading/list/table edits and durable checkpoint')

  // Human semantic API exercises the same Node target/hash CAS. This is
  // explicitly not an agent run or evidence of a model completing work.
  let current = await state()
  const target = current.outline.find(block => block.text.includes('Shared starting paragraph'))
  if (!target?.hash) throw new Error('Semantic outline/hash unavailable')
  await api(owner, 'POST', path + '/patch', { id: randomUUID(), schema: 2, epoch: current.epoch, target: target.id, text: target.text + ' AFTERCHECKPOINT', ifRev: current.rev, ifHash: target.hash }, workspace)
  await expect(editor).toContainText('AFTERCHECKPOINT', { timeout: 20000 })
  await doc.getByRole('button', { name: 'Compare', exact: true }).first().click()
  await expect(doc.getByRole('region', { name: 'Compare checkpoint' })).toBeVisible()
  record('Human semantic outline/hash CAS API edit and visible checkpoint comparison (not an agent run)')
  await doc.getByRole('button', { name: 'Restore', exact: true }).first().click()
  await alice.waitForLoadState('domcontentloaded')
  await saved(alice)
  await expect(editor).not.toContainText('AFTERCHECKPOINT')
  await expect(editor).toContainText('ALPHA'); await expect(editor).toContainText('BETA')
  const downloadPromise = alice.waitForEvent('download')
  await doc.getByRole('button', { name: 'Export Markdown', exact: true }).click()
  const download = await downloadPromise
  const exportPath = join(output, 'document.md'); await download.saveAs(exportPath)
  const markdown = await readFile(exportPath, 'utf8')
  if (!markdown.includes('ALPHA') || !markdown.includes('BETA') || markdown.includes('AFTERCHECKPOINT')) throw new Error('Export differs from restored saved state')
  await alice.reload(); await saved(alice); await expect(editor.locator('table')).toBeVisible()
  const finalIdentity = await api(owner, 'GET', '/api/v1/pages/d1b-acceptance', undefined, workspace)
  if (identity.id !== finalIdentity.id) throw new Error('Document workflow changed Page identity')
  await alice.screenshot({ path: join(output, 'document-restored.png'), fullPage: true })
  record('Compare/restore/export/reload preserve saved content and same Page identity')
  if (errors.length) throw new Error(`Browser errors: ${errors.join('; ')}`)
  report.result = 'passed'; console.log(`Model acceptance NOT PERFORMED: ${report.modelRun.reason}`)
} catch (error) {
  await finishRoutes()
  report.result = 'failed'; report.error = error.message; report.transportErrors = routeFailures
  await failurePage?.screenshot({ path: join(output, 'failure.png'), fullPage: true }).catch(() => {})
  throw error
} finally {
  await finishRoutes()
  await Promise.all([...diagnosticTasks])
  await browser?.close()
  service.kill('SIGTERM')
  let shutdownTimer
  const stopped = await Promise.race([serviceExit, new Promise(resolve => { shutdownTimer = setTimeout(() => resolve(null), 10000) })])
  clearTimeout(shutdownTimer)
  if (!stopped) { service.kill('SIGKILL'); await serviceExit }
  await writeFile(join(output, 'report.json'), JSON.stringify(report, null, 2), { mode: 0o600 })
  await writeFile(join(output, 'node-service.log'), serviceLog, { mode: 0o600 })
  console.log(`Acceptance evidence: ${output}`)
}
