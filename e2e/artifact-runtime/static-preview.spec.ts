import { test, expect } from '@playwright/test'
import { build } from 'vite'
import { startFixture, studio, sink } from './fixture'

async function sanitizerBundle() {
  const result = await build({ configFile: false, publicDir: false, logLevel: 'warn', build: { write: false, minify: false, lib: { entry: 'lib/pages/artifact-static.ts', name: 'StaticArtifact', formats: ['iife'] } } })
  const output = Array.isArray(result) ? result[0] : result
  if (!('output' in output) || output.output[0].type !== 'chunk') throw new Error('Sanitizer fixture build failed')
  return output.output[0].code + '\nwindow.staticArtifactDocument = StaticArtifact.staticArtifactDocument'
}

test('production static preview strips navigation and blocks resource traffic', async ({ browser }) => {
  const fixture = await startFixture()
  const context = await browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })
  try {
    const page = await context.newPage()
    await page.goto(`${sink}/control`)
    expect(fixture.hits).toContain('/control')
    await page.goto(studio)
    fixture.hits.length = 0
    await page.addScriptTag({ content: await sanitizerBundle() })
    const hostile = `<meta http-equiv="refresh" content="0;url=${sink}/refresh"><base href="${sink}"><link rel="preconnect" href="${sink}"><script>fetch('${sink}/script')</script><svg><foreignObject><iframe src="${sink}/svg"></iframe></foreignObject></svg><form action="${sink}/form"><input name="secret"></form><a href="${sink}/anchor" ping="${sink}/ping">Navigation disabled</a><img src="${sink}/image"><style>@import url('${sink}/css');h1{color:rgb(12,34,56)}body{background-image:url('${sink}/background')}</style><style>@media (min-width:1px){@supports(display:block){h1{color:rgb(12,34,56)}}}</style><h1 style="font-size:28px">Static report</h1><table><tr><td>42</td></tr></table>`
    await page.evaluate(async html => {
      const win = window as unknown as { staticArtifactDocument: (html: string) => Promise<string> }
      const iframe = document.createElement('iframe')
      iframe.title = 'Static preview'; iframe.setAttribute('sandbox', '')
      iframe.srcdoc = await win.staticArtifactDocument(html); document.body.append(iframe)
    }, hostile)
    const frame = page.frameLocator('iframe')
    await expect(frame.getByRole('heading', { name: 'Static report' })).toBeVisible()
    await expect(frame.getByRole('heading')).toHaveCSS('font-size', '28px')
    await expect(frame.getByRole('heading')).toHaveCSS('color', 'rgb(12, 34, 56)')
    await expect(frame.locator('script,meta[http-equiv="refresh"],base,form,svg,iframe')).toHaveCount(0)
    await expect(frame.locator('[href],[src],[ping],[onclick]')).toHaveCount(0)
    await frame.getByText('Navigation disabled').click()
    await page.waitForTimeout(700)
    expect(fixture.hits).toEqual([])
    expect(page.url()).toBe(`${studio}/`)
    expect(context.pages()).toHaveLength(1)
  } finally { await context.close(); await fixture.close() }
})

// Build actual PNG containers, including deliberately corrupt compressed data.
// CRC generation is independent of the production table-driven implementation.
function chunk(type: string, data: Buffer): Buffer {
  const name = Buffer.from(type), length = Buffer.alloc(4), crc = Buffer.alloc(4)
  length.writeUInt32BE(data.length)
  let value = 0xffffffff
  for (const byte of Buffer.concat([name, data])) {
    value ^= byte
    for (let bit = 0; bit < 8; bit++) value = (value >>> 1) ^ (value & 1 ? 0xedb88320 : 0)
  }
  crc.writeUInt32BE((value ^ 0xffffffff) >>> 0)
  return Buffer.concat([length, name, data, crc])
}
function pngContainer(width: number, height: number, compressed: Buffer, extra: Buffer[] = []): Buffer {
  const header = Buffer.alloc(13)
  header.writeUInt32BE(width); header.writeUInt32BE(height, 4); header[8] = 8; header[9] = 6
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', header), ...extra, chunk('IDAT', compressed), chunk('IEND', Buffer.alloc(0))])
}
const uri = (type: string, data: Buffer) => `data:image/${type};base64,${data.toString('base64')}`

