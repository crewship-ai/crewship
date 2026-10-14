// Opt-in real Router + migrated DB + static frontend conformance. Synthetic
// preauthenticated sessions, no HTTP mocks, login flow, LLM, Docker or dev4.
import { readFile, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium, expect } from '@playwright/test'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_ARTIFACT_WORKFLOW_FIXTURE, 'utf8'))
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(fixture.server)) throw new Error('Owned localhost fixture required')
const output = process.env.CREWSHIP_ARTIFACT_WORKFLOW_OUTPUT ?? await mkdtemp(join(tmpdir(), 'crewship-artifact-workflow-'))
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
const results = []
async function check(name, condition, detail = {}) {
  results.push({ name, status: condition ? 'PASS' : 'FAIL', ...detail })
  await writeFile(join(output, 'results.json'), JSON.stringify({ execution: 'real Router/DB/static frontend with synthetic preauthenticated sessions; no HTTP mocks or agent execution', results }, null, 2))
  if (!condition) throw new Error(name)
}
const contexts = {}, pages = {}
const endpoint = path => `${fixture.server}/api/v1/pages/browser-report${path}?workspace_id=${encodeURIComponent(fixture.workspace)}`
async function captureBoardDraft(page) {
  const button = page.getByRole('button', { name: 'Export unsaved draft', exact: true })
  if (!await button.isVisible()) return false
  const pending = page.waitForEvent('download', { timeout: 5000 })
  await button.click()
  await (await pending).saveAs(join(output, 'owner-board-draft.json'))
  return true
}
async function transferOutputThroughSettings(slug, name, type) {
  const ownershipURL = `${fixture.server}/api/v1/workspaces/${encodeURIComponent(fixture.workspace)}/page-ownership?workspace_id=${encodeURIComponent(fixture.workspace)}`
  const beforeResponse = await contexts.owner.request.get(ownershipURL)
  const before = (await beforeResponse.json()).pages?.find(page => page.slug === slug)
  await check(`real custody metadata selects the existing ${type} reader`, beforeResponse.status() === 200 && before?.type === type && before.owner_user_id === fixture.owner && before.eligible_successors.some(candidate => candidate.user_id === fixture.commenter))
  const owner = pages.owner
  const uiListing = owner.waitForResponse(response => response.request().method() === 'GET' && new URL(response.url()).pathname.endsWith('/page-ownership'))
  await owner.goto(`${fixture.server}/settings?tab=general`)
  const uiResponse = await uiListing
  await writeFile(join(output, 'owner-custody-response.json'), JSON.stringify({ status: uiResponse.status(), body: await uiResponse.json() }, null, 2))
  const card = owner.locator('#settings-card-artifact-and-board-ownership')
  await expect(card).toBeVisible()
  await expect(card.locator('iframe')).toHaveCount(0)
  await expect(card.getByRole('combobox', { name: `New owner for ${name}`, exact: true })).toBeVisible()
  await writeFile(join(output, 'owner-custody-controls.json'), JSON.stringify({ expected_label: `New owner for ${name}`, exact_label_matches: await card.getByLabel(`New owner for ${name}`, { exact: true }).count(), controls: await card.locator('select').evaluateAll(selects => selects.map(select => ({ id: select.id, labels: [...select.labels].map(label => label.textContent) }))) }, null, 2))
  await card.getByLabel(`New owner for ${name}`, { exact: true }).selectOption(fixture.commenter)
  const start = card.getByRole('button', { name: `Transfer ownership of ${name}`, exact: true })
  const posts = []
  const observe = request => {
    if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith(`/page-ownership/${before.id}`)) posts.push(request.postDataJSON())
  }
  owner.on('request', observe)
  try {
    await start.click()
    await expect(owner.getByRole('alertdialog')).toContainText('gaining control of this private output and its retained history')
    await expect(owner.getByRole('alertdialog')).toContainText('Existing grants are rechecked under current permissions')
    await owner.getByRole('button', { name: 'Cancel', exact: true }).click()
    await check(`cancelling ${type} ownership confirmation sends no transfer`, posts.length === 0)
    await start.click()
    const transferred = owner.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/page-ownership/${before.id}`))
    await owner.getByRole('button', { name: 'Confirm ownership transfer', exact: true }).click()
    const response = await transferred
    const summary = await response.json()
    await check(`Settings explicitly transfers ${type} ownership through the real route`, response.status() === 200 && summary.id === before.id && summary.owner_user_id === fixture.commenter && posts.length === 1 && JSON.stringify(posts[0]) === JSON.stringify({ expected_owner_user_id: fixture.owner, successor_user_id: fixture.commenter }))
  } finally { owner.off('request', observe) }
  const afterResponse = await contexts.owner.request.get(ownershipURL)
  const after = (await afterResponse.json()).pages?.find(page => page.id === before.id)
  await check(`real custody listing confirms the new ${type} owner`, afterResponse.status() === 200 && after?.owner_user_id === fixture.commenter)
}
// Called after the complete Board workflow in the Board delivery script.
export async function transferBoardOwnership(image) {
  const boardURL = `${fixture.server}/api/v1/pages/browser-board/board?workspace_id=${encodeURIComponent(fixture.workspace)}`
  const beforeResponse = await contexts.owner.request.get(boardURL)
  const boardBefore = await beforeResponse.json()
  expect(beforeResponse.status()).toBe(200)
  if (!image?.file_id) throw new Error('Board custody regression requires the preceding canonical raster upload receipt')
  const imageURL = `${boardURL.replace('/board?', `/board/files/${encodeURIComponent(image.file_id)}?`)}`
  const beforeImageResponse = await contexts.owner.request.get(imageURL)
  expect(beforeImageResponse.status()).toBe(200)
  const beforeImage = await beforeImageResponse.body()
  const grant = await contexts.owner.request.put(`${fixture.server}/api/v1/pages/browser-board/grants?workspace_id=${encodeURIComponent(fixture.workspace)}`, { data: { subject_type: 'user', subject: fixture.commenter, level: 'read' } })
  await check('owner restores explicit current Board read access before choosing a successor', grant.status() === 200)
  await transferOutputThroughSettings('browser-board', 'Browser Board', 'board')
  const afterResponse = await contexts.commenter.request.get(boardURL)
  const boardAfter = await afterResponse.json()
  const snapshot = board => ({ format: board.format, seq: board.seq, records: board.records, placements: board.placements, placement_tombstones: board.placement_tombstones })
  await check('Board custody transfer preserves saved sequence and every drawing and pin', afterResponse.status() === 200 && JSON.stringify(snapshot(boardAfter)) === JSON.stringify(snapshot(boardBefore)))
  const afterImageResponse = await contexts.commenter.request.get(imageURL)
  await check('new Board owner reads the unchanged canonical private raster', afterImageResponse.status() === 200 && afterImageResponse.headers()['content-type'] === 'image/png' && (await afterImageResponse.body()).equals(beforeImage))
}
try {
  for (const actor of ['owner', 'commenter']) {
    contexts[actor] = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await contexts[actor].addCookies([{ name: 'authjs.session-token', value: fixture.tokens[fixture[actor]], url: fixture.server, httpOnly: true, sameSite: 'Lax' }])
    await contexts[actor].addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
    pages[actor] = await contexts[actor].newPage()
  }
  let response = await contexts.owner.request.post(`${fixture.server}/api/v1/pages?workspace_id=${fixture.workspace}`, { data: { slug: 'browser-report', name: 'Browser report', panels: [], allow_empty: true } })
  await check('owner creates private empty human Page through production authorization route', response.status() === 201, { http_status: response.status() })
  const owner = pages.owner
  await owner.goto(`${fixture.server}/pages/browser-report`)
  await expect(owner.getByRole('region', { name: 'HTML artifact', exact: true })).toBeVisible()
  const firstSource = '<h1>First saved report</h1>' + Array.from({ length: 80 }, (_, index) => `<p>Scroll fixture row ${index + 1}</p>`).join('') + '<script>window.__artifactExecuted=true</script>'
  await owner.getByLabel('Upload HTML', { exact: true }).setInputFiles({ name: 'report.html', mimeType: 'text/html', buffer: Buffer.from(firstSource) })
  await expect(owner.getByLabel('HTML for the next revision')).toBeVisible()
  const firstSave = owner.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/artifact/revisions'))
  await owner.getByRole('button', { name: 'Save revision', exact: true }).click()
  response = await firstSave
  await check('browser HTML upload saves revision one through real route', response.status() === 201, { http_status: response.status() })
  await owner.getByRole('button', { name: 'Static preview', exact: true }).click()
  await expect(owner.frameLocator('iframe[title="Artifact static preview"]').getByRole('heading', { name: 'First saved report' })).toBeVisible()
  const preview = owner.frameLocator('iframe[title="Artifact static preview"]')
  await expect(preview.locator('script')).toHaveCount(0)
  await check('static preview strips submitted script and leaves its execution marker absent', !(await preview.locator('body').evaluate(() => window.__artifactExecuted)))
  response = await contexts.owner.request.put(endpoint('/grants'), { data: { subject_type: 'user', subject: fixture.commenter, level: 'comment' } })
  await check('owner explicitly grants commenter through real Page grant route', response.status() === 200, { http_status: response.status() })
  const commenter = pages.commenter
  await commenter.goto(`${fixture.server}/pages/browser-report`)
  await expect(commenter.getByRole('region', { name: 'HTML artifact', exact: true })).toBeVisible()
  const commentPreview = commenter.frameLocator('iframe[title="Artifact static preview"]')
  await expect(commentPreview.getByRole('heading', { name: 'First saved report' })).toBeVisible()
  await commentPreview.locator('body').evaluate(() => window.scrollTo(0, 600))
  await expect.poll(() => commentPreview.locator('body').evaluate(() => window.scrollY)).toBeGreaterThan(0)
  await check('real static iframe scrolls before viewport point capture', true)
  await commenter.getByRole('button', { name: 'Comment on a point', exact: true }).click()
  const picker = commenter.getByRole('button', { name: 'Choose comment point in preview', exact: true })
  await expect(picker).toBeEnabled()
  await expect.poll(() => commentPreview.locator('body').evaluate(() => window.scrollY)).toBe(0)
  await check('entering point capture reloads the scrolled iframe to its top', true)
  const captureBounds = await picker.boundingBox()
  if (!captureBounds) throw new Error('Point capture viewport unavailable')
  const clickPoint = { x: 120, y: 90 }
  await picker.click({ position: clickPoint })
  await expect(commenter.getByLabel('Comment point', { exact: true })).toBeVisible()
  await commenter.getByLabel('New thread', { exact: true }).fill('Synthetic comment anchored to first revision')
  const commentSave = commenter.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/artifact/threads'))
  await commenter.getByRole('button', { name: 'Post comment', exact: true }).click()
  response = await commentSave
  await check('commenter posts revision-bound thread through real route', response.status() === 201, { http_status: response.status() })
  const submittedPoint = response.request().postDataJSON()
  const storedPointThread = await response.json()
  const capturedAnchor = storedPointThread.anchor
  expect(capturedAnchor).toEqual(submittedPoint.anchor)
  await check('server persists the exact captured viewport point on revision one', storedPointThread.revision === 1 && capturedAnchor.schema_version === 1 && capturedAnchor.type === 'point' && capturedAnchor.coordinate_space === 'viewport' && Math.abs(capturedAnchor.x - clickPoint.x) < 1 && Math.abs(capturedAnchor.y - clickPoint.y) < 1 && capturedAnchor.viewport_width === captureBounds.width && capturedAnchor.viewport_height === captureBounds.height, { anchor: capturedAnchor })
  await expect(commenter.getByText('Synthetic comment anchored to first revision', { exact: true })).toBeVisible()
  const pointLocation = async page => {
    const frame = await page.getByTitle('Artifact static preview', { exact: true }).boundingBox()
    const point = await page.getByLabel('Comment point', { exact: true }).boundingBox()
    if (!frame || !point) return null
    return { x: Math.round(point.x + point.width / 2 - frame.x), y: Math.round(point.y + point.height / 2 - frame.y) }
  }
  await expect.poll(() => pointLocation(commenter)).toEqual(clickPoint)
  await commenter.setViewportSize({ width: 1000, height: 700 })
  await expect(commenter.getByLabel('Comment point', { exact: true })).toHaveCount(0)
  await expect(commenter.getByText(/This viewport differs from the captured size; the point is not overlaid/)).toBeVisible()
  await check('responsive viewport change hides the point with an explicit mismatch notice', true)
  await commenter.setViewportSize({ width: 1280, height: 900 })
  await expect(commenter.getByLabel('Comment point', { exact: true })).toBeVisible()
  await expect.poll(() => pointLocation(commenter)).toEqual(clickPoint)
  await expect(commenter.getByText(/This viewport differs from the captured size; the point is not overlaid/)).toHaveCount(0)
  response = await contexts.commenter.request.get(endpoint('/artifact/threads'))
  const afterResize = await response.json()
  expect(afterResize.threads[0]?.anchor).toEqual(capturedAnchor)
  await check('restoring the viewport shows the original point without rewriting its server anchor', response.status() === 200 && afterResize.threads.length === 1 && afterResize.threads[0].revision === 1)
  response = await contexts.commenter.request.get(endpoint('/artifact/requests'))
  const requests = await response.json()
  await check('commenter current request capability is false', response.status() === 200 && requests.can_dispatch === false)
  await expect(commenter.getByRole('button', { name: 'Send to agent', exact: true })).toHaveCount(0)
  response = await contexts.commenter.request.post(endpoint('/artifact/requests'), { data: { chat_id: 'not-authority', instruction: 'Must not execute', base_rev: 1, submission_id: 'commenter-forgery' } })
  await check('commenter cannot dispatch even by direct authenticated HTTP', response.status() === 403, { http_status: response.status() })
  await expect(commenter.getByLabel('Upload HTML', { exact: true })).toHaveCount(0)
  await owner.getByRole('button', { name: 'Source', exact: true }).click()
  await owner.getByRole('button', { name: 'Edit as new revision', exact: true }).click()
  await owner.getByLabel('HTML for the next revision').fill('<h1>Second saved report</h1>')
  const secondSave = owner.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/artifact/revisions'))
  await owner.getByRole('button', { name: 'Save revision', exact: true }).click()
  response = await secondSave
  await check('human edit creates revision two', response.status() === 201 && (await response.json()).revision === 2)
  await owner.getByRole('button', { name: 'Compare', exact: true }).click()
  await owner.getByLabel('Earlier revision', { exact: true }).selectOption('1')
  await owner.getByLabel('Later revision', { exact: true }).selectOption('2')
  await expect(owner.frameLocator('iframe[title="Compare revision 1"]').getByRole('heading', { name: 'First saved report' })).toBeVisible()
  await expect(owner.frameLocator('iframe[title="Compare revision 2"]').getByRole('heading', { name: 'Second saved report' })).toBeVisible()
  await check('real historical reads render both exact saved revisions for compare', true)
  await owner.getByRole('button', { name: 'History', exact: true }).click()
  const restore = owner.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/artifact/restore'))
  await owner.getByRole('button', { name: 'Restore as new revision', exact: true }).click()
  response = await restore
  await check('restore creates revision three rather than rewriting history', response.status() === 201 && (await response.json()).revision === 3)
  await expect(owner.frameLocator('iframe[title="Artifact static preview"]').getByRole('heading', { name: 'First saved report' })).toBeVisible()
  response = await contexts.owner.request.get(endpoint('/artifact/threads'))
  const discussion = await response.json()
  expect(discussion.threads[0]?.anchor).toEqual(capturedAnchor)
  await check('restore preserves original revision-bound comment anchor', response.status() === 200 && discussion.threads.length === 1 && discussion.threads[0].revision === 1)
  await expect(owner.getByLabel('Comment point', { exact: true })).toHaveCount(0)
  await preview.locator('body').evaluate(() => window.scrollTo(0, 600))
  await expect.poll(() => preview.locator('body').evaluate(() => window.scrollY)).toBeGreaterThan(0)
  await owner.getByRole('button', { name: 'Comments', exact: true }).click()
  await owner.getByRole('button', { name: 'Show comment point', exact: true }).click()
  await expect(owner.getByText('Revision 1 · latest 3', { exact: true })).toBeVisible()
  await expect(preview.getByRole('heading', { name: 'First saved report' })).toBeVisible()
  await expect.poll(() => preview.locator('body').evaluate(() => window.scrollY)).toBe(0)
  await expect(owner.getByLabel('Comment point', { exact: true })).toBeVisible()
  await expect.poll(() => pointLocation(owner)).toEqual(clickPoint)
  await check('viewing the historical point returns to revision one at the top and original viewport location', true)
  const artifactHistory = async () => Promise.all([1, 2, 3].map(async revision => {
    const response = await contexts.owner.request.get(`${endpoint('/artifact')}&revision=${revision}`)
    expect(response.status()).toBe(200)
    const { head_rev, revision: selected, html, manifest, manifest_status, revisions } = await response.json()
    return { head_rev, revision: selected, html, manifest, manifest_status, revisions }
  }))
  const artifactBeforeTransfer = await artifactHistory()
  const threadsBeforeTransfer = (await (await contexts.owner.request.get(endpoint('/artifact/threads'))).json()).threads
  await transferOutputThroughSettings('browser-report', 'Browser report', 'artifact')
  const artifactAfterTransfer = await artifactHistory()
  const threadsAfterTransfer = (await (await contexts.owner.request.get(endpoint('/artifact/threads'))).json()).threads
  await check('Artifact custody transfer preserves all retained source revisions, manifests, authors and comment anchors', JSON.stringify(artifactAfterTransfer) === JSON.stringify(artifactBeforeTransfer) && artifactAfterTransfer.every(revision => revision.head_rev === 3) && JSON.stringify(threadsAfterTransfer) === JSON.stringify(threadsBeforeTransfer) && threadsAfterTransfer[0]?.revision === 1 && JSON.stringify(threadsAfterTransfer[0]?.anchor) === JSON.stringify(capturedAnchor))
  const boardEndpoint = path => `${fixture.server}/api/v1/pages/browser-board${path}?workspace_id=${encodeURIComponent(fixture.workspace)}`
  response = await contexts.owner.request.post(`${fixture.server}/api/v1/pages?workspace_id=${fixture.workspace}`, { data: { slug: 'browser-board', name: 'Browser Board', panels: [], allow_empty: true } })
  await check('owner creates Board source through real route', response.status() === 201)
  const boardPage = await response.json()
  response = await contexts.owner.request.post(boardEndpoint('/board'), { data: { mutation_id: 'browser-board-init' } })
  await check('Board initializes in migrated database', response.status() === 200)
  response = await contexts.owner.request.put(boardEndpoint('/grants'), { data: { subject_type: 'user', subject: fixture.commenter, level: 'comment' } })
  await check('Board commenter has explicit live grant', response.status() === 200)
  const snapshots = { owner: [], commenter: [] }
  for (const actor of ['owner', 'commenter']) {
    pages[actor].on('websocket', socket => socket.on('framereceived', ({ payload }) => {
      try { const frame = JSON.parse(String(payload)); if (frame.type === 'presence.snapshot' && frame.channel === `board:${boardPage.id}`) snapshots[actor].push(frame) } catch { /* Ignore protocol/non-JSON frames. */ }
    }))
    await pages[actor].goto(`${fixture.server}/pages/browser-board`)
    await expect(pages[actor].getByRole('region', { name: 'Board', exact: true })).toBeVisible()
  }
  await owner.getByRole('button', { name: 'More tools', exact: true }).click()
  await expect(owner.getByRole('button', { name: /Mermaid to Excalidraw/i })).toBeHidden()
  await owner.locator('[data-testid="toolbar-embeddable"]').filter({ hasText: 'Mermaid to Excalidraw' }).dispatchEvent('click')
  await expect(owner.locator('.ttd-dialog')).toHaveCount(0)
  await owner.keyboard.press('Escape')
  await check('actual product Board cannot mount native Studio Mermaid converter', true)
  await expect.poll(() => snapshots.owner.some(frame => frame.payload.members.some(member => member.user_id === fixture.commenter)), { timeout: 15000 }).toBe(true)
  await expect.poll(() => snapshots.commenter.some(frame => frame.payload.members.some(member => member.user_id === fixture.owner)), { timeout: 15000 }).toBe(true)
  await check('real authenticated WebSocket room exposes authorized Board participants', true)
  const drawBoard = async (offset) => {
    const canvas = owner.locator('.excalidraw__canvas.interactive')
    await canvas.click({ position: { x: 700, y: 350 } })
    await owner.keyboard.press('r')
    const bounds = await canvas.boundingBox()
    if (!bounds) throw new Error('Board canvas unavailable')
    await owner.mouse.move(bounds.x + 280 + offset, bounds.y + 180)
    await owner.mouse.down(); await owner.mouse.move(bounds.x + 380 + offset, bounds.y + 260, { steps: 8 }); await owner.mouse.up()
    await expect(owner.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    const saved = owner.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/board/batches'))
    await owner.getByRole('button', { name: 'Save board', exact: true }).click()
    const result = await saved
    await check('real Board drawing commits through server batch validation', result.status() === 201)
  }
  await owner.getByRole('button', { name: 'Zoom in', exact: true }).click()
  const zoomBeforeSave = await owner.locator('.reset-zoom-button').innerText()
  await drawBoard(0)
  await expect(owner.getByText('Board · Saved sequence 1', { exact: true })).toBeVisible()
  await expect(owner.locator('.reset-zoom-button')).toHaveText(zoomBeforeSave)
  await check('saving the real Board preserves its zoom across canonical editor replacement', zoomBeforeSave !== '100%')
  response = await contexts.commenter.request.get(boardEndpoint('/board'))
  const savedBoard = await response.json()
  await check('separate commenter reads actual committed drawing', response.status() === 200 && savedBoard.seq === 1 && savedBoard.records.length === 1 && !savedBoard.can_write)
  response = await contexts.commenter.request.post(boardEndpoint('/board/batches'), { data: { mutation_id: 'commenter-forgery', base_seq: 1, deletes: [{ id: savedBoard.records[0].element_id, expected_rev: savedBoard.records[0].element_rev }] } })
  await check('commenter cannot submit a Board edit directly', response.status() === 403)
  response = await contexts.owner.request.post(boardEndpoint('/board/batches'), { data: { mutation_id: 'stale-batch', base_seq: 0, deletes: [{ id: savedBoard.records[0].element_id, expected_rev: savedBoard.records[0].element_rev + 1 }] } })
  await check('real server atomically rejects stale Board batch', response.status() === 409, { http_status: response.status() })
  response = await contexts.owner.request.get(boardEndpoint('/board'))
  await check('rejected batch preserves sequence and saved drawing', response.status() === 200 && (await response.json()).seq === 1)
  response = await contexts.owner.request.delete(`${boardEndpoint('/grants')}&subject_type=user&subject=${encodeURIComponent(fixture.commenter)}`)
  await check('owner revokes Board access through real grant route', response.status() === 200)
  await drawBoard(140)
  await expect(owner.getByText('Board · Saved sequence 2', { exact: true })).toBeVisible()
  await expect.poll(() => snapshots.owner.some(frame => frame.payload.members.some(member => member.board_seq === 2)), { timeout: 15000 }).toBe(true)
  await check('idle revoked connection receives no new sequence/cursor snapshot', !snapshots.commenter.some(frame => frame.payload.members.some(member => member.board_seq === 2)))
  response = await contexts.commenter.request.get(boardEndpoint('/board'))
  await check('revoked participant loses Board HTTP access too', [403, 404].includes(response.status()))
  response = await contexts.owner.request.get(boardEndpoint('/board'))
  const beforeMoves = await response.json()
  const moveSavedDrawings = async () => {
    const canvas = owner.locator('.excalidraw__canvas.interactive')
    await canvas.click({ position: { x: 700, y: 350 } })
    await owner.keyboard.press('v')
    await owner.keyboard.press('ControlOrMeta+a')
    await owner.keyboard.press('Shift+ArrowRight')
    await expect(owner.getByRole('button', { name: 'Save board', exact: true })).toBeEnabled()
    const committed = owner.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/board/batches'))
    await owner.getByRole('button', { name: 'Save board', exact: true }).click()
    await check('selected real drawings move through an explicit saved batch', (await committed).status() === 201)
  }
  await moveSavedDrawings()
  await expect(owner.getByText('Board · Saved sequence 3', { exact: true })).toBeVisible()
  await moveSavedDrawings()
  await expect(owner.getByText('Board · Saved sequence 4', { exact: true })).toBeVisible()
  const dirtyNotice = owner.getByText('Unsaved drawing changes. Save preserves the revisions you started from.', { exact: true })
  const assertCleanBoard = async name => {
    const canvas = owner.locator('.excalidraw__canvas.interactive')
    await expect(canvas).toBeVisible()
    // Let the remounted real editor deliver initialization callbacks before
    // asserting that accepted local inverses did not become unsaved drafts.
    await canvas.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    await expect(owner.getByRole('button', { name: 'Save board', exact: true })).toBeDisabled()
    await expect(dirtyNotice).toHaveCount(0)
    await check(name, true)
  }
  for (const sequence of [5, 6]) {
    const committed = owner.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/board/batches'))
    await owner.getByRole('button', { name: 'Undo last saved change', exact: true }).click()
    await check('consecutive saved undo commits with current server revisions', (await committed).status() === 201)
    await expect(owner.getByText(`Board · Saved sequence ${sequence}`, { exact: true })).toBeVisible()
    await assertCleanBoard(`saved undo at sequence ${sequence} settles without an unsaved drawing`)
  }
  response = await contexts.owner.request.get(boardEndpoint('/board'))
  const afterUndos = await response.json()
  const geometry = board => board.records.filter(r => !r.deleted && r.element).map(r => ({ id: r.element_id, x: r.element.x, y: r.element.y, width: r.element.width, height: r.element.height })).sort((a, b) => a.id.localeCompare(b.id))
  await check('two saved undos restore real server geometry to the original baseline', JSON.stringify(geometry(afterUndos)) === JSON.stringify(geometry(beforeMoves)))
  const foreignTarget = afterUndos.records.find(record => record.element_id !== savedBoard.records[0].element_id && !record.deleted && record.element)
  if (!foreignTarget) throw new Error('Second saved drawing unavailable for foreign-edit regression')
  await assertCleanBoard('Board remains clean immediately before the independent foreign edit')
  const foreignX = foreignTarget.element.x + 30
  response = await contexts.owner.request.post(boardEndpoint('/board/batches'), { data: { mutation_id: 'other-tab-change', base_seq: afterUndos.seq, upserts: [{ element: { ...foreignTarget.element, x: foreignX }, expected_rev: foreignTarget.element_rev }] } })
  await check('independent real server edit changes the older undo target', response.status() === 201)
  response = await contexts.owner.request.get(boardEndpoint('/board'))
  const foreignScene = await response.json()
  await expect(owner.getByText('Board · Saved sequence 7', { exact: true })).toBeVisible()
  const undo = owner.getByRole('button', { name: 'Undo last saved change', exact: true })
  const conflict = owner.getByText('This saved change can no longer be undone because connected content changed remotely.', { exact: true })
  const undoStatuses = []
  const observeUndo = response => {
    if (response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith('/board/batches')) undoStatuses.push(response.status())
  }
  owner.on('response', observeUndo)
  let undoOutcome
  try {
    // A refresh can leave an unsaved editor draft and disable saved undo.
    // The clean checkpoints above fence this from own-undo hydration. Preserve
    // evidence of a refresh-created draft as a UX gap, while checking safety.
    // Inspect and invoke atomically to avoid a race with that canonical refresh.
    const invoked = await undo.evaluate(button => {
      if (button.disabled) return false
      button.click()
      return true
    })
    if (invoked) {
      await expect.poll(async () => await conflict.isVisible() || undoStatuses.includes(409)).toBe(true)
      undoOutcome = undoStatuses.includes(409) ? 'server_conflict' : 'local_conflict'
    } else {
      await expect(undo).toBeDisabled()
      undoOutcome = 'disabled_guard'
      if (await dirtyNotice.isVisible()) {
        await captureBoardDraft(owner)
        await writeFile(join(output, 'owner-board-canonical.json'), JSON.stringify(foreignScene, null, 2))
      }
    }
  } finally { owner.off('response', observeUndo) }
  response = await contexts.owner.request.get(boardEndpoint('/board'))
  const afterRefusal = await response.json()
  await check('older saved undo refuses a foreign edit and preserves canonical content', response.status() === 200 && afterRefusal.seq === 7 && afterRefusal.seq === foreignScene.seq && JSON.stringify(afterRefusal.records) === JSON.stringify(foreignScene.records) && afterRefusal.records.find(record => record.element_id === foreignTarget.element_id)?.element?.x === foreignX && undoStatuses.every(status => status === 409), { undo_outcome: undoOutcome, undo_http_statuses: undoStatuses, unsaved_draft_after_foreign_refresh: await dirtyNotice.isVisible() })
  const imageURL = `${boardEndpoint('/board/files')}&base_seq=7&mutation_id=browser-raster-upload`
  const imageBody = Buffer.from(fixture.image_png, 'base64')
  response = await contexts.owner.request.post(imageURL, { data: imageBody, headers: { 'Content-Type': 'image/png' } })
  const uploaded = await response.json()
  await check('real streamed Board upload stores an authorized canonical raster', response.status() === 201 && uploaded.seq === 8 && uploaded.width === 2 && uploaded.height === 2 && uploaded.content_type === 'image/png')
  response = await contexts.owner.request.post(imageURL, { data: imageBody, headers: { 'Content-Type': 'image/png' } })
  const retryImage = await response.json()
  await check('exact raster retry preserves file identity and Board sequence', response.status() === 200 && retryImage.seq === 8 && retryImage.file_id === uploaded.file_id)
  response = await contexts.owner.request.get(boardEndpoint(`/board/files/${encodeURIComponent(uploaded.file_id)}`))
  await check('owner reads the real canonical raster through its authorized route', response.status() === 200 && response.headers()['content-type'] === 'image/png')
  response = await contexts.commenter.request.get(boardEndpoint(`/board/files/${encodeURIComponent(uploaded.file_id)}`))
  await check('revoked participant cannot read Board image bytes', [403, 404].includes(response.status()))
  await transferBoardOwnership(uploaded)
  console.log(`Artifact workflow browser passed ${results.length} checks; evidence: ${output}`)
} catch (error) {
  await writeFile(join(output, 'failure.txt'), String(error))
  await captureBoardDraft(pages.owner).catch(() => {})
  const canonical = contexts.owner ? await contexts.owner.request.get(`${fixture.server}/api/v1/pages/browser-board/board?workspace_id=${encodeURIComponent(fixture.workspace)}`, { timeout: 5000 }).catch(() => null) : null
  if (canonical?.ok()) await writeFile(join(output, 'owner-board-canonical.json'), JSON.stringify(await canonical.json(), null, 2))
  if (pages.owner) await pages.owner.locator('#settings-card-artifact-and-board-ownership').ariaSnapshot({ timeout: 1000 }).then(snapshot => writeFile(join(output, 'owner-custody-card.yml'), snapshot)).catch(() => {})
  for (const [actor, page] of Object.entries(pages)) await page.screenshot({ path: join(output, `${actor}-failure.png`) }).catch(() => {})
  throw error
} finally { await browser.close() }
