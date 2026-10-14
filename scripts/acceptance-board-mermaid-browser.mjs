// Owned real HTTPS Router/DB + production UI + pinned cross-site converter.
// Synthetic authenticated sessions and admitted source; no API responses mocked.
import { readFile, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium, expect } from '@playwright/test'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_BOARD_MERMAID_FIXTURE, 'utf8'))
if (!/^https:\/\/127\.0\.0\.1:\d+$/.test(fixture.server) || !/^https:\/\/127\.0\.0\.2:\d+$/.test(fixture.runtime)) throw new Error('Owned separate-site HTTPS fixtures required')
const output = process.env.CREWSHIP_BOARD_MERMAID_OUTPUT ?? await mkdtemp(join(tmpdir(), 'crewship-board-mermaid-'))
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, args: ['--no-proxy-server'] })
const contexts = [], pages = [], results = [], converterLoads = []
const endpoint = path => `${fixture.server}/api/v1/pages/${fixture.slug}/board${path}?workspace_id=${encodeURIComponent(fixture.workspace)}`
async function check(name, condition, detail = {}) {
  results.push({ name, status: condition ? 'PASS' : 'FAIL', ...detail })
  await writeFile(join(output, 'results.json'), JSON.stringify({ execution: 'real HTTPS Router/DB, production UI, pinned isolated converter, synthetic sessions/source; no API response mocks or LLM', results }, null, 2))
  if (!condition) throw new Error(name)
}
async function readState(context) {
  const response = await context.request.get(endpoint(`/requests/${fixture.request_id}`))
  if (response.status() !== 200) throw new Error(`Job read ${response.status()}: ${await response.text()}`)
  const board = await context.request.get(endpoint(''))
  if (board.status() !== 200) throw new Error(`Board read ${board.status()}`)
  return { job: await response.json(), board: await board.json() }
}
const insertPath = `/api/v1/pages/${fixture.slug}/board/requests/${fixture.request_id}/insert`
const isInsert = url => new URL(url).pathname === insertPath
try {
  for (const token of fixture.tokens) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, ignoreHTTPSErrors: true })
    contexts.push(context)
    // The HTTPS session endpoint reads the canonical __Secure- cookie name;
    // seeding the HTTP name authenticates API middleware but leaves the UI's
    // /api/auth/session probe unauthenticated.
    await context.addCookies([{ name: '__Secure-authjs.session-token', value: token, url: fixture.server, httpOnly: true, secure: true, sameSite: 'Lax' }])
    const session = await context.request.get(`${fixture.server}/api/auth/session`)
    const identity = await session.json()
    await check(`HTTPS browser session ${contexts.length} authenticates the synthetic owner`, session.status() === 200 && identity.user?.id === fixture.user_id)
    await context.addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
    const page = await context.newPage(); pages.push(page)
    page.on('response', response => { if (response.url().startsWith(fixture.runtime) && response.url().endsWith('/board-converter/runner.js')) converterLoads.push(response.status()) })
  }
  await pages[0].goto(`${fixture.server}/pages/${fixture.slug}`)
  const work = page => page.getByRole('region', { name: 'Board agent work', exact: true })
  const insert = page => work(page).getByRole('button', { name: 'Insert diagram', exact: true })
  await expect(insert(pages[0])).toBeEnabled()
  let state = await readState(contexts[0])
  await check('ready source waits for a human without drawing geometry', state.job.status === 'ready_to_insert' && state.job.mermaid_source === fixture.source && state.board.seq === 0 && state.board.records.length === 0)
  let abortedPayload
  await pages[0].route(url => isInsert(url), async route => { abortedPayload = route.request().postDataJSON(); await route.abort('failed') })
  await insert(pages[0]).click()
  await expect(work(pages[0]).getByRole('button', { name: 'Retry insertion', exact: true })).toBeEnabled({ timeout: 20000 })
  await check('actual pinned converter prepares valid geometry before simulated client loss', abortedPayload?.upserts?.length >= 5 && abortedPayload.base_seq === 0 && converterLoads.includes(200))
  state = await readState(contexts[0])
  await check('aborted insertion before network leaves durable job ready and Board unchanged', state.job.status === 'ready_to_insert' && state.board.seq === 0 && state.board.records.length === 0)
  await pages[0].close()
  pages[0] = await contexts[0].newPage()
  pages[0].on('response', response => { if (response.url().startsWith(fixture.runtime) && response.url().endsWith('/board-converter/runner.js')) converterLoads.push(response.status()) })
  await Promise.all(pages.map(page => page.goto(`${fixture.server}/pages/${fixture.slug}`)))
  await Promise.all(pages.map(page => expect(insert(page)).toBeEnabled()))
  await check('fresh tab and second authenticated session recover the durable ready result', true)
  const waiting = [], proposals = []
  let bothReady
  const barrier = new Promise(resolve => { bothReady = resolve })
  for (const [index, page] of pages.entries()) {
    await page.route(url => isInsert(url), async route => {
      proposals[index] = route.request().postDataJSON()
      waiting[index] = route
      if (waiting.filter(Boolean).length === 2) bothReady()
      // The test releases both actual requests only after both independent UI
      // conversions reached the insertion boundary. No response is fabricated.
    })
  }
  await Promise.all(pages.map(page => insert(page).click()))
  let barrierTimer
  try { await Promise.race([barrier, new Promise((_, reject) => { barrierTimer = setTimeout(() => reject(new Error('Both converted insertion requests did not reach barrier')), 20000) })]) } finally { clearTimeout(barrierTimer) }
  await check('two sessions independently convert the same frozen source before either commit', proposals.every(value => value.base_seq === 0 && value.source_hash === state.job.source_hash && value.converter_version === '2.2.2' && value.upserts.length >= 5) && proposals[0].mutation_id !== proposals[1].mutation_id)
  const responses = pages.map(page => page.waitForResponse(response => isInsert(response.url()) && response.request().method() === 'POST'))
  await Promise.all(waiting.map(route => route.continue()))
  const received = await Promise.all(responses)
  const statuses = received.map(response => response.status()).sort()
  const receipts = await Promise.all(received.map(response => response.json()))
  await check('actual Router commits one insertion and returns the existing result to its concurrent peer', statuses[0] === 200 && statuses[1] === 201 && receipts.every(value => value.status === 'inserted' && value.result_seq === 1) && receipts.filter(value => value.already_inserted === true).length === 1, { statuses })
  await Promise.all(pages.map(page => expect(work(page).getByText('Inserted at board sequence 1', { exact: true })).toBeVisible()))
  state = await readState(contexts[0])
  const ids = state.job.inserted_element_ids
  const winningIds = proposals[received.findIndex(response => response.status() === 201)].upserts.map(value => value.element.id)
  await check('one committed geometry set has no duplicate elements or second Board sequence', state.job.status === 'inserted' && state.board.seq === 1 && ids.length >= 5 && state.board.records.length === ids.length && ids.length === winningIds.length && ids.every(id => winningIds.includes(id)) && new Set(state.board.records.map(value => value.element_id)).size === ids.length && state.board.records.every(value => ids.includes(value.element_id)))
  await Promise.all(pages.map(page => page.reload()))
  await Promise.all(pages.map(page => expect(work(page).getByText('Inserted at board sequence 1', { exact: true })).toBeVisible()))
  await Promise.all(pages.map(page => expect(insert(page)).toHaveCount(0)))
  await check('both reloaded clients retain the same saved insertion without offering another insertion', true)
  console.log(`Board Mermaid workflow passed ${results.length} checks; evidence: ${output}`)
} catch (error) {
  await writeFile(join(output, 'failure.txt'), String(error))
  for (const [index, page] of pages.entries()) await page.screenshot({ path: join(output, `tab-${index}-failure.png`) }).catch(() => {})
  throw error
} finally { await browser.close() }