test('native raster validation accepts PNG/JPEG and refuses malformed containers and budgets before rendering', async ({ browser }) => {
  const fixture = await startFixture()
  const context = await browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })
  try {
    const page = await context.newPage()
    await page.goto(studio)
    const attempted: string[] = []
    // Observe from before DOMPurify parsing, not just after iframe creation.
    context.on('request', request => { if (/^https?:/.test(request.url())) attempted.push(request.url()) })
    fixture.hits.length = 0
    await page.addScriptTag({ content: await sanitizerBundle() })
    const jpeg = await page.evaluate(() => {
      const canvas = document.createElement('canvas'); canvas.width = 2; canvas.height = 3
      canvas.getContext('2d')!.fillRect(0, 0, 2, 3)
      return canvas.toDataURL('image/jpeg')
    })
    const { deflateSync } = await import('node:zlib')
    const compressed = deflateSync(Buffer.concat(Array.from({ length: 3 }, () => Buffer.from([0, 32, 96, 160, 255, 32, 96, 160, 255]))))
    const png = pngContainer(2, 3, compressed)
    const jpegBytes = Buffer.from(jpeg.split(',')[1], 'base64')
    const frameOffset = jpegBytes.indexOf(Buffer.from([255, 192]))
    expect(frameOffset).toBeGreaterThan(1)
    const frameLength = jpegBytes.readUInt16BE(frameOffset + 2)
    const duplicateJPEG = Buffer.concat([jpegBytes.subarray(0, frameOffset), jpegBytes.subarray(frameOffset, frameOffset + 2 + frameLength), jpegBytes.subarray(frameOffset)])
    const duplicatePNG = Buffer.concat([png.subarray(0, 33), png.subarray(8, 33), png.subarray(33)])
    const exif = Buffer.from('4578696600004d4d002a00000008000101120003000000010006000000000000', 'hex')
    const exifLength = Buffer.alloc(2); exifLength.writeUInt16BE(exif.length + 2)
    const rotatedJPEG = Buffer.concat([jpegBytes.subarray(0, 2), Buffer.from([255, 225]), exifLength, exif, jpegBytes.subarray(2)])
    const invalid = [
      uri('png', pngContainer(2, 3, Buffer.from('not zlib'))),
      uri('png', png.subarray(0, -8)), uri('png', duplicatePNG), uri('jpeg', duplicateJPEG),
      uri('jpeg', jpegBytes.subarray(0, -2)), uri('png', pngContainer(2049, 3, compressed)),
      uri('png', pngContainer(2, 3, compressed, [chunk('acTL', Buffer.alloc(8))])),
      uri('jpeg', png), uri('png', Buffer.concat([png, Buffer.from('<svg/>')])),
      uri('png', Buffer.alloc(1024 * 1024 + 1)), 'data:image/svg+xml;base64,PHN2Zz4=',
    ]
    const source = `<h1>Raster report</h1><img id="png" src="${uri('png', png)}"><img id="jpeg" src="${jpeg}"><img id="rotated" src="${uri('jpeg', rotatedJPEG)}">` + invalid.map((src, i) => `<img id="bad-${i}" src="${src}" onerror="fetch('${sink}/error')">`).join('') + `<img id="remote" src="${sink}/image" srcset="${sink}/srcset 2x"><template shadowrootmode="open"><img src="${sink}/template"><img src="${invalid[0]}"></template><noscript><img src="${sink}/noscript"></noscript><iframe src="${sink}/iframe"></iframe><link rel="stylesheet" href="${sink}/style"><style>@import '${sink}/import';body{background:url('${sink}/css')}</style><style>@media screen{@import '${sink}/nested';}</style><div style="background:image-set('${sink}/set' 1x)">Safe text</div><script>fetch('${sink}/script')</script>`
    await page.evaluate(async html => {
      const win = window as unknown as { staticArtifactDocument: (html: string) => Promise<string> }
      const iframe = document.createElement('iframe'); iframe.title = 'Validated raster'; iframe.setAttribute('sandbox', '')
      iframe.srcdoc = await win.staticArtifactDocument(html); document.body.append(iframe)
    }, source)
    const frame = page.frameLocator('iframe[title="Validated raster"]')
    for (const [id, width, height] of [['png', 2, 3], ['jpeg', 2, 3], ['rotated', 3, 2]] as const) {
      await expect.poll(() => frame.locator(`#${id}`).evaluate((node: HTMLImageElement) => [node.naturalWidth, node.naturalHeight])).toEqual([width, height])
    }
    await expect(frame.locator('img[id^="bad-"][src],#remote[src],[srcset],script,iframe,link,template,noscript')).toHaveCount(0)
    // A real metadata-heavy PNG crosses only the cumulative byte budget.
    const padded = uri('png', pngContainer(2, 3, compressed, [chunk('tEXt', Buffer.concat([Buffer.from('note\0'), Buffer.alloc(900 * 1024, 65)]))]))
    const counts = await page.evaluate(async ({ small, padded }) => {
      const render = (window as unknown as { staticArtifactDocument: (html: string) => Promise<string> }).staticArtifactDocument
      const count = async (html: string) => new DOMParser().parseFromString(await render(html), 'text/html').querySelectorAll('img[src]').length
      const canvas = document.createElement('canvas'); canvas.width = canvas.height = 2048
      const large = canvas.toDataURL('image/png')
      return {
        occurrences: await count(Array.from({ length: 33 }, () => `<img src="${small}">`).join('')),
        bytes: await count(Array.from({ length: 5 }, () => `<img src="${padded}">`).join('')),
        pixels: await count(Array.from({ length: 3 }, () => `<img src="${large}">`).join('')),
      }
    }, { small: uri('png', png), padded })
    expect(counts).toEqual({ occurrences: 32, bytes: 4, pixels: 2 })
    await page.waitForTimeout(300)
    expect(attempted).toEqual([])
    expect(fixture.hits).toEqual([])
  } finally { await context.close(); await fixture.close() }
})

