// Real Yrs UI adapter. The product currently lacks an independent browser
// model projection; supply an integration module, never a JSON mock endpoint.
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { pathToFileURL } from 'node:url'
import { resolve } from 'node:path'
import { MissingContract, eventually } from './run.mjs'
import { localContext, localURL } from './local.mjs'

export function requireOwnedProductionTransport() {
  // The built production editor refuses plaintext iframe URLs, including
  // loopback. An arbitrary HTTPS URL is not evidence of an owned TLS proxy.
  throw new MissingContract('Production WordCraft requires owned local TLS transport; the CLI currently supports only the helper HTTP backend. See README owned TLS contract.')
}

export async function setup({ browser, scenario, baseURL, output, verifyIdentity }) {
  requireOwnedProductionTransport()
  const fixturePath = process.env.WORDCRAFT_NIGHT_FIXTURE
  if (!fixturePath) throw new MissingContract('WORDCRAFT_NIGHT_FIXTURE is required: disposable per-scenario document paths and two authenticated identities')
  const fixture = JSON.parse(await readFile(fixturePath, 'utf8'))
  const item = fixture.scenarios?.[scenario]
  if (!item) throw new MissingContract(`No disposable document for scenario ${scenario}`)
  const url = localURL(item.path, baseURL)
  assert.ok(item.identities?.length === 2 && item.identities[0] !== item.identities[1])
  if (!process.env.WORDCRAFT_NIGHT_INTEGRATION) throw new MissingContract('Yrs requires independent real authority/client projection and committed receipt integration; WORDCRAFT_NIGHT_INTEGRATION missing')
  const integration = await import(pathToFileURL(resolve(process.env.WORDCRAFT_NIGHT_INTEGRATION)).href)
  if (typeof integration.connect !== 'function') throw new MissingContract('Integration must export connect({pages,contexts,...})')
  const contexts = []
  try {
    for (const storageState of item.storageStates) contexts.push(await localContext(browser, baseURL, { storageState }))
    if (typeof verifyIdentity !== 'function') throw new MissingContract('Authenticated product identity verifier is required')
    await Promise.all(contexts.map(context => verifyIdentity(context)))
    const pages = await Promise.all(contexts.map(context => context.newPage()))
    const backend = await integration.connect({ pages, contexts, baseURL, fixture: item, scenario, output })
    const canvas = page => page.frameLocator('iframe[title="WordCraft editor"]').locator('#wordcraft_canvas')
    const host = page => page.locator('section[aria-label$=" editor"]').filter({ has: page.locator('iframe[title="WordCraft editor"]') })
    await Promise.all(pages.map(page => page.goto(url.href)))
    for (const page of pages) {
      await canvas(page).waitFor({ state: 'visible' })
      await eventually(async () => assert.match(await host(page).getByRole('status').innerText(), /^(No unsaved changes|Saved)$/), 'Original Yrs host not ready')
    }
    return {
      ...backend, pages, identities: item.identities, canvas,
      glyphCrop: async () => {
        if (!item.glyphCrop) throw new MissingContract('Glyph-only fixed crop must be calibrated on the real canvas')
        return item.glyphCrop
      },
      resetBlank: async () => {
        if (typeof backend.read !== 'function') throw new MissingContract('Independent read is missing')
        const state = await backend.read(pages[0])
        if (state.paragraphs.length === 1 && state.paragraphs[0].text === '') return
        const cursor = await backend.receiptCursor(pages[0])
        await canvas(pages[0]).click()
        await pages[0].keyboard.press('Control+Home')
        await pages[0].keyboard.press('Control+a')
        await pages[0].keyboard.press('Backspace')
        await backend.waitSaved(pages[0], cursor)
      },
      close: async () => {
        try { await backend.close?.() } finally { await Promise.all(contexts.map(context => context.close())) }
      },
    }
  } catch (error) { await Promise.all(contexts.map(context => context.close())); throw error }
}
