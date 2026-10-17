// Owned synthetic Router/static-export fixture only; never a shared server.
import { readFile, mkdtemp } from 'node:fs/promises'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import { chromium, expect } from '@playwright/test'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_INVITATION_FIXTURE, 'utf8'))
const output=await mkdtemp(join(tmpdir(),'crewship-guest-invitation-browser-'))
const browser = await chromium.launch({ headless: true })
try {
  for (const kind of ['new', 'existing']) {
    const context = await browser.newContext()
    try {
      const page = await context.newPage()
      const token = fixture[`${kind}_token`]
      await page.goto(`${fixture.server}/join#invitation_token=${encodeURIComponent(token)}`)
      await expect(page.getByRole('heading', { name: 'Accept the invitation' })).toBeVisible()
      // The heading can be server-rendered before the capture effect hydrates.
      await expect(page).toHaveURL(url => !url.href.includes(token))
      await expect(page.getByRole('link', { name: 'Sign in' })).toBeVisible()
      for (const href of await page.locator('a[href]').evaluateAll(nodes => nodes.map(n => n.getAttribute('href')))) expect(href).not.toContain(token)
      if (kind === 'new') {
        await page.getByRole('link', { name: 'Create an account' }).click()
        await page.getByLabel('Full name').fill('New invited reader')
        await page.getByLabel('Email', { exact: true }).fill(fixture.new_email)
        await page.getByLabel('Password', { exact: true }).fill(fixture.password)
        await page.getByLabel('Confirm password', { exact: true }).fill(fixture.password)
        const signup = page.waitForResponse(r => r.url().endsWith('/api/v1/auth/signup') && r.request().method() === 'POST')
        await page.getByRole('button', { name: 'Create account' }).click()
        expect((await signup).status()).toBe(202)
        await expect(page).toHaveURL(url => url.pathname === "/login")
      } else {
        await page.getByRole('link', { name: 'Sign in' }).click()
      }
      await page.getByLabel('Email', { exact: true }).fill(fixture[`${kind}_email`])
      await page.getByLabel('Password', { exact: true }).fill(fixture.password)
      await page.getByRole('button', { name: 'Sign in', exact: true }).click()
      await expect(page.getByRole('heading', { name: 'Accept the invitation' })).toBeVisible()
      const accepted = page.waitForResponse(r => r.url().endsWith('/api/v1/auth/invitation/accept') && r.request().method() === 'POST')
      await page.getByRole('button', { name: 'Accept invitation' }).click()
      const initialStatus=(await accepted).status()
      if(kind==='existing'){
        expect(initialStatus).toBe(400)
        await expect(page.getByRole('status')).toContainText('entire account across all workspaces')
        await page.getByRole('checkbox',{name:'I understand this applies to the whole account'}).check()
        const confirmed=page.waitForResponse(r=>r.url().endsWith('/api/v1/auth/invitation/accept')&&r.request().method()==='POST')
        await page.getByRole('button',{name:'Accept Guest restrictions'}).click()
        expect((await confirmed).status()).toBe(200)
      } else expect(initialStatus).toBe(200)
      // Same cookie session sees the joined workspace through the real API.
      const response = await context.request.get(`${fixture.server}/api/v1/workspaces`)
      expect(response.status()).toBe(200)
      const workspaces=await response.json()
      expect(workspaces.some(w => w.id === fixture.workspace&&w.currentUserAccessMode==='restricted')).toBe(true)
      expect(workspaces.every(w=>w.restricted_session===true)).toBe(true)
      await page.goto(fixture.server+'/credentials')
      await expect(page.getByText('Not available with your access',{exact:true})).toBeVisible()
      await expect(page.getByRole('link',{name:'Settings',exact:true}).first()).toBeVisible()
      await page.screenshot({path:join(output,`${kind}-guest-access.png`),fullPage:true})
      console.log(`PASS ${kind} Guest invitation -> sign in -> explicit acceptance -> initial grants/account ceiling; screenshot ${join(output,`${kind}-guest-access.png`)}`)
    } finally { await context.close() }
  }
} finally { await browser.close() }
