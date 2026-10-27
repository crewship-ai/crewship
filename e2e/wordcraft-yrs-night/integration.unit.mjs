// Local synthetic HTTP/discovery regression controls only, not product evidence.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'
import { request } from '@playwright/test'
import { verifyProductIdentity } from './identity.mjs'
import { setup as setupProduction } from './main-adapter.mjs'
import { MissingContract } from './run.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const sha = 'a'.repeat(40)
test('identity preflight uses actual authenticated cookie context, validates response and rejects redirects', async () => {
  const seen = []
  let redirect = false
  let redirectFollowed = 0
  const server = createServer((req, res) => {
    seen.push(req.headers.cookie ?? '')
    if (req.url === '/redirected') { redirectFollowed++; res.end('{}'); return }
    if (req.headers.cookie !== 'unit-session=allowed') { res.writeHead(401); res.end(); return }
    if (redirect) { res.writeHead(302, { location: '/redirected' }); res.end(); return }
    res.setHeader('content-type', 'application/json')
    res.end(JSON.stringify({ commit: sha }))
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const base = `http://127.0.0.1:${server.address().port}`
  const anonymous = await request.newContext()
  const authenticated = await request.newContext({ storageState: { cookies: [{ name: 'unit-session', value: 'allowed', domain: '127.0.0.1', path: '/', expires: -1, httpOnly: true, secure: false, sameSite: 'Lax' }], origins: [] } })
  try {
    assert.equal((await anonymous.get(`${base}/api/v1/system/version`)).status(), 401)
    assert.equal(await verifyProductIdentity({ request: authenticated }, base, sha), sha)
    assert.equal(seen.at(-1), 'unit-session=allowed')
    await assert.rejects(verifyProductIdentity({ request: authenticated }, base, 'b'.repeat(40)), /Unexpected product binary/)
    await assert.rejects(verifyProductIdentity({ request: anonymous }, base, sha), /Authenticated product identity response required/)
    redirect = true
    await assert.rejects(verifyProductIdentity({ request: authenticated }, base, sha), /API redirects are forbidden/)
    assert.equal(redirectFollowed, 0, 'Identity check must never follow a redirect')
  } finally {
    await Promise.all([anonymous.dispose(), authenticated.dispose()])
    await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  }
})
test('default Playwright --list never imports the Node harness units or starts an app server', async () => {
  const env = { ...process.env, PLAYWRIGHT_BASE_URL: 'http://127.0.0.1:9', CREWSHIP_INSTANCE_ID: 'wordcraft-discovery-unit' }
  delete env.NODE_TEST_CONTEXT
  const { stdout, stderr } = await promisify(execFile)(process.execPath, [
    fileURLToPath(new URL('../../node_modules/@playwright/test/cli.js', import.meta.url)),
    'test', '--config', 'playwright.config.ts', '--list', 'e2e/wordcraft-yrs-night', '--pass-with-no-tests', '--reporter=json',
  ], { cwd: root, env, maxBuffer: 4 * 1024 * 1024 })
  assert.equal(stderr, '')
  const report = JSON.parse(stdout)
  assert.deepEqual(report.errors, [])
  assert.deepEqual(report.suites, [])
})
test('production adapter rejects HTTP and an arbitrary claimed HTTPS origin before opening a context', async () => {
  for (const baseURL of ['http://127.0.0.1:48081', 'https://127.0.0.1:48082']) {
    await assert.rejects(setupProduction({ baseURL, browser: { newContext() { throw new Error('Unowned transport opened browser context') } } }), error => error instanceof MissingContract && error.message.includes('owned local TLS'))
  }
})
