import { expect, type Browser, type BrowserContext, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { parseC1Response, WordCraftHTTPObserver } from './http-transport'
import { installNativeQueueObserver, waitNativeQueue } from './native-queue'

export const ENGINE_SHA = '7ef2633a7e84956522d1ca47b99fe6feef16c1db'
export const STAGES = ['ui', 'engine', 'saved', 'other-clients', 'reload', 'undo'] as const
export type Stage = typeof STAGES[number]
export interface Run { len: number; props?: Record<string, unknown> }
export interface Paragraph { kind: 'para'; text: string; runs: Run[]; props?: Record<string, unknown> }
export interface CommittedState {
  engine_sha: string
  revision: string
  principal_id: string
  ids: string[]
  native_digest: string
  document: { body: Paragraph[]; numbering?: { nums: { id: number; abstractId: number; levelOverrides?: [number, { format: string }][] }[]; abstracts: { id: number; levels: { format: string }[] }[] } }
}
export interface Editors { a: Page; b: Page; contexts: BrowserContext[]; statePath: string; observers: WordCraftHTTPObserver[] }
const observers = new WeakMap<Page, { observer: WordCraftHTTPObserver; endpoint: string }>()
const readers = new WeakMap<Page, { endpoint: string; view: string; actor: string }>()
export function observerFor(page: Page) { const value = observers.get(page); if (!value) throw Error("Actual C1 page observer is missing"); return value }
const u64 = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18_446_744_073_709_551_615n

export function settings(feature?: string) {
  const required = (name: string) => {
    const value = process.env[name]?.trim()
    if (!value) throw new Error(`${name} is required; missing C1 integration is a failure, never a skip`)
    return value
  }
  const base = new URL(required('WORDCRAFT_CE_BASE_URL'))
  const fixtures = process.env.WORDCRAFT_CE_DOCUMENTS ? JSON.parse(process.env.WORDCRAFT_CE_DOCUMENTS) as Record<string, { path: string; statePath: string }> : undefined
  const fixture = feature && fixtures ? fixtures[feature] : undefined
  if (feature && fixtures && !fixture) throw Error(`Missing separate disposable document for ${feature}`)
  const documentURL = new URL(fixture?.path ?? required('WORDCRAFT_CE_DOCUMENT_PATH'), base)
  const statePath = fixture?.statePath ?? required('WORDCRAFT_CE_STATE_PATH')
  const endpoint = new URL(statePath, base)
  const productSHA = required('WORDCRAFT_CE_PRODUCT_SHA')
  if (!/^[a-f0-9]{40}$/.test(productSHA)) throw Error('Exact product commit SHA is required')
  if (!['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.search || base.hash) throw Error('Invalid base URL')
  if (documentURL.origin !== base.origin || documentURL.username || documentURL.password || documentURL.hash) throw Error('Document must be on configured origin')
  if (endpoint.origin !== base.origin || !/^\/api\/v1\/pages\/(?:%40|@)[^/]+\/mooring\/command-session$/.test(endpoint.pathname) || !endpoint.searchParams.get('workspace_id') || [...endpoint.searchParams.keys()].some(key => key !== 'workspace_id')) throw Error('State path must identify the actual same-origin C1 command-session endpoint and workspace')
  return { documentURL: documentURL.href, endpoint: endpoint.href, statePath, productSHA, authA: required('WORDCRAFT_CE_AUTH_A'), authB: required('WORDCRAFT_CE_AUTH_B') }
}

/** Independent confirmed SQL projection through the EXISTING public read-only
 * open(write:false)/read actions. No create, submit, outcome retry or raw command. */
export async function readState(page: Page, path: string): Promise<CommittedState> {
  const endpoint = new URL(path, page.url()).href
  const post = async (data: object) => {
    const response = await page.request.post(endpoint, { data, headers: { origin: new URL(page.url()).origin } })
    expect(response.status(), 'Successful authenticated C1 control read is required').toBe(200)
    return parseC1Response(await response.json())
  }
  let reader = readers.get(page)
  if (!reader || reader.endpoint !== endpoint) {
    const opened = await post({ action: 'open', write: false })
    expect(opened.outcome).toBeNull()
    reader = { endpoint, view: opened.view, actor: opened.actor }; readers.set(page, reader)
  }
  const confirmed = await post({ action: 'read', view: reader.view })
  expect(confirmed.outcome).toBeNull()
  expect(confirmed.actor).toBe(reader.actor)
  const state: CommittedState = { engine_sha: confirmed.engine, revision: String(confirmed.projection.revision), principal_id: confirmed.actor,
    ids: confirmed.projection.ids, native_digest: createHash('sha256').update(confirmed.projection.native_json).digest('hex'), document: JSON.parse(confirmed.projection.native_json) }
  expect(state.engine_sha, 'Never fall back to the legacy Yjs document').toBe(ENGINE_SHA)
  expect(u64(state.revision)).toBe(true)
  expect(Array.isArray(state.document?.body)).toBe(true)
  expect(state.ids).toHaveLength(state.document.body.length)
  for (const paragraph of state.document.body) {
    expect(paragraph.kind, 'This fixture must contain paragraphs only').toBe('para')
    expect(typeof paragraph.text).toBe('string')
    expect(Array.isArray(paragraph.runs)).toBe(true)
    expect(paragraph.runs.every(run => Number.isSafeInteger(run.len) && run.len >= 0)).toBe(true)
    expect(paragraph.runs.reduce((sum, run) => sum + run.len, 0)).toBe(Buffer.byteLength(paragraph.text, 'utf8'))
  }
  return state
}

export const host = (page: Page) => page.locator('section[aria-label$=" command editor"]')
export const canvas = (page: Page) => host(page).frameLocator('iframe[title="WordCraft command editor"]').locator('#wordcraft_canvas')
export const text = (state: CommittedState) => state.document.body.map(p => p.text).join('\n')

export async function openEditors(browser: Browser, beforeNavigate?: (a: Page, b: Page) => Promise<void>, feature?: string): Promise<Editors> {
  const config = settings(feature)
  const contexts: BrowserContext[] = []
  const active: WordCraftHTTPObserver[] = []
  try {
    contexts.push(await browser.newContext({ storageState: config.authA, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, colorScheme: 'light' }))
    contexts.push(await browser.newContext({ storageState: config.authB, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, colorScheme: 'light' }))
    const [a, b] = await Promise.all(contexts.map(context => context.newPage()))
    for (const page of [a, b]) {
      const versionResponse = await page.request.get(new URL('/api/v1/system/version', config.documentURL).href)
      expect(versionResponse.status()).toBe(200)
      const version = await versionResponse.json() as { commit: string; dirty: boolean }
      expect(version.commit, 'Do not mutate an unexpected deployed release').toBe(config.productSHA)
      expect(version.dirty).toBe(false)
      const observer = new WordCraftHTTPObserver(page, config.endpoint)
      observers.set(page, { observer, endpoint: config.endpoint }); active.push(observer)
      await observer.start(); await installNativeQueueObserver(page)
    }
    await beforeNavigate?.(a, b)
    await Promise.all([a.goto(config.documentURL), b.goto(config.documentURL)])
    for (const page of [a, b]) {
      await expect(host(page)).toHaveCount(1)
      await expect(host(page).locator('iframe[title="WordCraft command editor"]')).toHaveCount(1)
      await expect(host(page).getByRole('status')).toHaveText(/^(No unsaved changes|Saved)$/)
      await expect(canvas(page)).toBeVisible()
    }
    const [stateA, stateB] = await Promise.all([readState(a, config.statePath), readState(b, config.statePath)])
    expect(stateA.principal_id, 'Participant undo needs two authenticated identities, not two tabs of one account').not.toBe(stateB.principal_id)
    expect(contexts[0]).not.toBe(contexts[1])
    return { a, b, contexts, statePath: config.statePath, observers: active }
  } catch (error) {
    await Promise.all(active.map(observer => observer.close()))
    await Promise.all(contexts.map(context => context.close()))
    throw error
  }
}

export async function focusDocument(page: Page) { await canvas(page).click() }
export async function selectAll(page: Page) {
  await focusDocument(page)
  await page.keyboard.press('Control+Home')
  await page.keyboard.press('Control+A')
}
export async function selectSeedWord(page: Page, prefixLength: number, wordLength: number) {
  await focusDocument(page)
  await page.keyboard.press('Control+Home')
  for (let index = 0; index < prefixLength; index++) await page.keyboard.press('ArrowRight')
  for (let index = 0; index < wordLength; index++) await page.keyboard.press('Shift+ArrowRight')
}
export async function waitSaved(page: Page, revision?: string, opId?: string) {
  const { observer, endpoint } = observerFor(page)
  if (opId !== undefined) {
    const receipt = await observer.saved(opId)
    if (revision !== undefined) expect(receipt.revision).toBe(revision)
    await waitNativeQueue(page, receipt.revision, undefined, true)
    await expect(host(page).getByRole('status')).toHaveText('Saved')
  } else {
    // On reload this actual host says "No unsaved changes". That label alone
    // proves nothing: require the exact independent confirmed SQL projection
    // and actual native queue/bootstrap evidence as well.
    const confirmed = await readState(page, endpoint)
    if (revision !== undefined) expect(confirmed.revision).toBe(revision)
    await waitNativeQueue(page, confirmed.revision, observer.initialRevision)
    await expect(host(page).getByRole('status')).toHaveText(/^(No unsaved changes|Saved)$/)
  }
}
export async function waitRendered(page: Page, revision: string) {
  // Product 8a exposes a real native QUEUE-consumption message, not a GPU ACK.
  // Every visual stage additionally requires stable document glyph pixels.
  await waitNativeQueue(page, revision, observerFor(page).observer.initialRevision)
  await expect(canvas(page)).toBeVisible()
}
export async function typeConfirmed(page: Page, value: string) {
  const observer = observerFor(page).observer
  for (const character of value) {
    const cursor = observer.cursor()
    await page.keyboard.type(character)
    const command = await observer.commandSince(cursor, 'text.insert')
    const receipt = await observer.saved(command.op_id, command.transportId)
    await waitSaved(page, receipt.revision, command.op_id)
  }
}
export async function pressConfirmed(page: Page, key: string, commandId: string) {
  const observer = observerFor(page).observer, cursor = observer.cursor()
  await page.keyboard.press(key)
  const command = await observer.commandSince(cursor, commandId), receipt = await observer.saved(command.op_id, command.transportId)
  await waitSaved(page, receipt.revision, command.op_id)
}

export function selectedProps(state: CommittedState, start: number, length: number): Record<string, unknown>[] {
  const paragraph = state.document.body[0]
  let offset = 0, covered = 0
  const properties: Record<string, unknown>[] = []
  for (const run of paragraph.runs) {
    if (offset < start + length && offset + run.len > start) {
      properties.push(run.props ?? {})
      covered += Math.min(offset + run.len, start + length) - Math.max(offset, start)
    }
    offset += run.len
  }
  expect(covered, 'Every selected byte must have verified formatting').toBe(length)
  expect(properties.length, 'Formatting must cover the selected bytes in the native document').toBeGreaterThan(0)
  return properties
}

/** Preserve peer list definitions as well as paragraph references across undo. */
export function referencedList(state: CommittedState, paragraph: Paragraph) {
  const reference = paragraph.props?.numbering as { num: number; level: number } | undefined
  if (!reference || reference.num === 0) return undefined
  const list = state.document.numbering?.nums.find(item => item.id === reference.num)
  expect(list, 'A preserved peer paragraph must retain its referenced list definition').toBeTruthy()
  const abstract = state.document.numbering?.abstracts.find(item => item.id === list?.abstractId)
  expect(abstract, 'A preserved list must retain its abstract definition').toBeTruthy()
  return { list, abstract }
}
