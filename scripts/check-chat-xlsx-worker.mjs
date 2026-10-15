// Real Chromium acceptance of the shipped local XLSX worker, with synthetic data.
import { chromium } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { zipSync, strToU8 } from 'fflate'
import assert from 'node:assert/strict'

const bundle = await readFile(resolve('out/chat-workers/xlsx-v1.js'), 'utf8')
assert.match(bundle, /MIT License/)
const archive = (sheet, extras = {}) => zipSync({
 '[Content_Types].xml': strToU8('<Types/>'),
 'xl/workbook.xml': strToU8('<workbook xmlns:r="r"><sheets><sheet name="Žofie" r:id="r1"/></sheets></workbook>'),
 'xl/_rels/workbook.xml.rels': strToU8('<Relationships><Relationship Id="r1" Type="office/worksheet" Target="worksheets/sheet1.xml"/></Relationships>'),
 'xl/worksheets/sheet1.xml': strToU8(`<worksheet><sheetData>${sheet}</sheetData></worksheet>`),
 ...extras,
})
const browser = await chromium.launch({ headless: true })
try {
 const context = await browser.newContext(), external = []
 await context.route('**/*', route => {
  const url = new URL(route.request().url())
  if (url.origin !== 'http://crewship-xlsx.test') { external.push(url.origin); return route.abort() }
  return route.fulfill({ contentType: url.pathname.endsWith('.js') ? 'application/javascript' : 'text/html', body: url.pathname.endsWith('.js') ? bundle : '<!doctype html><title>XLSX acceptance</title>' })
 })
 const page = await context.newPage()
 await page.goto('http://crewship-xlsx.test/')
 const run = bytes => page.evaluate(bytes => new Promise((resolve, reject) => {
  const worker = new Worker('/chat-workers/xlsx-v1.js', { type: 'module' })
  const timer = setTimeout(() => { worker.terminate(); reject(new Error('Worker timed out')) }, 5000)
  worker.onerror = event => { clearTimeout(timer); worker.terminate(); reject(new Error(event.message)) }
  worker.onmessage = event => { clearTimeout(timer); worker.terminate(); resolve(event.data) }
  const source = new Uint8Array(bytes).buffer
  worker.postMessage({ bytes: source }, [source])
 }), Array.from(bytes))
 const fixture = await run(await readFile(resolve('components/features/chat/artifact/__tests__/fixtures/openpyxl-hyperlink-chartsheet.xlsx')))
 assert.deepEqual(fixture.workbook.sheets.map(sheet => sheet.name), ['Data', 'After chart'])
 assert.deepEqual(fixture.workbook.sheets[0].table.rows, [['Žofie', '42']])
 const valid = await run(archive('<row r="2"><c r="A2" t="inlineStr"><is><t> Unicode Žofie </t></is></c><c r="B2"><f>1+1</f><v>2</v></c><c r="C2"><f>1+2</f></c></row>'))
 assert.equal(valid.workbook.sheets[0].name, 'Žofie')
 assert.deepEqual(valid.workbook.sheets[0].table.rows[1], [' Unicode Žofie ', '2', '[Formula not evaluated: no cached result]'])
 assert.equal(valid.workbook.missingFormulaResults, 1)
 assert.match((await run(new Uint8Array([1, 2, 3]))).error, /supported XLSX/)
 assert.match((await run(archive('<row><c r="XFD1"><v>1</v></c></row>'))).error, /100-column/)
 assert.match((await run(archive('', { 'xl/vbaProject.bin': new Uint8Array([1]) }))).error, /Macros/)
 assert.equal(external.length, 0)
 console.log(JSON.stringify({ browser: await browser.version(), workerBytes: Buffer.byteLength(bundle), savedValuesAndCoordinates: true, missingFormulaMarked: true, corruptLimitMacroRefused: true, externalRequests: external.length }))
} finally { await browser.close() }
