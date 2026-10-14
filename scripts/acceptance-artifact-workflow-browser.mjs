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
  await check('server persists the exact captured viewport point on revision one', storedPointThread.revision === 1 && capturedAnchor.schema_version === 1 && capturedAnchor.type === 'point' && capturedAnchor.coordinate_space === 'viewport' && Math.abs(capturedAnchor.x - clickPoint.x) < 1 && Math.abs(capturedAnchor.y - clickPoint.y) < 1 && capturedAnchor.viewport_width === captureBounds.width && capturedAnchor.viewport_height === captureBounds.height && JSON.stringify(capturedAnchor) === JSON.stringify(submittedPoint.anchor), { anchor: capturedAnchor })
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
  await check('restoring the viewport shows the original point without rewriting its server anchor', response.status() === 200 && afterResize.threads.length === 1 && afterResize.threads[0].revision === 1 && JSON.stringify(afterResize.threads[0].anchor) === JSON.stringify(capturedAnchor))
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
  await check('restore preserves original revision-bound comment anchor', response.status() === 200 && discussion.threads.length === 1 && discussion.threads[0].revision === 1 && JSON.stringify(discussion.threads[0].anchor) === JSON.stringify(capturedAnchor))
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
  await expect(owner.getByRole('button', { name: 'New board', exact: true })).toHaveCount(0)
  response = await contexts.owner.request.get(endpoint('/board'))
  await check('Artifacts delivery omits Board editor and Board HTTP route', response.status() === 404)
  console.log(`Artifact workflow browser passed ${results.length} checks; evidence: ${output}`)
} catch (error) {
  await writeFile(join(output, 'failure.txt'), String(error))
  for (const [actor, page] of Object.entries(pages)) await page.screenshot({ path: join(output, `${actor}-failure.png`) }).catch(() => {})
  throw error
} finally { await browser.close() }
