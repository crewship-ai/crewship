// Owned synthetic Router/static-export fixture only; never a shared server.
import { readFile } from 'node:fs/promises'
import { chromium, expect } from '@playwright/test'
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_INVITATION_FIXTURE, 'utf8'))
const browser = await chromium.launch({ headless: true })
try {
  for (const kind of ['new', 'existing']) {
    const context = await browser.newContext()
    try {
      const page = await context.newPage()
      const token = fixture[`${kind}_token`]
      await page.goto(`${fixture.server}/join#invitation_token=${encodeURIComponent(token)}`)
      await expect(page.getByRole('heading', { name: 'Accept invitation' })).toBeVisible()
      expect(page.url()).not.toContain(token)
      for (const href of await page.locator('a[href]').evaluateAll(nodes => nodes.map(n => n.getAttribute('href')))) expect(href).not.toContain(token)
      if (kind === 'new') {
        await page.getByRole('link', { name: 'Create an account' }).click()
        await page.getByLabel('Full Name').fill('New invited reader')
        await page.getByLabel('Email', { exact: true }).fill(fixture.new_email)
        await page.getByLabel('Password', { exact: true }).fill(fixture.password)
        await page.getByLabel('Confirm Password', { exact: true }).fill(fixture.password)
        const signup = page.waitForResponse(r => r.url().endsWith('/api/v1/auth/signup') && r.request().method() === 'POST')
        await page.getByRole('button', { name: 'Sign Up' }).click()
        expect((await signup).status()).toBe(202)
        await expect(page).toHaveURL(url => url.pathname === "/login")
      } else {
        await page.getByRole('link', { name: 'Sign in' }).click()
      }
      await page.getByLabel('Email', { exact: true }).fill(fixture[`${kind}_email`])
      await page.getByLabel('Password', { exact: true }).fill(fixture.password)
      await page.getByRole('button', { name: 'Sign In', exact: true }).click()
      await expect(page.getByRole('heading', { name: 'Accept invitation' })).toBeVisible()
      const accepted = page.waitForResponse(r => r.url().endsWith('/api/v1/auth/invitation/accept') && r.request().method() === 'POST')
      await page.getByRole('button', { name: 'Accept invitation' }).click()
      expect((await accepted).status()).toBe(200)
      // Same cookie session sees the joined workspace through the real API.
      const response = await context.request.get(`${fixture.server}/api/v1/workspaces`)
      expect(response.status()).toBe(200)
      expect((await response.json()).some(w => w.id === fixture.workspace)).toBe(true)
      console.log(`PASS ${kind} invitation -> sign in -> explicit accept -> same-session workspace`)
    } finally { await context.close() }
  }
} finally { await browser.close() }
