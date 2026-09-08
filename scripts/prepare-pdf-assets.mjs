// Serve PDF.js workers/fonts locally; never depend on a third-party CDN.
import { cp, mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
const require = createRequire(import.meta.url)
const source = path.dirname(require.resolve('pdfjs-dist/package.json'))
const target = path.resolve('public/pdfjs')
await mkdir(target, { recursive: true })
for (const name of ['cmaps', 'standard_fonts', 'wasm', 'LICENSE']) {
  await cp(path.join(source, name), path.join(target, name), { recursive: true })
}
await cp(path.join(source, 'build/pdf.worker.min.mjs'), path.join(target, 'pdf.worker.min.mjs'))
