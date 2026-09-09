import fs from 'node:fs/promises'
import { createHash } from 'node:crypto'
import path from 'node:path'
import { execFileSync } from 'node:child_process'
import { build } from 'vite'

// All stdout is one bounded artifact. Build diagnostics use stderr.
console.log = (...args) => console.error(...args)
const root = '/work/project'
try {
  let input = ''
  for await (const chunk of process.stdin) {
    input += chunk
    if (Buffer.byteLength(input) > 4 * 1024 * 1024) throw new Error('Source input exceeds limit')
  }
  const project = JSON.parse(input)
  if (project.format !== 'crewship-page-source/v1' || project.runtime !== 'react-vite-typescript/v1' || !Array.isArray(project.files) || project.files.length > 256) throw new Error('Unsupported source profile')
  await fs.mkdir(root)
  let total = 0
  const names = new Set()
  for (const file of project.files) {
    if (typeof file.path !== 'string' || !/^[\w./@+-]+$/.test(file.path) || path.posix.normalize(file.path) !== file.path || path.isAbsolute(file.path) || file.path.split('/').some(p => p === '..' || p === 'node_modules' || p.startsWith('.'))) throw new Error(`Unsafe source path ${JSON.stringify(file.path)}: use ASCII letters, digits, _ . / @ + -; hidden paths are unsupported`)
    if (names.has(file.path.toLowerCase())) throw new Error('Duplicate source path')
    names.add(file.path.toLowerCase())
    if (!['utf8', 'base64'].includes(file.encoding)) throw new Error('Unsupported source encoding')
    const bytes = Buffer.from(file.content, file.encoding === 'base64' ? 'base64' : 'utf8')
    total += bytes.length
    if (bytes.length > 512 * 1024 || total > 2 * 1024 * 1024) throw new Error('Decoded source exceeds limit')
    const target = path.join(root, file.path)
    await fs.mkdir(path.dirname(target), { recursive: true })
    await fs.writeFile(target, bytes, { flag: 'wx', mode: 0o600 })
  }
  // The initial offline profile has a pinned dependency set. Never install
  // request-supplied packages or execute package scripts/config plugins.
  const expected = JSON.parse(await fs.readFile('/opt/pages/package.json', 'utf8'))
  const actual = JSON.parse(await fs.readFile(root + '/package.json', 'utf8'))
  const canonical = value => JSON.stringify(Object.entries(value ?? {}).sort(([a], [b]) => a.localeCompare(b)))
  for (const field of ['dependencies', 'devDependencies']) {
    if (canonical(expected[field]) !== canonical(actual[field])) throw new Error('Dependency set differs from the installed Pages profile; start from the supplied starter')
  }
  if (!(await fs.readFile(root + '/pnpm-lock.yaml')).equals(await fs.readFile('/opt/pages/pnpm-lock.yaml'))) throw new Error('Lockfile differs from the installed Pages profile')
  if ([...names].some(name => /(^|\/)(vite|postcss|tailwind)\.config\./.test(name))) throw new Error('Custom build configuration is not supported by this preview profile')
  await fs.symlink('/opt/pages/node_modules', root + '/node_modules')
  const config = { compilerOptions: { target: 'ES2022', module: 'ESNext', moduleResolution: 'Bundler', jsx: 'react-jsx', noEmit: true, strict: true, skipLibCheck: true, allowSyntheticDefaultImports: true, types: ['vite/client', 'react', 'react-dom'], paths: { '@crewship/pages': ['/opt/pages/sdk.ts'] } }, include: [root + '/src/**/*.ts', root + '/src/**/*.tsx'] }
  await fs.writeFile(root + '/tsconfig.pages.json', JSON.stringify(config))
  execFileSync('/opt/pages/node_modules/.bin/tsc', ['--project', root + '/tsconfig.pages.json', '--pretty', 'false'], { timeout: 45000, maxBuffer: 64 * 1024, stdio: ['ignore', 'pipe', 'pipe'] })
  const result = await build({
    configFile: false, root, publicDir: false, logLevel: 'silent',
    define: { 'process.env.NODE_ENV': JSON.stringify('production') },
    resolve: { alias: [{ find: '@crewship/pages', replacement: '/opt/pages/sdk.ts' }] },
    css: { postcss: { plugins: [] } },
    build: { write: false, sourcemap: false, cssCodeSplit: false, minify: true,
      lib: { entry: root + '/src/main.tsx', name: 'CrewshipPage', formats: ['iife'] },
      rolldownOptions: { output: { inlineDynamicImports: true } } }
  })
  const output = (Array.isArray(result) ? result.flatMap(item => item.output) : result.output)
  const js = output.filter(item => item.type === 'chunk')
  const assets = output.filter(item => item.type === 'asset')
  if (js.length !== 1 || js[0].imports.length || js[0].dynamicImports.length || assets.some(item => !item.fileName.endsWith('.css'))) throw new Error('Preview must compile to one self-contained JavaScript bundle and CSS')
  const profileHash = createHash('sha256')
  for (const name of ['package.json', 'pnpm-lock.yaml', 'sdk.ts', 'build.mjs']) {
    profileHash.update(name).update('\0').update(await fs.readFile('/opt/pages/' + name)).update('\0')
  }
  const artifact = { format: 'crewship-page-preview/v1', javascript: js[0].code, css: assets.map(item => typeof item.source === 'string' ? item.source : Buffer.from(item.source).toString('utf8')).join('\n'), toolchain: '', profile_sha256: profileHash.digest('hex') }
  if (Buffer.byteLength(artifact.javascript) + Buffer.byteLength(artifact.css) > 2 * 1024 * 1024) throw new Error('Compiled artifact exceeds limit')
  process.stdout.write(JSON.stringify(artifact))
} catch (error) {
  process.stderr.write(String(error.stdout ?? '') + '\n' + String(error.message ?? error) + '\n')
  process.exitCode = 1
}
