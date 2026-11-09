// Called only by TestDocumentCanvasProposalBrowser (#5149). Production frontend,
// Router, Docs engine, chat bridge, orchestrator and sidecar; container
// execution is a synthetic subprocess that calls the real doc_draft MCP tools.
// No model is called. The owner enables Canvas in the UI and places two
// formatted blocks, asks for a proposal in a private chat (the original human
// message carries the explicit schema-6 target), then accepts one proposal and
// rejects the other in the browser. Formatting and Canvas geometry are kept.
import assert from 'node:assert/strict'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { chromium } from 'playwright'
import * as Y from 'yjs'

const fixture = JSON.parse(await readFile(process.env.CREWSHIP_DOCS_CANVAS_FIXTURE, 'utf8'))
assert.match(fixture.server, /^http:\/\/127\.0\.0\.1:\d+$/)
const output = process.env.CREWSHIP_DOCS_CANVAS_OUTPUT
await mkdir(join(output, 'video'), { recursive: true })
const base = fixture.server
const q = `workspace_id=${encodeURIComponent(fixture.workspace)}`
const decode = value => Uint8Array.from(Buffer.from(value, 'base64'))
const pass = name => console.log('PASS', name)
// Handled at once so a timeout reaches the failure screenshot instead of crashing the process.
const settle = promise => { promise.catch(() => {}); return promise }
const browser = await chromium.launch({ headless: true })
let page
try {
  const viewport = { width: 1440, height: 1000 }
  const owner = await browser.newContext({ viewport, reducedMotion: 'reduce', recordVideo: { dir: join(output, 'video'), size: viewport } })
  await owner.addCookies([{ name: 'authjs.session-token', value: fixture.token, url: base, httpOnly: true, sameSite: 'Lax' }])
  await owner.addInitScript(({ workspace, user }) => localStorage.setItem(`crewship.workspaceId:${user}`, workspace), { workspace: fixture.workspace, user: fixture.owner })
  let enabled = false
  for (let i = 0; i < 100 && !enabled; i++) {
    const status = await owner.request.get(`${base}/api/v1/documents/status?${q}`)
    enabled = status.ok() && (await status.json()).enabled === true
    if (!enabled) await new Promise(r => setTimeout(r, 100))
  }
  assert.ok(enabled, 'the real Docs engine is available')

  // A personal document at the creation schema with formatted blocks.
  const created = await owner.request.post(`${base}/api/v1/pages?${q}`, { data: { name: 'Canvas proposals', slug: 'canvas-proposals', panels: [], allow_empty: true } })
  assert.equal(created.status(), 201, await created.text())
  const pageId = (await created.json()).id
  // A proposing agent needs edit access granted to it on a shared document (the UI says so too).
  const grant = await owner.request.put(`${base}/api/v1/pages/canvas-proposals/grants?${q}`, { data: { subject_type: 'agent', subject: fixture.agent, level: 'write' } })
  assert.ok(grant.status() < 300, await grant.text())
  const t = (text, ...marks) => marks.length ? { type: 'text', text, marks } : { type: 'text', text }
  const bold = { type: 'bold' }, red = { type: 'textColor', attrs: { color: 'red' } }
  const body = { type: 'doc', content: [
    { type: 'paragraph', attrs: { id: 'lead' }, content: [t('Agree the '), t('launch date', bold), t(' with '), t('sales', red), t('.')] },
    { type: 'paragraph', attrs: { id: 'other' }, content: [t('Book the venue.')] },
    { type: 'paragraph', attrs: { id: 'tail' }, content: [t('Outside the mandate.')] },
  ] }
  const init = await owner.request.post(`${base}/api/v1/pages/canvas-proposals/document/initialize?${q}`, { data: { id: crypto.randomUUID(), schema: 5, page_id: pageId, body } })
  assert.equal(init.status(), 200, await init.text())
  const read = async () => {
    const response = await owner.request.get(`${base}/api/v1/pages/canvas-proposals/document?${q}`)
    assert.equal(response.status(), 200)
    const value = await response.json()
    const doc = new Y.Doc({ gc: false })
    Y.applyUpdate(doc, decode(value.snapshot)); for (const update of value.updates ?? []) Y.applyUpdate(doc, decode(update))
    const placements = doc.getMap('surface:placements').toJSON()
    doc.destroy()
    const block = id => value.content.content.find(b => b.attrs?.id === id)
    return { value, placements, block }
  }

  // Evidence written next to the screenshots: the stored state (placements, block marks) and what the cards show.
  const evidence = async (name, state) => {
    const cards = await page.evaluate(() => {
      const world = document.querySelector('[data-canvas-world]')
      return { worldTransform: world?.style.transform ?? null, cards: Object.fromEntries([...document.querySelectorAll('[data-placement]')].map(card => {
        const box = card.getBoundingClientRect()
        return [card.dataset.placement, { style: { left: card.style.left, top: card.style.top, width: card.style.width, minHeight: card.style.minHeight }, screen: { x: Math.round(box.x), y: Math.round(box.y), w: Math.round(box.width), h: Math.round(box.height) }, text: card.textContent, bold: [...card.querySelectorAll('strong')].map(n => n.textContent), color: [...card.querySelectorAll('[data-text-color]')].map(n => [n.getAttribute('data-text-color'), n.textContent]) }]
      })) }
    })
    const stored = { rev: state.value.rev, schema: state.value.schema, placements: state.placements, blocks: { lead: state.block('lead'), other: state.block('other'), tail: state.block('tail') } }
    await writeFile(join(output, `${name}.json`), JSON.stringify({ stored, shown: cards }, null, 2))
    return cards
  }
  page = await owner.newPage()
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(`${base}/artifacts/${encodeURIComponent(pageId)}`)
  await page.getByLabel('Document editor', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Status: Live' }).waitFor({ timeout: 20000 })
  await page.getByRole('tab', { name: 'Canvas', exact: true }).click()
  const upgraded = settle(page.waitForResponse(r => r.url().includes('/document/upgrade') && r.request().method() === 'POST'))
  await page.getByRole('alertdialog').getByRole('button', { name: 'Enable Canvas' }).click()
  assert.equal((await upgraded).status(), 200)
  await page.getByRole('application', { name: 'Canvas' }).waitFor()
  assert.equal((await read()).value.schema, 6)
  pass('the owner enables Canvas in the UI (schema 6)')

  // Only the stored state counts: a later placement's update need not name its root.
  for (const [id, label] of [['lead', 'Agree the launch date with sales.'], ['other', 'Book the venue.']]) {
    const add = page.getByRole('button', { name: label, exact: true })
    if (!await add.isVisible()) await page.getByText(/^Add blocks \(\d+\)$/).click()
    await add.click()
    await page.locator(`[data-placement="${id}"]`).waitFor()
  }
  let placed
  for (let i = 0; i < 80; i++) { placed = await read(); if (Object.keys(placed.placements).length === 2) break; await new Promise(r => setTimeout(r, 250)) }
  assert.deepEqual(Object.keys(placed.placements).sort(), ['lead', 'other'], 'both placements are stored')
  await page.getByRole('button', { name: 'Status: Saved' }).waitFor({ timeout: 15000 })
  const originalLead = placed.block('lead')
  // The card shows the Page formatting: bold and the red palette colour.
  await page.locator('[data-placement="lead"] strong', { hasText: 'launch date' }).waitFor()
  await page.locator('[data-placement="lead"] [data-text-color="red"]', { hasText: 'sales' }).waitFor()
  await page.screenshot({ path: join(output, '01-canvas-placed.png') })
  const shownBefore = await evidence('01-stored-before-proposal', placed)
  for (const id of ['lead', 'other']) assert.equal(shownBefore.cards[id].style.left, `${placed.placements[id].x}px`)
  pass('two formatted blocks are placed on the Canvas')

  // The original human message in the private chat names the Doc, two blocks and schema 6.
  const target = { submission_id: 'canvas-browser-proposal', page_id: pageId, schema: 6, rev: placed.value.rev, generation: placed.value.generation, blocks: ['lead', 'other'] }
  const chat = await owner.newPage()
  await chat.goto(`${base}/`)
  await chat.evaluate(async ({ server, chat, workspace, target }) => {
    const ticket = await (await fetch(`${server}/api/v1/ws-token?workspace_id=${workspace}`)).json()
    await new Promise((resolve, reject) => {
      const socket = new WebSocket(server.replace('http:', 'ws:') + '/ws')
      const timer = setTimeout(() => { socket.close(); reject(new Error('synthetic agent proposal run timed out')) }, 90000)
      socket.onopen = () => { socket.send(JSON.stringify({ type: 'auth', token: ticket.token })); socket.send(JSON.stringify({ type: 'ping' })) }
      socket.onerror = () => reject(new Error('chat WebSocket failed'))
      socket.onmessage = event => {
        const frame = JSON.parse(event.data)
        if (frame.type === 'pong') {
          socket.send(JSON.stringify({ type: 'subscribe', channel: 'session:' + chat }))
          socket.send(JSON.stringify({ type: 'send_message', payload: { session_id: chat, content: 'Propose a sharper first paragraph and a second one.', metadata: { document_iteration_intent: target } } }))
        }
        if (frame.type === 'error' || frame.payload?.type === 'error') { clearTimeout(timer); socket.close(); reject(new Error(JSON.stringify(frame))) }
        if (frame.payload?.type === 'done') { clearTimeout(timer); socket.close(); resolve() }
      }
    })
  }, { server: base, chat: fixture.chat, workspace: fixture.workspace, target })
  await chat.close()
  const listed = await (await owner.request.get(`${base}/api/v1/pages/canvas-proposals/document/proposals?${q}`)).json()
  assert.equal(listed.proposals?.length, 2, JSON.stringify(listed))
  const unchanged = await read()
  assert.equal(unchanged.value.rev, placed.value.rev, 'proposing changes nothing')
  pass('an admitted agent run proposes twice at schema 6 through the sidecar; the document is unchanged')

  // Review in the browser: reject one, accept the other.
  await page.reload()
  await page.getByLabel('Document editor', { exact: true }).waitFor()
  await page.getByRole('button', { name: /^Status: (Live|Saved)$/ }).waitFor({ timeout: 20000 })
  const openProposals = async () => {
    await page.getByRole('button', { name: 'Proposals', exact: true }).first().click()
    const list = page.getByRole('region', { name: 'Document proposals' })
    await list.waitFor()
    return list
  }
  const openOne = async (list, text) => {
    const items = list.getByRole('button', { name: /· open · revision/ })
    await items.first().waitFor({ timeout: 15000 })
    for (let i = 0; i < await items.count(); i++) {
      await items.nth(i).click()
      const review = page.getByRole('region', { name: /^Proposal from / })
      await review.waitFor()
      if ((await review.innerText()).includes(text)) return review
    }
    throw new Error(`no open proposal shows ${text}`)
  }
  let list = await openProposals()
  let review = await openOne(list, 'Second paragraph, rejected in the browser.')
  await page.screenshot({ path: join(output, '02-proposal-to-reject.png') })
  await review.getByRole('button', { name: 'Reject proposal' }).click()
  const rejected = settle(page.waitForResponse(r => /\/proposals\/[^/]+\/reject/.test(r.url()) && r.request().method() === 'POST'))
  await review.getByRole('button', { name: 'Confirm reject' }).click()
  assert.equal((await rejected).status(), 200)
  const afterReject = await read()
  assert.equal(afterReject.value.rev, placed.value.rev, 'rejecting writes nothing')
  assert.deepEqual(afterReject.block('other'), placed.block('other'))
  pass('reject in the browser closes the proposal and writes nothing')

  review = await openOne(list, 'by Friday')
  await page.screenshot({ path: join(output, '03-proposal-to-accept.png') })
  const accepted = settle(page.waitForResponse(r => /\/proposals\/[^/]+\/accept/.test(r.url()) && r.request().method() === 'POST'))
  await review.getByRole('button', { name: 'Accept proposal' }).click()
  assert.equal((await accepted).status(), 200)
  await page.waitForLoadState('load')
  await page.getByLabel('Document editor', { exact: true }).waitFor()
  const after = await read()
  assert.equal(after.value.schema, 6)
  assert.equal(after.value.rev, placed.value.rev + 1)
  const lead = JSON.stringify(after.block('lead'))
  assert.match(lead, /by Friday/, lead)
  const marked = (block, text) => block.content.find(node => node.text === text)?.marks?.map(mark => mark.type) ?? []
  assert.deepEqual(marked(after.block('lead'), 'launch date'), ['bold'], `bold kept: ${lead}`)
  assert.deepEqual(marked(after.block('lead'), 'sales'), ['textColor'], `colour kept: ${lead}`)
  assert.equal(JSON.stringify(after.block('lead').content.find(node => node.text === 'sales').marks), JSON.stringify(originalLead.content.find(node => node.text === 'sales').marks))
  assert.deepEqual(after.placements, placed.placements, 'Canvas geometry is unchanged by accept and reject')
  assert.deepEqual(after.block('other'), placed.block('other'))
  assert.deepEqual(after.block('tail'), placed.block('tail'))
  pass('accept in the browser applies the text, keeps bold and colour, and keeps the Canvas geometry')

  await page.getByRole('tab', { name: 'Canvas', exact: true }).click()
  const card = page.locator('[data-placement="lead"]')
  await card.waitFor()
  await page.waitForFunction(() => document.querySelector('[data-placement="lead"]')?.textContent?.includes('by Friday'), null, { timeout: 15000 })
  await page.locator('[data-placement="lead"] strong', { hasText: 'launch date' }).waitFor()
  await page.locator('[data-placement="lead"] [data-text-color="red"]', { hasText: 'sales' }).waitFor()
  await page.screenshot({ path: join(output, '04-canvas-after-accept.png') })
  const shownAfter = await evidence('04-stored-after-accept', after)
  // World coordinates are the stored ones for both cards; the screen offset is the view's pan (worldTransform), not the layout.
  for (const id of ['lead', 'other']) {
    const want = placed.placements[id], got = shownAfter.cards[id].style
    assert.deepEqual([got.left, got.top, got.width, got.minHeight], [`${want.x}px`, `${want.y}px`, `${want.w}px`, `${want.h}px`], `${id}: ${JSON.stringify(got)}`)
    assert.equal(shownAfter.cards[id].screen.y - shownAfter.cards.lead.screen.y, shownBefore.cards[id].screen.y - shownBefore.cards.lead.screen.y, 'relative positions on screen are unchanged')
  }
  assert.deepEqual(shownAfter.cards.lead.bold, ['launch date'])
  assert.deepEqual(shownAfter.cards.lead.color, [['red', 'sales']])
  list = await openProposals()
  await list.getByRole('button', { name: /· accepted · revision/ }).waitFor()
  await list.getByRole('button', { name: /· rejected · revision/ }).waitFor()
  await page.screenshot({ path: join(output, '05-proposals-decided.png') })
  pass('the Canvas card shows the accepted text at its placement; both proposals are decided')
  assert.deepEqual(errors, [], 'no page errors')
} catch (error) {
  if (page) await page.screenshot({ path: join(output, 'failure.png') }).catch(() => {})
  throw error
} finally {
  await browser.close()
}
