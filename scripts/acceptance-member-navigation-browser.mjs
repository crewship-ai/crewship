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
  for (const [actor, user, width] of [['owner', fixture.owner, 1280], ['desktop', 'nav-client-a', 1280], ['phone', 'nav-client-a', 390], ['other', 'nav-client-b', 1280], ['reader', 'nav-page-reader', 1280]]) {
    contexts[actor] = await browser.newContext({ viewport: { width, height: 900 } })
    await contexts[actor].addCookies([{ name: 'authjs.session-token', value: fixture.tokens[user], url: fixture.server, httpOnly: true, sameSite: 'Lax' }])
    await contexts[actor].addInitScript(({id,user}) => localStorage.setItem(`crewship.workspaceId:${user}`, id), {id:fixture.workspace,user})
    await contexts[actor].addInitScript(() => {
      window.__navigationUnavailableShown = false
      window.__trustedSettingsShown = false
      new MutationObserver(() => {
        if (document.querySelector('input[aria-label="Full name"],nav[aria-label="Settings sections"]')) window.__trustedSettingsShown = true
        if ([...document.querySelectorAll('h1,h2')].some(node => node.textContent === 'Not available with your access')) window.__navigationUnavailableShown = true
      }).observe(document, { childList: true, subtree: true, characterData: true })
    })
    pages[actor] = await contexts[actor].newPage()
  }
  // A real directory omission must preserve selection and a mounted portal draft.
  // Soft-delete is fixture-only; transient catalog errors are covered separately.
  async function availability(omit) {
    const response = await contexts.owner.request.post(`${fixture.server}/__fixture/workspace-availability`, {
      headers: {'X-Fixture-Control':'synthetic-workspace-availability'}, data:{omit},
    })
    if(response.status()!==204) throw new Error(`fixture availability ${response.status()}`)
    const directory=await contexts.owner.request.get(`${fixture.server}/api/v1/workspaces`)
    const rows=await directory.json()
    await check(`real directory ${omit?'omits':'restores'} selected workspace and retains sibling`,
      directory.status()===200 && rows.some(row=>row.id===fixture.workspace)===!omit && rows.some(row=>row.id===fixture.empty_workspace))
  }
  await open('owner','/settings?tab=members')
  await pages.owner.reload()
  await expect(pages.owner.getByRole('heading', { name: 'Members', exact: true }).first()).toBeVisible()
  await expect(pages.owner.getByText('Failed to load settings', {exact:true})).toHaveCount(0)
  await expect(pages.owner.getByText('nav-client-a@example.test', {exact:true}).first()).toBeVisible()
  await check('F5 Settings members deep link loads real member data', true)
  await open('owner','/settings')
  await pages.owner.reload()
  await expect(pages.owner.getByText('Failed to load settings', {exact:true})).toHaveCount(0)
  await expect(pages.owner.getByLabel(/full name/i)).toBeVisible()
  await check('F5 Settings profile waits for workspace fence before metadata fetch', true)
  const guestSettingsRequests=[]
  const observeSettings=request=>{if(new URL(request.url()).pathname.startsWith('/api/v1/workspaces/'+fixture.workspace+'/')) guestSettingsRequests.push(request.url())}
  pages.desktop.on('request',observeSettings)
  await open('desktop','/settings?tab=members')
  await pages.desktop.reload()
  await expect(pages.desktop.getByRole('heading',{name:'Account security',exact:true})).toBeVisible()
  await expect(pages.desktop.getByRole('textbox',{name:'Full name'})).toHaveCount(0)
  await check('Guest Settings reload never flashes trusted settings', !(await pages.desktop.evaluate(()=>window.__trustedSettingsShown)))
  await expect(pages.desktop.getByText('Your access',{exact:true})).toBeVisible()
  await expect(pages.desktop.getByText('No individual resources are shared with you in this workspace. Ask your administrator for access.',{exact:true})).toBeVisible()
  await check('Guest Settings explains own empty policy without workspace metadata or roster requests',guestSettingsRequests.length>0 && guestSettingsRequests.every(url=>new URL(url).pathname.endsWith('/access/me')))
  pages.desktop.off('request',observeSettings)
  await open('owner','/pages')
  await pages.owner.getByRole('button',{name:'New page',exact:true}).first().click()
  let editor=pages.owner.getByRole('dialog',{name:/New page/})
  await expect(editor).toBeVisible()
  const code=editor.locator('.cm-content')
  await code.click()
  await pages.owner.keyboard.press('ControlOrMeta+End')
  await pages.owner.keyboard.insertText('\n# preserved workspace draft')
  await expect(code).toContainText('preserved workspace draft')
  let writes=0
  const countWrites=request=>{if(request.method()!=='GET'&&new URL(request.url()).pathname.startsWith('/api/v1/pages'))writes++}
  pages.owner.on('request',countWrites)
  await availability(true)
  let unavailable=pages.owner.getByRole('dialog',{name:'Selected workspace is unavailable'})
  await expect(unavailable).toBeVisible({timeout:70000})
  await expect(editor).toHaveCount(0)
  await expect.poll(()=>unavailable.evaluate(element=>element.contains(document.activeElement))).toBe(true)
  await check('omission preserves persisted selected workspace',await pages.owner.evaluate(user=>localStorage.getItem(`crewship.workspaceId:${user}`),fixture.owner)===fixture.workspace)
  await unavailable.press('Escape')
  await expect(unavailable).toBeVisible()
  await pages.owner.evaluate(()=>{
    const button=[...document.querySelectorAll('button')].find(button=>button.textContent?.trim()==='Create page')
    if(!button) throw new Error('Mounted old editor submit control missing')
    button.click()
  })
  await unavailable.press('ControlOrMeta+Enter')
  await check('unavailable portal and keyboard send no Page write',writes===0)
  pages.owner.once('dialog',dialog=>dialog.dismiss())
  await unavailable.getByRole('button',{name:'Choose Empty workspace',exact:true}).click()
  await expect(unavailable).toBeVisible()
  await check('cancelled explicit choice retains selected workspace',await pages.owner.evaluate(user=>localStorage.getItem(`crewship.workspaceId:${user}`),fixture.owner)===fixture.workspace)
  await availability(false)
  await unavailable.getByRole('button',{name:'Retry workspace',exact:true}).click()
  await expect(unavailable).toHaveCount(0)
  editor=pages.owner.getByRole('dialog',{name:/New page/})
  await expect(editor.locator('.cm-content')).toContainText('preserved workspace draft')
  await check('same workspace recovery restores original portal draft',true)
  await availability(true)
  unavailable=pages.owner.getByRole('dialog',{name:'Selected workspace is unavailable'})
  await expect(unavailable).toBeVisible({timeout:70000})
  pages.owner.once('dialog',dialog=>dialog.accept())
  await unavailable.getByRole('button',{name:'Choose Empty workspace',exact:true}).click()
  await expect(unavailable).toHaveCount(0)
  await expect(pages.owner.getByRole('dialog',{name:/New page/})).toHaveCount(0)
  await check('confirmed explicit choice changes context and discards old draft',await pages.owner.evaluate(user=>localStorage.getItem(`crewship.workspaceId:${user}`),fixture.owner)===fixture.empty_workspace)
  await availability(false)
  pages.owner.off('request',countWrites)
  await pages.owner.evaluate(({id,user})=>localStorage.setItem(`crewship.workspaceId:${user}`,id),{id:fixture.workspace,user:fixture.owner})
  // Subsequent preexisting scenarios load the selected fixture workspace anew.
  await pages.owner.reload()
  // Page creation uses the existing role-or-capability contract; reads remain available.
  await open('reader', '/pages')
  await expect(pages.reader.getByRole('button', {name:'New page', exact:true})).toHaveCount(0)
  await expect(pages.reader.getByRole('button', {name:'Import', exact:true})).toHaveCount(0)
  await check('trusted VIEWER reads Pages without creation/import shortcuts', true)
  await open('owner', '/pages')
  await expect(pages.owner.getByRole('button', {name:'New page', exact:true}).first()).toBeVisible()
  await expect(pages.owner.getByRole('button', {name:'Import', exact:true}).first()).toBeVisible()
  await check('OWNER creation and import remain available', true)
  const capabilityPath = `${fixture.server}/api/v1/workspaces/${fixture.workspace}/members/nav-page-reader/capabilities`
  let capResponse = await contexts.owner.request.patch(capabilityPath, {data:{grant:['page.create']}})
  await check('real owner grants page.create', capResponse.status() === 200)
  await open('reader', '/pages')
  await expect(pages.reader.getByRole('button', {name:'Import', exact:true}).first()).toBeVisible()
  await pages.reader.getByRole('button', {name:'New page', exact:true}).first().click()
  const creationDialog = pages.reader.getByRole('dialog', {name:/New page/})
  await expect(creationDialog).toBeVisible()
  await check('explicit capability enables VIEWER creation editor', true)
  let creationRequests = 0
  pages.reader.on('request', request => {if(request.method()==='POST' && new URL(request.url()).pathname==='/api/v1/pages') creationRequests++})
  capResponse = await contexts.owner.request.patch(capabilityPath, {data:{revoke:['page.create']}})
  await check('real owner revokes page.create', capResponse.status() === 200)
  const revokeStarted = Date.now()
  await expect(creationDialog.getByRole('button', {name:'Create page', exact:true})).toBeDisabled({timeout:70000})
  await creationDialog.press('Meta+Enter')
  await expect(creationDialog).toBeVisible()
  await check('open draft survives capability revoke and keyboard sends no create', creationRequests===0, {elapsed_ms:Date.now()-revokeStarted})
  await creationDialog.getByRole('button', {name:'Cancel',exact:true}).click()
  await expect(pages.reader.getByRole('button', {name:'New page',exact:true})).toHaveCount(0)
  await expect(pages.reader.getByRole('button', {name:'Import',exact:true})).toHaveCount(0)
  await directDenied('desktop'); await directDenied('phone'); await directDenied('other')
  await policy([{ kind: 'agent', id: 'nav-alex', operation: 'discover' }, { kind: 'agent', id: 'nav-alex', operation: 'chat' }])
  await open('desktop', '/settings')
  await expect(pages.desktop.getByText('Agent · Alex · discover · Allowed', {exact:true})).toBeVisible()
  await expect(pages.desktop.getByText('Agent · Alex · chat · Subject to execution checks', {exact:true})).toBeVisible()
  await check('Guest own access validates real response and labels resource kind/state', true)
  await open('owner', '/settings?tab=members')
  await pages.owner.locator('[data-member="nav-client-a"] button').first().click()
  const grantEditor=pages.owner.getByRole('region',{name:'Resource access for nav-client-a',exact:true})
  await grantEditor.getByRole('button',{name:'Edit resource access',exact:true}).click()
  await expect(grantEditor.getByText(`Issuer: ${fixture.owner} · Active`,{exact:true})).toHaveCount(2)
  await expect(grantEditor.getByRole('button',{name:'Reissue as me',exact:true})).toHaveCount(0)
  await check('real admin grant editor shows server-provided issuer and active status', true)
  for (const actor of ['desktop', 'phone']) {
    await open(actor, '/chat')
    await expect(pages[actor].getByRole('link', { name: 'Chat', exact: true }).first()).toBeVisible()
    if (actor === 'phone') await pages[actor].getByRole('button', { name: 'Show conversations', exact: true }).click()
    await expect(pages[actor].getByText('Alex', { exact: true }).first()).toBeVisible()
    await check(`${actor} granted Chat navigation and Alex content visible`, true)
    await check(`${actor} initial loading never flashes access denial`, !(await pages[actor].evaluate(() => window.__navigationUnavailableShown)))
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
  // The workspace switch leaves the pointer over the rail's hover overlay.
  // Move into content so the captured notice and Crewship mark remain readable.
  await desktop.mouse.move(1100, 700)
  await expect(desktop.locator('[data-slot="sidebar"][data-hover="true"]')).toHaveCount(0)
  await desktop.screenshot({ path: join(output, 'revoked-navigation.png'), animations: 'disabled' })
} catch (error) {
  results.push({ name: 'browser harness', status: 'ERROR', error: String(error) })
  for (const [actor, page] of Object.entries(pages)) await page.screenshot({ path: join(output, `${actor}-failure.png`) }).catch(() => {})
  throw error
} finally { await save(); await browser.close() }
console.log(JSON.stringify({ member_navigation_browser: 'PASS', checks: results.length, output }))
