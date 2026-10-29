// Regenerate the embedded engine with its upstream MIT notices. Run from the repository root.
import { build } from 'esbuild'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
const root = dirname(fileURLToPath(import.meta.url))
const require = createRequire(import.meta.url)
const notices = []
for (const dependency of ['yjs', 'y-protocols', 'lib0']) {
 const manifest = require.resolve(`${dependency}/package.json`)
 notices.push(`${dependency}\n${await readFile(join(dirname(manifest), 'LICENSE'), 'utf8')}`)
}
await build({ entryPoints: [join(root, 'engine.mjs')], outfile: join(root, 'engine.cjs'), bundle: true, platform: 'node', target: 'node22', format: 'cjs', minify: true, legalComments: 'inline', banner: { js: '/*\n' + notices.join('\n\n').replaceAll('*/', '* /') + '\n*/' } })
