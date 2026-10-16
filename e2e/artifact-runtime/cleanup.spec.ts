import { test, expect, type Browser, type BrowserContext } from '@playwright/test'
import { FixtureResources } from './resources'
import { startComponentFixture } from './component-fixture'

test('setup failure closes every previously acquired fixture resource', async () => {
  for (const failure of ['fixture', 'context', 'route']) {
    const closed: string[] = []
    const context = {
      route: async () => { throw new Error('route setup failed') },
      close: async () => { closed.push('context'); throw new Error('context close failed') },
    } as unknown as BrowserContext
    const browser = { newContext: async () => {
      if (failure === 'context') throw new Error('browser unavailable')
      return context
    } } as unknown as Browser
    const factories = {
      bootstrap: async () => ({ response: async () => ({ status: 200, headers: {}, body: '' }), close: async () => { closed.push('bootstrap') } }),
      fixture: async () => {
        if (failure === 'fixture') throw new Error('TLS fixture unavailable')
        return { proxy: 'http://127.0.0.1:1', documents: new Map<string, string>(), hits: [] as string[], tunnels: [] as string[], close: async () => { closed.push('fixture') } }
      },
    }
    await expect(startComponentFixture(browser, factories)).rejects.toThrow()
    expect(closed.sort()).toEqual(failure === 'fixture' ? ['bootstrap'] : failure === 'context' ? ['bootstrap', 'fixture'] : ['bootstrap', 'context', 'fixture'])
  }
})

test('a hung closer cannot prevent independent cleanup or hang teardown', async () => {
  const resources = new FixtureResources(10)
  let closed = false
  resources.own(() => new Promise(() => {}))
  resources.own(async () => { closed = true })
  await expect(resources.close()).rejects.toThrow('Runtime fixture cleanup failed')
  expect(closed).toBe(true)
  await resources.close()
})
