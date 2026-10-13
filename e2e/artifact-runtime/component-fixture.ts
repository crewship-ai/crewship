import { FixtureResources } from './resources'
import { build } from 'vite'
import path from 'node:path'
import type { Browser } from '@playwright/test'
import { startFixture, studio, runtime } from './fixture'
import { startGoArtifactBootstrap, artifactPreparationPath } from './go-bootstrap'

let bundle: Promise<string> | undefined
async function componentBundle(): Promise<string> {
  bundle ??= (async () => {
    const result = await build({ configFile: false, publicDir: false, logLevel: 'warn', resolve: { alias: { '@': path.resolve('.') } }, define: { 'process.env.NODE_ENV': JSON.stringify('production') }, build: { write: false, minify: false, lib: { entry: path.resolve('e2e/artifact-runtime/component.tsx'), name: 'RuntimeComponentFixture', formats: ['iife'] }, rollupOptions: { output: { inlineDynamicImports: true } } } })
    const outputs = Array.isArray(result) ? result : [result]
    const chunks = outputs.flatMap(output => 'output' in output ? output.output : []).filter(item => item.type === 'chunk')
    if (chunks.length !== 1) throw new Error('Component fixture requires one self-contained script')
    return chunks[0].code
  })()
  return bundle
}

export async function startComponentFixture(browser: Browser, factories = { bootstrap: startGoArtifactBootstrap, fixture: startFixture }) {
  const source = await componentBundle()
  const resources = new FixtureResources()
  try {
    const bootstrap = await factories.bootstrap()
    resources.own(bootstrap.close)
    const fixture = await factories.fixture()
    resources.own(fixture.close)
    const context = await browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })
    resources.own(() => context.close())
    const unexpected: string[] = []
    await context.route('**/*', async route => {
    const url = new URL(route.request().url())
    if (url.origin === studio) {
      if (url.pathname !== '/') { await route.fulfill({ status: 404 }); return }
      await route.fulfill({ contentType: 'text/html', body: `<!doctype html><title>Actual runtime component fixture</title><div id="root"></div><script>${source.replace(/<\/script/gi, '<\\/script')}</script>` })
      return
    }
    if (url.origin === runtime && url.pathname === artifactPreparationPath) {
      await route.fulfill(await bootstrap.response())
      return
    }
    unexpected.push(route.request().url()); await route.abort()
    })
    const page = await context.newPage()
    await page.goto(studio)
    return { page, unexpected, fixture, close: () => resources.close() }
  } catch (error) {
    try { await resources.close() } catch (cleanupError) { throw new AggregateError([error, cleanupError], 'Runtime fixture setup and cleanup failed') }
    throw error
  }
}
