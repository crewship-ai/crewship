// Real Chromium check of the built local worker; all HTTP responses are synthetic.
import { chromium } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import assert from 'node:assert/strict'
const bundle = await readFile(resolve('out/chat-workers/table-v1.js'), 'utf8')
const browser = await chromium.launch({ headless: true })
try {
 const context = await browser.newContext()
 const external = []
 await context.route('**/*', route => {
  const url = new URL(route.request().url())
  if (url.origin !== 'http://crewship-worker.test') { external.push(url.origin); return route.abort() }
  return route.fulfill({ contentType: url.pathname.endsWith('.js') ? 'application/javascript' : 'text/html', body: url.pathname.endsWith('.js') ? bundle : '<!doctype html><title>Local worker acceptance</title>' })
 })
 const page = await context.newPage()
 await page.goto('http://crewship-worker.test/')
 const run = text => page.evaluate(text => new Promise((resolve, reject) => {
  const worker = new Worker('/chat-workers/table-v1.js', { type: 'module' })
  const timer = setTimeout(() => { worker.terminate(); reject(new Error('Worker timed out')) }, 5000)
  worker.onerror = event => { clearTimeout(timer); worker.terminate(); reject(new Error(event.message)) }
  worker.onmessage = event => { clearTimeout(timer); worker.terminate(); resolve(event.data) }
  const bytes = new TextEncoder().encode(text).buffer
  worker.postMessage({ bytes, separator: ',' }, [bytes])
 }), text)
 const valid = await run('Žofie,"a,b"\r\nsecond,"line\nnext"')
 assert.deepEqual(valid.table.rows, [['Žofie','a,b'],['second','line\nnext']])
 const malformed = await run('"unterminated')
 assert.match(malformed.error, /Unterminated/)
 const bounded = await run(Array(102).fill('x').join(','))
 assert.match(bounded.error, /column or cell/)
 assert.equal(external.length, 0, 'Preview must not request an external parser')
 const evidence = { browser: await browser.version(), worker: 'out/chat-workers/table-v1.js', bytes: Buffer.byteLength(bundle), validUnicodeQuotedMultiline: true, malformedRefused: true, limitRefused: true, externalParserRequests: external.length > 0 }
 console.log(JSON.stringify(evidence))
} finally { await browser.close() }
