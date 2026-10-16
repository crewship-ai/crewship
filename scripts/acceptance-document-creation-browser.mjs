// Real browser/WS/Bridge/Node/SQL with a synthetic provider tool invocation.
// This proves the human entrypoint, not a live model's authoring behavior.
import assert from 'node:assert/strict'
import { readFile, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { chromium, expect } from '@playwright/test'
import { loopbackOrigin } from '../tools/docs-d0/fixture-boundaries.mjs'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_DOCUMENT_CREATE_FIXTURE, 'utf8'))
const origin = loopbackOrigin(fixture.server)
const outputBase = process.env.CREWSHIP_DOCUMENT_CREATE_OUTPUT || tmpdir()
await mkdir(outputBase, { recursive: true, mode: 0o700 })
const output = await mkdtemp(join(outputBase, 'crewship-document-creation-'))
const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
const captures = []
const scenarios = []
const report = { execution: 'real static frontend, WS Hub, Bridge, trusted IPC admission, Node preparation, SQL creation and human editor; synthetic provider tool, no real model/Docker claim', sourceHead: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), sourceDirty: execFileSync('git', ['status', '--porcelain'], { encoding: 'utf8' }).trim(), scenarios, captures }
let page = await context.newPage()
async function capture(name) {
 const path = join(output, `${name}.png`)
 await page.screenshot({ path, fullPage: true })
 captures.push({ name, path })
}
try {
 await context.addCookies([{ name: 'authjs.session-token', value: fixture.token, url: origin, httpOnly: true, sameSite: 'Lax' }])
 await context.addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
 const identity = await context.request.get(`${origin}/api/auth/session`)
 assert.equal(identity.status(), 200)
 assert.equal((await identity.json()).user.id, fixture.owner)
 await page.goto(`${origin}/chat?agent=${encodeURIComponent(fixture.agent)}&new=1`)
 const choice = page.getByRole('button', { name: 'Create document', exact: true })
 await expect(choice).toBeVisible()
 await choice.click()
 await expect(choice).toHaveAttribute('aria-pressed', 'true')
 // The first Send creates the chat row. Consent must survive that transition.
 const composer = page.locator('textarea').last()
 await composer.fill('Write a useful launch plan document with a checklist and code example.')
 await composer.press('Enter')
 const card = page.getByRole('link', { name: 'Open document Launch plan', exact: true })
 await expect(card).toBeVisible({ timeout: 45000 })
 const href = await card.getAttribute('href')
 const target = new URL(href, origin)
 assert.equal(target.searchParams.get('workspace_id'), fixture.workspace)
 assert.ok(target.searchParams.get('ref'))
 scenarios.push('first new-chat typed output choice, one human Send, durable server result card')
 await capture('first-message-document-card')
 const [documentPage] = await Promise.all([context.waitForEvent('page'), card.click()])
 page = documentPage
 await page.waitForLoadState('domcontentloaded')
 const editor = page.locator('.ProseMirror[contenteditable="true"]')
 await expect(editor).toBeVisible({ timeout: 20000 })
 await expect(editor).toContainText('This useful document came from the first chat message.')
 await expect(editor).toContainText('Confirm the launch owner')
 await expect(editor.locator('pre')).toContainText('status: ready')
 assert.equal(new URL(page.url()).searchParams.get('ref'), target.searchParams.get('ref'))
 scenarios.push('stable PageID/workspace link opens populated native schema3 Docs')
 await capture('populated-document')
 await editor.click()
 await page.keyboard.press('Control+End')
 await page.keyboard.insertText(' Human follow-up survives reload.')
 // Poll the actual durable read, rather than assuming a debounce completed.
 await expect.poll(async () => {
  const response = await context.request.get(`${origin}/api/v1/pages/browser-launch-plan/document?workspace_id=${encodeURIComponent(fixture.workspace)}`)
  if (response.status() !== 200) return false
  const body = await response.json()
  return body.rev > 0 && body.outline.some(block => block.text.includes('Human follow-up survives reload.'))
 }, { timeout: 20000 }).toBe(true)
 await page.reload()
 await expect(editor).toContainText('Human follow-up survives reload.', { timeout: 20000 })
 scenarios.push('human editor mutation durably saved and reloaded')
 await capture('human-edit-reloaded')
 report.status = 'PASS'
} catch (error) {
 report.status = 'FAIL'
 // Synthetic fixture errors only; never persist cookies or Authorization.
 report.failure = String(error).replaceAll(fixture.token, '[fixture credential]')
 await capture('failure').catch(() => {})
 throw error
} finally {
 await writeFile(join(output, 'report.json'), JSON.stringify(report, null, 2), { mode: 0o600 })
 console.log(JSON.stringify({ status: report.status, report: join(output, 'report.json'), scenarios, captures }))
 await context.close()
 await browser.close()
}
