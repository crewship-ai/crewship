// Excalidraw fonts are served by this instance, never a mandatory external CDN.
import { cp, mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
const require = createRequire(import.meta.url)
const entry = require.resolve('@excalidraw/excalidraw')
const source = path.join(path.dirname(entry), 'fonts')
const target = path.resolve(process.argv.includes('--production') ? 'public/excalidraw-assets' : 'e2e/board-editor/public/excalidraw-assets')
await mkdir(target, { recursive: true })
await cp(source, path.join(target, 'fonts'), { recursive: true })

await cp(path.resolve('public/fonts/excalidraw-notices'), path.join(target, 'notices'), { recursive: true })