test('actual chat HTML and Markdown components decode the shared raster fixtures and clear replaced source', async ({ browser }) => {
  const { readFile } = await import('node:fs/promises')
  const path = await import('node:path')
  const rasters = JSON.parse(await readFile('e2e/artifact-runtime/raster-fixtures.json', 'utf8')) as { png: string; jpeg: string }
  const result = await build({ configFile: false, publicDir: false, logLevel: 'warn', resolve: { alias: { '@': path.resolve('.') } }, define: { 'process.env.NODE_ENV': JSON.stringify('production') }, build: { write: false, minify: false, lib: { entry: 'e2e/artifact-runtime/static-file-preview.tsx', name: 'StaticFiles', formats: ['iife'] }, rollupOptions: { output: { inlineDynamicImports: true } } } })
  const output = Array.isArray(result) ? result[0] : result
  if (!('output' in output) || output.output[0].type !== 'chunk') throw new Error('Chat preview fixture build failed')
  const fixture = await startFixture()
  const context = await browser.newContext({ proxy: { server: fixture.proxy }, ignoreHTTPSErrors: true })
  try {
    const page = await context.newPage()
    await page.goto(studio)
    await page.evaluate(() => { const root = document.createElement('div'); root.id = 'root'; document.body.append(root) })
    const attempted: string[] = []
    context.on('request', request => { if (/^https?:/.test(request.url())) attempted.push(request.url()) })
    await page.addScriptTag({ content: output.output[0].code })
    await expect(page.locator('iframe')).toHaveCount(2)
    await page.evaluate(({ png, jpeg, sink }) => (window as unknown as { showStaticFiles: (html: string) => void }).showStaticFiles(`<h1>Shared raster report</h1><img id="png" src="${png}"><img id="jpeg" src="${jpeg}"><img id="remote" src="${sink}/image" srcset="${sink}/image2 2x"><style>@import '${sink}/import';</style><script>fetch('${sink}/script')</script>`), { ...rasters, sink })
    for (const title of ['Preview report.html', 'Preview report.md']) {
      const frame = page.frameLocator(`iframe[title="${title}"]`)
      for (const id of ['png', 'jpeg']) await expect.poll(() => frame.locator(`#${id}`).evaluate((node: HTMLImageElement) => [node.naturalWidth, node.naturalHeight])).toEqual([2, 3])
      await expect(frame.locator('#remote[src],[srcset],script')).toHaveCount(0)
    }
    await page.evaluate(() => (window as unknown as { showStaticFiles: (html: string) => void }).showStaticFiles('<h1>Replacement without images</h1>'))
    for (const title of ['Preview report.html', 'Preview report.md']) {
      await expect(page.locator(`iframe[title="${title}"]`)).toHaveAttribute('srcdoc', /Replacement without images/)
      const frame = page.frameLocator(`iframe[title="${title}"]`)
      await expect(frame.getByRole('heading', { name: 'Replacement without images' })).toBeVisible()
      await expect(frame.locator('img')).toHaveCount(0)
    }
    // Rapid A→B→A must finish on A, including when B has native decodes pending.
    await page.evaluate(({ png, jpeg }) => {
      const show = (window as unknown as { showStaticFiles: (html: string) => void }).showStaticFiles
      show('<h1>Final A</h1>')
      setTimeout(() => { show(`<h1>Transient B</h1><img src="${png}"><img src="${jpeg}">`); setTimeout(() => show('<h1>Final A</h1>'), 0) }, 0)
    }, rasters)
    for (const title of ['Preview report.html', 'Preview report.md']) {
      const frame = page.frameLocator(`iframe[title="${title}"]`)
      await expect(frame.getByRole('heading', { name: 'Final A' })).toBeVisible()
      await expect(frame.locator('img')).toHaveCount(0)
    }
    expect(attempted).toEqual([])
  } finally { await context.close(); await fixture.close() }
})
