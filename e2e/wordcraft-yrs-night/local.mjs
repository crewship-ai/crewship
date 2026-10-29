import assert from 'node:assert/strict'

export function localURL(value, base) {
  const url = new URL(value, base)
  const origin = new URL(base)
  assert.equal(origin.hostname, '127.0.0.1')
  assert.equal(origin.protocol, 'http:')
  assert.ok(['http:', 'ws:'].includes(url.protocol))
  assert.equal(url.hostname, '127.0.0.1')
  assert.equal(url.port, origin.port)
  assert.equal(url.username + url.password, '')
  return url
}

export async function localContext(browser, base, options = {}) {
  localURL(base, base)
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, serviceWorkers: 'block', ...options })
  await context.route('**/*', async route => {
    try { localURL(route.request().url(), base) } catch { await route.abort('blockedbyclient'); return }
    // Forward to the actual server, allowing no automatic redirect in the
    // driver. Validate Location before the browser can follow it.
    const response = await route.fetch({ maxRedirects: 0 })
    const location = response.headers().location
    if (location) {
      try { localURL(new URL(location, route.request().url()).href, base) } catch { await route.abort('blockedbyclient'); return }
    }
    await route.fulfill({ response })
  })
  await context.routeWebSocket('**/*', socket => {
    try { localURL(socket.url(), base) } catch { socket.close({ code: 1008, reason: 'Disposable local origin required' }); return }
    socket.connectToServer()
  })
  return context
}

export async function localRequest(context, base, path, options = {}) {
  const url = localURL(path, base)
  const response = await context.request.fetch(url.href, { ...options, maxRedirects: 0 })
  assert.ok(response.status() < 300 || response.status() >= 400, 'API redirects are forbidden')
  return response
}
