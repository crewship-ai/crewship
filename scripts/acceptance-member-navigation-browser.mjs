// Real production frontend + Router + migrated synthetic DB. No route mocks.
import { readFile, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium, expect } from '@playwright/test'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_MEMBER_NAV_FIXTURE, 'utf8'))
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(fixture.server)) throw new Error('Owned localhost fixture required')
const output = process.env.CREWSHIP_MEMBER_NAV_OUTPUT ?? await mkdtemp(join(tmpdir(), 'crewship-member-navigation-browser-'))
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
const results = []
const contexts = {}, pages = {}
const save = () => writeFile(join(output, 'results.json'), JSON.stringify({ execution: 'real Router/DB/browser sessions; no execution runtime or Docker', results }, null, 2))
async function check(name, condition, detail = {}) { results.push({ name, status: condition ? 'PASS' : 'FAIL', ...detail }); await save(); if (!condition) throw new Error(name) }
async function policy(rights) {
  const path = `${fixture.server}/api/v1/workspaces/${fixture.workspace}/members/${fixture.members['nav-client-a']}/access?workspace_id=${fixture.workspace}`
  let response = await contexts.owner.request.get(path)
  if (response.status() !== 200) throw new Error(`owner policy read ${response.status()}`)
  const current = await response.json()
  response = await contexts.owner.request.put(path, { data: { ...current, mode: 'restricted', rights } })
  await check(`real owner replaces member policy (${rights.length} grants)`, response.status() === 200, { http_status: response.status() })
}
async function open(actor, route) {
  await pages[actor].goto(fixture.server + route)
  await expect(pages[actor].getByRole('button', { name: /User menu/ })).toBeVisible()
}
async function directDenied(actor) {
  let sentAgentDirectory = false
  const listener = request => { if (new URL(request.url()).pathname === '/api/v1/agents') sentAgentDirectory = true }
  pages[actor].on('request', listener)
  await open(actor, '/chat')
  await expect(pages[actor].getByText('Not available with your access', { exact: true })).toBeVisible()
  pages[actor].off('request', listener)
  await check(`${actor} denied direct Chat URL never mounts agent directory`, !sentAgentDirectory)
}
try {
  for (const [actor, user, width] of [['owner', fixture.owner, 1280], ['desktop', 'nav-client-a', 1280], ['phone', 'nav-client-a', 390], ['other', 'nav-client-b', 1280]]) {
    contexts[actor] = await browser.newContext({ viewport: { width, height: 900 } })
    await contexts[actor].addCookies([{ name: 'authjs.session-token', value: fixture.tokens[user], url: fixture.server, httpOnly: true, sameSite: 'Lax' }])
    await contexts[actor].addInitScript(id => localStorage.setItem('crewship.workspaceId', id), fixture.workspace)
    pages[actor] = await contexts[actor].newPage()
  }
  await directDenied('desktop'); await directDenied('phone'); await directDenied('other')
  await policy([{ kind: 'agent', id: 'nav-alex', operation: 'discover' }, { kind: 'agent', id: 'nav-alex', operation: 'chat' }])
  for (const actor of ['desktop', 'phone']) {
    await open(actor, '/chat')
    await expect(pages[actor].getByRole('link', { name: 'Chat', exact: true }).first()).toBeVisible()
    if (actor === 'phone') await pages[actor].getByRole('button', { name: 'Show conversations', exact: true }).click()
    await expect(pages[actor].getByText('Alex', { exact: true }).first()).toBeVisible()
    await check(`${actor} granted Chat navigation and Alex content visible`, true)
  }
  await directDenied('other')
  // Switch using the real workspace control, without replacing the session.
  const desktop = pages.desktop
  await desktop.getByRole('button', { name: /^Current workspace:/ }).click()
  await desktop.getByRole('menuitem', { name: /Empty workspace/ }).click()
  await desktop.keyboard.press('Escape')
  await expect(desktop.getByRole('menu')).toHaveCount(0)
  await expect(desktop.getByText('Not available with your access', { exact: true })).toBeVisible()
  await expect(desktop.getByRole('link', { name: 'Chat', exact: true })).toHaveCount(0)
  await check('workspace switch does not borrow another membership Chat projection', true)
  await desktop.getByRole('button', { name: /^Current workspace:/ }).click()
  await desktop.getByRole('menuitem', { name: /^Test/ }).click()
  await desktop.keyboard.press('Escape')
  await expect(desktop.getByRole('menu')).toHaveCount(0)
  await expect(desktop.getByText('Alex', { exact: true }).first()).toBeVisible()
  const started = Date.now()
  await policy([])
  // Both tabs stay mounted on Chat; no navigation, reload, or manual refetch.
  for (const actor of ['desktop', 'phone']) {
    await expect(pages[actor].getByText('Not available with your access', { exact: true })).toBeVisible({ timeout: 70000 })
    await expect(pages[actor].getByRole('link', { name: 'Chat', exact: true })).toHaveCount(0)
    await expect(pages[actor].getByText('Alex', { exact: true })).toHaveCount(0)
    await check(`${actor} open Chat revoked automatically by foreground watcher`, true, { elapsed_ms: Date.now() - started })
    const cookies = await contexts[actor].cookies()
    await check(`${actor} original browser session unchanged`, cookies.some(cookie => cookie.name === 'authjs.session-token' && cookie.value === fixture.tokens['nav-client-a']))
  }
  await directDenied('other')
  await desktop.screenshot({ path: join(output, 'revoked-navigation.png') })
} catch (error) {
  results.push({ name: 'browser harness', status: 'ERROR', error: String(error) })
  for (const [actor, page] of Object.entries(pages)) await page.screenshot({ path: join(output, `${actor}-failure.png`) }).catch(() => {})
  throw error
} finally { await save(); await browser.close() }
console.log(JSON.stringify({ member_navigation_browser: 'PASS', checks: results.length, output }))
